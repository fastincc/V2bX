package node

import (
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/limiter"
	"testing"
)

func TestDeviceOnlyUpdatePreservesAuthenticatedUser(t *testing.T) {
	limiter.Init()
	old := []panel.UserInfo{{Id: 1, Uuid: "fixture", DeviceLimit: 0}}
	current := []panel.UserInfo{{Id: 1, Uuid: "fixture", DeviceLimit: 2}}
	l := limiter.AddLimiter("test", &conf.LimitConfig{}, old, map[int]int{})
	l.UpdateDeviceLimits("test", current)
	deleted, added := compareUserList(old, current)
	if len(deleted) != 0 || len(added) != 0 {
		t.Fatal("device-only update must not remove/re-add authentication")
	}
	u := format.UserTag("test", "fixture")
	for _, ip := range []string{"192.0.2.1", "192.0.2.2"} {
		if _, r := l.CheckLimit(u, ip, true, true); r {
			t.Fatal("unexpected reject")
		}
	}
	if _, r := l.CheckLimit(u, "192.0.2.3", true, true); !r {
		t.Fatal("device-only hot update not applied")
	}
}
