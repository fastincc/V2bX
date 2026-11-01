package limiter

import (
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/dynamic"
	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/conf"
	"testing"
	"time"
)

func TestDynamicNodeIsolationExpiryAndGeneration(t *testing.T) {
	Init()
	u := []panel.UserInfo{{Id: 1, Uuid: "synthetic", DeviceLimit: 1, DynamicSpeedLimit: &dynamic.Policy{Version: 1, Revision: 1, Enabled: true, WindowSeconds: 60, ThresholdBytes: 1000, SpeedMbps: 1, DurationSeconds: 2}}}
	a := AddLimiter("dynamic-a", &conf.LimitConfig{}, u, nil)
	b := AddLimiter("dynamic-b", &conf.LimitConfig{}, u, nil)
	defer DeleteLimiter("dynamic-a")
	defer DeleteLimiter("dynamic-b")
	key := format.UserTag("dynamic-a", "synthetic")
	x := a.DynamicControl(key)
	y := b.DynamicControl(format.UserTag("dynamic-b", "synthetic"))
	if _, r := a.CheckLimit(key, "192.0.2.1", true, true); r {
		t.Fatal("first admission")
	}
	x.Observe(1000)
	a.TickDynamicPolicies(time.Now())
	b.TickDynamicPolicies(time.Now())
	if !x.Status().Limited || y.Status().Limited {
		t.Fatal("cross-node policy state")
	}
	a.TickDynamicPolicies(x.Status().Until)
	if _, r := a.CheckLimit(key, "192.0.2.1", true, true); r {
		t.Fatal("expiry deleted authorized user")
	}
	if _, r := a.CheckLimit(key, "192.0.2.2", true, true); !r {
		t.Fatal("expiry cleared device limit")
	}
	a.UpdateUser("dynamic-a", nil, u)
	a.UpdateUser("dynamic-a", u, nil)
	if a.DynamicControl(key) == x || a.DynamicControl(key).Status().Limited {
		t.Fatal("retired generation reused")
	}
}

func TestDynamicManagerShutdownAndFixedMinimum(t *testing.T) {
	Init()
	u := []panel.UserInfo{{Id: 1, Uuid: "synthetic", SpeedLimit: 1, DynamicSpeedLimit: &dynamic.Policy{Version: 1, Revision: 1, Enabled: true, WindowSeconds: 60, ThresholdBytes: 1, SpeedMbps: 4, DurationSeconds: 1}}}
	for i := 0; i < 50; i++ {
		l := AddLimiter("dynamic-close", &conf.LimitConfig{}, u, nil)
		h := l.DynamicControl(format.UserTag("dynamic-close", "synthetic"))
		h.Observe(1)
		h.Tick(time.Now())
		if !h.ActiveLimit() {
			t.Fatal("effective minimum not enforced")
		}
		if i == 0 {
			start := time.Now()
			h.Wait(12500, nil)
			if time.Since(start) < 80*time.Millisecond {
				t.Fatal("temporary rule loosened fixed ceiling")
			}
		}
		DeleteLimiter("dynamic-close")
		done := make(chan struct{})
		go func() { l.dynamic.wg.Wait(); close(done) }()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("node scheduler leaked")
		}
	}
}

func TestDynamicTransportRebuildClearsUnsupportedState(t *testing.T) {
	Init()
	u := []panel.UserInfo{{Id: 1, Uuid: "synthetic", DynamicSpeedLimit: &dynamic.Policy{Version: 1, Revision: 1, Enabled: true, WindowSeconds: 60, ThresholdBytes: 1, SpeedMbps: 1, DurationSeconds: 1}}}
	l := AddLimiter("dynamic-transport", &conf.LimitConfig{}, u, nil)
	defer DeleteLimiter("dynamic-transport")
	key := format.UserTag("dynamic-transport", "synthetic")
	old := l.DynamicControl(key)
	l.RejectDynamicPolicyForRawCopy(key)
	l.UpdateDynamicPolicies("dynamic-transport", u)
	if old.Status().Enabled {
		t.Fatal("Vision rejection lost")
	}
	l.ResetUnsupportedDynamicPolicies("dynamic-transport", u)
	if l.DynamicControl(key) == old || !l.DynamicControl(key).Status().Enabled {
		t.Fatal("buffered transport could not restore policy")
	}
}

func TestDynamicReplacingNodeStopsPreviousScheduler(t *testing.T) {
	Init()
	users := []panel.UserInfo{{Id: 1, Uuid: "synthetic", DynamicSpeedLimit: &dynamic.Policy{Version: 1, Revision: 1, Enabled: true, WindowSeconds: 60, ThresholdBytes: 1, SpeedMbps: 1, DurationSeconds: 30}}}
	old := AddLimiter("dynamic-replace", &conf.LimitConfig{}, users, nil)
	h := old.DynamicControl(format.UserTag("dynamic-replace", "synthetic"))
	h.Observe(1)
	h.Tick(time.Now())
	next := AddLimiter("dynamic-replace", &conf.LimitConfig{}, users, nil)
	defer DeleteLimiter("dynamic-replace")
	select {
	case <-old.dynamic.done:
	default:
		t.Fatal("old scheduler not closed")
	}
	if h.Status().Enabled || h.ActiveLimit() {
		t.Fatal("old policy retained")
	}
	if !next.DynamicControl(format.UserTag("dynamic-replace", "synthetic")).Status().Enabled {
		t.Fatal("replacement policy disabled")
	}
}

func TestDynamicSchedulerTriggersAndExpiresWithoutMoreTraffic(t *testing.T) {
	Init()
	users := []panel.UserInfo{{Id: 1, Uuid: "synthetic", DynamicSpeedLimit: &dynamic.Policy{Version: 1, Revision: 1, Enabled: true, WindowSeconds: 60, ThresholdBytes: 1000, SpeedMbps: 1, DurationSeconds: 1}}}
	l := AddLimiter("dynamic-timer", &conf.LimitConfig{}, users, nil)
	defer DeleteLimiter("dynamic-timer")
	h := l.DynamicControl(format.UserTag("dynamic-timer", "synthetic"))
	h.Observe(1000)
	deadline := time.Now().Add(2500 * time.Millisecond)
	for !h.ActiveLimit() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !h.ActiveLimit() {
		t.Fatal("scheduler did not trigger")
	}
	deadline = time.Now().Add(2500 * time.Millisecond)
	for h.ActiveLimit() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if h.ActiveLimit() || !h.Status().Enabled {
		t.Fatal("idle penalty did not expire while preserving rule")
	}
}
