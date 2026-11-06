package dispatcher

import (
	"context"
	"net"
	"strings"
	"time"

	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/common/dynamic"
	"github.com/InazumaV/V2bX/common/rate"
	"github.com/InazumaV/V2bX/common/visionsplice"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/juju/ratelimit"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/protocol"
	"github.com/xtls/xray-core/proxy/vless"
)

func (d *DefaultDispatcher) SetVisionSplice(tag string, enabled bool) {
	if enabled && visionsplice.Supported {
		d.VisionSplice.Store(tag, new(visionsplice.Stats))
	} else {
		d.VisionSplice.Delete(tag)
	}
}
func (d *DefaultDispatcher) visionEnabled(tag string, user *protocol.MemoryUser) bool {
	if !visionsplice.Supported || !visionUser(user) {
		return false
	}
	if _, ok := d.VisionSplice.Load(tag); !ok {
		return false
	}
	return true
}
func visionUser(user *protocol.MemoryUser) bool {
	if user == nil {
		return false
	}
	a, ok := user.Account.(*vless.MemoryAccount)
	return ok && strings.Contains(a.Flow, "vision")
}

// The ordinary unlimited path needs shared readiness/accounting, but does not
// opt in to panel-managed Vision limits or the adjustable-splice prototype.
func basicVisionSplice(user *protocol.MemoryUser, fixed *ratelimit.Bucket, prototype bool) bool {
	return visionsplice.Supported && !prototype && fixed == nil && visionUser(user)
}
func (d *DefaultDispatcher) dynamicControl(l *limiter.Limiter, key string, u *protocol.MemoryUser, managed bool) *dynamic.Control {
	if managed {
		return l.DynamicControl(key)
	}
	return dynamicLinkControl(l, key, u)
}

type visionWriter struct {
	buf.Writer
	manager *LinkManager
	counter *counter.XrayTrafficCounter
	control *dynamic.Control
	fixed   *ratelimit.Bucket
	stats   *visionsplice.Stats
}

type basicVisionWriter struct {
	buf.Writer
	manager *LinkManager
	counter *counter.XrayTrafficCounter
}

func (w *basicVisionWriter) Close() error { return common.Close(w.Writer) }
func (w *basicVisionWriter) Interrupt()   { common.Interrupt(w.Writer) }
func (w *basicVisionWriter) CopyRawSpliceIngress(ctx context.Context, src net.Conn, dst *net.TCPConn, read, written func(int64)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	end, ok := w.manager.beginRead(func() { cancel(); src.Close(); dst.Close() })
	if !ok {
		return net.ErrClosed
	}
	defer end()
	_, err := visionsplice.CopyIngress(ctx, dst, src, func(n int64) {
		// Preserve SizeStatWriter's ingress boundary, including output errors.
		w.counter.Add(n)
		if read != nil {
			read(n)
		}
	}, written)
	return err
}

func newVisionWriter(w buf.Writer, m *LinkManager, c *counter.XrayTrafficCounter, h *dynamic.Control, b *ratelimit.Bucket) *visionWriter {
	return &visionWriter{Writer: w, manager: m, counter: c, control: h, fixed: b}
}
func (w *visionWriter) Close() error { return common.Close(w.Writer) }
func (w *visionWriter) Interrupt()   { common.Interrupt(w.Writer) }
func (w *visionWriter) CopyRawSplice(ctx context.Context, src net.Conn, dst *net.TCPConn, transportCount func(int64)) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	end, ok := w.manager.beginRead(func() { cancel(); src.Close(); dst.Close() })
	if !ok {
		return net.ErrClosed
	}
	defer end()
	_, e := visionsplice.Copy(ctx, dst, src, visionsplice.Options{
		Stats:   w.stats,
		Limited: func() bool { return w.fixed != nil || w.control.ActiveLimit() },
		Before: func(ctx context.Context, n int64) error {
			if e := w.control.Wait(n, func() (time.Time, <-chan struct{}, error) { d, _ := ctx.Deadline(); return d, ctx.Done(), ctx.Err() }); e != nil {
				return e
			}
			return rate.WaitBucketContext(ctx, w.fixed, n)
		},
		Progress: func(n int64) { w.counter.Add(n); transportCount(n) },
	})
	return e
}

// Fixed pacing on the direct reader is opt-in with the prototype. Preserve
// the original ordinary-reader path when there is no managed fixed ceiling.
type visionFixedReader struct {
	*dynamicReader
	fixed *ratelimit.Bucket
}

func newVisionReadLimiter(ctx context.Context, r buf.Reader, h *dynamic.Control, m *LinkManager, source net.Conn, b *ratelimit.Bucket) buf.Reader {
	base := newDynamicReader(ctx, r, h, m, source)
	if b == nil {
		return base
	}
	d, ok := base.(*dynamicReader)
	if !ok {
		return base
	}
	return &visionFixedReader{dynamicReader: d, fixed: b}
}
func (r *visionFixedReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	end, ok := r.manager.beginRead(r.Interrupt)
	if !ok {
		return nil, net.ErrClosed
	}
	defer end()
	mb, e := r.Reader.ReadMultiBuffer()
	if e != nil {
		defer r.cancel()
	}
	if len(mb) > 0 {
		if err := r.control.Wait(int64(mb.Len()), func() (time.Time, <-chan struct{}, error) {
			d, _ := r.ctx.Deadline()
			return d, r.ctx.Done(), r.ctx.Err()
		}); err != nil {
			buf.ReleaseMulti(mb)
			return nil, err
		}
		if err := rate.WaitBucketContext(r.ctx, r.fixed, int64(mb.Len())); err != nil {
			buf.ReleaseMulti(mb)
			return nil, err
		}
	}
	return mb, e
}

func (d *DefaultDispatcher) wrapVision(tag string, w buf.Writer, m *LinkManager, c *counter.XrayTrafficCounter, h *dynamic.Control, b *ratelimit.Bucket) buf.Writer {
	v := newVisionWriter(w, m, c, h, b)
	if stats, ok := d.VisionSplice.Load(tag); ok {
		v.stats = stats.(*visionsplice.Stats)
	}
	return v
}
func (d *DefaultDispatcher) VisionSpliceStats(tag string) (int64, int64) {
	if value, ok := d.VisionSplice.Load(tag); ok {
		s := value.(*visionsplice.Stats)
		return s.Calls.Load(), s.Bytes.Load()
	}
	return 0, 0
}
