package dispatcher

import (
	"context"
	"github.com/InazumaV/V2bX/common/dynamic"
	"github.com/xtls/xray-core/common/buf"
	"io"
	"sync/atomic"
	"testing"
	"time"
)

type dynamicPrefixReader struct{ read bool }

func (r *dynamicPrefixReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	if r.read {
		return nil, io.EOF
	}
	r.read = true
	mb := make(buf.MultiBuffer, 16)
	for i := range mb {
		mb[i] = buf.New()
		mb[i].Extend(buf.Size)
	}
	return mb, nil
}
func (r *dynamicPrefixReader) ReadMultiBufferTimeout(time.Duration) (buf.MultiBuffer, error) {
	return r.ReadMultiBuffer()
}
func TestDynamicXraySniffPrefixPreservedAndPacedOnce(t *testing.T) {
	h := dynamic.New(0)
	defer h.Close()
	h.Apply(&dynamic.Policy{Version: 1, Revision: 1, Enabled: true, WindowSeconds: 60, ThresholdBytes: 1, SpeedMbps: 1, DurationSeconds: 30}, time.Now())
	h.Observe(1)
	h.Tick(time.Now())
	var charged atomic.Int64
	reader := &CounterReader{Reader: &dynamicPrefixReader{}, Counter: &charged, Dynamic: h}
	cache := &cachedReader{reader: reader}
	prefix := buf.New()
	defer prefix.Release()
	start := time.Now()
	if e := cache.Cache(prefix, 20*time.Millisecond); e != nil {
		t.Fatal(e)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("sniff timeout consumed by rate wait")
	}
	if charged.Load() != int64(16*buf.Size) {
		t.Fatal("ingress count", charged.Load())
	}
	r := newDynamicReader(context.Background(), cache, h, nil, nil)
	defer r.(*dynamicReader).Close()
	mb, e := r.ReadMultiBuffer()
	defer buf.ReleaseMulti(mb)
	if e != nil || mb.Len() != 16*buf.Size {
		t.Fatal("sniff prefix lost", e, mb.Len())
	}
	if time.Since(start) < 230*time.Millisecond {
		t.Fatal("cached prefix bypassed quota")
	}
	if charged.Load() != int64(16*buf.Size) {
		t.Fatal("cached prefix charged twice")
	}
}
func TestDynamicXrayDirectReadDrainCancellation(t *testing.T) {
	h := dynamic.New(0)
	defer h.Close()
	h.Apply(&dynamic.Policy{Version: 1, Revision: 1, Enabled: true, WindowSeconds: 60, ThresholdBytes: 1, SpeedMbps: 1, DurationSeconds: 30}, time.Now())
	h.Observe(1)
	h.Tick(time.Now())
	lm := NewLinkManager("synthetic")
	r := newDynamicReader(context.Background(), &dynamicPrefixReader{}, h, lm, nil)
	done := make(chan error, 1)
	go func() { mb, e := r.ReadMultiBuffer(); buf.ReleaseMulti(mb); done <- e }()
	time.Sleep(20 * time.Millisecond)
	lm.CloseAll()
	select {
	case e := <-done:
		if e == nil {
			t.Fatal("drained read succeeded")
		}
	case <-time.After(200 * time.Millisecond):
		t.Fatal("direct read pacing did not cancel")
	}
	if !lm.Quiescent() {
		t.Fatal("reader lifecycle leaked")
	}
}
