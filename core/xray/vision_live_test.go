//go:build linux && vision_live

package xray

import (
	"bytes"
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/dynamic"
	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/conf"
	vcore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/pires/go-proxyproto"
	xapplog "github.com/xtls/xray-core/app/log"
	"github.com/xtls/xray-core/app/proxyman"
	xlog "github.com/xtls/xray-core/common/log"
	xn "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/serial"
	"github.com/xtls/xray-core/common/uuid"
	xc "github.com/xtls/xray-core/core"
	coreconf "github.com/xtls/xray-core/infra/conf"
	xtls "github.com/xtls/xray-core/transport/internet/tls"
)

type visionWire struct {
	net.Conn
	read, write *atomic.Int64
}

func (c *visionWire) Read(p []byte) (int, error) {
	n, e := c.Conn.Read(p)
	c.read.Add(int64(n))
	return n, e
}
func (c *visionWire) Write(p []byte) (int, error) {
	n, e := c.Conn.Write(p)
	c.write.Add(int64(n))
	return n, e
}

type visionFixture struct {
	x            *Xray
	client       *xc.Instance
	l            *limiter.Limiter
	tag          string
	u            panel.UserInfo
	info         *panel.NodeInfo
	target       net.Listener
	roots        *x509.CertPool
	up, down     atomic.Int64
	innerVersion uint16
	source       atomic.Int32
}

