//go:build sing

package sing

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/common/dynamic"
	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/anytls/sing-anytls/padding"
	"github.com/anytls/sing-anytls/session"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/route"
	"github.com/sagernet/sing/common/atomic"
	M "github.com/sagernet/sing/common/metadata"
)

// Unlike an application completion marker, this test requires actual EOF from
// the production ConnectionManager before the client closes its own stream.
func TestAnyTLSTargetEOFClosesStreamAndAccountsTail(t *testing.T) {
	for _, mode := range []string{"unlimited", "static", "dynamic-disabled", "dynamic-active", "combined"} {
		t.Run(mode, func(t *testing.T) {
			f := newReviewFixture(t, true) // trusted synthetic PROXY IP, QUICKACK off
			u := panel.UserInfo{Id: 123, Uuid: "test-user", DeviceLimit: 1}
			if mode == "static" || mode == "combined" {
				u.SpeedLimit = 8
			}
			if mode == "dynamic-disabled" || mode == "dynamic-active" || mode == "combined" {
				u.DynamicSpeedLimit = &dynamic.Policy{Version: 1, Revision: 1, Enabled: mode != "dynamic-disabled", WindowSeconds: 60, ThresholdBytes: 1, SpeedMbps: 4, DurationSeconds: 60}
			}
			l := limiter.AddLimiter("review", &conf.LimitConfig{}, []panel.UserInfo{u}, nil)
			if mode == "dynamic-active" || mode == "combined" {
				h := l.DynamicControl(format.UserTag("review", "test-user"))
				// Use the same public policy scheduler entry used by regular tests.
				if h == nil {
					t.Fatal("missing dynamic control")
				}
				h.Observe(1)
				l.TickDynamicPolicies(time.Now())
				if !h.Status().Limited {
					t.Fatal("policy did not activate")
				}
			}
			target, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer target.Close()
			payload := bytes.Repeat([]byte("eof-tail"), 8193)
			done := make(chan error, 2)
			go func() {
				for i := 0; i < 2; i++ {
					c, e := target.Accept()
					if e != nil {
						done <- e
						return
					}
					c.SetDeadline(time.Now().Add(5 * time.Second))
					var request [1]byte
					_, e = io.ReadFull(c, request[:])
					if e == nil {
						_, e = c.Write(payload)
					}
					c.Close()
					done <- e
				}
			}()
			manager := route.NewConnectionManager(log.NewNOPFactory().Logger())
			defer manager.Close()
			f.router.forward = &quickACKRouter{base: f.router, manager: manager, outbound: &quickACKOutbound{address: target.Addr().String()}}
			cl := f.client(t)
			defer cl.Close()
			for i := 0; i < 2; i++ {
				c, e := cl.CreateProxy(context.Background(), M.ParseSocksaddr("eof.invalid:80"))
				if e != nil {
					t.Fatal(e)
				}
				defer c.Close()
				c.SetDeadline(time.Now().Add(3 * time.Second))
				if _, e = c.Write([]byte{1}); e != nil {
					t.Fatal(e)
				}
				b, e := io.ReadAll(c)
				// The pinned AnyTLS client maps remote FIN to net.ErrClosed.
				// Accept that terminal signal, never a timeout or a truncated payload.
				if (e != nil && !errors.Is(e, net.ErrClosed)) || !bytes.Equal(b, payload) {
					t.Fatalf("target EOF not propagated or tail changed: bytes=%d err=%v", len(b), e)
				}
				if e = <-done; e != nil {
					t.Fatal(e)
				}
				select {
				case ip := <-f.router.sources:
					if ip != "198.51.100.9" {
						t.Fatal("real IP changed", ip)
					}
				case <-time.After(time.Second):
					t.Fatal("missing source")
				}
				v, ok := f.router.hook.counter.Load("review")
				if !ok {
					t.Fatal("missing accounting")
				}
				s := v.(*counter.TrafficCounter).GetCounter("test-user")
				deadline := time.Now().Add(time.Second)
				for s.Owners.Load() != 0 && time.Now().Before(deadline) {
					time.Sleep(time.Millisecond)
				}
				if s.Owners.Load() != 0 || s.UpCounter.Load() != int64(i+1) || s.DownCounter.Load() != int64((i+1)*len(payload)) {
					t.Fatal("EOF leaked owner or changed tail billing", s.Owners.Load(), s.UpCounter.Load(), s.DownCounter.Load())
				}
				c.Close() // after the EOF assertion, never used to make it pass
			}
		})
	}
}

