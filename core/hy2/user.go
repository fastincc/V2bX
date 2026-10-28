package hy2

import (
	"fmt"
	"net"
	"sync"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	vCore "github.com/InazumaV/V2bX/core"
	"github.com/apernet/hysteria/core/v2/server"
)

var _ server.Authenticator = &V2bX{}

type V2bX struct {
	usersMap map[string]int
	mutex    sync.RWMutex
}

func (v *V2bX) Authenticate(addr net.Addr, auth string, tx uint64) (ok bool, id string) {
	v.mutex.RLock()
	defer v.mutex.RUnlock()
	if _, exists := v.usersMap[auth]; exists {
		return true, auth
	}
	return false, ""
}

func (h *Hysteria2) AddUsers(p *vCore.AddUsersParams) (int, error) {
	h.nodesMu.RLock()
	defer h.nodesMu.RUnlock()
	n, ok := h.Hy2nodes[p.Tag]
	if !ok {
		return 0, fmt.Errorf("node not found")
	}
	n.Auth.mutex.Lock()
	defer n.Auth.mutex.Unlock()
	for _, u := range p.Users {
		n.Auth.usersMap[u.Uuid] = u.Id
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
		delete(n.Auth.usersMap, u.Uuid)
		if v, ok := n.TrafficLogger.(*HookServer).Counter.Load(tag); ok {
			v.(*counter.TrafficCounter).Delete(u.Uuid)
		}
	}
	return nil
}

func (h *Hysteria2) GetUserTrafficSlice(tag string, reset bool) ([]panel.UserTraffic, error) {
	trafficSlice := make([]panel.UserTraffic, 0)
	h.nodesMu.RLock()
	defer h.nodesMu.RUnlock()
	if _, ok := h.Hy2nodes[tag]; !ok {
		return nil, nil
	}
	auth := h.Hy2nodes[tag].Auth
	auth.mutex.RLock()
	defer auth.mutex.RUnlock()
	hook := h.Hy2nodes[tag].TrafficLogger.(*HookServer)
	if v, ok := hook.Counter.Load(tag); ok {
		c := v.(*counter.TrafficCounter)
		c.Counters.Range(func(key, value interface{}) bool {
			uuid := key.(string)
			traffic := value.(*counter.TrafficStorage)
			up, down, ready := traffic.Sample(hook.ReportMinTrafficBytes, reset)
			if ready {
				if auth.usersMap[uuid] == 0 {
					c.Delete(uuid)
					return true
				}
				trafficSlice = append(trafficSlice, panel.UserTraffic{
					UID:      auth.usersMap[uuid],
					Upload:   up,
					Download: down,
				})
			}
			return true
		})
		if len(trafficSlice) == 0 {
			return nil, nil
		}
		return trafficSlice, nil
	}
	return nil, nil
}