func newVisionFixture(t *testing.T, enabled bool, fixed int) *visionFixture {
	t.Helper()
	id := uuid.New()
	f := &visionFixture{tag: "vision-live", u: panel.UserInfo{Id: 51, Uuid: id.String(), DeviceLimit: 1, SpeedLimit: fixed}}
	f.source.Store(9)
	f.innerVersion = tls.VersionTLS13
	if os.Getenv("VISION_INNER_TLS12") != "" {
		f.innerVersion = tls.VersionTLS12
	}
	key, e := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "vision.test"}, DNSNames: []string{"vision.test"}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if e != nil {
		t.Fatal(e)
	}
	priv, _ := x509.MarshalPKCS8PrivateKey(key)
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: priv})
	dir := t.TempDir()
	cf, kf := filepath.Join(dir, "cert.pem"), filepath.Join(dir, "key.pem")
	os.WriteFile(cf, certPEM, 0600)
	os.WriteFile(kf, keyPEM, 0600)
	pair, e := tls.X509KeyPair(certPEM, keyPEM)
	if e != nil {
		t.Fatal(e)
	}
	f.roots = x509.NewCertPool()
	f.roots.AppendCertsFromPEM(certPEM)
	limiter.Init()
	f.l = limiter.AddLimiter(f.tag, &conf.LimitConfig{}, []panel.UserInfo{f.u}, nil)
	t.Cleanup(func() { limiter.DeleteLimiter(f.tag) })
	cfg := conf.NewXrayConfig()
	if os.Getenv("VISION_DEBUG") != "" {
		cfg.LogConfig.Level = "debug"
	}
	core, e := New(&conf.CoreConfig{XrayConfig: cfg})
	if e != nil {
		t.Fatal(e)
	}
	f.x = core.(*Xray)
	if e = f.x.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { f.x.Close() })
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	f.info = &panel.NodeInfo{Type: "vless", Security: panel.Tls, Common: &panel.CommonNode{ServerPort: port}, VAllss: &panel.VAllssNode{Network: "tcp", NetworkSettings: []byte(`{}`), Flow: "xtls-rprx-vision"}}
	opt := &conf.Options{ListenIP: "127.0.0.1", XrayOptions: conf.NewXrayOptions(), CertConfig: &conf.CertConfig{CertMode: "file", CertFile: cf, KeyFile: kf}}
	opt.XrayOptions.DisableSniffing = true
	opt.XrayOptions.EnableProxyProtocol = true
	opt.XrayOptions.EnableVisionSplice = enabled
	// The test uses immutable certificates: the pinned dependency's unrelated
	// certificate reload goroutine races its handshake reader. Production defaults
	// are unchanged; this fixture exercises the same built listener/dispatcher.
	inbound, e := buildInbound(opt, f.info, f.tag)
	if e != nil {
		t.Fatal(e)
	}
	rawReceiver, e := inbound.ReceiverSettings.GetInstance()
	if e != nil {
		t.Fatal(e)
	}
	receiver := rawReceiver.(*proxyman.ReceiverConfig)
	for i, security := range receiver.StreamSettings.SecuritySettings {
		rawTLS, e := security.GetInstance()
		if e != nil {
			t.Fatal(e)
		}
		if cfg, ok := rawTLS.(*xtls.Config); ok {
			for _, certificate := range cfg.Certificate {
				certificate.OneTimeLoading = true
			}
			receiver.StreamSettings.SecuritySettings[i] = serial.ToTypedMessage(cfg)
		}
	}
	inbound.ReceiverSettings = serial.ToTypedMessage(receiver)
	f.x.dispatcher.SetVisionSplice(f.tag, enabled)
	if e = f.x.addInbound(inbound); e != nil {
		t.Fatal(e)
	}
	outbound, e := buildOutbound(opt, f.tag)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.x.addOutbound(outbound); e != nil {
		t.Fatal(e)
	}
	if _, e = f.x.AddUsers(&vcore.AddUsersParams{Tag: f.tag, NodeInfo: f.info, Users: []panel.UserInfo{f.u}}); e != nil {
		t.Fatal(e)
	}
	// Separate trusted ingress fixture injects PROXY v2 before the outer TLS.
	gate, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { gate.Close() })
	go func() {
		for {
			a, e := gate.Accept()
			if e != nil {
				return
			}
			go func() {
				defer a.Close()
				b, e := net.Dial("tcp", fmt.Sprintf("127.0.0.1:%d", port))
				if e != nil {
					return
				}
				defer b.Close()
				h := &proxyproto.Header{Version: 2, Command: proxyproto.PROXY, TransportProtocol: proxyproto.TCPv4, SourceAddr: &net.TCPAddr{IP: net.IPv4(198, 51, 100, byte(f.source.Load())), Port: 23456}, DestinationAddr: b.RemoteAddr()}
				if _, e = h.WriteTo(b); e != nil {
					return
				}
				done := make(chan struct{})
				go func() { io.Copy(b, a); b.Close(); close(done) }()
				io.Copy(a, b)
				a.Close()
				<-done
			}()
		}
	}()
	config := fmt.Sprintf(`{"log":{"loglevel":"none"},"outbounds":[{"protocol":"vless","settings":{"vnext":[{"address":"127.0.0.1","port":%d,"users":[{"id":%q,"encryption":"none","flow":"xtls-rprx-vision"}]}]},"streamSettings":{"network":"tcp","security":"tls","tlsSettings":{"serverName":"vision.test","certificates":[{"certificateFile":%q,"usage":"verify"}]}}}]}`, gate.Addr().(*net.TCPAddr).Port, f.u.Uuid, cf)
	if os.Getenv("VISION_DEBUG") != "" {
		config = strings.Replace(config, `"loglevel":"none"`, `"loglevel":"debug"`, 1)
	}
	var cc coreconf.Config
	if e = json.Unmarshal([]byte(config), &cc); e != nil {
		t.Fatal(e)
	}
	parsed, e := cc.Build()
	if e != nil {
		t.Fatal(e)
	}
	f.client, e = xc.New(parsed)
	if e != nil {
		t.Fatal(e)
	}
	if e = f.client.Start(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { f.client.Close() })
	f.target, e = net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { f.target.Close() })
	go func() {
		for {
			raw, e := f.target.Accept()
			if e != nil {
				return
			}
			go func() {
				defer raw.Close()
				c := tls.Server(&visionWire{Conn: raw, read: &f.up, write: &f.down}, &tls.Config{Certificates: []tls.Certificate{pair}, MinVersion: f.innerVersion, MaxVersion: f.innerVersion, CurvePreferences: []tls.CurveID{tls.X25519}})
				defer c.Close()
				var size [4]byte
				for {
					if _, e := io.ReadFull(c, size[:]); e != nil {
						return
					}
					request := binary.BigEndian.Uint32(size[:])
					upload := request&(1<<31) != 0
					n := int(request & ((1 << 31) - 1))
					if n > 64<<20 {
						return
					}
					if upload {
						if _, e := io.CopyN(io.Discard, c, int64(n)); e != nil {
							return
						}
						if _, e := c.Write([]byte{1}); e != nil {
							return
						}
						continue
					}
					b := bytes.Repeat([]byte{69}, min(n, 32<<10))
					for n > 0 {
						v := min(n, len(b))
						if _, e := c.Write(b[:v]); e != nil {
							return
						}
						n -= v
					}
				}
			}()
		}
	}()
	return f
}
func (f *visionFixture) open() (*tls.Conn, error) {
	dest, _ := xn.ParseDestination("tcp:" + f.target.Addr().String())
	raw, e := xc.Dial(context.Background(), f.client, dest)
	if e != nil {
		return nil, e
	}
	raw.SetDeadline(time.Now().Add(10 * time.Second))
	c := tls.Client(raw, &tls.Config{ServerName: "vision.test", RootCAs: f.roots, MinVersion: f.innerVersion, MaxVersion: f.innerVersion, CurvePreferences: []tls.CurveID{tls.X25519}})
	if e = c.Handshake(); e != nil {
		raw.Close()
		return nil, e
	}
	return c, nil
}
func (f *visionFixture) dial(t *testing.T) *tls.Conn {
	t.Helper()
	c, e := f.open()
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.Close() })
	// Prime with complete small TLS application records before bulk payloads.
	// Upstream Vision intentionally keeps buffering if its initial record
	// boundary detector never sees a complete application record.
	visionRead(t, c, 64)
	visionRead(t, c, 64)
	return c
}
func visionRead(t *testing.T, c *tls.Conn, n int) time.Duration {
	t.Helper()
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(n))
	c.SetDeadline(time.Now().Add(12 * time.Second))
	start := time.Now()
	if _, e := c.Write(size[:]); e != nil {
		t.Fatal(e)
	}
	p := make([]byte, n)
	if _, e := io.ReadFull(c, p); e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(p, bytes.Repeat([]byte{69}, n)) {
		t.Fatal("payload corrupted")
	}
	return time.Since(start)
}
func (f *visionFixture) rule(revision int64, speed int, enabled bool) {
	u := f.u
	u.DynamicSpeedLimit = &dynamic.Policy{Version: 1, Revision: revision, Enabled: enabled, WindowSeconds: 60, ThresholdBytes: 1, SpeedMbps: speed, DurationSeconds: 30}
	f.l.UpdateDynamicPolicies(f.tag, []panel.UserInfo{u})
}
func TestVisionRealSpliceHotRateIPDeviceBilling(t *testing.T) {
	f := newVisionFixture(t, true, 0)
	c := f.dial(t)
	for i := 0; i < 4; i++ {
		visionRead(t, c, 256<<10)
	}
	calls, _ := f.x.dispatcher.VisionSpliceStats(f.tag)
	if calls == 0 {
		t.Fatal("actual Vision did not enter managed splice")
	}
	f.rule(1, 1, true)
	h := f.l.DynamicControl(format.UserTag(f.tag, f.u.Uuid))
	h.Observe(1)
	f.l.TickDynamicPolicies(time.Now())
	one := visionRead(t, c, 125000)
	if one < 850*time.Millisecond || one > 3*time.Second {
		t.Fatal("live 1Mbps", one)
	}
	f.rule(2, 4, true)
	four := visionRead(t, c, 125000)
	if four < 180*time.Millisecond || four > time.Second {
		t.Fatal("live 4Mbps", four)
	}
	f.rule(3, 4, false)
	off := visionRead(t, c, 125000)
	if off > 500*time.Millisecond {
		t.Fatal("live off", off)
	}
	var up, down int64
	for end := time.Now().Add(time.Second); time.Now().Before(end); {
		rows, e := f.x.GetUserTrafficSlice(f.tag, false)
		if e != nil {
			t.Fatal(e)
		}
		up, down = 0, 0
		for _, r := range rows {
			up += r.Upload
			down += r.Download
		}
		if up == f.up.Load() && down == f.down.Load() {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if up != f.up.Load() || down != f.down.Load() {
		t.Fatalf("wire reconciliation up=%d/%d down=%d/%d", up, f.up.Load(), down, f.down.Load())
	}
	online, e := f.l.GetOnlineDevice()
	if e != nil || len(*online) != 1 || (*online)[0].IP != "198.51.100.9" {
		t.Fatal("PROXY real IP", e)
	}
	if _, reject := f.l.CheckLimit(format.UserTag(f.tag, f.u.Uuid), "198.51.100.10", true, true); !reject {
		t.Fatal("second device admitted")
	}
	f.source.Store(10)
	denied, err := f.open()
	f.source.Store(9)
	if err == nil {
		denied.Close()
		t.Fatal("actual second-device Vision handshake admitted")
	}
	if e = f.x.DrainTraffic(f.tag); e != nil {
		t.Fatal(e)
	}
	t.Logf("VISION_REAL one=%s four=%s off=%s up=%d down=%d splice_calls=%d ip_device=true", one, four, off, up, down, calls)
}

func visionUpload(t *testing.T, c *tls.Conn, n int) time.Duration {
	t.Helper()
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], uint32(n)|(1<<31))
	c.SetDeadline(time.Now().Add(12 * time.Second))
	start := time.Now()
	if _, e := c.Write(size[:]); e != nil {
		t.Fatal(e)
	}
	if _, e := io.CopyN(c, bytes.NewReader(bytes.Repeat([]byte{37}, n)), int64(n)); e != nil {
		t.Fatal(e)
	}
	var ack [1]byte
	if _, e := io.ReadFull(c, ack[:]); e != nil || ack[0] != 1 {
		t.Fatal("upload ack", e)
	}
	return time.Since(start)
}
func TestVisionRealStaticCeilingUploadAndRevoke(t *testing.T) {
	f := newVisionFixture(t, true, 4)
	c := f.dial(t)
	for i := 0; i < 4; i++ {
		visionRead(t, c, 256<<10)
	}
	f.rule(1, 1, true)
	h := f.l.DynamicControl(format.UserTag(f.tag, f.u.Uuid))
	h.Observe(1)
	f.l.TickDynamicPolicies(time.Now())
	slow := visionUpload(t, c, 125000)
	if slow < 850*time.Millisecond || slow > 3*time.Second {
		t.Fatal("upload live rate", slow)
	}
	f.rule(2, 20, true)
	fast := visionRead(t, c, 250000)
	if fast < 400*time.Millisecond || fast > 2*time.Second {
		t.Fatal("fixed ceiling", fast)
	}
	if e := f.x.DelUsers([]panel.UserInfo{f.u}, f.tag, f.info); e != nil {
		t.Fatal(e)
	}
	c.SetDeadline(time.Now().Add(time.Second))
	var p [1]byte
	if _, e := c.Read(p[:]); e == nil {
		t.Fatal("revoked connection survived")
	}
	if e := f.x.DrainTraffic(f.tag); e != nil {
		t.Fatal(e)
	}
	rows, e := f.x.GetUserTrafficSlice(f.tag, true)
	if e != nil || len(rows) != 1 || rows[0].Upload < 125000 || rows[0].Download < 250000 {
		t.Fatal("revoked tail", e)
	}
	rows, e = f.x.GetUserTrafficSlice(f.tag, true)
	if e != nil || len(rows) != 0 {
		t.Fatal("duplicate tail", e)
	}
	t.Logf("VISION_STATIC upload_1mbps=%s download_fixed4_dynamic20=%s revoke_and_tail=true", slow, fast)
}
func TestVisionRealRepeatedConnections(t *testing.T) {
	f := newVisionFixture(t, os.Getenv("VISION_RECONNECT_LEGACY") == "", 0)
	fds := func() int { v, _ := os.ReadDir("/proc/self/fd"); return len(v) }
	runtime.GC()
	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	before := fds()
	for i := 0; i < 30; i++ {
		c := f.dial(t)
		visionRead(t, c, 128<<10)
		visionRead(t, c, 128<<10)
		c.Close()
	}
	if e := f.x.DrainTraffic(f.tag); e != nil {
		t.Fatal(e)
	}
	// The fixture's TCP gateway uses Go ReadFrom, whose kernel pipes are pooled
	// until a GC cycle. Clear that pool before checking for leaked descriptors.
	runtime.GC()
	runtime.GC()
	time.Sleep(20 * time.Millisecond)
	// Xray's existing outbound half-close timeout is 4 seconds.
	deadline := time.Now().Add(6 * time.Second)
	for fds() > before+3 && time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(20 * time.Millisecond)
	}
	if after := fds(); after > before+3 {
		entries, _ := os.ReadDir("/proc/self/fd")
		for _, entry := range entries {
			target, _ := os.Readlink("/proc/self/fd/" + entry.Name())
			t.Log("FD", entry.Name(), target)
		}
		stack := make([]byte, 1<<20)
		n := runtime.Stack(stack, true)
		t.Log(string(stack[:n]))
		t.Fatalf("fd leak %d -> %d", before, after)
	}
	t.Logf("VISION_RECONNECT connections=30 fd_before=%d fd_after=%d", before, fds())
}
func TestVisionRealLegacyDefault(t *testing.T) {
	f := newVisionFixture(t, false, 0)
	c := f.dial(t)
	visionRead(t, c, 256<<10)
	visionUpload(t, c, 125000)
	if calls, _ := f.x.dispatcher.VisionSpliceStats(f.tag); calls != 0 {
		t.Fatal("default used prototype")
	}
}

