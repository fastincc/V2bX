//go:build linux

package tcpquickack

import (
	"net"
	"sync/atomic"

	"golang.org/x/sys/unix"
)

const Supported = true

// New leaves anything other than a bare TCP socket untouched. In particular,
// it must not unwrap connections carrying protocol/security/accounting state.
func New(c net.Conn) net.Conn {
	tcp, ok := c.(*net.TCPConn)
	if !ok {
		return c
	}
	raw, err := tcp.SyscallConn()
	if err != nil {
		return c
	}
	h := new(hint)
	var failed atomic.Bool
	// Reuse the callback instead of allocating a closure for every hint.
	control := func(fd uintptr) {
		if unix.SetsockoptInt(int(fd), unix.IPPROTO_TCP, unix.TCP_QUICKACK, 1) != nil {
			failed.Store(true)
		}
	}
	h.ack = func() error {
		// RawConn.Control owns the descriptor during the syscall, preventing a
		// concurrent Close from letting the hint affect a reused descriptor.
		err := raw.Control(control)
		if err != nil {
			return err
		}
		if failed.Load() {
			return unix.ENOPROTOOPT
		}
		return nil
	}
	return &conn{Conn: c, h: h}
}
