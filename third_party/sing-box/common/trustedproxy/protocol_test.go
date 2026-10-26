package trustedproxy

import (
	"bufio"
	"bytes"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/netip"
	"strings"
	"testing"
	"time"
)

type peerConn struct {
	net.Conn
	peer string
}

func (c peerConn) RemoteAddr() net.Addr { a, _ := net.ResolveTCPAddr("tcp", c.peer); return a }

func v2(src, dst string, tlv []byte) []byte {
	s, d := netip.MustParseAddr(src), netip.MustParseAddr(dst)
	payload := append(append([]byte{}, s.AsSlice()...), d.AsSlice()...)
	payload = append(payload, 0x30, 0x39, 0x01, 0xbb)
	payload = append(payload, tlv...)
	family := byte(0x11)
	if s.Is6() {
		family = 0x21
	}
	h := append(append([]byte{}, signature...), 0x21, family, 0, 0)
	binary.BigEndian.PutUint16(h[14:], uint16(len(payload)))
	return append(h, payload...)
}

func TestHeadersAndBufferedTLSBytes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		header []byte
		want   string
	}{
		{"v1ipv4", []byte("PROXY TCP4 198.51.100.7 203.0.113.2 12345 443\r\n"), "198.51.100.7:12345"},
		{"v1ipv6", []byte("PROXY TCP6 2001:db8::7 2001:db8::2 12345 443\r\n"), "[2001:db8::7]:12345"},
		{"v2ipv4", v2("198.51.100.7", "203.0.113.2", nil), "198.51.100.7:12345"},
		{"v2ipv6", v2("2001:db8::7", "2001:db8::2", nil), "[2001:db8::7]:12345"},
		{"v2tlv", v2("198.51.100.7", "203.0.113.2", []byte{0xea, 0, 2, 1, 2}), "198.51.100.7:12345"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			p, _ := New([]string{"192.0.2.1/32"}, time.Second, 0)
			payload := []byte{0x16, 0x03, 0x03, 0, 5, 1, 2, 3, 4, 5}
			go func() { _, _ = b.Write(append(tc.header, payload...)) }()
			c, err := p.Accept(peerConn{a, "192.0.2.1:1000"})
			if err != nil {
				t.Fatal(err)
			}
			if c.RemoteAddr().String() != tc.want {
				t.Fatal(c.RemoteAddr())
			}
			got := make([]byte, len(payload))
			if _, err = io.ReadFull(c, got); err != nil || !bytes.Equal(got, payload) {
				t.Fatalf("prefetched TLS bytes lost: %x %v", got, err)
			}
			go func() { _, _ = b.Write([]byte("next")) }()
			got = make([]byte, 4)
			if _, err = io.ReadFull(c, got); err != nil || string(got) != "next" {
				t.Fatal("subsequent reads failed", err)
			}
		})
	}
}

func TestRejectInvalidHeaders(t *testing.T) {
	local := v2("198.51.100.7", "203.0.113.2", nil)
	local[12] = 0x20
	udp := v2("198.51.100.7", "203.0.113.2", nil)
	udp[13] = 0x12
	oversize := v2("198.51.100.7", "203.0.113.2", nil)
	binary.BigEndian.PutUint16(oversize[14:], 65535)
	for _, h := range [][]byte{
		[]byte{0x16, 0x03, 0x03}, []byte("PROXY UNKNOWN\r\n"), []byte("PROXY TCP4 198.51.100.1 203.0.113.2 123 443\n"),
		[]byte("PROXY TCP4 ::1 ::1 123 443\r\n"), []byte("PROXY TCP4 0.0.0.0 203.0.113.2 123 443\r\n"),
		[]byte("PROXY TCP4 198.51.100.1 203.0.113.2 65536 443\r\n"), []byte("PROXY " + strings.Repeat("a", 200)),
		local, udp, oversize, v2("198.51.100.7", "203.0.113.2", []byte{1, 0, 8, 1}),
	} {
		a, b := net.Pipe()
		p, _ := New([]string{"192.0.2.0/24"}, 50*time.Millisecond, 0)
		go func() { _, _ = b.Write(h); b.Close() }()
		_, err := p.Accept(peerConn{a, "192.0.2.1:1000"})
		a.Close()
		b.Close()
		if err == nil {
			t.Fatalf("accepted invalid header %x", h)
		}
	}
}

func TestUntrustedPeerIsRejectedBeforeRead(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	p, _ := New([]string{"192.0.2.1/32"}, time.Second, 0)
	start := time.Now()
	_, err := p.Accept(peerConn{a, "192.0.2.2:1000"})
	if err == nil || time.Since(start) > 200*time.Millisecond {
		t.Fatal("untrusted peer was not rejected immediately", err)
	}
}

