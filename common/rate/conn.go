package rate

import (
	"net"
	"sync"
	"time"

	"github.com/juju/ratelimit"
	S "github.com/sagernet/sing/common"
	N "github.com/sagernet/sing/common/network"
)

func NewConnRateLimiter(c net.Conn, l *ratelimit.Bucket) net.Conn {
	return wrapRateDuplex(c, l)
}

type Conn struct {
	net.Conn
	limiter     *ratelimit.Bucket
	read, write *waitState
	once        sync.Once
	closeErr    error
}

func (c *Conn) Read(b []byte) (n int, err error) {
	if err = c.read.check(); err != nil {
		return 0, err
	}
	n, err = c.Conn.Read(b)
	if n > 0 {
		if waitErr := c.read.wait(c.limiter, int64(n)); err == nil {
			err = waitErr
		}
	}
	return n, err
}
func (c *Conn) Write(b []byte) (n int, err error) {
	if err = c.write.check(); err != nil {
		return 0, err
	}
	n, err = c.Conn.Write(b)
	if n > 0 {
		if waitErr := c.write.wait(c.limiter, int64(n)); err == nil {
			err = waitErr
		}
	}
	return n, err
}
func (c *Conn) Close() error {
	c.once.Do(func() {
		c.read.update(time.Time{}, true)
		c.write.update(time.Time{}, true)
		c.closeErr = c.Conn.Close()
	})
	return c.closeErr
}
func (c *Conn) closeRead() error  { c.read.update(time.Time{}, true); return N.CloseRead(c.Conn) }
func (c *Conn) closeWrite() error { c.write.update(time.Time{}, true); return N.CloseWrite(c.Conn) }
func (c *Conn) SetDeadline(t time.Time) error {
	c.read.update(t, false)
	c.write.update(t, false)
	return c.Conn.SetDeadline(t)
}
func (c *Conn) SetReadDeadline(t time.Time) error {
	c.read.update(t, false)
	return c.Conn.SetReadDeadline(t)
}
func (c *Conn) SetWriteDeadline(t time.Time) error {
	c.write.update(t, false)
	return c.Conn.SetWriteDeadline(t)
}

// Select optional half-close methods once, without adding a hot-path wrapper.
type rateReadConn struct{ Conn }
type rateWriteConn struct{ Conn }
type rateDuplexConn struct{ Conn }

func (c *rateReadConn) CloseRead() error    { return c.closeRead() }
func (c *rateWriteConn) CloseWrite() error  { return c.closeWrite() }
func (c *rateDuplexConn) CloseRead() error  { return c.closeRead() }
func (c *rateDuplexConn) CloseWrite() error { return c.closeWrite() }
func wrapRateDuplex(c net.Conn, l *ratelimit.Bucket) net.Conn {
	_, rd := S.Cast[N.ReadCloser](c)
	_, wr := S.Cast[N.WriteCloser](c)
	switch {
	case rd && wr:
		return &rateDuplexConn{Conn{Conn: c, limiter: l, read: newWaitState(), write: newWaitState()}}
	case rd:
		return &rateReadConn{Conn{Conn: c, limiter: l, read: newWaitState(), write: newWaitState()}}
	case wr:
		return &rateWriteConn{Conn{Conn: c, limiter: l, read: newWaitState(), write: newWaitState()}}
	default:
		return &Conn{Conn: c, limiter: l, read: newWaitState(), write: newWaitState()}
	}
}
