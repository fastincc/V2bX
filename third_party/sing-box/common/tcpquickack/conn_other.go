//go:build !linux

package tcpquickack

import "net"

const Supported = false

func New(c net.Conn) net.Conn { return c }
