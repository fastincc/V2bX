//go:build !linux

package visionsplice

import (
	"context"
	"net"
)

func CopyIngress(context.Context, *net.TCPConn, net.Conn, func(int64), func(int64)) (int64, error) {
	return 0, ErrUnsupported
}
