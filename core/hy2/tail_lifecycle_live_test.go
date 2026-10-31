package hy2

import (
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	vcore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	client "github.com/apernet/hysteria/core/v2/client"
	"github.com/apernet/hysteria/core/v2/server"
	"go.uber.org/zap"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"
)

type tailLifecycleConnFactory struct{ ip string }

func (f tailLifecycleConnFactory) New(net.Addr) (net.PacketConn, error) {
	return net.ListenUDP("udp", &net.UDPAddr{IP: net.ParseIP(f.ip)})
}

type tailLifecycleFixture struct {
	h        *Hysteria2
	tag      string
	addr     *net.UDPAddr
	roots    *x509.CertPool
	tcp, udp string
}

func newTailLifecycleFixture(t testing.TB, deviceLimit int, ctl *tailLifecycleControl) *tailLifecycleFixture {
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
	auth := &V2bX{usersMap: map[string]int{}}
	n := Hysteria2node{Auth: auth, Tag: tag, Logger: h.Logger, EventLogger: &serverLogger{Auth: auth, Tag: tag, logger: h.Logger}, TrafficLogger: &HookServer{Auth: auth, Tag: tag, logger: h.Logger}}
	cfg, err := n.getHyConfig(info, &conf.Options{ListenIP: "127.0.0.1", CertConfig: &conf.CertConfig{CertMode: "file", CertFile: cert, KeyFile: keyfile}}, &serverConfig{})
	if err != nil {
		t.Fatal(err)
	}
	cfg.Authenticator = auth
	if ctl != nil {
		cfg.TrafficLogger = &tailLifecycleTraffic{TrafficLogger: n.TrafficLogger, c: ctl}
		cfg.EventLogger = &tailLifecycleEvents{EventLogger: n.EventLogger, c: ctl}
	}
	srv, err := server.NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	n.Hy2server = srv
	h.Hy2nodes[tag] = n
	go srv.Serve()
	if _, e = h.AddUsers(&vcore.AddUsersParams{Tag: tag, Users: users}); e != nil {
		t.Fatal(e)
	}
	return &tailLifecycleFixture{h, tag, addr, roots, echo.Addr().String(), udp.LocalAddr().String()}
}
func (f *tailLifecycleFixture) dial(ip string) (client.Client, error) {
	c, _, e := client.NewClient(&client.Config{ConnFactory: tailLifecycleConnFactory{ip}, ServerAddr: f.addr, Auth: "synthetic-live", TLSConfig: client.TLSConfig{ServerName: "test.invalid", RootCAs: f.roots}})
	return c, e
}
func tailLifecycleExchange(c net.Conn, p []byte) error {
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
func tailLifecycleSecondSource(t testing.TB) string {
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

type tailLifecycleControl struct {
	downstream                      bool
	entered                         chan uint64
	release, disconnected, untraced chan struct{}
	result                          chan bool
	once                            atomic.Bool
}
type tailLifecycleTraffic struct {
	server.TrafficLogger
	c *tailLifecycleControl
}

func (h *tailLifecycleTraffic) LogTraffic(id string, tx, rx uint64) bool {
	n := tx
	if h.c.downstream {
		n = rx
	}
	if n > 0 && h.c.once.CompareAndSwap(false, true) {
		h.c.entered <- n
		<-h.c.release
		ok := h.TrafficLogger.LogTraffic(id, tx, rx)
		h.c.result <- ok
		return ok
	}
	return h.TrafficLogger.LogTraffic(id, tx, rx)
}
func (h *tailLifecycleTraffic) UntraceStream(s server.HyStream) {
	h.TrafficLogger.UntraceStream(s)
	close(h.c.untraced)
}

type tailLifecycleEvents struct {
	server.EventLogger
	c *tailLifecycleControl
}

func (l *tailLifecycleEvents) Disconnect(a net.Addr, id string, e error) {
	l.EventLogger.Disconnect(a, id, e)
	close(l.c.disconnected)
}
func tailLifecycleWait(t *testing.T, ch <-chan struct{}, what string) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(3 * time.Second):
		t.Fatal(what)
	}
}
func TestTailLifecycleHy2RealLateCallback(t *testing.T) {
	ctl := &tailLifecycleControl{entered: make(chan uint64, 1), release: make(chan struct{}), disconnected: make(chan struct{}), untraced: make(chan struct{}), result: make(chan bool, 1)}
	f := newTailLifecycleFixture(t, 0, ctl)
	target, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer target.Close()
	accepted := make(chan net.Conn, 1)
	go func() {
		c, e := target.Accept()
		if e == nil {
			accepted <- c
		}
	}()
	client, e := f.dial("127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	defer client.Close()
	stream, e := client.TCP(target.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer stream.Close()
	remote := <-accepted
	defer remote.Close()
	if _, e = stream.Write([]byte("tail-data")); e != nil {
		t.Fatal(e)
	}
	var bytes uint64
	select {
	case bytes = <-ctl.entered:
	case <-time.After(3 * time.Second):
		t.Fatal("no ingress callback")
	}
	client.Close()
	tailLifecycleWait(t, ctl.disconnected, "no disconnect")
	remote.Close()
	select {
	case <-ctl.untraced:
		t.Fatal("stream completed before pending traffic callback")
	case <-time.After(30 * time.Millisecond):
	}
	close(ctl.release)
	var ok bool
	select {
	case ok = <-ctl.result:
	case <-time.After(3 * time.Second):
		t.Fatal("callback did not finish")
	}
	tailLifecycleWait(t, ctl.untraced, "stream never drained")
	rows, e := f.h.GetUserTrafficSlice(f.tag, false)
	if e != nil {
		t.Fatal(e)
	}
	var up int64
	for _, r := range rows {
		up += r.Upload
	}
	t.Logf("real_QUIC_ingress=%d callback_accepted=%v sampled_upload=%d disconnect_then_callback_then_untrace=true", bytes, ok, up)
	if !ok || up != int64(bytes) {
		t.Fatal("late ingress lost", ok, up, bytes)
	}
	auth := f.h.Hy2nodes[f.tag].Auth
	deadline := time.Now().Add(3 * time.Second)
	for {
		auth.mutex.Lock()
		left := len(auth.sessions)
		auth.mutex.Unlock()
		if left == 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("session handle did not drain")
		}
		time.Sleep(time.Millisecond)
	}
}

// The UDP manager must hold the physical session until its receive/send logger
// has returned, even if QUIC shutdown is reported first.
func TestTailLifecycleRealUDPLateCallback(t *testing.T) {
	for _, downstream := range []bool{false, true} {
		t.Run(fmt.Sprintf("downstream=%v", downstream), func(t *testing.T) {
			ctl := &tailLifecycleControl{entered: make(chan uint64, 1), release: make(chan struct{}), disconnected: make(chan struct{}), untraced: make(chan struct{}), result: make(chan bool, 1), downstream: downstream}
			f := newTailLifecycleFixture(t, 0, ctl)
			c, e := f.dial("127.0.0.1")
			if e != nil {
				t.Fatal(e)
			}
			defer c.Close()
			u, e := c.UDP()
			if e != nil {
				t.Fatal(e)
			}
			defer u.Close()
			if e = u.Send([]byte("tail-data"), f.udp); e != nil {
				t.Fatal(e)
			}
			var n uint64
			select {
			case n = <-ctl.entered:
			case <-time.After(3 * time.Second):
				t.Fatal("no UDP callback")
			}
			c.Close()
			tailLifecycleWait(t, ctl.disconnected, "no UDP disconnect")
			a := f.h.Hy2nodes[f.tag].Auth
			a.mutex.Lock()
			retained := len(a.sessions)
			a.mutex.Unlock()
			close(ctl.release)
			if retained != 1 {
				t.Fatal("UDP session dropped before callback", retained)
			}
			select {
			case ok := <-ctl.result:
				if !ok {
					t.Fatal("UDP callback rejected")
				}
			case <-time.After(3 * time.Second):
				t.Fatal("UDP callback blocked")
			}
			deadline := time.Now().Add(3 * time.Second)
			for {
				a.mutex.Lock()
				left := len(a.sessions)
				a.mutex.Unlock()
				if left == 0 {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("UDP handle leaked")
				}
				time.Sleep(time.Millisecond)
			}
			rows, err := f.h.GetUserTrafficSlice(f.tag, true)
			if err != nil || len(rows) != 1 {
				t.Fatal(rows, err)
			}
			if rows[0].Upload != int64(n) || (downstream && rows[0].Download != int64(n)) || (!downstream && rows[0].Download != 0) {
				t.Fatal("UDP accounting", rows)
			}
			t.Logf("delayed UDP downstream=%v bytes=%d attributed exactly once, session reclaimed", downstream, n)
		})
	}
}
