package sing

import (
	"net"
	"time"

	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

// Closing a UDP NAT session does not guarantee that buffered reads or writes
// stop immediately. The router continues with the tracker's returned connection,
// so return a terminal connection that cannot expose or unwrap the rejected one.
func rejectPacketConnection(conn N.PacketConn) N.PacketConn {
	addr := conn.LocalAddr()
	err := conn.Close()
	return &rejectedPacketConn{addr: addr, closeErr: err}
}

type rejectedPacketConn struct {
	addr     net.Addr
	closeErr error
}

var _ N.PacketConn = (*rejectedPacketConn)(nil)

func (*rejectedPacketConn) ReadPacket(*buf.Buffer) (M.Socksaddr, error) {
	return M.Socksaddr{}, net.ErrClosed
}

func (*rejectedPacketConn) WritePacket(buffer *buf.Buffer, _ M.Socksaddr) error {
	buffer.Release()
	return net.ErrClosed
}

func (c *rejectedPacketConn) Close() error                   { return c.closeErr }
func (c *rejectedPacketConn) LocalAddr() net.Addr            { return c.addr }
func (*rejectedPacketConn) SetDeadline(time.Time) error      { return net.ErrClosed }
func (*rejectedPacketConn) SetReadDeadline(time.Time) error  { return net.ErrClosed }
func (*rejectedPacketConn) SetWriteDeadline(time.Time) error { return net.ErrClosed }
