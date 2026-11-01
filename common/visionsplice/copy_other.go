//go:build !linux

package visionsplice

import (
	"context"
	"net"
)

const Supported = false

func Copy(context.Context, *net.TCPConn, net.Conn, Options) (int64, error) { return 0, ErrUnsupported }
