package limiter

import (
	"net"
	"net/netip"
	"sync"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/format"
)

// Keep disconnected sources briefly, matching the default panel online window.
// Reporting must not clear admission state. Active streams never expire.
const deviceIdleTTL = 180 * time.Second

type deviceLease struct {
	active  int
	expires time.Time
}

// admitDevice is called with stateMu held. The panel supplies counts, not IP sets;
// max(local, panel) avoids double-counting. Cross-node admission is still eventual.
func (l *Limiter) admitDevice(user, ip string, uid, limit int, now time.Time) bool {
	if l.deviceLeases == nil {
		l.deviceLeases = make(map[string]map[string]*deviceLease)
	}
	leases := l.deviceLeases[user]
	if leases == nil {
		leases = make(map[string]*deviceLease)
		l.deviceLeases[user] = leases
	}
	for addr, lease := range leases {
		if lease.active == 0 && !lease.expires.After(now) {
			delete(leases, addr)
		}
	}
	if lease := leases[ip]; lease != nil {
		lease.expires = now.Add(deviceIdleTTL)
		return true
	}
	if limit > 0 && (len(leases) >= limit || l.AliveList[uid] >= limit) {
		return false
	}
	leases[ip] = &deviceLease{expires: now.Add(deviceIdleTTL)}
	return true
}

func (l *Limiter) UpdateAliveList(alive map[int]int) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	l.AliveList = alive
}

func (l *Limiter) UpdateDeviceLimits(tag string, users []panel.UserInfo) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	for _, user := range users {
		if value, ok := l.UserLimitInfo.Load(format.UserTag(tag, user.Uuid)); ok {
			value.(*UserLimitInfo).DeviceLimit = user.DeviceLimit
		}
	}
}

// TrackDeviceConn pins the accepted source while an actual TCP stream is alive.
// It changes neither authentication nor the traffic accounting wrappers.
func (l *Limiter) TrackDevice(user, ip string) func() {
	if addr, err := netip.ParseAddr(ip); err == nil {
		ip = addr.Unmap().String()
	}
	l.stateMu.Lock()
	lease := l.deviceLeases[user][ip]
	if lease != nil {
		lease.active++
	}
	l.stateMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			if lease == nil {
				return
			}
			l.stateMu.Lock()
			defer l.stateMu.Unlock()
			lease.active--
			lease.expires = time.Now().Add(deviceIdleTTL)
		})
	}
}
func (l *Limiter) TrackDeviceConn(c net.Conn, user, ip string) net.Conn {
	return &deviceConn{Conn: c, release: l.TrackDevice(user, ip)}
}

type deviceConn struct {
	net.Conn
	once    sync.Once
	release func()
}

func (c *deviceConn) Close() error { err := c.Conn.Close(); c.once.Do(c.release); return err }
