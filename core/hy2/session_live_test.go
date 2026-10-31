package hy2

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	vcore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	client "github.com/apernet/hysteria/core/v2/client"
	hyerrors "github.com/apernet/hysteria/core/v2/errors"
	"go.uber.org/zap"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

type sessionConnFactory struct{ ip string }

func (f sessionConnFactory) New(net.Addr) (net.PacketConn, error) {
	return net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(f.ip)})
}

type hy2SessionFixture struct {
	h        *Hysteria2
	tag      string
	addr     *net.UDPAddr
	roots    *x509.CertPool
	tcp, udp string
}

func newHy2SessionFixture(t testing.TB, deviceLimit int) *hy2SessionFixture {
	t.Helper()
	limiter.Init()
	dir := t.TempDir()
	pub, key, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"test.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, template, template, pub, key)
	if e != nil {
		t.Fatal(e)
	}
	pk, e := x509.MarshalPKCS8PrivateKey(key)
	if e != nil {
		t.Fatal(e)
	}
	cert, keyfile := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	cp := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	if e = os.WriteFile(cert, cp, 0600); e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(keyfile, pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: pk}), 0600); e != nil {
		t.Fatal(e)
	}
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(cp)
	echo, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { echo.Close() })
	go func() {
		for {
			c, e := echo.Accept()
			if e != nil {
				return
			}
			go func() { defer c.Close(); io.Copy(c, c) }()
		}
	}()
	udp, e := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { udp.Close() })
	go func() {
		p := make([]byte, 65535)
		for {
			n, a, e := udp.ReadFromUDP(p)
			if e != nil {
				return
			}
			udp.WriteToUDP(p[:n], a)
		}
	}()
	socket, e := net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP("127.0.0.1")})
	if e != nil {
		t.Fatal(e)
	}
	addr := socket.LocalAddr().(*net.UDPAddr)
	socket.Close()
	tag := "session-live"
	h := &Hysteria2{Hy2nodes: map[string]Hysteria2node{}, Logger: zap.NewNop()}
	users := []panel.UserInfo{{Id: 7, Uuid: "synthetic-live", DeviceLimit: deviceLimit}}
	limiter.AddLimiter(tag, &conf.LimitConfig{}, users, nil)
	t.Cleanup(func() { h.Close(); limiter.DeleteLimiter(tag) })
	info := &panel.NodeInfo{Type: "hysteria2", Common: &panel.CommonNode{ServerPort: addr.Port}, Hysteria2: &panel.Hysteria2Node{}}
	if e = h.AddNode(tag, info, &conf.Options{ListenIP: "127.0.0.1", CertConfig: &conf.CertConfig{CertMode: "file", CertFile: cert, KeyFile: keyfile}}); e != nil {
		t.Fatal(e)
	}
	if _, e = h.AddUsers(&vcore.AddUsersParams{Tag: tag, Users: users}); e != nil {
		t.Fatal(e)
	}
	return &hy2SessionFixture{h, tag, addr, roots, echo.Addr().String(), udp.LocalAddr().String()}
}
func (f *hy2SessionFixture) dial(ip string) (client.Client, error) {
	c, _, e := client.NewClient(&client.Config{ConnFactory: sessionConnFactory{ip}, ServerAddr: f.addr, Auth: "synthetic-live", TLSConfig: client.TLSConfig{ServerName: "test.invalid", RootCAs: f.roots}})
	return c, e
}
func hyExchange(c net.Conn, p []byte) error {
	c.SetDeadline(time.Now().Add(5 * time.Second))
	if _, e := c.Write(p); e != nil {
		return e
	}
	r := make([]byte, len(p))
	_, e := io.ReadFull(c, r)
	if e == nil && !bytes.Equal(p, r) {
		return fmt.Errorf("payload changed")
	}
	return e
}
func localSecondSource(t testing.TB) string {
	t.Helper()
	addresses, e := net.InterfaceAddrs()
	if e != nil {
		t.Fatal(e)
	}
	for _, a := range addresses {
		ip, _, e := net.ParseCIDR(a.String())
		if e == nil && ip.To4() != nil && !ip.IsLoopback() && ip.IsGlobalUnicast() {
			return ip.String()
		}
	}
	t.Fatal("test needs a second assigned local IPv4 address")
	return ""
}
func TestHy2SessionRealQUICDeviceIsolation(t *testing.T) {
	secondSource := localSecondSource(t)
	f := newHy2SessionFixture(t, 1)
	a, e := f.dial("127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	stream, e := a.TCP(f.tcp)
	if e != nil {
		t.Fatal(e)
	}
	defer stream.Close()
	var total int64
	for i := 0; i < 10; i++ {
		if b, e := f.dial(secondSource); e == nil {
			b.Close()
			t.Fatal("over-limit second IP authenticated")
		} else {
			var authError hyerrors.AuthError
			if !errors.As(e, &authError) {
				t.Fatalf("expected authentication rejection, not setup/transport failure: %v", e)
			}
		}
		if e = hyExchange(stream, []byte("ping")); e != nil {
			t.Fatal("denied connection broke existing stream", e)
		}
		total += 4
	}
	same, e := f.dial("127.0.0.1")
	if e != nil {
		t.Fatal("same IP second port denied", e)
	}
	next, e := same.TCP(f.tcp)
	if e != nil {
		t.Fatal(e)
	}
	if e = hyExchange(next, []byte("same")); e != nil {
		t.Fatal(e)
	}
	total += 4
	next.Close()
	same.Close()
	u, e := a.UDP()
	if e != nil {
		t.Fatal(e)
	}
	defer u.Close()
	if e = u.Send([]byte("datagram"), f.udp); e != nil {
		t.Fatal(e)
	}
	received := make(chan error, 1)
	go func() {
		b, _, e := u.Receive()
		if e == nil && !bytes.Equal(b, []byte("datagram")) {
			e = fmt.Errorf("UDP payload changed")
		}
		received <- e
	}()
	select {
	case e = <-received:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("UDP echo timed out")
	}
	total += 8
	l, _ := limiter.GetLimiter(f.tag)
	online, _ := l.GetOnlineDevice()
	if len(*online) != 1 || (*online)[0].IP != "127.0.0.1" {
		t.Fatal("real source or device state changed", online)
	}
	stream.Close()
	u.Close()
	a.Close()
	auth := f.h.Hy2nodes[f.tag].Auth
	deadline := time.Now().Add(3 * time.Second)
	for {
		auth.mutex.RLock()
		remaining := len(auth.sessions) + len(auth.streams)
		auth.mutex.RUnlock()
		if remaining == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("disconnected sessions retained", remaining)
		}
		time.Sleep(10 * time.Millisecond)
	}
	var rows []panel.UserTraffic
	if e = f.h.PersistTrafficSnapshot(f.tag, func(v []panel.UserTraffic) error { rows = v; return nil }); e != nil {
		t.Fatal(e)
	}
	if len(rows) != 1 || rows[0].UID != 7 || rows[0].Upload != total || rows[0].Download != total {
		t.Fatal("real TCP/UDP reconciliation", total, rows)
	}
	t.Logf("10 rejected handshakes; valid TCP/UDP and same-IP multiport passed; real IP correct; upload=download=%d; session state reclaimed", total)
}

