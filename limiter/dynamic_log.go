package limiter

import (
	"sync"
	"sync/atomic"
	"time"

	log "github.com/sirupsen/logrus"
)

const dynamicDiagnosticCapacity = 256

type dynamicDiagnosticKind uint8

const (
	dynamicChanged dynamicDiagnosticKind = iota
	dynamicRejected
	dynamicUnsupported
)

// Only safe scalar diagnostics belong here, never traffic/audit records,
// credentials, complete user objects, or references to a live Control.
type dynamicDiagnostic struct {
	at     time.Time
	uid    int
	kind   dynamicDiagnosticKind
	state  string
	reason string
}

func (e dynamicDiagnostic) level() log.Level {
	if e.kind == dynamicChanged {
		return log.InfoLevel
	}
	return log.WarnLevel
}

// A blocked io.Writer/Hook cannot generally be cancelled. Keep one lazy worker
// for the whole process, not one per user, node, reload, or diagnostic. Neither
// a node's policy scheduler nor its Close waits for this process-lifetime worker.
// The fixed queue and drop count bound resources even if the sink never returns.
type dynamicDiagnosticLog struct {
	logger  *log.Logger
	events  chan dynamicDiagnostic
	dropped atomic.Uint64
	start   sync.Once
	done    chan struct{} // isolated test drain observation; never awaited by node Close
}

func newDynamicDiagnosticLog(logger *log.Logger) *dynamicDiagnosticLog {
	return &dynamicDiagnosticLog{
		logger: logger,
		events: make(chan dynamicDiagnostic, dynamicDiagnosticCapacity),
		done:   make(chan struct{}),
	}
}

var dynamicDiagnostics = newDynamicDiagnosticLog(log.StandardLogger())

func (q *dynamicDiagnosticLog) emit(e dynamicDiagnostic) {
	// In the pinned Logrus version this only reads the atomic level; it does
	// not acquire the logger mutex that a blocked Writer might already hold.
	if !q.logger.IsLevelEnabled(e.level()) {
		return
	}
	q.start.Do(func() { go q.run() })
	select {
	case q.events <- e:
	default:
		q.dropped.Add(1)
	}
}

func (q *dynamicDiagnosticLog) run() {
	defer close(q.done)
	var reported uint64
	// Production never closes this channel: shutting down a node must neither
	// close a shared process queue nor wait for arbitrary logging IO.
	for e := range q.events {
		fields := log.Fields{"uid": e.uid}
		var message string
		switch e.kind {
		case dynamicChanged:
			fields["state"] = e.state
			message = "Dynamic speed limit changed"
		case dynamicRejected:
			fields["reason"] = e.reason
			message = "Dynamic speed policy rejected"
		case dynamicUnsupported:
			message = "Dynamic speed policy unsupported on Xray Vision raw copy; fixed limit unchanged"
		}
		q.logger.WithTime(e.at).WithFields(fields).Log(e.level(), message)
		if total := q.dropped.Load(); total != reported && q.logger.IsLevelEnabled(log.WarnLevel) {
			q.logger.WithFields(log.Fields{"dropped": total - reported, "dropped_total": total}).Warn("Dynamic speed diagnostic queue overflow; policy execution continued")
			reported = total
		}
	}
}
