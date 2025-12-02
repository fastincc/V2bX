package rate

import (
	"net"

	"github.com/juju/ratelimit"
)

func NewConnRateLimiter(c net.Conn, l *ratelimit.Bucket) *Conn {
	return &Conn{
		Conn:    c,
		limiter: l,
	}
}

type Conn struct {
	net.Conn
	limiter *ratelimit.Bucket
}

func (c *Conn) Read(b []byte) (n int, err error) {
	c.limiter.Wait(int64(len(b)))
	return c.Conn.Read(b)
}

func (c *Conn) Write(b []byte) (n int, err error) {
	c.limiter.Wait(int64(len(b)))
	return c.Conn.Write(b)
}