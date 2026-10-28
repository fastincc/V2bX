package counter

import (
	"io"
	"net"
	"testing"
	"time"
)

type delayedRead struct {
	net.Conn
	entered, release chan struct{}
}

func (c *delayedRead) Read(b []byte) (int, error) {
	close(c.entered)
	<-c.release
	b[0] = 42
	return 1, io.EOF
}
func (c *delayedRead) Close() error { return nil }
func TestCloseDoesNotRetireInflightBytes(t *testing.T) {
	s := &TrafficStorage{}
	raw := &delayedRead{entered: make(chan struct{}), release: make(chan struct{})}
	c := NewLeasedConn(raw, s)
	done := make(chan struct{})
	go func() { defer close(done); c.Read(make([]byte, 1)) }()
	<-raw.entered
	c.Close()
	if s.Owners.Load() != 1 {
		t.Fatal("lease released before final read")
	}
	close(raw.release)
	<-done
	if s.Owners.Load() != 0 || s.UpCounter.Load() != 1 {
		t.Fatal("final bytes lost")
	}
	c.Close()
	if s.Owners.Load() != 0 {
		t.Fatal("double release")
	}
}
func TestLeaseHalfCloseRetainsOwner(t *testing.T) {
	a, b := net.Pipe()
	defer b.Close()
	s := &TrafficStorage{}
	c := NewLeasedConn(a, s)
	c.(interface{ CloseWrite() error }).CloseWrite()
	if s.Owners.Load() != 1 {
		t.Fatal("half-close released owner")
	}
	c.Close()
	if s.Owners.Load() != 0 {
		t.Fatal("leak")
	}
}

type discardConn struct{ net.Conn }

func (discardConn) Write(p []byte) (int, error) { return len(p), nil }
func (discardConn) Close() error                { return nil }
func BenchmarkAccountingWrappers(b *testing.B) {
	for _, tracked := range []bool{false, true} {
		name := "original"
		if tracked {
			name = "leased"
		}
		b.Run(name, func(b *testing.B) {
			s := &TrafficStorage{}
			var c net.Conn = NewConnCounter(discardConn{}, s)
			if tracked {
				c = NewLeasedConn(discardConn{}, s)
			}
			defer c.Close()
			data := make([]byte, 16384)
			b.ReportAllocs()
			b.SetBytes(int64(len(data)))
			b.ResetTimer()
			for i := 0; i < b.N; i++ {
				c.Write(data)
			}
			_ = time.Second
		})
	}
}
