package rate

import (
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juju/ratelimit"
	"github.com/xtls/xray-core/common/buf"
)

type rateWriterFixture struct{ bytes, closed, interrupted atomic.Int64 }

func (w *rateWriterFixture) WriteMultiBuffer(m buf.MultiBuffer) error {
	w.bytes.Add(int64(m.Len()))
	buf.ReleaseMulti(m)
	return nil
}
func (w *rateWriterFixture) Close() error { w.closed.Add(1); return nil }
func (w *rateWriterFixture) Interrupt()   { w.interrupted.Add(1) }
func TestWriterCancellationOwnsUnsentBuffer(t *testing.T) {
	for _, interrupt := range []bool{false, true} {
		f := new(rateWriterFixture)
		w := NewRateLimitWriter(f, ratelimit.NewBucketWithRate(1, 1)).(*Writer)
		b := buf.New()
		b.Write(make([]byte, 100))
		done := make(chan error, 1)
		go func() { done <- w.WriteMultiBuffer(buf.MultiBuffer{b}) }()
		time.Sleep(5 * time.Millisecond)
		if interrupt {
			w.Interrupt()
		} else {
			w.Close()
		}
		w.Close()
		select {
		case e := <-done:
			if !errors.Is(e, net.ErrClosed) {
				t.Fatal(e)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("writer stuck")
		}
		if f.bytes.Load() != 0 || !b.IsEmpty() {
			t.Fatal("unsent buffer forwarded or not released")
		}
		if f.closed.Load()+f.interrupted.Load() != 1 {
			t.Fatal("duplicate cleanup")
		}
	}
}
func TestWriterForwardOnce(t *testing.T) {
	f := new(rateWriterFixture)
	w := NewRateLimitWriter(f, ratelimit.NewBucketWithRate(1000000, 1000000))
	b := buf.New()
	b.Write([]byte("data"))
	if e := w.WriteMultiBuffer(buf.MultiBuffer{b}); e != nil {
		t.Fatal(e)
	}
	if f.bytes.Load() != 4 || !b.IsEmpty() {
		t.Fatal("ownership/count")
	}
}
