package limiter

import (
	"github.com/InazumaV/V2bX/api/panel"
	"testing"
	"time"
)

func TestDeviceCheckedPinExpiryAndRevocation(t *testing.T) {
	l, user := deviceFixture(1)
	if _, ok := l.TrackAdmittedDevice(user, "192.0.2.1"); ok {
		t.Fatal("unadmitted source pinned")
	}
	l.CheckLimit(user, "192.0.2.1", false, true)
	release, ok := l.TrackAdmittedDevice(user, "::ffff:192.0.2.1")
	if !ok {
		t.Fatal("mapped IP pin failed")
	}
	release2, ok := l.TrackAdmittedDevice(user, "192.0.2.1")
	if !ok {
		t.Fatal("same IP pin failed")
	}
	lease := l.deviceLeases[user]["192.0.2.1"]
	lease.expires = time.Now().Add(-time.Hour)
	for i := 0; i < 3; i++ {
		rows, _ := l.GetOnlineDevice()
		if len(*rows) != 1 || (*rows)[0].IP != "192.0.2.1" {
			t.Fatal("online source lost", rows)
		}
	}
	if _, rejected := l.CheckLimit(user, "192.0.2.2", false, true); !rejected {
		t.Fatal("active expired source released")
	}
	release()
	release()
	if lease.active != 1 {
		t.Fatal("duplicate release", lease.active)
	}
	l.UpdateUser("test", nil, []panel.UserInfo{{Id: 1, Uuid: "test-user"}})
	if _, ok := l.TrackAdmittedDevice(user, "192.0.2.1"); ok {
		t.Fatal("revoked user pinned")
	}
	l.UpdateUser("test", []panel.UserInfo{{Id: 2, Uuid: "test-user", DeviceLimit: 1}}, nil)
	l.CheckLimit(user, "192.0.2.1", false, true)
	replacement := l.deviceLeases[user]["192.0.2.1"]
	release2()
	if replacement.active != 0 || lease.active != 0 {
		t.Fatal("old release changed new generation")
	}
	replacement.expires = time.Now().Add(-time.Second)
	if _, ok := l.TrackAdmittedDevice(user, "192.0.2.1"); ok {
		t.Fatal("expired idle lease resurrected")
	}
	if _, rejected := l.CheckLimit(user, "192.0.2.2", false, true); rejected {
		t.Fatal("released lease blocked new source")
	}
	for _, ip := range []string{"", "bad", "fe80::1%en0"} {
		if _, ok := l.TrackAdmittedDevice(user, ip); ok {
			t.Fatal("invalid source", ip)
		}
	}
}
