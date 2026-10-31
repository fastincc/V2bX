package hy2

import (
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
)

func (h *Hysteria2) PersistTrafficSnapshot(tag string, persist func([]panel.UserTraffic) error) error {
	defer h.trafficGate.Lock(tag)()
	h.nodesMu.RLock()
	node, ok := h.Hy2nodes[tag]
	h.nodesMu.RUnlock()
	if !ok {
		return fmt.Errorf("traffic node not found")
	}
	hook := node.TrafficLogger.(*HookServer)
	if value, ok := hook.Counter.Load(tag); ok {
		c := value.(*counter.TrafficCounter)
		node.Auth.mutex.Lock()
		snapshot := counter.PrepareSnapshot(c, node.Auth.uidLocked, func(string) int64 { return 0 })
		node.Auth.mutex.Unlock()
		if err := snapshot.Persist(persist); err != nil {
			return err
		}
		node.Auth.mutex.Lock()
		node.Auth.reapLocked(c)
		node.Auth.mutex.Unlock()
	}
	return nil
}
