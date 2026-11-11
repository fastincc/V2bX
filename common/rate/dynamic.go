package rate

import (
	"net"
	"sync"
	"time"

	"github.com/InazumaV/V2bX/common/dynamic"
	S "github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/xtls/xray-core/common"
	xbuf "github.com/xtls/xray-core/common/buf"
)

// The stable handle remains installed even while disabled, allowing an existing
// unlimited stream to receive a later panel policy. It exposes no unwrap path.
type DynamicConn struct {
	net.Conn
	control     *dynamic.Control
	read, write *waitState
	once        sync.Once
	err         error
}

func NewDynamicConn(c net.Conn, h *dynamic.Control) net.Conn {
	if h == nil {
		return c
	}
	return wrapDynamicDuplex(c, h)
}
func (c *DynamicConn) Read(p []byte) (int, error) {
	n, e := c.Conn.Read(p)
	c.control.Observe(int64(n))
	if w := c.control.Wait(int64(n), c.read.snapshot); w != nil && e == nil {
		e = w
	}
	return n, e
}
func (c *DynamicConn) Write(p []byte) (int, error) {
	if e := c.control.Wait(int64(len(p)), c.write.snapshot); e != nil {
		return 0, e
	}
	n, e := c.Conn.Write(p)
	c.control.Observe(int64(n))
	return n, e
}
func (c *DynamicConn) Close() error {
	c.once.Do(func() { c.read.update(time.Time{}, true); c.write.update(time.Time{}, true); c.err = c.Conn.Close() })
	return c.err
}
func (c *DynamicConn) closeRead() error { c.read.update(time.Time{}, true); return N.CloseRead(c.Conn) }
func (c *DynamicConn) closeWrite() error {
	c.write.update(time.Time{}, true)
	return N.CloseWrite(c.Conn)
}
func (c *DynamicConn) SetDeadline(t time.Time) error {
	c.read.update(t, false)
	c.write.update(t, false)
	return c.Conn.SetDeadline(t)
}
func (c *DynamicConn) SetReadDeadline(t time.Time) error {
	c.read.update(t, false)
	return c.Conn.SetReadDeadline(t)
}
func (c *DynamicConn) SetWriteDeadline(t time.Time) error {
	c.write.update(t, false)
	return c.Conn.SetWriteDeadline(t)
}

type DynamicPacketConn struct {
	N.PacketConn
	control     *dynamic.Control
	read, write *waitState
	once        sync.Once
	err         error
}

func NewDynamicPacketConn(c N.PacketConn, h *dynamic.Control) N.PacketConn {
	if h == nil {
		return c
	}
	return &DynamicPacketConn{PacketConn: c, control: h, read: newWaitState(), write: newWaitState()}
}
func (c *DynamicPacketConn) ReadPacket(b *buf.Buffer) (M.Socksaddr, error) {
	a, e := c.PacketConn.ReadPacket(b)
	if e != nil {
		return a, e
	}
	c.control.Observe(int64(b.Len()))
	return a, c.control.Wait(int64(b.Len()), c.read.snapshot)
}
func (c *DynamicPacketConn) WritePacket(b *buf.Buffer, a M.Socksaddr) error {
	n := int64(b.Len())
	if e := c.control.Wait(n, c.write.snapshot); e != nil {
		b.Release()
		return e
	}
	e := c.PacketConn.WritePacket(b, a)
	if e == nil {
		c.control.Observe(n)
	}
	return e
}
func (c *DynamicPacketConn) Close() error {
	c.once.Do(func() {
		c.read.update(time.Time{}, true)
		c.write.update(time.Time{}, true)
		c.err = c.PacketConn.Close()
	})
	return c.err
}
func (c *DynamicPacketConn) SetDeadline(t time.Time) error {
	c.read.update(t, false)
	c.write.update(t, false)
	return c.PacketConn.SetDeadline(t)
}
func (c *DynamicPacketConn) SetReadDeadline(t time.Time) error {
	c.read.update(t, false)
	return c.PacketConn.SetReadDeadline(t)
}
func (c *DynamicPacketConn) SetWriteDeadline(t time.Time) error {
	c.write.update(t, false)
	return c.PacketConn.SetWriteDeadline(t)
}
func (c *DynamicPacketConn) FrontHeadroom() int { return N.CalculateFrontHeadroom(c.PacketConn) }
func (c *DynamicPacketConn) RearHeadroom() int  { return N.CalculateRearHeadroom(c.PacketConn) }

// Xray observes bytes at its existing SizeStatWriter/CounterReader accounting
// points. This wrapper only schedules; it must not double-observe those bytes.
type DynamicWriter struct {
	writer  xbuf.Writer
	control *dynamic.Control
	wait    *waitState
	once    sync.Once
}

func NewDynamicWriter(w xbuf.Writer, h *dynamic.Control) xbuf.Writer {
	if h == nil {
		return w
	}
	return &DynamicWriter{writer: w, control: h, wait: newWaitState()}
}
func (w *DynamicWriter) WriteMultiBuffer(b xbuf.MultiBuffer) error {
	if e := w.control.Wait(int64(b.Len()), w.wait.snapshot); e != nil {
		xbuf.ReleaseMulti(b)
		return e
	}
	return w.writer.WriteMultiBuffer(b)
}
func (w *DynamicWriter) Close() error {
	var e error
	w.once.Do(func() { w.wait.update(time.Time{}, true); e = common.Close(w.writer) })
	return e
}
func (w *DynamicWriter) Interrupt() {
	w.once.Do(func() { w.wait.update(time.Time{}, true); common.Interrupt(w.writer) })
}

// Select optional half-close methods once, without adding a hot-path wrapper.
type dynamicReadConn struct{ DynamicConn }
type dynamicWriteConn struct{ DynamicConn }
type dynamicDuplexConn struct{ DynamicConn }

func (c *dynamicReadConn) CloseRead() error    { return c.closeRead() }
func (c *dynamicWriteConn) CloseWrite() error  { return c.closeWrite() }
func (c *dynamicDuplexConn) CloseRead() error  { return c.closeRead() }
func (c *dynamicDuplexConn) CloseWrite() error { return c.closeWrite() }
func wrapDynamicDuplex(c net.Conn, h *dynamic.Control) net.Conn {
	_, rd := S.Cast[N.ReadCloser](c)
	_, wr := S.Cast[N.WriteCloser](c)
	switch {
	case rd && wr:
		return &dynamicDuplexConn{DynamicConn{Conn: c, control: h, read: newWaitState(), write: newWaitState()}}
	case rd:
		return &dynamicReadConn{DynamicConn{Conn: c, control: h, read: newWaitState(), write: newWaitState()}}
	case wr:
		return &dynamicWriteConn{DynamicConn{Conn: c, control: h, read: newWaitState(), write: newWaitState()}}
	default:
		return &DynamicConn{Conn: c, control: h, read: newWaitState(), write: newWaitState()}
	}
}
