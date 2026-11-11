package limiter

import (
	"net"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/common/rate"
	"github.com/InazumaV/V2bX/conf"
	N "github.com/sagernet/sing/common/network"
)

func TestEOFCloseReleasesDeviceWithoutRemovingGrace(t *testing.T) {
	Init()
	const tag, user, ip = "eof-device", "synthetic", "198.51.100.9"
	l := AddLimiter(tag, &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: user, DeviceLimit: 1}}, nil)
	defer DeleteLimiter(tag)
	key := format.UserTag(tag, user)
	if _, reject := l.CheckLimit(key, ip, true, true); reject {
		t.Fatal("initial device rejected")
	}
	a, b := net.Pipe()
	defer b.Close()
	s := new(counter.TrafficStorage)
	c := counter.NewWrappedLeasedConn(l.TrackDeviceConn(a, key, ip), s, func(v net.Conn) net.Conn { return rate.NewDynamicConn(v, l.DynamicControl(key)) })
	defer c.Close()
	if w, ok := c.(N.WriteCloser); ok {
		w.CloseWrite()
	} else {
		c.Close()
	}
	c.Close()
	l.stateMu.Lock()
	lease := l.deviceLeases[key][ip]
	active, expiry := lease.active, lease.expires
	l.stateMu.Unlock()
	if active != 0 || !expiry.After(time.Now()) || s.Owners.Load() != 0 {
		t.Fatal("EOF did not release exactly once or removed grace", active, s.Owners.Load())
	}
	if _, reject := l.CheckLimit(key, "198.51.100.10", true, true); !reject {
		t.Fatal("EOF bypassed existing device grace window")
	}
	l.stateMu.Lock()
	lease.expires = time.Now().Add(-time.Second)
	l.stateMu.Unlock()
	if _, reject := l.CheckLimit(key, "198.51.100.10", true, true); reject {
		t.Fatal("closed device stayed pinned after grace")
	}
}