type visionSpliceLogObserver struct {
	previous xlog.Handler
	calls    atomic.Int64
}

func (o *visionSpliceLogObserver) Handle(msg xlog.Message) {
	// The fixture's TCP gateway can use Go splice, but cannot emit this Xray
	// transition. The in-process client has no raw inbound TCP writer to splice.
	if strings.Contains(msg.String(), "CopyRawConn splice") {
		o.calls.Add(1)
	}
	o.previous.Handle(msg)
}

func observeVisionSplice(t *testing.T, f *visionFixture) *visionSpliceLogObserver {
	t.Helper()
	previous := f.client.GetFeature((*xapplog.Instance)(nil)).(xlog.Handler)
	o := &visionSpliceLogObserver{previous: previous}
	xlog.RegisterHandler(o)
	t.Cleanup(func() { xlog.RegisterHandler(previous) })
	return o
}

func TestVisionRealDefaultSpliceSnapshotsAndRevoke(t *testing.T) {
	if os.Getenv("VISION_INNER_TLS12") != "" {
		t.Skip("TLS 1.2 is covered by the buffered-fallback test")
	}
	f := newVisionFixture(t, false, 0)
	logs := observeVisionSplice(t, f)
	c := f.dial(t)
	var persistedUp, persistedDown int64
	persist := func(rows []panel.UserTraffic) error {
		for _, r := range rows {
			if r.UID != f.u.Id {
				t.Fatal("wrong user attribution", r.UID)
			}
			persistedUp += r.Upload
			persistedDown += r.Download
		}
		return nil
	}
	for i := 0; i < 3; i++ {
		visionRead(t, c, 256<<10)
		visionUpload(t, c, 32768)
		if i == 0 {
			failed := errors.New("synthetic durable-save failure")
			if e := f.x.PersistTrafficSnapshot(f.tag, func([]panel.UserTraffic) error { return failed }); e != failed {
				t.Fatal("failed snapshot was not retained", e)
			}
		}
		for end := time.Now().Add(time.Second); time.Now().Before(end); {
			if e := f.x.PersistTrafficSnapshot(f.tag, persist); e != nil {
				t.Fatal(e)
			}
			if persistedUp == f.up.Load() && persistedDown == f.down.Load() {
				break
			}
			time.Sleep(time.Millisecond)
		}
		if persistedUp != f.up.Load() || persistedDown != f.down.Load() {
			t.Fatalf("open-stream snapshot %d: up=%d/%d down=%d/%d", i, persistedUp, f.up.Load(), persistedDown, f.down.Load())
		}
	}
	if logs.calls.Load() == 0 {
		t.Fatal("default Vision never reached Xray raw splice (legacy -18 negative control)")
	}
	if calls, _ := f.x.dispatcher.VisionSpliceStats(f.tag); calls != 0 {
		t.Fatal("default enabled adjustable prototype")
	}
	f.rule(1, 1, true)
	h := f.l.DynamicControl(format.UserTag(f.tag, f.u.Uuid))
	if h.Status().Enabled {
		t.Fatal("default Vision accepted an unsupported panel policy")
	}
	online, e := f.l.GetOnlineDevice()
	if e != nil || len(*online) != 1 || (*online)[0].IP != "198.51.100.9" {
		t.Fatal("PROXY real IP", e)
	}
	f.source.Store(10)
	denied, e := f.open()
	f.source.Store(9)
	if e == nil {
		denied.Close()
		t.Fatal("actual second device admitted")
	}
	if e = f.x.DelUsers([]panel.UserInfo{f.u}, f.tag, f.info); e != nil {
		t.Fatal(e)
	}
	c.SetReadDeadline(time.Now().Add(time.Second))
	var b [1]byte
	if _, e = c.Read(b[:]); e == nil {
		t.Fatal("revoked default splice survived")
	} else if nerr, ok := e.(net.Error); ok && nerr.Timeout() {
		t.Fatal("revocation left raw copy blocked", e)
	}
	if e = f.x.DrainTraffic(f.tag); e != nil {
		t.Fatal(e)
	}
	if e = f.x.PersistTrafficSnapshot(f.tag, persist); e != nil {
		t.Fatal(e)
	}
	if e = f.x.PersistTrafficSnapshot(f.tag, func([]panel.UserTraffic) error {
		t.Fatal("retired traffic reported twice")
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	t.Logf("VISION_DEFAULT splice_transitions=%d snapshots=3 up=%d down=%d ip_device_revoke=true prototype=false", logs.calls.Load(), persistedUp, persistedDown)
}

func TestVisionRealDefaultStaticRemainsBuffered(t *testing.T) {
	f := newVisionFixture(t, false, 1)
	logs := observeVisionSplice(t, f)
	c := f.dial(t)
	// Consume the existing fixed bucket's burst before measuring steady state.
	visionRead(t, c, 250000)
	elapsed := visionRead(t, c, 125000)
	if elapsed < 800*time.Millisecond || elapsed > 3*time.Second {
		t.Fatal("existing static ceiling changed", elapsed)
	}
	if logs.calls.Load() != 0 {
		t.Fatal("default fixed ceiling was bypassed by raw copy")
	}
}
func TestVisionRealProfile(t *testing.T) {
	duration, e := time.ParseDuration(os.Getenv("VISION_PROFILE_DURATION"))
	if e != nil || duration <= 0 {
		t.Skip("explicit performance run only")
	}
	mode := os.Getenv("VISION_PROFILE_MODE")
	parallel, _ := strconv.Atoi(os.Getenv("VISION_PROFILE_PARALLEL"))
	if parallel < 1 {
		parallel = 1
	}
	if parallel > 8 {
		t.Fatal("parallel")
	}
	f := newVisionFixture(t, mode != "legacy", 0)
	if mode == "observe" {
		u := f.u
		u.DynamicSpeedLimit = &dynamic.Policy{Version: 1, Revision: 1, Enabled: true, WindowSeconds: 60, ThresholdBytes: 1 << 60, SpeedMbps: 1, DurationSeconds: 30}
		f.l.UpdateDynamicPolicies(f.tag, []panel.UserInfo{u})
	}
	cs := make([]*tls.Conn, parallel)
	for i := range cs {
		cs[i] = f.dial(t)
		visionRead(t, cs[i], 256<<10)
		visionRead(t, cs[i], 256<<10)
	}
	runtime.GC()
	var before, after syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &before)
	var total atomic.Int64
	start := time.Now()
	end := start.Add(duration)
	var wg sync.WaitGroup
	for _, c := range cs {
		wg.Add(1)
		go func(c *tls.Conn) {
			defer wg.Done()
			var size [4]byte
			binary.BigEndian.PutUint32(size[:], 8<<20)
			for time.Now().Before(end) {
				c.SetDeadline(time.Now().Add(20 * time.Second))
				if _, e := c.Write(size[:]); e != nil {
					t.Error(e)
					return
				}
				n, e := io.CopyN(io.Discard, c, 8<<20)
				total.Add(n)
				if e != nil {
					t.Error(e)
					return
				}
			}
		}(c)
	}
	wg.Wait()
	wall := time.Since(start).Seconds()
	syscall.Getrusage(syscall.RUSAGE_SELF, &after)
	cpu := func(r syscall.Rusage) float64 {
		return float64(r.Utime.Sec+r.Stime.Sec) + float64(r.Utime.Usec+r.Stime.Usec)/1e6
	}
	calls, raw := f.x.dispatcher.VisionSpliceStats(f.tag)
	if mode != "legacy" && calls == 0 {
		t.Fatal("profile did not splice")
	}
	out, _ := json.Marshal(map[string]any{"mode": mode, "parallel": parallel, "seconds": wall, "mib_s": float64(total.Load()) / wall / (1 << 20), "cpu_seconds": cpu(after) - cpu(before), "rss_bytes": after.Maxrss * 1024, "bytes": total.Load(), "splice_calls": calls, "splice_bytes": raw})
	t.Log("VISION_REAL_PROFILE " + string(out))
}

func TestVisionRealSharedQuotaAndBlockedHotUpdate(t *testing.T) {
	f := newVisionFixture(t, true, 0)
	a, b := f.dial(t), f.dial(t)
	for _, c := range []*tls.Conn{a, b} {
		visionRead(t, c, 256<<10)
		visionRead(t, c, 256<<10)
	}
	f.rule(1, 1, true)
	h := f.l.DynamicControl(format.UserTag(f.tag, f.u.Uuid))
	h.Observe(1)
	f.l.TickDynamicPolicies(time.Now())
	start := time.Now()
	var wg sync.WaitGroup
	wg.Add(2)
	go func() { defer wg.Done(); visionRead(t, a, 125000) }()
	go func() { defer wg.Done(); visionUpload(t, b, 125000) }()
	wg.Wait()
	shared := time.Since(start)
	if shared < 1800*time.Millisecond || shared > 4*time.Second {
		t.Fatal("shared duplex quota", shared)
	}
	done := make(chan struct{})
	start = time.Now()
	go func() { defer close(done); visionRead(t, a, 500000) }()
	time.Sleep(200 * time.Millisecond)
	f.rule(2, 20, true)
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("blocked transfer ignored new rate")
	}
	changed := time.Since(start)
	if changed > 1500*time.Millisecond {
		t.Fatal("slow wakeup", changed)
	}
	f.rule(3, 1, true)
	// Force an in-flight pipe/quota wait, revoke, then retain its accounting tail.
	var size [4]byte
	binary.BigEndian.PutUint32(size[:], 4<<20)
	if _, e := a.Write(size[:]); e != nil {
		t.Fatal(e)
	}
	time.Sleep(100 * time.Millisecond)
	if e := f.x.DelUsers([]panel.UserInfo{f.u}, f.tag, f.info); e != nil {
		t.Fatal(e)
	}
	if e := f.x.DrainTraffic(f.tag); e != nil {
		t.Fatal(e)
	}
	rows, e := f.x.GetUserTrafficSlice(f.tag, true)
	if e != nil || len(rows) != 1 || rows[0].Download < 500000 || rows[0].Download >= 6<<20 {
		t.Fatal("active revoke accounting", e)
	}
	if rows, e = f.x.GetUserTrafficSlice(f.tag, true); e != nil || len(rows) != 0 {
		t.Fatal("active revoke duplicated tail", e)
	}
	t.Logf("VISION_SHARED duplex_1mbps=%s blocked_to_20mbps=%s active_revoke=true", shared, changed)
}

