package dispatcher

import (
	"github.com/xtls/xray-core/common"
	"net"
	"sync/atomic"
	"time"

	"github.com/xtls/xray-core/common/buf"
)

var _ buf.TimeoutReader = (*CounterReader)(nil)

type CounterReader struct {
	Reader  buf.TimeoutReader
	Counter *atomic.Int64
	Manager *LinkManager
	Source  net.Conn
}

func (c *CounterReader) ReadMultiBufferTimeout(timeout time.Duration) (buf.MultiBuffer, error) {
	if c.Manager != nil {
		end, ok := c.Manager.beginRead(func() {
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
	}
	return mb, nil
}

func (c *CounterReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	if c.Manager != nil {
		end, ok := c.Manager.beginRead(func() {
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
	}
	return mb, nil
}
