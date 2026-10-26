package limiter

import (
	"github.com/sagernet/sing/common"
	N "github.com/sagernet/sing/common/network"
	"net"
	"sync/atomic"
	"testing"
)

type halfDeviceConn struct {
	net.Conn
	half atomic.Int32
}

func (c *halfDeviceConn) CloseWrite() error { c.half.Add(1); return nil }
func TestDeviceConnPreservesHalfCloseAndFinalRelease(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	base := &halfDeviceConn{Conn: a}
	var released atomic.Int32
	c := &deviceConn{Conn: base, release: func() { released.Add(1) }}
	half, ok := common.Cast[N.WriteCloser](c)
	if !ok {
		t.Fatal("half-close capability hidden")
	}
	if err := half.CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if base.half.Load() != 1 || released.Load() != 0 {
		t.Fatal("half-close released active lease")
	}
	c.Close()
	c.Close()
	if released.Load() != 1 {
		t.Fatal("lease not released exactly once")
	}
}