func TestVisionRealTLS12BufferedFallback(t *testing.T) {
	t.Setenv("VISION_INNER_TLS12", "1")
	f := newVisionFixture(t, true, 0)
	c := f.dial(t)
	visionRead(t, c, 256<<10)
	f.rule(1, 1, true)
	h := f.l.DynamicControl(format.UserTag(f.tag, f.u.Uuid))
	h.Observe(1)
	f.l.TickDynamicPolicies(time.Now())
	elapsed := visionRead(t, c, 125000)
	if elapsed < 850*time.Millisecond || elapsed > 3*time.Second {
		t.Fatal("TLS12 fallback rate", elapsed)
	}
	if calls, _ := f.x.dispatcher.VisionSpliceStats(f.tag); calls != 0 {
		t.Fatal("unsafe TLS12 raw bypass")
	}
	t.Logf("VISION_TLS12 buffered_fallback=true elapsed=%s", elapsed)
}

func TestVisionRealConcurrentAccountingSnapshots(t *testing.T) {
	f := newVisionFixture(t, true, 0)
	c := f.dial(t)
	visionRead(t, c, 256<<10)
	visionRead(t, c, 256<<10)
	f.rule(1, 100, true)
	h := f.l.DynamicControl(format.UserTag(f.tag, f.u.Uuid))
	h.Observe(1)
	f.l.TickDynamicPolicies(time.Now())
	done := make(chan struct{})
	go func() { defer close(done); visionRead(t, c, 8<<20) }()
	var up, down int64
	samples := 0
	capture := func() {
		rows, e := f.x.GetUserTrafficSlice(f.tag, true)
		if e != nil {
			t.Fatal(e)
		}
		samples++
		for _, row := range rows {
			if row.UID != f.u.Id {
				t.Fatal("cross-user attribution")
			}
			up += row.Upload
			down += row.Download
		}
	}
	ticker := time.NewTicker(2 * time.Millisecond)
	defer ticker.Stop()
loop:
	for {
		select {
		case <-done:
			break loop
		case <-ticker.C:
			capture()
		}
	}
	for deadline := time.Now().Add(time.Second); time.Now().Before(deadline); {
		capture()
		if up == f.up.Load() && down == f.down.Load() {
			break
		}
		time.Sleep(time.Millisecond)
	}
	if up != f.up.Load() || down != f.down.Load() {
		t.Fatalf("snapshot reconciliation up=%d/%d down=%d/%d", up, f.up.Load(), down, f.down.Load())
	}
	calls, _ := f.x.dispatcher.VisionSpliceStats(f.tag)
	if calls == 0 {
		t.Fatal("no actual splice")
	}
	t.Logf("VISION_SNAPSHOTS samples=%d up=%d down=%d exact=true", samples, up, down)
}
