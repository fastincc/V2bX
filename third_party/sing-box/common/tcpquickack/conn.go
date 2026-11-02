// Package tcpquickack provides an opt-in hint for directly connected AnyTLS
// destinations. It neither changes payloads nor implements flow control.
package tcpquickack

import (
	"net"
	"sync/atomic"
	"time"

	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	N "github.com/sagernet/sing/common/network"
)

const smallRead = 2048
const minInterval = 2 * time.Millisecond

var epoch = time.Now()

type hint struct {
	next     atomic.Int64
	disabled atomic.Bool
	ack      func() error
}

func (h *hint) afterRead(n, capacity int) {
	// No clock, atomic access, allocation or syscall for full bulk reads.
	// Read sizes are only a heuristic: TCP/GRO need not preserve packet edges.
	if n <= 0 || (n > smallRead && n >= capacity) {
		return
	}
	if h.disabled.Load() {
		return
	}
	now := time.Since(epoch).Nanoseconds()
	if !h.claim(now) {
		return
	}
	if h.ack() != nil {
		// Unsupported sockets, seccomp failures and concurrent close must never
		// interrupt forwarding or generate repeated error logs/syscalls.
		h.disabled.Store(true)
	}
}

func (h *hint) claim(now int64) bool {
	next := h.next.Load()
	return now >= next && h.next.CompareAndSwap(next, now+int64(minInterval))
}

type conn struct {
	net.Conn
	h *hint
}

func (c *conn) Read(p []byte) (int, error) {
	n, err := c.Conn.Read(p)
	c.h.afterRead(n, len(p))
	return n, err
}

// Only writes may bypass the hint. In particular, do not expose SyscallConn
// or a replaceable reader, which would silently bypass post-read QUICKACK.
func (c *conn) Upstream() any           { return c.Conn }
func (c *conn) ReaderReplaceable() bool { return false }
func (c *conn) WriterReplaceable() bool { return true }
func (c *conn) CloseWrite() error       { return c.Conn.(*net.TCPConn).CloseWrite() }
func (c *conn) CloseRead() error        { return c.Conn.(*net.TCPConn).CloseRead() }

// Preserve sing's zero-copy buffer ownership and vectorised read machinery;
// a plain Read-only wrapper would fall back to less efficient copying.
func (c *conn) CreateReadWaiter() (N.ReadWaiter, bool) {
	w, ok := bufio.CreateReadWaiter(c.Conn)
	if !ok {
		return nil, false
	}
	return &readWaiter{w, c.h}, true
}

func (c *conn) CreateVectorisedReadWaiter() (N.VectorisedReadWaiter, bool) {
	w, ok := bufio.CreateVectorisedReadWaiter(c.Conn)
	if !ok {
		return nil, false
	}
	return &vectorWaiter{w, c.h}, true
}

type readWaiter struct {
	N.ReadWaiter
	h *hint
}

func (w *readWaiter) WaitReadBuffer() (*buf.Buffer, error) {
	b, err := w.ReadWaiter.WaitReadBuffer()
	if b != nil {
		w.h.afterRead(b.Len(), b.Len()+b.FreeLen())
	}
	return b, err
}

type vectorWaiter struct {
	N.VectorisedReadWaiter
	h *hint
}

func (w *vectorWaiter) WaitReadBuffers() ([]*buf.Buffer, error) {
	bs, err := w.VectorisedReadWaiter.WaitReadBuffers()
	// The last buffer retains the short-read signal even when earlier full
	// buffers contain coalesced data. Do not scan/copy the batch payload.
	if len(bs) > 0 {
		b := bs[len(bs)-1]
		w.h.afterRead(b.Len(), b.Len()+b.FreeLen())
	}
	return bs, err
}
