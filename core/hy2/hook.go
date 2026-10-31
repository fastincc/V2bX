package hy2

import (
	"sync"

	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/apernet/hysteria/core/v2/server"
	"go.uber.org/zap"
)

var _ server.TrafficLogger = (*HookServer)(nil)

type HookServer struct {
	Auth                  *V2bX
	Tag                   string
	logger                *zap.Logger
	Counter               sync.Map
	ReportMinTrafficBytes int64
}

func (h *HookServer) TraceStream(stream server.HyStream, stats *server.StreamStats) {
	if h.Auth == nil {
		return
	}
	h.Auth.mutex.Lock()
	defer h.Auth.mutex.Unlock()
	s := h.Auth.sessions[stats.AuthID]
	if s == nil || s.closed {
		return
	}
	if h.Auth.streams == nil {
		h.Auth.streams = make(map[server.HyStream]*hySession)
	}
	if _, exists := h.Auth.streams[stream]; exists {
		return
	}
	s.streams++
	h.Auth.streams[stream] = s
}

func (h *HookServer) UntraceStream(stream server.HyStream) {
	if h.Auth == nil {
		return
	}
	h.Auth.mutex.Lock()
	defer h.Auth.mutex.Unlock()
	if s := h.Auth.streams[stream]; s != nil {
		delete(h.Auth.streams, stream)
		s.streams--
		if s.closed && s.streams == 0 && (!s.managed || s.drained) {
			delete(h.Auth.sessions, s.id)
		}
	}
}

func (h *HookServer) LogTraffic(id string, tx, rx uint64) (ok bool) {
	// Deny stale sessions after revocation as well as cross-node identities.
	if h.Auth == nil {
		return false
	}
	h.Auth.mutex.Lock()
	defer h.Auth.mutex.Unlock()
	session := h.Auth.sessions[id]
	if session == nil || session.denied {
		return false
	}
	generation := session.generation
	identity, exists := h.Auth.identities[generation]
	if h.Auth.paused || !exists || identity.retired || h.Auth.active[identity.credential] != generation {
		return false
	}
	limiterinfo, err := limiter.GetLimiter(h.Tag)
	if err != nil {
		return false
	}
	if _, ok := limiterinfo.UserLimitInfo.Load(format.UserTag(h.Tag, identity.credential)); !ok {
		return false
	}

	tc := counter.LoadOrCreate(&h.Counter, h.Tag)
	tc.Rx(generation, int(rx))
	tc.Tx(generation, int(tx))
	return true
}

// The bundled Hy2 core calls offline only after all transport callbacks drain.
// A physical disconnect alone must not discard ingress already read by a worker.
func (h *HookServer) LogOnlineState(id string, online bool) {
	if h.Auth == nil {
		return
	}
	h.Auth.mutex.Lock()
	defer h.Auth.mutex.Unlock()
	if s := h.Auth.sessions[id]; s != nil {
		if online {
			s.managed = true
		} else {
			s.drained = true
			if s.closed && s.streams == 0 {
				delete(h.Auth.sessions, id)
			}
		}
	}
}
