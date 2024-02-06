package hy2

import (
	"sync"

	"github.com/InazumaV/V2bX/common/counter"
)

type HookServer struct {
	Tag     string
	Counter sync.Map
}

func (h *HookServer) Log(id string, tx, rx uint64) (ok bool) {
	if c, ok := h.Counter.Load(h.Tag); ok {
		c.(*counter.TrafficCounter).Rx(id, int(rx))
		c.(*counter.TrafficCounter).Tx(id, int(rx))
		return true
	} else {
		c := counter.NewTrafficCounter()
		h.Counter.Store(h.Tag, c)
		c.Rx(id, int(rx))
		c.Tx(id, int(rx))
		return true
	}
}
