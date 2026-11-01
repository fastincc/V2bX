package dispatcher

import (
	"context"
	"github.com/InazumaV/V2bX/common/dynamic"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"net"
	"sync"
	"time"
)

// Pace after sniffing/cache assembly, never inside ReadMultiBufferTimeout.
// Otherwise a rate timeout can discard a successfully pre-read protocol prefix.
// CounterReader still observes/accounts ingress once, including cached bytes.
type dynamicReader struct {
	buf.Reader
	control *dynamic.Control
	manager *LinkManager
	source  net.Conn
	ctx     context.Context
	cancel  context.CancelFunc
	once    sync.Once
}

func newDynamicReader(ctx context.Context, r buf.Reader, c *dynamic.Control, m *LinkManager, source net.Conn) buf.Reader {
	if c == nil {
		return r
	}
	ctx, cancel := context.WithCancel(ctx)
	return &dynamicReader{Reader: r, control: c, manager: m, source: source, ctx: ctx, cancel: cancel}
}
func (r *dynamicReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	if r.manager != nil {
		end, ok := r.manager.beginRead(r.Interrupt)
		if !ok {
			return nil, net.ErrClosed
		}
		defer end()
	}
	mb, e := r.Reader.ReadMultiBuffer()
	if len(mb) > 0 {
		if err := r.control.Wait(int64(mb.Len()), func() (time.Time, <-chan struct{}, error) {
			d, _ := r.ctx.Deadline()
			return d, r.ctx.Done(), r.ctx.Err()
		}); err != nil {
			buf.ReleaseMulti(mb)
			return nil, err
		}
	}
	if e != nil {
		r.cancel()
	}
	return mb, e
}
func (r *dynamicReader) Interrupt() {
	r.once.Do(func() {
		r.cancel()
		common.Interrupt(r.Reader)
		if r.source != nil {
			r.source.Close()
		}
	})
}
func (r *dynamicReader) Close() error { r.Interrupt(); return common.Close(r.Reader) }
