package sing

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-box/route"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type quickACKOutbound struct {
	adapter.Outbound
	address string
}

func (o *quickACKOutbound) Type() string { return "direct" }
func (o *quickACKOutbound) DialContext(ctx context.Context, network string, _ M.Socksaddr) (net.Conn, error) {
	return (&net.Dialer{}).DialContext(ctx, network, o.address)
}

type quickACKRouter struct {
	adapter.Router
	base     *reviewRouter
	manager  *route.ConnectionManager
	outbound *quickACKOutbound
	seen     atomic.Bool
}

func (r *quickACKRouter) RouteConnectionEx(ctx context.Context, c net.Conn, m adapter.InboundContext, onClose N.CloseHandlerFunc) {
	if m.AnyTLSAdaptiveQuickACK && m.InboundType == "anytls" {
		r.seen.Store(true)
	}
	c = r.base.hook.RoutedConnection(ctx, c, m, nil, r.outbound)
	r.base.sources <- m.Source.Addr.String()
	r.manager.NewConnection(ctx, r.outbound, c, m, func(err error) {
		if onClose != nil {
			onClose(err)
		}
	})
}

// Exercise actual TLS/AnyTLS, PROXY v2, the existing accounting/limiter hooks,
// the production ConnectionManager and a bare destination TCP socket.
func newQuickACKForwardFixture(t *testing.T) *reviewFixture {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		for {
			c, e := l.Accept()
			if e != nil {
				return
			}
			go func() {
				defer c.Close()
				c.SetDeadline(time.Now().Add(15 * time.Second))
				p := make([]byte, 8192)
				for {
					n, e := c.Read(p)
					if n > 0 {
						if _, w := c.Write(p[:n]); w != nil {
							return
						}
					}
					if e != nil {
						return
					}
				}
			}()
		}
	}()
	f := newReviewFixtureQuickACK(t, true, []option.AnyTLSUser{{Name: "test-user", Password: "test-password"}}, log.NewNOPFactory().Logger(), true, true)
	m := route.NewConnectionManager(log.NewNOPFactory().Logger())
	r := &quickACKRouter{base: f.router, manager: m, outbound: &quickACKOutbound{address: l.Addr().String()}}
	f.router.forward = r
	t.Cleanup(func() {
		m.Close()
		if !r.seen.Load() {
			t.Error("enabled metadata never reached real destination route")
		}
	})
	return f
}

func TestQuickACKConfigMapsToInbound(t *testing.T) {
	for _, enable := range []bool{false, true} {
		var n conf.NodeConfig
		raw := `{"Core":"sing","NodeType":"anytls","AnyTLSAdaptiveQuickACK":false}`
		if enable {
			raw = `{"Core":"sing","NodeType":"anytls","AnyTLSAdaptiveQuickACK":true}`
		}
		if e := json.Unmarshal([]byte(raw), &n); e != nil {
			t.Fatal(e)
		}
		info := &panel.NodeInfo{Type: "anytls", Common: &panel.CommonNode{ServerPort: 12345}, AnyTls: &panel.AnyTlsNode{}}
		o, e := getInboundOptions("quickack", info, &n.Options)
		if e != nil {
			t.Fatal(e)
		}
		if o.Options.(*option.AnyTLSInboundOptions).AdaptiveQuickACK != enable {
			t.Fatal("local setting lost")
		}
	}
}

func TestQuickACKRealIPDeviceRevocationBilling(t *testing.T) {
	f := newQuickACKForwardFixture(t)
	l, _ := limiter.GetLimiter("review")
	l.UpdateDeviceLimits("review", []panel.UserInfo{{Id: 123, Uuid: "test-user", DeviceLimit: 1}})
	a := f.client(t)
	defer a.Close()
	s, e := a.CreateProxy(context.Background(), M.ParseSocksaddr("echo.invalid:80"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	// Pass the existing 512k buffer-growth threshold, then use tiny reads.
	if e = dynamicEcho(s, 2<<20); e != nil {
		t.Fatal(e)
	}
	if e = dynamicEcho(s, 45); e != nil {
		t.Fatal(e)
	}
	select {
	case ip := <-f.router.sources:
		if ip != "198.51.100.9" {
			t.Fatal("real IP changed", ip)
		}
	case <-time.After(time.Second):
		t.Fatal("missing IP")
	}
	b := f.clientSource(t, "test-password", 10)
	defer b.Close()
	rejected, e := b.CreateProxy(context.Background(), M.ParseSocksaddr("echo.invalid:80"))
	if e == nil {
		defer rejected.Close()
		rejected.SetDeadline(time.Now().Add(time.Second))
		_, we := rejected.Write([]byte("deny"))
		_, re := io.ReadFull(rejected, make([]byte, 4))
		if we == nil && re == nil {
			t.Fatal("second real device transferred data")
		}
	}
	value, _ := f.router.hook.counter.Load("review")
	tc := value.(*counter.TrafficCounter).GetCounter("test-user")
	want := int64((2 << 20) + 45)
	deadline := time.Now().Add(time.Second)
	for tc.DownCounter.Load() != want && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	if tc.UpCounter.Load() != want || tc.DownCounter.Load() != want {
		t.Fatal("counting changed", tc.UpCounter.Load(), tc.DownCounter.Load(), want)
	}
	if e = f.inbound.DelUsers([]string{"test-user"}); e != nil {
		t.Fatal(e)
	}
	s.SetDeadline(time.Now().Add(time.Second))
	_, we := s.Write(bytes.Repeat([]byte{1}, 16))
	_, re := io.ReadFull(s, make([]byte, 16))
	if we == nil && re == nil {
		t.Fatal("revoked stream survived")
	}
	t.Logf("QUICKACK_COMPAT real_ip=true actual_second_device_rejected=true revoked_stream_closed=true up=%d down=%d", want, want)
}
