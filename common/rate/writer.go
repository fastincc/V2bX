package rate

import (
	"sync"
	"time"

	"github.com/juju/ratelimit"
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
)

type Writer struct {
	writer   buf.Writer
	limiter  *ratelimit.Bucket
	wait     *waitState
	once     sync.Once
	closeErr error
}

func NewRateLimitWriter(writer buf.Writer, limiter *ratelimit.Bucket) buf.Writer {
	return &Writer{writer: writer, limiter: limiter, wait: newWaitState()}
}
func (w *Writer) Close() error {
	w.once.Do(func() { w.wait.update(time.Time{}, true); w.closeErr = common.Close(w.writer) })
	return w.closeErr
}
func (w *Writer) Interrupt() {
	w.once.Do(func() { w.wait.update(time.Time{}, true); common.Interrupt(w.writer) })
}
func (w *Writer) WriteMultiBuffer(mb buf.MultiBuffer) error {
	if err := w.wait.wait(w.limiter, int64(mb.Len())); err != nil {
		buf.ReleaseMulti(mb)
		return err
	}
	return w.writer.WriteMultiBuffer(mb)
}
