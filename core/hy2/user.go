package hy2

import (
	"fmt"
	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/limiter"
	"net"
	"net/netip"
	"strconv"
	"sync"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	vCore "github.com/InazumaV/V2bX/core"
	"github.com/apernet/hysteria/core/v2/server"
)

var _ server.Authenticator = &V2bX{}

type V2bX struct {
	usersMap    map[string]int
	mutex       sync.RWMutex
	active      map[string]string
	identities  map[string]hyIdentity
	tag         string
	sessions    map[string]*hySession
	streams     map[server.HyStream]*hySession
	nextSession uint64
	sequence    uint64
	paused      bool
}

type hyIdentity struct {
	credential string
	uid        int
	retired    bool
}

// Connection IDs are opaque runtime handles, never accounting keys or credentials.
// Multiple connections share their original accounting generation, not admission state.
type hySession struct {
	id, generation   string
	uid              int
	denied, closed   bool
	managed, drained bool
	streams          int
}

func (v *V2bX) Authenticate(addr net.Addr, auth string, tx uint64) (bool, string) {
	v.mutex.Lock()
	defer v.mutex.Unlock()
	if v.paused {
		return false, ""
	}
	generation, ok := v.active[auth]
	if !ok {
		return false, ""
	}
	ip, e := netip.ParseAddr(extractIPFromAddr(addr))
	if e != nil || ip.Zone() != "" {
		return false, ""
	}
	l, e := limiter.GetLimiter(v.tag)
	if e != nil {
		return false, ""
	}
	if _, reject := l.CheckLimit(format.UserTag(v.tag, auth), ip.Unmap().String(), false, true); reject {
		return false, ""
	}
	if v.sessions == nil {
		v.sessions = make(map[string]*hySession)
	}
	v.nextSession++
	id := generation + "/connection-" + strconv.FormatUint(v.nextSession, 10)
	v.sessions[id] = &hySession{id: id, generation: generation, uid: v.identities[generation].uid}
	return true, id
}

// Disconnect can precede the last TCP copy callback. Keep the connection handle
// until the transport drain barrier and traced streams finish. Its original
// counter is owned by the user generation and is never deleted by disconnect.
func (v *V2bX) disconnect(id string) {
	v.mutex.Lock()
	defer v.mutex.Unlock()
	if s := v.sessions[id]; s != nil {
		s.closed = true
		if s.streams == 0 && (!s.managed || s.drained) {
			delete(v.sessions, id)
		}
	}
}
func (v *V2bX) addLocked(credential string, uid int) {
	if v.active == nil {
		v.active = map[string]string{}
		v.identities = map[string]hyIdentity{}
	}
	if _, ok := v.active[credential]; ok {
		return
	}
	v.sequence++
	id := "accounting-" + strconv.FormatUint(v.sequence, 10)
	v.active[credential] = id
	v.identities[id] = hyIdentity{credential: credential, uid: uid}
	v.usersMap[credential] = uid
}
func (v *V2bX) uidLocked(id string) int { return v.identities[id].uid }

func (h *Hysteria2) AddUsers(p *vCore.AddUsersParams) (int, error) {
	h.nodesMu.RLock()
	defer h.nodesMu.RUnlock()
	n, ok := h.Hy2nodes[p.Tag]
	if !ok {
		return 0, fmt.Errorf("node not found")
	}
	n.Auth.mutex.Lock()
	defer n.Auth.mutex.Unlock()
	n.Auth.tag = p.Tag
	for _, u := range p.Users {
		n.Auth.addLocked(u.Uuid, u.Id)
	}
	return len(p.Users), nil
}

func (h *Hysteria2) DelUsers(users []panel.UserInfo, tag string, _ *panel.NodeInfo) error {
	h.nodesMu.RLock()
	defer h.nodesMu.RUnlock()
	n, ok := h.Hy2nodes[tag]
	if !ok {
		return fmt.Errorf("node not found")
	}
	n.Auth.mutex.Lock()
	defer n.Auth.mutex.Unlock()
	for _, u := range users {
		id, ok := n.Auth.active[u.Uuid]
		if !ok {
			continue
		}
		entry := n.Auth.identities[id]
		entry.retired = true
		n.Auth.identities[id] = entry
		delete(n.Auth.active, u.Uuid)
		delete(n.Auth.usersMap, u.Uuid)

	}
	return nil
}

func (h *Hysteria2) GetUserTrafficSlice(tag string, reset bool) ([]panel.UserTraffic, error) {
	defer h.trafficGate.Lock(tag)()
	trafficSlice := make([]panel.UserTraffic, 0)
	h.nodesMu.RLock()
	defer h.nodesMu.RUnlock()
	if _, ok := h.Hy2nodes[tag]; !ok {
		return nil, nil
	}
	auth := h.Hy2nodes[tag].Auth
	auth.mutex.Lock()
	defer auth.mutex.Unlock()
	hook := h.Hy2nodes[tag].TrafficLogger.(*HookServer)
	if v, ok := hook.Counter.Load(tag); ok {
		c := v.(*counter.TrafficCounter)
		c.Counters.Range(func(key, value interface{}) bool {
			uuid := key.(string)
			traffic := value.(*counter.TrafficStorage)
			up, down, ready := traffic.Sample(func() int64 {
				if auth.identities[uuid].retired {
					return 0
				}
				return hook.ReportMinTrafficBytes
			}(), reset)
			if ready {
				if auth.uidLocked(uuid) == 0 {
					c.Delete(uuid)
					return true
				}
				trafficSlice = append(trafficSlice, panel.UserTraffic{
					UID:      auth.uidLocked(uuid),
					Upload:   up,
					Download: down,
				})
			}
			return true
		})
		auth.reapLocked(c)
		if len(trafficSlice) == 0 {
			return nil, nil
		}
		return trafficSlice, nil
	}
	return nil, nil
}

// LogTraffic and revocation share this lock. No retired generation can acquire
// a new counter or append bytes after this check, including after re-addition.
func (v *V2bX) reapLocked(c *counter.TrafficCounter) {
	scanned := 0
	for id, e := range v.identities {
		if !e.retired {
			continue
		}
		scanned++
		if scanned > 256 {
			break
		}
		if c.GetUpCount(id) != 0 || c.GetDownCount(id) != 0 {
			continue
		}
		c.Delete(id)
		delete(v.identities, id)
	}
}
