//go:build linux

package tcpquickack

import (
	"bytes"
	"errors"
	"io"
	"net"
	"sync"
	"testing"
	"time"

	"github.com/sagernet/sing/common/bufio"
	N "github.com/sagernet/sing/common/network"
)

func tcpPair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	l, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	a, e := net.Dial("tcp", l.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	b, e := l.Accept()
	if e != nil {
		a.Close()
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Close(); b.Close() })
	a.SetDeadline(time.Now().Add(5 * time.Second))
	b.SetDeadline(time.Now().Add(5 * time.Second))
	return a.(*net.TCPConn), b.(*net.TCPConn)
}

func TestSocketReadersAndHalfClose(t *testing.T) {
	for _, mode := range []string{"read", "wait", "vector"} {
		t.Run(mode, func(t *testing.T) {
			a, b := tcpPair(t)
			c, ok := New(a).(*conn)
			if !ok {
				t.Fatal("raw TCP not wrapped")
			}
			if N.UnwrapReader(c) != c || N.UnwrapWriter(c) != a || N.SyscallAvailableForRead(c) {
				t.Fatal("read hint bypass or write fast path lost")
			}
			if _, e := b.Write([]byte("tail")); e != nil {
				t.Fatal(e)
			}
			var got []byte
			switch mode {
			case "read":
				got = make([]byte, 4)
				if _, e := io.ReadFull(c, got); e != nil {
					t.Fatal(e)
				}
			case "wait":
				w, ok := bufio.CreateReadWaiter(bufio.NewExtendedReader(c))
				if !ok {
					t.Fatal("waiter lost")
				}
				w.InitializeReadWaiter(N.ReadWaitOptions{})
				p, e := w.WaitReadBuffer()
				if e != nil {
					t.Fatal(e)
				}
				got = append(got, p.Bytes()...)
				p.Release()
			case "vector":
				w, ok := bufio.CreateVectorisedReadWaiter(bufio.NewExtendedReader(c))
				if !ok {
					t.Fatal("vector waiter lost")
				}
				w.InitializeReadWaiter(N.ReadWaitOptions{BatchSize: 8})
				ps, e := w.WaitReadBuffers()
				if e != nil {
					t.Fatal(e)
				}
				for _, p := range ps {
					got = append(got, p.Bytes()...)
					p.Release()
				}
			}
			if !bytes.Equal(got, []byte("tail")) || c.h.next.Load() == 0 || c.h.disabled.Load() {
				t.Fatal("payload or syscall failed", mode)
			}
			if e := c.CloseWrite(); e != nil {
				t.Fatal(e)
			}
			if _, e := b.Read(make([]byte, 1)); e != io.EOF {
				t.Fatal("half-close did not deliver EOF", e)
			}
			b.Write([]byte("after FIN"))
			back := make([]byte, 9)
			if _, e := io.ReadFull(c, back); e != nil || string(back) != "after FIN" {
				t.Fatal("half-close discarded response", e)
			}
		})
	}
}

func TestOpaqueSocketNotUnwrapped(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	if New(a) != a {
		t.Fatal("non TCP changed")
	}
	x, _ := tcpPair(t)
	opaque := &struct{ net.Conn }{x}
	if New(opaque) != opaque {
		t.Fatal("opaque wrapper bypassed")
	}
}

func TestHintConcurrentSocketClose(t *testing.T) {
	for i := 0; i < 50; i++ {
		a, _ := tcpPair(t)
		c := New(a).(*conn)
		var wg sync.WaitGroup
		for j := 0; j < 4; j++ {
			wg.Add(1)
			go func() { defer wg.Done(); c.h.afterRead(45, 45) }()
		}
		a.Close()
		wg.Wait()
		c.h.next.Store(0)
		c.h.afterRead(45, 45)
		if !c.h.disabled.Load() {
			t.Fatal("closed socket did not disable hint")
		}
	}
}

func TestSocketHintFailurePreservesPayload(t *testing.T) {
	a, b := tcpPair(t)
	c := New(a).(*conn)
	c.h.ack = func() error { return errors.New("synthetic seccomp denial") }
	b.Write([]byte("data"))
	p := make([]byte, 4)
	if _, e := io.ReadFull(c, p); e != nil || string(p) != "data" || !c.h.disabled.Load() {
		t.Fatal("hint failure affected read", e)
	}
	b.Write([]byte("more"))
	if _, e := io.ReadFull(c, p); e != nil || string(p) != "more" {
		t.Fatal("fallback affected next read", e)
	}
}

func TestSocketIPv6(t *testing.T) {
	l, e := net.Listen("tcp6", "[::1]:0")
	if e != nil {
		t.Skip("IPv6 unavailable", e)
	}
	defer l.Close()
	a, e := net.Dial("tcp6", l.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer a.Close()
	b, e := l.Accept()
	if e != nil {
		t.Fatal(e)
	}
	defer b.Close()
	a.SetDeadline(time.Now().Add(time.Second))
	b.SetDeadline(time.Now().Add(time.Second))
	c := New(a).(*conn)
	b.Write([]byte("ipv6"))
	p := make([]byte, 4)
	if _, e := io.ReadFull(c, p); e != nil || string(p) != "ipv6" || c.h.disabled.Load() {
		t.Fatal("IPv6 hint or data failed", e)
	}
}
