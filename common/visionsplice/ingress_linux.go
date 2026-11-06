//go:build linux

package visionsplice

import (
	"context"
	"errors"
	"io"
	"net"
	"syscall"

	"golang.org/x/sys/unix"
)

// CopyIngress restores ordinary Vision's zero-copy path without enabling the
// adjustable-quota prototype. Read accounts bytes consumed from the destination,
// before writing to the client, just like SizeStatWriter on the buffered path.
// Written is a separate transport counter. Neither callback is called twice
// when a kernel that cannot splice requires buffered fallback.
func CopyIngress(ctx context.Context, dst *net.TCPConn, src net.Conn, read, written func(int64)) (total int64, err error) {
	stop := context.AfterFunc(ctx, func() { src.Close(); dst.Close() })
	defer stop()
	sc, ok := src.(syscall.Conn)
	if !ok {
		return copyIngressBuffered(ctx, dst, src, -1, 0, read, written)
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
		// No source bytes have been consumed yet.
		return copyIngressBuffered(ctx, dst, src, -1, 0, read, written)
	}
	defer unix.Close(fds[0])
	defer unix.Close(fds[1])
	_, _ = unix.FcntlInt(uintptr(fds[1]), unix.F_SETPIPE_SZ, 1<<20)
	for {
		if e = ctx.Err(); e != nil {
			return total, e
		}
		var pending int64
		var callErr error
		e = sr.Read(func(fd uintptr) bool {
			for {
				n, err := unix.Splice(int(fd), nil, fds[1], nil, 1<<20, unix.SPLICE_F_NONBLOCK)
				pending, callErr = int64(n), err
				if callErr != unix.EINTR {
					return callErr != unix.EAGAIN
				}
			}
		})
		if pending > 0 && read != nil {
			read(pending)
		}
		if e != nil {
			return total, e
		}
		if callErr != nil {
			if spliceUnavailable(callErr) {
				n, err := copyIngressBuffered(ctx, dst, src, fds[0], max(pending, 0), read, written)
				return total + n, err
			}
			return total, callErr
		}
		if pending == 0 {
			return total, nil
		}
		for pending > 0 {
			if e = ctx.Err(); e != nil {
				return total, e
			}
			var moved int64
			e = dr.Write(func(fd uintptr) bool {
				for {
					n, err := unix.Splice(fds[0], nil, int(fd), nil, int(pending), unix.SPLICE_F_NONBLOCK)
					moved, callErr = int64(n), err
					if callErr != unix.EINTR {
						return callErr != unix.EAGAIN
					}
				}
			})
			if moved > 0 {
				pending -= moved
				total += moved
				if written != nil {
					written(moved)
				}
			}
			if e != nil {
				return total, e
			}
			if callErr != nil {
				if spliceUnavailable(callErr) {
					// These bytes have already been read and billed. Flush them
					// before consuming anything else from src, without rebilling.
					n, err := copyIngressBuffered(ctx, dst, src, fds[0], pending, read, written)
					return total + n, err
				}
				return total, callErr
			}
			if moved == 0 {
				return total, io.ErrNoProgress
			}
		}
	}
}

func spliceUnavailable(err error) bool {
	return errors.Is(err, unix.EINVAL) || errors.Is(err, unix.ENOSYS) ||
		errors.Is(err, unix.EOPNOTSUPP) || errors.Is(err, unix.EPERM) || errors.Is(err, unix.EACCES)
}

// pending pipe bytes are already included in read. Allocate a user buffer only
// on fallback, and preserve source order even if splice-to-output was refused.
func copyIngressBuffered(ctx context.Context, dst io.Writer, src io.Reader, pipeFD int, pending int64, read, written func(int64)) (total int64, err error) {
	buffer := make([]byte, 32<<10)
	write := func(p []byte) error {
		for len(p) > 0 {
			if err := ctx.Err(); err != nil {
				return err
			}
			n, err := dst.Write(p)
			if n > 0 {
				total += int64(n)
				if written != nil {
					written(int64(n))
				}
				p = p[n:]
			}
			if err != nil {
				return err
			}
			if n == 0 {
				return io.ErrShortWrite
			}
		}
		return nil
	}
	for pending > 0 {
		if err = ctx.Err(); err != nil {
			return total, err
		}
		n, err := unix.Read(pipeFD, buffer[:min(int64(len(buffer)), pending)])
		if err == unix.EINTR {
			continue
		}
		if err != nil {
			return total, err
		}
		if n == 0 {
			return total, io.ErrUnexpectedEOF
		}
		pending -= int64(n)
		if err = write(buffer[:n]); err != nil {
			return total, err
		}
	}
	for {
		if err = ctx.Err(); err != nil {
			return total, err
		}
		n, readErr := src.Read(buffer)
		if n > 0 {
			if read != nil {
				read(int64(n))
			}
			if err = write(buffer[:n]); err != nil {
				return total, err
			}
		}
		if readErr != nil {
			if readErr == io.EOF {
				readErr = nil
			}
			return total, readErr
		}
	}
}
