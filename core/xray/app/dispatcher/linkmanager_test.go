package dispatcher

import (
	"context"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"sync/atomic"
	"testing"
)

type delayedWriter struct {
	started, finish chan struct{}
	bytes           atomic.Int64
}

func (w *delayedWriter) WriteMultiBuffer(b buf.MultiBuffer) error {
	close(w.started)
	<-w.finish
	w.bytes.Add(int64(b.Len()))
	buf.ReleaseMulti(b)
	return nil
}
func TestRetiredManagerWaitsForFinalAccounting(t *testing.T) {
	lm := NewLinkManager("synthetic")
	d := &delayedWriter{started: make(chan struct{}), finish: make(chan struct{})}
	w := lm.wrap(d, nil)
	b := buf.New()
	b.Write([]byte("tail"))
	done := make(chan struct{})
	go func() { w.WriteMultiBuffer(buf.MultiBuffer{b}); close(done) }()
	<-d.started
	lm.CloseAll()
	if lm.Quiescent() {
		t.Fatal("in-flight count released early")
	}
	b = buf.New()
	b.Write([]byte("new"))
	if w.WriteMultiBuffer(buf.MultiBuffer{b}) == nil {
		t.Fatal("new work accepted after close")
	}
	close(d.finish)
	<-done
	if !lm.Quiescent() || d.bytes.Load() != 4 {
		t.Fatal("final accounting lost")
	}
}

func TestGenerationPreservesRoutingIdentity(t *testing.T) {
	d := new(DefaultDispatcher)
	d.LinkManagers.Store("generation", NewLinkManager("node|synthetic"))
	user := &protocol.MemoryUser{Email: "generation"}
	ctx := session.ContextWithInbound(context.Background(), &session.Inbound{User: user, Tag: "node"})
	out := session.InboundFromContext(d.routeContext(ctx))
	if out.User.Email != "node|synthetic" || user.Email != "generation" || out.Tag != "node" {
		t.Fatal("routing identity compatibility failed")
	}
}
