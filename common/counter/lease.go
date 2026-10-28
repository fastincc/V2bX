package counter

import (
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"net"
	"sync"
)

// A lease outlives Close until every in-flight I/O has accounted its final bytes.
// Do not expose counter-unwrapping callbacks: those can run after Close returns.
type lease struct {
	mu                        sync.Mutex
	operations                int
	closing, closed, released bool
	storage                   *TrafficStorage
	once                      sync.Once
}

func newLease(s *TrafficStorage) *lease { s.Owners.Add(1); return &lease{storage: s} }
func (l *lease) begin() bool {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.closing {
		return false
	}
	l.operations++
	return true
}
func (l *lease) release() {
	if l.closed && l.operations == 0 && !l.released {
		l.released = true
		l.storage.unregister(l)
		l.storage.Owners.Add(-1)
	}
}
func (l *lease) end() { l.mu.Lock(); defer l.mu.Unlock(); l.operations--; l.release() }
func (l *lease) close(f func() error) (err error) {
	l.once.Do(func() {
		l.mu.Lock()
		l.closing = true
		l.mu.Unlock()
		err = f()
		l.mu.Lock()
		l.closed = true
		l.release()
		l.mu.Unlock()
	})
	return
}

type leasedConn struct {
	net.Conn
	owner *lease
}

func NewLeasedConn(c net.Conn, s *TrafficStorage) net.Conn {
	v := &leasedConn{c, newLease(s)}
	s.register(v.owner, v.Close)
	return v
}
func (c *leasedConn) Read(p []byte) (int, error) {
	if !c.owner.begin() {
		return 0, net.ErrClosed
	}
	defer c.owner.end()
	n, e := c.Conn.Read(p)
	c.owner.storage.UpCounter.Add(int64(n))
	return n, e
}
func (c *leasedConn) Write(p []byte) (int, error) {
	if !c.owner.begin() {
		return 0, net.ErrClosed
	}
	defer c.owner.end()
	n, e := c.Conn.Write(p)
	c.owner.storage.DownCounter.Add(int64(n))
	return n, e
}
func (c *leasedConn) Close() error      { return c.owner.close(c.Conn.Close) }
func (c *leasedConn) CloseRead() error  { return N.CloseRead(c.Conn) }
func (c *leasedConn) CloseWrite() error { return N.CloseWrite(c.Conn) }

type leasedPacketConn struct {
	N.PacketConn
	owner *lease
}

func NewLeasedPacketConn(c N.PacketConn, s *TrafficStorage) N.PacketConn {
	v := &leasedPacketConn{c, newLease(s)}
	s.register(v.owner, v.Close)
	return v
}
func (c *leasedPacketConn) ReadPacket(b *buf.Buffer) (M.Socksaddr, error) {
	if !c.owner.begin() {
		return M.Socksaddr{}, net.ErrClosed
	}
	defer c.owner.end()
	a, e := c.PacketConn.ReadPacket(b)
	if e == nil {
		c.owner.storage.UpCounter.Add(int64(b.Len()))
	}
	return a, e
}
func (c *leasedPacketConn) WritePacket(b *buf.Buffer, a M.Socksaddr) error {
	if !c.owner.begin() {
		return net.ErrClosed
	}
	defer c.owner.end()
	n := b.Len()
	e := c.PacketConn.WritePacket(b, a)
	if e == nil {
		c.owner.storage.DownCounter.Add(int64(n))
	}
	return e
}
func (c *leasedPacketConn) Close() error { return c.owner.close(c.PacketConn.Close) }
