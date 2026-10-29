package hy2

import "fmt"

func (h *Hysteria2) DrainTraffic(tag string) error {
	h.nodesMu.RLock()
	defer h.nodesMu.RUnlock()
	n, ok := h.Hy2nodes[tag]
	if !ok {
		return fmt.Errorf("node not found")
	}
	n.Auth.mutex.Lock()
	n.Auth.paused = true
	n.Auth.mutex.Unlock()
	return nil
}
func (h *Hysteria2) ResumeTraffic(tag string) error {
	h.nodesMu.RLock()
	defer h.nodesMu.RUnlock()
	n, ok := h.Hy2nodes[tag]
	if !ok {
		return fmt.Errorf("node not found")
	}
	n.Auth.mutex.Lock()
	n.Auth.paused = false
	n.Auth.mutex.Unlock()
	return nil
}
