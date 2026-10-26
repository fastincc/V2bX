package sing

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/limiter"
	client "github.com/anytls/sing-anytls"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	server "github.com/sagernet/sing-box/protocol/anytls"
	B "github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type reviewRouter struct {
	adapter.Router
	forward      adapter.Router
	testProtocol string
	hook         HookServer
	done         chan struct{}
	sources      chan string
}

func (r *reviewRouter) RouteConnectionEx(ctx context.Context, c net.Conn, m adapter.InboundContext, closeHandler N.CloseHandlerFunc) {
	if r.forward != nil {
		r.forward.RouteConnectionEx(ctx, c, m, closeHandler)
		return
	}
	if r.testProtocol != "" {
		m.Protocol = r.testProtocol
	}
	c = r.hook.RoutedConnection(ctx, c, m, nil, nil)
	r.sources <- m.Source.Addr.String()
	go func() {
		defer func() {
			c.Close()
			r.done <- struct{}{}
			if closeHandler != nil {
				closeHandler(nil)
			}
		}()
		B.Copy(c, c)
	}()
}

func (r *reviewRouter) RoutePacketConnectionEx(ctx context.Context, c N.PacketConn, m adapter.InboundContext, closeHandler N.CloseHandlerFunc) {
	if r.forward != nil {
		r.forward.RoutePacketConnectionEx(ctx, c, m, closeHandler)
		return
	}
	r.sources <- m.Source.Addr.String()
	c.Close()
	r.done <- struct{}{}
	if closeHandler != nil {
		closeHandler(nil)
	}
}

type reviewFixture struct {
	listener net.Listener
	roots    *x509.CertPool
	router   *reviewRouter
	enabled  bool
	inbound  *server.Inbound
}

func newReviewFixture(t testing.TB, enabled bool) *reviewFixture {
	t.Helper()
	return newReviewFixtureUsers(t, enabled, []option.AnyTLSUser{{Name: "test-user", Password: "test-password"}}, log.NewNOPFactory().Logger())
}

func newReviewFixtureUsers(t testing.TB, enabled bool, users []option.AnyTLSUser, logger log.ContextLogger) *reviewFixture {
	return newReviewFixtureAudit(t, enabled, users, logger, false)
}

