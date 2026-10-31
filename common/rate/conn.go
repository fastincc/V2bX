package rate

import (
	"net"
	"sync"
	"time"

	"github.com/juju/ratelimit"
	N "github.com/sagernet/sing/common/network"
)

func NewConnRateLimiter(c net.Conn, l *ratelimit.Bucket) *Conn {
	return &Conn{Conn: c, limiter: l, read: newWaitState(), write: newWaitState()}
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
func (c *Conn) CloseRead() error  { c.read.update(time.Time{}, true); return N.CloseRead(c.Conn) }
func (c *Conn) CloseWrite() error { c.write.update(time.Time{}, true); return N.CloseWrite(c.Conn) }
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
