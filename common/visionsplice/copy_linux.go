//go:build linux

package visionsplice

import (
	"context"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

const Supported = true

// Copy keeps payload in kernel pipe pages. It never falls back to user-space
// copying: an unsupported transport is an explicit error for this prototype.
func Copy(ctx context.Context, dst *net.TCPConn, src net.Conn, o Options) (total int64, err error) {
	sc, ok := src.(syscall.Conn)
	if !ok {
		return 0, ErrUnsupported
	}
	sr, e := sc.SyscallConn()
	if e != nil {
		return 0, e
	}
	dr, e := dst.SyscallConn()
	if e != nil {
		return 0, e
	}
	var fds [2]int
	if e = unix.Pipe2(fds[:], unix.O_CLOEXEC|unix.O_NONBLOCK); e != nil {
		return 0, e
	}
	defer unix.Close(fds[0])
	defer unix.Close(fds[1])
	// Match Go's large splice ceiling when allowed; refusal is harmless and
	// leaves the kernel's default pipe capacity in effect.
	_, _ = unix.FcntlInt(uintptr(fds[1]), unix.F_SETPIPE_SZ, 1<<20)
	stop := context.AfterFunc(ctx, func() { src.Close(); dst.Close() })
	defer stop()
	for {
		if e = ctx.Err(); e != nil {
			return total, e
		}
		var n int64
		var callErr error
		e = sr.Read(func(fd uintptr) bool {
			for {
				n, callErr = unix.Splice(int(fd), nil, fds[1], nil, 1<<20, unix.SPLICE_F_NONBLOCK)
				if callErr == unix.EINTR {
					continue
				}
				break
			}
			return callErr != unix.EAGAIN
		})
		if e != nil {
			return total, e
		}
		if callErr != nil {
			return total, callErr
		}
		if n == 0 {
			return total, nil
		}
		for n > 0 {
			chunk := n
			if o.Limited != nil && o.Limited() {
				chunk = min(chunk, 16<<10)
			}
			if o.Before != nil {
				if e = o.Before(ctx, chunk); e != nil {
					return total, e
				}
			}
			left := chunk
			for left > 0 {
				if e = ctx.Err(); e != nil {
					return total, e
				}
				var moved int64
				e = dr.Write(func(fd uintptr) bool {
					for {
						moved, callErr = unix.Splice(fds[0], nil, int(fd), nil, int(left), unix.SPLICE_F_NONBLOCK)
						if callErr == unix.EINTR {
							continue
						}
						break
					}
					return callErr != unix.EAGAIN
				})
				if moved > 0 {
					total += moved
					n -= moved
					left -= moved
					if o.Stats != nil {
						o.Stats.Calls.Add(1)
						o.Stats.Bytes.Add(moved)
					}
					if o.Progress != nil {
						o.Progress(moved)
					}
				}
				if e != nil {
					return total, e
				}
				if callErr != nil {
					return total, callErr
				}
				if moved == 0 {
					return total, syscall.EIO
				}
			}
		}
	}
}