// Use one explicit TLS session, not a client pool that could choose separate
// physical connections, so stream-close isolation is actually exercised.
func TestAnyTLSEOFKeepsSiblingStream(t *testing.T) {
	f := newReviewFixture(t, true)
	target, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer target.Close()
	payload := bytes.Repeat([]byte("tail"), 1024)
	go func() {
		for {
			c, e := target.Accept()
			if e != nil {
				return
			}
			go func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(5 * time.Second))
				var mode [1]byte
				if _, e := io.ReadFull(c, mode[:]); e != nil {
					return
				}
				if mode[0] == 1 {
					c.Write(payload)
				} else {
					io.Copy(c, c)
				}
			}()
		}
	}()
	manager := route.NewConnectionManager(log.NewNOPFactory().Logger())
	defer manager.Close()
	f.router.forward = &quickACKRouter{base: f.router, manager: manager, outbound: &quickACKOutbound{address: target.Addr().String()}}
	raw, err := net.Dial("tcp", f.listener.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer raw.Close()
	raw.SetDeadline(time.Now().Add(5 * time.Second))
	header := append([]byte("\r\n\r\n\x00\r\nQUIT\n"), 0x21, 0x11, 0, 12, 198, 51, 100, 9, 0, 0, 0, 0, 0x30, 0x39, 0, 0)
	if _, err := raw.Write(header); err != nil {
		t.Fatal(err)
	}
	tc := tls.Client(raw, &tls.Config{ServerName: "review.invalid", RootCAs: f.roots})
	if err := tc.HandshakeContext(context.Background()); err != nil {
		t.Fatal(err)
	}
	auth := sha256.Sum256([]byte("test-password"))
	if _, err := tc.Write(append(auth[:], 0, 0)); err != nil {
		t.Fatal(err)
	}
	var pad atomic.TypedValue[*padding.PaddingFactory]
	padding.UpdatePaddingScheme(padding.DefaultPaddingScheme, &pad)
	sess := session.NewClientSession(tc, &pad, log.NewNOPFactory().Logger())
	sess.Run()
	defer sess.Close()
	open := func(mode byte) net.Conn {
		c, e := sess.OpenStream()
		if e != nil {
			t.Fatal(e)
		}
		c.SetDeadline(time.Now().Add(3 * time.Second))
		if e := M.SocksaddrSerializer.WriteAddrPort(c, M.ParseSocksaddr("eof.invalid:80")); e != nil {
			t.Fatal(e)
		}
		if _, e := c.Write([]byte{mode}); e != nil {
			t.Fatal(e)
		}
		return c
	}
	hold := open(0)
	defer hold.Close()
	reviewEcho(t, hold, []byte("before"))
	ending := open(1)
	defer ending.Close()
	b, e := io.ReadAll(ending)
	if (e != nil && !errors.Is(e, net.ErrClosed)) || !bytes.Equal(b, payload) {
		t.Fatal("stream FIN missing or tail lost", len(b), e)
	}
	if sess.IsClosed() {
		t.Fatal("logical EOF closed shared TLS session")
	}
	reviewEcho(t, hold, []byte("after"))
	v, _ := f.router.hook.counter.Load("review")
	s := v.(*counter.TrafficCounter).GetCounter("test-user")
	deadline := time.Now().Add(time.Second)
	for s.Owners.Load() != 1 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.Owners.Load() != 1 {
		t.Fatal("ended stream retained owner or sibling lost owner", s.Owners.Load())
	}
	hold.Close()
	ending.Close()
	deadline = time.Now().Add(time.Second)
	for s.Owners.Load() != 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if s.Owners.Load() != 0 || s.UpCounter.Load() != 13 || s.DownCounter.Load() != int64(len(payload)+11) {
		t.Fatal("multiplex EOF accounting/release mismatch", s.Owners.Load(), s.UpCounter.Load(), s.DownCounter.Load())
	}
}
