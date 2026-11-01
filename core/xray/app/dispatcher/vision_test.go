package dispatcher

import (
	"context"
	"github.com/InazumaV/V2bX/common/dynamic"
	"github.com/juju/ratelimit"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/proxy/vless"
	"io"
	"runtime"
	"sync"
	"testing"
)

func TestVisionPrototypeScopeAndSharedHandshakeState(t *testing.T) {
	d := new(DefaultDispatcher)
	user := &protocol.MemoryUser{Email: "generation", Account: &vless.MemoryAccount{Flow: "xtls-rprx-vision"}}
	if d.visionEnabled("a", user) {
		t.Fatal("default must be off")
	}
	d.SetVisionSplice("a", true)
	if d.visionEnabled("a", user) != (runtime.GOOS == "linux") {
		t.Fatal("platform gating")
	}
	if d.visionEnabled("b", user) || d.visionEnabled("a", &protocol.MemoryUser{}) {
		t.Fatal("scope leak")
	}
	d.SetVisionSplice("a", false)
	if d.visionEnabled("a", user) {
		t.Fatal("disable")
	}
	d.LinkManagers.Store("generation", NewLinkManager("a|synthetic"))
	in := &session.Inbound{User: user, CanSpliceCopy: 2}
	in.EnableManagedSplice()
	out := session.InboundFromContext(d.routeContext(session.ContextWithInbound(context.Background(), in)))
	in.SetSpliceCopy(1)
	if out.GetSpliceCopy() != 1 || out.User.Email != "a|synthetic" || in.User.Email != "generation" {
		t.Fatal("routing snapshot lost shared handshake or identity")
	}
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for n := 0; n < 1000; n++ {
				in.SetSpliceCopy(1)
				_ = out.GetSpliceCopy()
			}
		}()
	}
	wg.Wait()
}

type visionTailReader struct{}

func (visionTailReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	b := buf.New()
	b.Write([]byte("tail"))
	return buf.MultiBuffer{b}, io.EOF
}
func TestVisionFixedReaderPreservesFinalBuffer(t *testing.T) {
	h := dynamic.New(1)
	defer h.Close()
	m := NewLinkManager("synthetic")
	r := newVisionReadLimiter(context.Background(), visionTailReader{}, h, m, nil, ratelimit.NewBucketWithRate(125000, 125000))
	mb, e := r.ReadMultiBuffer()
	defer buf.ReleaseMulti(mb)
	if e != io.EOF || mb.Len() != 4 {
		t.Fatal("final buffer discarded", e)
	}
}
