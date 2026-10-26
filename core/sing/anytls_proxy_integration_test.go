package sing

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"net/netip"
	"sync/atomic"
	"testing"
	"time"

	client "github.com/anytls/sing-anytls"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	server "github.com/sagernet/sing-box/protocol/anytls"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/uot"
)

type proxyTestRouter struct {
	adapter.Router
	sources chan adapter.InboundContext
}

func (r *proxyTestRouter) RoutePacketConnectionEx(ctx context.Context, c N.PacketConn, m adapter.InboundContext, closeHandler N.CloseHandlerFunc) {
	r.sources <- m
	c.Close()
	if closeHandler != nil {
		closeHandler(nil)
	}
}

func (r *proxyTestRouter) RouteConnectionEx(ctx context.Context, c net.Conn, m adapter.InboundContext, closeHandler N.CloseHandlerFunc) {
	r.sources <- m
	go func() {
		defer c.Close()
		c.SetDeadline(time.Now().Add(3 * time.Second))
		b := make([]byte, 4)
		_, err := io.ReadFull(c, b)
		if err == nil {
			_, err = c.Write(b)
		}
		if closeHandler != nil {
			closeHandler(err)
		}
	}()
}

// This exercises the actual patched sing AnyTLS inbound, TLS, password auth,
// logical stream creation, and the metadata supplied to the V2bX routing hook.
// The relay is simulated locally; no external node or panel is contacted.
func TestAnyTLSProxyActualInbound(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"test.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	key, err := x509.MarshalPKCS8PrivateKey(priv)
	if err != nil {
		t.Fatal(err)
	}
	certPEM := string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}))
	keyPEM := string(pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: key}))
	roots := x509.NewCertPool()
	roots.AppendCertsFromPEM([]byte(certPEM))
	v1 := []byte("PROXY TCP4 198.51.100.7 203.0.113.2 12345 443\r\n")
	v2 := []byte("\r\n\r\n\x00\r\nQUIT\n")
	v2 = append(v2, 0x21, 0x21, 0, 36)
	v2 = append(v2, netip.MustParseAddr("2001:db8::7").AsSlice()...)
	v2 = append(v2, netip.MustParseAddr("2001:db8::2").AsSlice()...)
	ports := make([]byte, 4)
	binary.BigEndian.PutUint16(ports, 12345)
	binary.BigEndian.PutUint16(ports[2:], 443)
	v2 = append(v2, ports...)
	v2Empty := append([]byte(nil), v2...)
	for i := 32; i < 48; i++ {
		v2Empty[i] = 0
	}
	v2Empty[50], v2Empty[51] = 0, 0
	for _, tc := range []struct {
		name     string
		enabled  bool
		trusted  string
		header   []byte
		password string
		want     string
		reject   bool
	}{
		{"disabled-original", false, "", nil, "test-only-password", "127.0.0.1", false},
		{"v1-ipv4", true, "127.0.0.1/32", v1, "test-only-password", "198.51.100.7", false},
		{"v2-ipv6", true, "127.0.0.1/32", v2, "test-only-password", "2001:db8::7", false},
		{"optional-no-list", true, "", v2, "test-only-password", "2001:db8::7", false},
		{"optional-wrong-password", true, "", v2, "wrong-password", "", true},
		{"optional-missing-header", true, "", nil, "test-only-password", "", true},
		{"v2-empty-destination", true, "127.0.0.1/32", v2Empty, "test-only-password", "2001:db8::7", false},
		{"missing-header", true, "127.0.0.1/32", nil, "test-only-password", "", true},
		{"untrusted-peer", true, "192.0.2.9/32", v1, "test-only-password", "", true},
		{"wrong-password", true, "127.0.0.1/32", v1, "wrong-test-password", "", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			router := &proxyTestRouter{sources: make(chan adapter.InboundContext, 8)}
			opts := option.AnyTLSInboundOptions{Users: []option.AnyTLSUser{{Name: "test-user", Password: "test-only-password"}}, InboundTLSOptionsContainer: option.InboundTLSOptionsContainer{TLS: &option.InboundTLSOptions{Enabled: true, Certificate: []string{certPEM}, Key: []string{keyPEM}}}}
			if tc.enabled {
				opts.TrustedProxyProtocol = &option.AnyTLSProxyProtocolOptions{Enabled: true}
				if tc.trusted != "" {
					opts.TrustedProxyProtocol.TrustedProxies = []string{tc.trusted}
				}
			}
			in, err := server.NewInbound(ctx, router, log.NewNOPFactory().Logger(), "test-anytls", opts)
			if err != nil {
				t.Fatal(err)
			}
			defer in.Close()
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			go func() {
				for {
					conn, err := listener.Accept()
					if err != nil {
						return
					}
					go in.(*server.Inbound).NewConnectionEx(ctx, conn, adapter.InboundContext{Source: M.SocksaddrFromNet(conn.RemoteAddr())}, nil)
				}
			}()
			var dials atomic.Int32
			cl, err := client.NewClient(ctx, client.ClientConfig{Password: tc.password, Logger: log.NewNOPFactory().Logger(), IdleSessionCheckInterval: time.Minute, IdleSessionTimeout: time.Minute, MinIdleSession: 1, DialOut: func(ctx context.Context) (net.Conn, error) {
				dials.Add(1)
				conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", listener.Addr().String())
				if err != nil {
					return nil, err
				}
				conn.SetDeadline(time.Now().Add(2 * time.Second))
				if len(tc.header) > 0 {
					if _, err = conn.Write(tc.header); err != nil {
						conn.Close()
						return nil, err
					}
				}
				tc := tls.Client(conn, &tls.Config{ServerName: "test.invalid", RootCAs: roots, MinVersion: tls.VersionTLS13})
				if err = tc.HandshakeContext(ctx); err != nil {
					conn.Close()
					return nil, err
				}
				conn.SetDeadline(time.Time{})
				return tc, nil
			}})
			if err != nil {
				t.Fatal(err)
			}
			defer cl.Close()
			for i := 0; i < 2; i++ {
				stream, err := cl.CreateProxy(ctx, M.ParseSocksaddr("example.com:443"))
				if err == nil {
					stream.SetDeadline(time.Now().Add(time.Second))
					_, err = stream.Write([]byte("ping"))
					if err == nil {
						b := make([]byte, 4)
						_, err = io.ReadFull(stream, b)
						if err == nil && string(b) != "ping" {
							t.Fatal("payload corrupted")
						}
					}
					stream.Close()
				}
				if tc.reject {
					if err == nil {
						t.Fatal("invalid connection succeeded")
					}
					select {
					case m := <-router.sources:
						t.Fatal("unauthorized connection reached router", m.Source)
					default:
					}
					return
				}
				if err != nil {
					t.Fatal(err)
				}
				select {
				case m := <-router.sources:
					if m.Source.Addr.String() != tc.want || m.User != "test-user" || m.Inbound != "test-anytls" || m.Destination.String() != "example.com:443" {
						t.Fatalf("routing identity changed: %+v", m)
					}
				case <-ctx.Done():
					t.Fatal("no route metadata")
				}
				time.Sleep(30 * time.Millisecond)
			}
			t.Logf("two logical streams, physical connections: %d", dials.Load())
			udpStream, err := cl.CreateProxy(ctx, M.Socksaddr{Fqdn: uot.LegacyMagicAddress, Port: 443})
			if err != nil {
				t.Fatal(err)
			}
			select {
			case m := <-router.sources:
				if m.Source.Addr.String() != tc.want || m.User != "test-user" || m.Domain != uot.LegacyMagicAddress {
					t.Fatalf("UoT source identity lost: %+v", m)
				}
			case <-ctx.Done():
				t.Fatal("no UoT metadata")
			}
			udpStream.Close()
		})
	}
}