// Opt-in real QUIC A/B profile; fixture works unchanged against -13 via overlay.
func TestHy2SessionTransportProfile(t *testing.T) {
	raw := os.Getenv("HY2_PROFILE_DURATION")
	if raw == "" {
		t.Skip("set HY2_PROFILE_DURATION")
	}
	duration, e := time.ParseDuration(raw)
	if e != nil || duration < time.Second || duration > time.Minute {
		t.Fatal("invalid duration")
	}
	parallel, _ := strconv.Atoi(os.Getenv("HY2_PROFILE_PARALLEL"))
	if parallel == 0 {
		parallel = 1
	}
	if parallel < 1 || parallel > 16 {
		t.Fatal("invalid parallelism")
	}
	f := newHy2SessionFixture(t, 0)
	var total atomic.Int64
	var wg sync.WaitGroup
	var before, after syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &before)
	start := time.Now()
	end := start.Add(duration)
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, e := f.dial("127.0.0.1")
			if e != nil {
				t.Error(e)
				return
			}
			defer c.Close()
			s, e := c.TCP(f.tcp)
			if e != nil {
				t.Error(e)
				return
			}
			defer s.Close()
			p := bytes.Repeat([]byte{42}, 32768)
			for time.Now().Before(end) {
				if e = hyExchange(s, p); e != nil {
					t.Error(e)
					return
				}
				total.Add(int64(len(p)))
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	syscall.Getrusage(syscall.RUSAGE_SELF, &after)
	var up, down int64
	if e = f.h.PersistTrafficSnapshot(f.tag, func(rows []panel.UserTraffic) error {
		for _, r := range rows {
			up += r.Upload
			down += r.Download
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	if up != total.Load() || down != total.Load() {
		t.Fatal("profile accounting", up, down, total.Load())
	}
	cpu := func(r syscall.Rusage) float64 {
		return float64(r.Utime.Sec+r.Stime.Sec) + float64(r.Utime.Usec+r.Stime.Usec)/1e6
	}
	rss := after.Maxrss
	if runtime.GOOS == "linux" {
		rss *= 1024
	}
	data, _ := json.Marshal(map[string]any{"parallel": parallel, "seconds": elapsed.Seconds(), "bytes_each_direction": up, "aggregate_mib_s": float64(up+down) / (1 << 20) / elapsed.Seconds(), "cpu_seconds": cpu(after) - cpu(before), "max_rss_bytes": rss, "reconciled": true})
	t.Log("HY2_PROFILE " + string(data))
}
