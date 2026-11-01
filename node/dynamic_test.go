package node

import (
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/dynamic"
	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/common/visionsplice"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/limiter"
	"testing"
	"time"
)

func TestDynamicPolicySyncPreservesIdentityDeviceAndETag(t *testing.T) {
	limiter.Init()
	old := []panel.UserInfo{{Id: 1, Uuid: "synthetic", DeviceLimit: 1}}
	l := limiter.AddLimiter("dynamic-sync", &conf.LimitConfig{}, old, nil)
	defer limiter.DeleteLimiter("dynamic-sync")
	api, _ := panel.New(&conf.ApiConfig{APIHost: "http://127.0.0.1:1", NodeType: "anytls"})
	core := &syncTestCore{live: map[string]int{"synthetic": 1}}
	c := &Controller{server: core, apiClient: api, tag: "dynamic-sync", info: &panel.NodeInfo{}, Options: &conf.Options{}, limiter: l, userList: old}
	key := format.UserTag(c.tag, "synthetic")
	h := l.DynamicControl(key)
	if _, r := l.CheckLimit(key, "192.0.2.1", true, true); r {
		t.Fatal("first device")
	}
	next := append([]panel.UserInfo(nil), old...)
	next[0].DynamicSpeedLimit = &dynamic.Policy{Version: 1, Revision: 1, Enabled: true, WindowSeconds: 60, ThresholdBytes: 100, SpeedMbps: 1, DurationSeconds: 10}
	if e := c.applyUserSnapshot(next); e != nil {
		t.Fatal(e)
	}
	if core.addCalls != 0 || core.deleteCalls != 0 || l.DynamicControl(key) != h {
		t.Fatal("policy update replaced authorization")
	}
	h.Observe(100)
	l.TickDynamicPolicies(time.Now())
	end := h.Status().Until
	c.applyUserSnapshot(next)
	if h.Status().Until != end {
		t.Fatal("snapshot extended punishment")
	}
	if _, r := l.CheckLimit(key, "192.0.2.2", true, true); !r {
		t.Fatal("policy update reset device admission")
	}
	c.applyUserSnapshot(old)
	if h.Status().Enabled || h.Status().Limited {
		t.Fatal("omission did not disable")
	}
	if core.addCalls != 0 || core.deleteCalls != 0 {
		t.Fatal("disable disconnected user")
	}
}

func TestVisionPrototypeRebuildReactivatesOnlyOptedInUsers(t *testing.T) {
	limiter.Init()
	users := []panel.UserInfo{{Id: 1, Uuid: "synthetic", DynamicSpeedLimit: &dynamic.Policy{Version: 1, Revision: 1, Enabled: true, WindowSeconds: 60, ThresholdBytes: 100, SpeedMbps: 1, DurationSeconds: 10}}}
	l := limiter.AddLimiter("vision-rebuild", &conf.LimitConfig{}, users, nil)
	defer limiter.DeleteLimiter("vision-rebuild")
	key := format.UserTag("vision-rebuild", "synthetic")
	l.RejectDynamicPolicyForRawCopy(key)
	c := &Controller{tag: "vision-rebuild", limiter: l, Options: &conf.Options{XrayOptions: conf.NewXrayOptions()}}
	info := &panel.NodeInfo{VAllss: &panel.VAllssNode{Flow: "xtls-rprx-vision"}}
	c.restoreBufferedDynamicPolicies(info, users)
	if l.DynamicControl(key).Status().Enabled {
		t.Fatal("default restored excluded Vision")
	}
	c.XrayOptions.EnableVisionSplice = true
	c.restoreBufferedDynamicPolicies(info, users)
	if l.DynamicControl(key).Status().Enabled != visionsplice.Supported {
		t.Fatal("platform-aware opt-in restore")
	}
}
