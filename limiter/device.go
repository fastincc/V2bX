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

// admitDevice is called with stateMu held. Snapshot IPs and local leases are
// unioned; cross-node admission remains eventual, not a distributed lock.
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
	if limit > 0 {
		if l.hasSnapshot {
			known := make(map[string]bool, len(leases))
			for addr := range leases {
				known[addr] = true
			}
			globalKnown := false
			for addr, expiry := range l.globalIPs[uid] {
				if expiry.After(now) {
					known[addr] = true
					if addr == ip {
						globalKnown = true
					}
				}
			}
			// Existing global sources can move between nodes without taking a new slot.
			// Expired snapshots cannot authorize a new source during a panel outage.
			if !globalKnown && (!l.snapshotUntil.After(now) || len(known) >= limit) {
				return false
			}
		} else if len(leases) >= limit || l.AliveList[uid] >= limit {
			return false
		}
	}
	leases[ip] = &deviceLease{expires: now.Add(deviceIdleTTL)}
	return true
}

func (l *Limiter) UpdateAliveList(alive map[int]int) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	l.AliveList = alive
}

// UpdateAliveSnapshot installs only a successfully decoded response. Remaining
// TTLs use the local monotonic clock, avoiding cross-host wall-clock skew.
func (l *Limiter) UpdateAliveSnapshot(alive map[int]int, snapshot *panel.DeviceSnapshot) {
	l.stateMu.Lock()
	defer l.stateMu.Unlock()
	l.AliveList = alive
	if snapshot == nil {
		// Legacy panels retain count-only behavior. Once snapshots were negotiated,
		// do not downgrade a running limiter to less precise state on missing fields.
		return
	}
	now := time.Now()
	ips := make(map[int]map[string]time.Time, len(snapshot.IPs))
	for uid, rows := range snapshot.IPs {
		if uid <= 0 {
			continue
		}
		ips[uid] = make(map[string]time.Time)
		for raw, seconds := range rows {
			addr, err := netip.ParseAddr(raw)
			if err != nil || addr.Zone() != "" || seconds <= 0 {
				continue
			}
			if seconds > snapshot.TTL {
				seconds = snapshot.TTL
			}
			ips[uid][addr.Unmap().String()] = now.Add(time.Duration(seconds) * time.Second)
		}
	}
	l.globalIPs = ips
	l.hasSnapshot = true
	l.snapshotUntil = now.Add(time.Duration(snapshot.TTL) * time.Second)
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

// Upstream preserves optional transport capabilities (including half-close)
// discovered by the core without bypassing our final Close accounting.
func (c *deviceConn) Upstream() any { return c.Conn }

// TrackAdmittedDevice pins an existing, still valid lease. It cannot create a
// lease or resurrect a revoked user. Used after protocol admission succeeds.
func (l *Limiter) TrackAdmittedDevice(user, ip string) (func(), bool) {
	addr, err := netip.ParseAddr(ip)
	if err != nil || addr.Zone() != "" {
		return nil, false
	}
	ip = addr.Unmap().String()
	l.stateMu.Lock()
	lease := l.deviceLeases[user][ip]
	_, userExists := l.UserLimitInfo.Load(user)
	if !userExists || lease == nil || (lease.active == 0 && !lease.expires.After(time.Now())) {
		l.stateMu.Unlock()
		return nil, false
	}
	lease.active++
	l.stateMu.Unlock()
	var once sync.Once
	return func() {
		once.Do(func() {
			l.stateMu.Lock()
			lease.active--
			lease.expires = time.Now().Add(deviceIdleTTL)
			l.stateMu.Unlock()
		})
	}, true
}
