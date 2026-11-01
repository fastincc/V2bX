package dispatcher

import (
	"context"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/common/dynamic"
	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/xtls/xray-core/common/buf"
	XN "github.com/xtls/xray-core/common/net"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/proxy/vless"
	"testing"
	"time"
)

func TestDynamicXrayExistingLinkSharedRateBillingAndRevoke(t *testing.T) {
	limiter.Init()
	users := []panel.UserInfo{{Id: 1, Uuid: "synthetic", DeviceLimit: 1}}
	l := limiter.AddLimiter("dyn-link", &conf.LimitConfig{}, users, nil)
	defer limiter.DeleteLimiter("dyn-link")
	d := new(DefaultDispatcher)
	lm := NewLinkManager(format.UserTag("dyn-link", "synthetic"))
	d.LinkManagers.Store("generation", lm)
	ctx := session.ContextWithInbound(context.Background(), &session.Inbound{Tag: "dyn-link", User: &protocol.MemoryUser{Email: "generation"}, Source: XN.TCPDestination(XN.LocalHostIP, 1234)})
	a, b, _, e := d.getLink(ctx, XN.Network_TCP)
	if e != nil {
		t.Fatal(e)
	}
	defer lm.CloseAll()
	for _, r := range []buf.Reader{a.Reader, b.Reader} {
		go func(r buf.Reader) {
			for {
				mb, e := r.ReadMultiBuffer()
				buf.ReleaseMulti(mb)
				if e != nil {
					return
				}
			}
		}(r)
	}
	write := func(w buf.Writer, n int) error {
		for n > 0 {
			p := buf.New()
			m := min(n, int(p.Cap()))
			p.Extend(int32(m))
			if e := w.WriteMultiBuffer(buf.MultiBuffer{p}); e != nil {
				return e
			}
			n -= m
		}
		return nil
	}
	if e = write(a.Writer, 1000); e != nil {
		t.Fatal(e)
	}
	users[0].DynamicSpeedLimit = &dynamic.Policy{Version: 1, Revision: 1, Enabled: true, WindowSeconds: 60, ThresholdBytes: 1000, SpeedMbps: 1, DurationSeconds: 30}
	l.UpdateDynamicPolicies("dyn-link", users)
	if e = write(a.Writer, 1000); e != nil {
		t.Fatal(e)
	}
	l.TickDynamicPolicies(time.Now())
	start := time.Now()
	done := make(chan error, 2)
	go func() { done <- write(a.Writer, 62500) }()
	go func() { done <- write(b.Writer, 62500) }()
	for i := 0; i < 2; i++ {
		if e = <-done; e != nil {
			t.Fatal(e)
		}
	}
	elapsed := time.Since(start)
	if elapsed < 850*time.Millisecond || elapsed > 2500*time.Millisecond {
		t.Fatal("existing links do not share dynamic quota", elapsed)
	}
	v := *users[0].DynamicSpeedLimit
	v.Revision = 2
	v.Enabled = false
	users[0].DynamicSpeedLimit = &v
	l.UpdateDynamicPolicies("dyn-link", users)
	start = time.Now()
	if e = write(a.Writer, 62500); e != nil {
		t.Fatal(e)
	}
	if time.Since(start) > 500*time.Millisecond {
		t.Fatal("disable not applied")
	}
	value, _ := d.Counter.Load("dyn-link")
	c := value.(*counter.TrafficCounter).GetCounter("generation")
	if c.UpCounter.Load() != 127000 || c.DownCounter.Load() != 62500 {
		t.Fatal("accounting changed", c.UpCounter.Load(), c.DownCounter.Load())
	}
	v.Revision = 3
	v.Enabled = true
	l.UpdateDynamicPolicies("dyn-link", users)
	if e = write(a.Writer, 1000); e != nil {
		t.Fatal(e)
	}
	l.TickDynamicPolicies(time.Now())
	go func() { done <- write(a.Writer, 10000000) }()
	time.Sleep(20 * time.Millisecond)
	lm.CloseAll()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("revoked writer still succeeds")
		}
	case <-time.After(time.Second):
		t.Fatal("revoked writer stuck")
	}
	t.Logf("DYNAMIC_XRAY same_links=true shared_1mbps=%s before_revoke_up=127000 down=62500", elapsed)
}

func TestDynamicVisionPreservesSpliceAndRejectsPolicy(t *testing.T) {
	limiter.Init()
	users := []panel.UserInfo{{Id: 1, Uuid: "synthetic", DynamicSpeedLimit: &dynamic.Policy{Version: 1, Revision: 1, Enabled: true, WindowSeconds: 60, ThresholdBytes: 1000, SpeedMbps: 1, DurationSeconds: 30}}}
	l := limiter.AddLimiter("vision", &conf.LimitConfig{}, users, nil)
	defer limiter.DeleteLimiter("vision")
	key := format.UserTag("vision", "synthetic")
	h := l.DynamicControl(key)
	if !h.Status().Enabled {
		t.Fatal("fixture")
	}
	u := &protocol.MemoryUser{Account: &vless.MemoryAccount{Flow: "xtls-rprx-vision"}}
	if dynamicLinkControl(l, key, u) != nil || h.Status().Enabled {
		t.Fatal("Vision policy silently bypassed")
	}
	l.UpdateDynamicPolicies("vision", users)
	if h.Status().Enabled {
		t.Fatal("unsupported policy re-enabled")
	}
	if _, reject := l.CheckLimit(key, "192.0.2.1", true, true); reject {
		t.Fatal("policy rejection revoked authorization")
	}
}