func TestTimeoutAndDeadlineReset(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	p, _ := New([]string{"192.0.2.1/32"}, 20*time.Millisecond, 0)
	_, err := p.Accept(peerConn{a, "192.0.2.1:1000"})
	if err == nil {
		t.Fatal("missing header did not time out")
	}
	// Successful parsing must clear the temporary deadline, otherwise long-lived
	// AnyTLS sessions would fail after HeaderTimeoutSeconds.
	a2, b2 := net.Pipe()
	defer a2.Close()
	defer b2.Close()
	go func() {
		_, _ = b2.Write([]byte("PROXY TCP4 198.51.100.7 203.0.113.2 12345 443\r\n"))
		time.Sleep(60 * time.Millisecond)
		_, _ = b2.Write([]byte("ok"))
	}()
	c, err := p.Accept(peerConn{a2, "192.0.2.1:1000"})
	if err != nil {
		t.Fatal(err)
	}
	got := make([]byte, 2)
	if _, err = io.ReadFull(c, got); err != nil || string(got) != "ok" {
		t.Fatal("deadline not cleared", err)
	}
}

func TestPolicyValidationAndVersions(t *testing.T) {
	for _, cidrs := range [][]string{nil, {"bad"}, {"0.0.0.0/0"}, {"::/0"}} {
		if _, err := New(cidrs, 0, 0); err == nil {
			t.Fatalf("accepted %v", cidrs)
		}
	}
	for _, timeout := range []time.Duration{-1, 31 * time.Second} {
		if _, err := New([]string{"192.0.2.1/32"}, timeout, 0); err == nil {
			t.Fatal("accepted timeout")
		}
	}
	if _, err := New([]string{"192.0.2.1/32"}, 0, 3); err == nil {
		t.Fatal("accepted version")
	}
	for _, version := range []int{1, 2} {
		a, b := net.Pipe()
		p, _ := New([]string{"192.0.2.1/32"}, time.Second, version)
		h := v2("198.51.100.7", "203.0.113.2", nil)
		if version == 2 {
			h = []byte("PROXY TCP4 198.51.100.7 203.0.113.2 12345 443\r\n")
		}
		go func() { _, _ = b.Write(h); b.Close() }()
		_, err := p.Accept(peerConn{a, "192.0.2.1:1000"})
		a.Close()
		b.Close()
		if err == nil {
			t.Fatal("accepted wrong version")
		}
	}
}

func TestRealTLSHandshakeAfterProxyHeader(t *testing.T) {
	pub, priv, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), DNSNames: []string{"test.invalid"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, pub, priv)
	if err != nil {
		t.Fatal(err)
	}
	cert := tls.Certificate{Certificate: [][]byte{der}, PrivateKey: priv}
	ca, _ := x509.ParseCertificate(der)
	roots := x509.NewCertPool()
	roots.AddCert(ca)
	for _, header := range [][]byte{[]byte("PROXY TCP4 198.51.100.7 203.0.113.2 12345 443\r\n"), v2("2001:db8::7", "2001:db8::2", nil)} {
		a, b := net.Pipe()
		p, _ := New([]string{"192.0.2.1/32"}, time.Second, 0)
		done := make(chan error, 1)
		go func() {
			defer a.Close()
			c, err := p.Accept(peerConn{a, "192.0.2.1:1000"})
			if err != nil {
				done <- err
				return
			}
			s := tls.Server(c, &tls.Config{Certificates: []tls.Certificate{cert}, MinVersion: tls.VersionTLS13})
			s.SetDeadline(time.Now().Add(3 * time.Second))
			if err = s.Handshake(); err != nil {
				done <- err
				return
			}
			buf := make([]byte, 4)
			_, err = io.ReadFull(s, buf)
			if err == nil {
				_, err = s.Write(buf)
			}
			done <- err
		}()
		b.SetDeadline(time.Now().Add(3 * time.Second))
		if _, err = b.Write(header); err != nil {
			t.Fatal(err)
		}
		c := tls.Client(b, &tls.Config{RootCAs: roots, ServerName: "test.invalid", MinVersion: tls.VersionTLS13})
		if err = c.Handshake(); err != nil {
			t.Fatal(err)
		}
		if _, err = c.Write([]byte("ping")); err != nil {
			t.Fatal(err)
		}
		buf := make([]byte, 4)
		if _, err = io.ReadFull(c, buf); err != nil || string(buf) != "ping" {
			t.Fatal("TLS payload failed", err)
		}
		b.Close()
		if err = <-done; err != nil {
			t.Fatal(err)
		}
	}
}

func FuzzHeaderParser(f *testing.F) {
	f.Add([]byte("PROXY TCP4 198.51.100.7 203.0.113.2 12345 443\r\n"))
	f.Add(v2("198.51.100.7", "203.0.113.2", nil))
	f.Fuzz(func(t *testing.T, b []byte) {
		if len(b) == 0 {
			return
		}
		r := bufio.NewReader(bytes.NewReader(b))
		if b[0] == 'P' {
			_, _ = readV1(r)
		} else {
			_, _ = readV2(r)
		}
	})
}

