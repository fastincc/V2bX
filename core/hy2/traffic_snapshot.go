package hy2

import (
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
)

func (h *Hysteria2) PersistTrafficSnapshot(tag string, persist func([]panel.UserTraffic) error) error {
	h.nodesMu.RLock()
	defer h.nodesMu.RUnlock()
	node, ok := h.Hy2nodes[tag]
	if !ok {
		return fmt.Errorf("traffic node not found")
	}
	node.Auth.mutex.Lock()
	defer node.Auth.mutex.Unlock()
	hook := node.TrafficLogger.(*HookServer)
	if value, ok := hook.Counter.Load(tag); ok {
		c := value.(*counter.TrafficCounter)
		err := counter.PersistSnapshot(c, node.Auth.uidLocked, func(string) int64 { return 0 }, persist)
		if err == nil {
			node.Auth.reapLocked(c)
		}
		return err
	}
	return nil
}