func newReviewFixtureAudit(t testing.TB, enabled bool, users []option.AnyTLSUser, logger log.ContextLogger, audit bool) *reviewFixture {
	t.Helper()
	ctx := context.Background()
	limiter.Init()
	var panelUsers []panel.UserInfo
	for i, u := range users {
		panelUsers = append(panelUsers, panel.UserInfo{Id: i + 123, Uuid: u.Name})
	}
	limiter.AddLimiter("review", &conf.LimitConfig{}, panelUsers, map[int]int{})
	pub, priv, e := ed25519.GenerateKey(rand.Reader)
	if e != nil {
		t.Fatal(e)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"review.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, e := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if e != nil {
		t.Fatal(e)
	}
	key, e := x509.MarshalPKCS8PrivateKey(priv)
	if e != nil {
		t.Fatal(e)
	}
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keypem := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key})
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM(cert)
	r := &reviewRouter{done: make(chan struct{}, 10000), sources: make(chan string, 10000)}
	opts := option.AnyTLSInboundOptions{AuditAccess: audit, Users: users, InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{TLS: &option.InboundTLSOptions{Enabled: true, Certificate: []string{string(cert)}, Key: []string{string(keypem)}}}}
	if enabled {
		opts.TrustedProxyProtocol = &option.AnyTLSProxyProtocolOptions{Enabled: true, Version: 2}
	}
	in, e := server.NewInbound(ctx, r, logger, "review", opts)
	if e != nil {
		t.Fatal(e)
	}
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { l.Close(); in.Close(); limiter.DeleteLimiter("review") })
	go func() {
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			go in.(*server.Inbound).NewConnectionEx(ctx, c, adapter.InboundContext{Source: M.SocksaddrFromNet(c.RemoteAddr())}, nil)
		}
	}()
	return &reviewFixture{l, roots, r, enabled, in.(*server.Inbound)}
}
func (f *reviewFixture) client(t testing.TB) *client.Client {
	t.Helper()
	return f.clientPassword(t, "test-password")
}
func (f *reviewFixture) clientPassword(t testing.TB, password string) *client.Client {
	t.Helper()
	cl, e := client.NewClient(context.Background(), client.ClientConfig{Password: password, Logger: log.NewNOPFactory().Logger(), IdleSessionCheckInterval: time.Minute, IdleSessionTimeout: time.Minute, MinIdleSession: 1, DialOut: func(ctx context.Context) (net.Conn, error) {
		c, e := (&net.Dialer{}).DialContext(ctx, "tcp", f.listener.Addr().String())
		if e != nil {
			return nil, e
		}
		c.SetDeadline(time.Now().Add(10 * time.Second))
		if f.enabled {
			h := append([]byte("\r\n\r\n\x00\r\nQUIT\n"), 0x21, 0x11, 0, 12, 198, 51, 100, 9, 0, 0, 0, 0, 0x30, 0x39, 0, 0)
			if _, e = c.Write(h); e != nil {
				c.Close()
				return nil, e
			}
		}
		tc := tls.Client(c, &tls.Config{ServerName: "review.invalid", RootCAs: f.roots})
		if e = tc.HandshakeContext(ctx); e != nil {
			c.Close()
			return nil, e
		}
		c.SetDeadline(time.Time{})
		return tc, nil
	}})
	if e != nil {
		t.Fatal(e)
	}
	return cl
}
func reviewEcho(t testing.TB, s net.Conn, payload []byte) {
	t.Helper()
	s.SetDeadline(time.Now().Add(5 * time.Second))
	if _, e := s.Write(payload); e != nil {
		t.Fatal(e)
	}
	b := make([]byte, len(payload))
	if _, e := io.ReadFull(s, b); e != nil {
		t.Fatal(e)
	}
	if !bytes.Equal(b, payload) {
		t.Fatal("payload mismatch")
	}
}
func TestAnyTLSProxyRealHookAccounting(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		t.Run(name, func(t *testing.T) {
			f := newReviewFixture(t, enabled)
			cl := f.client(t)
			defer cl.Close()
			payload := bytes.Repeat([]byte("count-test"), 4096)
			for i := 0; i < 3; i++ {
				s, e := cl.CreateProxy(context.Background(), M.ParseSocksaddr("echo.invalid:80"))
				if e != nil {
					t.Fatal(e)
				}
				reviewEcho(t, s, payload)
				s.Close()
				select {
				case <-f.router.done:
				case <-time.After(3 * time.Second):
					t.Fatal("stream not closed")
				}
				source := <-f.router.sources
				want := "127.0.0.1"
				if enabled {
					want = "198.51.100.9"
				}
				if source != want {
					t.Fatal("source mismatch", source)
				}
			}
			v, ok := f.router.hook.counter.Load("review")
			if !ok {
				t.Fatal("missing traffic counter")
			}
			c := v.(*counter.TrafficCounter)
			want := int64(len(payload) * 3)
			if c.GetUpCount("test-user") != want || c.GetDownCount("test-user") != want || c.Len() != 1 {
				t.Fatalf("counter mismatch: up=%d down=%d users=%d want=%d", c.GetUpCount("test-user"), c.GetDownCount("test-user"), c.Len(), want)
			}
			t.Logf("up=%d down=%d, one authenticated user, three streams", want, want)
		})
	}
}
func BenchmarkAnyTLSProxyConnection(b *testing.B) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		b.Run(name, func(b *testing.B) {
			f := newReviewFixture(b, enabled)
			payload := []byte("ping")
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				cl := f.client(b)
				s, e := cl.CreateProxy(context.Background(), M.ParseSocksaddr("echo.invalid:80"))
				if e != nil {
					b.Fatal(e)
				}
				reviewEcho(b, s, payload)
				s.Close()
				cl.Close()
				<-f.router.done
				<-f.router.sources
			}
		})
	}
}
func BenchmarkAnyTLSProxyStream(b *testing.B) {
	for _, enabled := range []bool{false, true} {
		name := "disabled"
		if enabled {
			name = "enabled"
		}
		b.Run(name, func(b *testing.B) {
			f := newReviewFixture(b, enabled)
			cl := f.client(b)
			defer cl.Close()
			s, e := cl.CreateProxy(context.Background(), M.ParseSocksaddr("echo.invalid:80"))
			if e != nil {
				b.Fatal(e)
			}
			defer s.Close()
			payload := bytes.Repeat([]byte("x"), 32768)
			reviewEcho(b, s, []byte("warm"))
			b.SetBytes(int64(len(payload)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				reviewEcho(b, s, payload)
			}
		})
	}
}