func TestV2EmptyDestinationCompatibility(t *testing.T) {
	for _, family := range []struct{ src, dst, empty string }{{"198.51.100.7", "203.0.113.2", "0.0.0.0"}, {"2001:db8::7", "2001:db8::2", "::"}} {
		for _, tc := range []struct {
			name, src, dst string
			sp, dp         uint16
			accept         bool
		}{
			{"empty-destination", family.src, family.empty, 12345, 0, true},
			{"normal-destination", family.src, family.dst, 12345, 443, true},
			{"empty-address-only", family.src, family.empty, 12345, 443, false},
			{"empty-port-only", family.src, family.dst, 12345, 0, false},
			{"empty-source", family.empty, family.empty, 12345, 0, false},
			{"zero-source-port", family.src, family.empty, 0, 0, false},
		} {
			t.Run(family.src+"/"+tc.name, func(t *testing.T) {
				h := v2(tc.src, tc.dst, nil)
				binary.BigEndian.PutUint16(h[len(h)-4:], tc.sp)
				binary.BigEndian.PutUint16(h[len(h)-2:], tc.dp)
				payload := []byte{0x16, 0x03, 0x03, 0, 1, 42}
				r := bufio.NewReader(bytes.NewReader(append(h, payload...)))
				got, err := readV2(r)
				if !tc.accept {
					if err == nil {
						t.Fatal("accepted invalid source/destination")
					}
					return
				}
				if err != nil || got != netip.AddrPortFrom(netip.MustParseAddr(tc.src), tc.sp) {
					t.Fatalf("source changed: %v %v", got, err)
				}
				rest, _ := io.ReadAll(r)
				if !bytes.Equal(rest, payload) {
					t.Fatal("consumed subsequent TLS bytes")
				}
			})
		}
	}
}

func TestNormalizedAddressValidation(t *testing.T) {
	for _, version := range []int{1, 2} {
		for _, tc := range []struct {
			address string
			valid   bool
		}{
			{"::ffff:0.0.0.0", false}, {"::ffff:224.0.0.1", false}, {"::", false}, {"ff02::1", false},
			{"::ffff:198.51.100.7", true}, {"2001:db8::7", true},
		} {
			t.Run(fmt.Sprintf("v%d/%s", version, tc.address), func(t *testing.T) {
				h := v2(tc.address, "2001:db8::2", nil)
				if version == 1 {
					h = []byte("PROXY TCP6 " + tc.address + " 2001:db8::2 12345 443\r\n")
				}
				r := bufio.NewReader(bytes.NewReader(h))
				var got netip.AddrPort
				var err error
				if version == 1 {
					got, err = readV1(r)
				} else {
					got, err = readV2(r)
				}
				if !tc.valid {
					if err == nil {
						t.Fatalf("accepted invalid normalized source %v", got)
					}
					return
				}
				if err != nil || got.Addr() != netip.MustParseAddr(tc.address).Unmap() {
					t.Fatalf("valid mapped source changed: %v %v", got, err)
				}
			})
		}
	}
	for _, a := range []string{"::ffff:0.0.0.0", "::ffff:224.0.0.1", "fe80::1%eth0", "::ffff:198.51.100.7%eth0"} {
		if validAddress(netip.MustParseAddr(a)) {
			t.Fatalf("invalid address accepted: %s", a)
		}
	}
}

func TestOptionalPeerPolicy(t *testing.T) {
	if _, err := NewWithTrust(nil, time.Second, 2, true); err == nil {
		t.Fatal("strict mode accepted empty list")
	}
	for _, tc := range []struct {
		name   string
		cidrs  []string
		header []byte
		want   bool
	}{
		{"no-list-valid", nil, v2("198.51.100.7", "203.0.113.2", nil), true},
		{"no-list-missing-header", nil, []byte("not a proxy header"), false},
		{"no-list-invalid-source", nil, v2("::ffff:0.0.0.0", "2001:db8::2", nil), false},
		{"existing-list-enforced", []string{"192.0.2.1/32"}, nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			p, err := NewWithTrust(tc.cidrs, 50*time.Millisecond, 2, false)
			if err != nil {
				t.Fatal(err)
			}
			a, b := net.Pipe()
			defer a.Close()
			defer b.Close()
			// Keep the peer open through Accept's deadline restoration. Closing it
			// immediately after Write races net.Pipe.SetReadDeadline and tests
			// an already-disconnected peer instead of a valid live connection.
			go func() { b.Write(tc.header) }()
			c, err := p.Accept(peerConn{a, "192.0.2.99:1000"})
			if (err == nil) != tc.want {
				t.Fatalf("unexpected policy result %v", err)
			}
			if tc.want && c.RemoteAddr().String() != "198.51.100.7:12345" {
				t.Fatal("source mismatch")
			}
		})
	}
}
