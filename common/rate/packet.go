package rate

import (
	"github.com/juju/ratelimit"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"net"
	"os"
	"sync"
	"time"
)

// PacketConn keeps datagram boundaries and shares the existing per-user bucket
// with TCP. It deliberately exposes no unwrapping path that could bypass limits.
type PacketConn struct {
	closeErr error
	N.PacketConn
	bucket                      *ratelimit.Bucket
	done                        chan struct{}
	once                        sync.Once
	mu                          sync.Mutex
	readDeadline, writeDeadline time.Time
	changed                     chan struct{}
}

func NewPacketConnRateLimiter(c N.PacketConn, b *ratelimit.Bucket) *PacketConn {
	return &PacketConn{PacketConn: c, bucket: b, done: make(chan struct{}), changed: make(chan struct{})}
}
func (c *PacketConn) wait(n int, read bool) error {
	select {
	case <-c.done:
		return net.ErrClosed
	default:
	}
	ready := time.Now().Add(c.bucket.Take(int64(n)))
	for {
		c.mu.Lock()
		deadline := c.writeDeadline
		if read {
			deadline = c.readDeadline
		}
		changed := c.changed
		c.mu.Unlock()
		now := time.Now()
		if !deadline.IsZero() && !deadline.After(now) {
			return os.ErrDeadlineExceeded
		}
		if !ready.After(now) {
			return nil
		}
		end := ready
		if !deadline.IsZero() && deadline.Before(end) {
			end = deadline
		}
		timer := time.NewTimer(time.Until(end))
		select {
		case <-c.done:
			timer.Stop()
			return net.ErrClosed
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
}
func (c *PacketConn) ReadPacket(b *buf.Buffer) (M.Socksaddr, error) {
	a, e := c.PacketConn.ReadPacket(b)
	if e != nil {
		return a, e
	}
	if e = c.wait(b.Len(), true); e != nil {
		return a, e
	}
	return a, nil
}
func (c *PacketConn) WritePacket(b *buf.Buffer, a M.Socksaddr) error {
	if e := c.wait(b.Len(), false); e != nil {
		b.Release()
		return e
	}
	return c.PacketConn.WritePacket(b, a)
}
func (c *PacketConn) Close() error {
	c.once.Do(func() { close(c.done); c.closeErr = c.PacketConn.Close() })
	return c.closeErr
}
func (c *PacketConn) setDeadline(t time.Time, read, write bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if read {
		c.readDeadline = t
	}
	if write {
		c.writeDeadline = t
	}
	close(c.changed)
	c.changed = make(chan struct{})
}
func (c *PacketConn) SetDeadline(t time.Time) error {
	c.setDeadline(t, true, true)
	return c.PacketConn.SetDeadline(t)
}
func (c *PacketConn) SetReadDeadline(t time.Time) error {
	c.setDeadline(t, true, false)
	return c.PacketConn.SetReadDeadline(t)
}
func (c *PacketConn) SetWriteDeadline(t time.Time) error {
	c.setDeadline(t, false, true)
	return c.PacketConn.SetWriteDeadline(t)
}

func (c *PacketConn) FrontHeadroom() int { return N.CalculateFrontHeadroom(c.PacketConn) }
func (c *PacketConn) RearHeadroom() int  { return N.CalculateRearHeadroom(c.PacketConn) }
