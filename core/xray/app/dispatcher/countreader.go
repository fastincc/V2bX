package dispatcher

import (
	"context"
	"github.com/InazumaV/V2bX/common/dynamic"
	"github.com/xtls/xray-core/common"
	"net"
	"sync"
	"sync/atomic"
	"time"

	"github.com/xtls/xray-core/common/buf"
)

var _ buf.TimeoutReader = (*CounterReader)(nil)

type CounterReader struct {
	Reader   buf.TimeoutReader
	Counter  *atomic.Int64
	Manager  *LinkManager
	Source   net.Conn
	Dynamic  *dynamic.Control
	Context  context.Context
	stopOnce sync.Once
	initOnce sync.Once
	stopped  chan struct{}
}

func (c *CounterReader) ReadMultiBufferTimeout(timeout time.Duration) (buf.MultiBuffer, error) {
	c.initOnce.Do(func() { c.stopped = make(chan struct{}) })
	if c.Manager != nil {
		end, ok := c.Manager.beginRead(func() {
			c.stopOnce.Do(func() { close(c.stopped) })
			if c.Source != nil {
				c.Source.Close()
			} else {
				common.Interrupt(c.Reader)
			}
		})
		if !ok {
			return nil, net.ErrClosed
		}
		defer end()
	}
	mb, err := c.Reader.ReadMultiBufferTimeout(timeout)
	if err != nil {
		return nil, err
	}
	if mb.Len() > 0 {
		c.Counter.Add(int64(mb.Len()))
		c.Dynamic.Observe(int64(mb.Len()))
		if e := c.Dynamic.Wait(int64(mb.Len()), c.dynamicState); e != nil {
			buf.ReleaseMulti(mb)
			return nil, e
		}
	}
	return mb, nil
}

func (c *CounterReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	c.initOnce.Do(func() { c.stopped = make(chan struct{}) })
	if c.Manager != nil {
		end, ok := c.Manager.beginRead(func() {
			c.stopOnce.Do(func() { close(c.stopped) })
			if c.Source != nil {
				c.Source.Close()
			} else {
				common.Interrupt(c.Reader)
			}
		})
		if !ok {
			return nil, net.ErrClosed
		}
		defer end()
	}
	mb, err := c.Reader.ReadMultiBuffer()
	if err != nil {
		return nil, err
	}
	if mb.Len() > 0 {
		c.Counter.Add(int64(mb.Len()))
		c.Dynamic.Observe(int64(mb.Len()))
		if e := c.Dynamic.Wait(int64(mb.Len()), c.dynamicState); e != nil {
			buf.ReleaseMulti(mb)
			return nil, e
		}
	}
	return mb, nil
}

func (c *CounterReader) dynamicState() (time.Time, <-chan struct{}, error) {
	select {
	case <-c.stopped:
		return time.Time{}, c.stopped, net.ErrClosed
	default:
	}
	if c.Context != nil {
		d, _ := c.Context.Deadline()
		return d, c.stopped, c.Context.Err()
	}
	return time.Time{}, c.stopped, nil
}
