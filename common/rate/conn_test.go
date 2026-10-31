package rate

import (
	"errors"
	"io"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"

	"github.com/juju/ratelimit"
)

type tcpRateFixture struct {
	net.Conn
	calls                           chan struct{}
	ioErr                           error
	closed, readClosed, writeClosed atomic.Int32
}

func (c *tcpRateFixture) Read(p []byte) (int, error) {
	c.calls <- struct{}{}
	return copy(p, "data"), c.ioErr
}
func (c *tcpRateFixture) Write(p []byte) (int, error) {
	c.calls <- struct{}{}
	return min(4, len(p)), c.ioErr
}
func (c *tcpRateFixture) Close() error                     { c.closed.Add(1); return nil }
func (c *tcpRateFixture) CloseRead() error                 { c.readClosed.Add(1); return nil }
func (c *tcpRateFixture) CloseWrite() error                { c.writeClosed.Add(1); return nil }
func (c *tcpRateFixture) SetDeadline(time.Time) error      { return nil }
func (c *tcpRateFixture) SetReadDeadline(time.Time) error  { return nil }
func (c *tcpRateFixture) SetWriteDeadline(time.Time) error { return nil }

func TestTCPRateWaitCancellationPreservesBytes(t *testing.T) {
	for _, read := range []bool{true, false} {
		for _, mode := range []string{"close", "deadline", "half-close"} {
			for _, partialErr := range []error{nil, io.ErrUnexpectedEOF} {
				t.Run(time.Now().Format("150405.000000")+mode, func(t *testing.T) {
					f := &tcpRateFixture{calls: make(chan struct{}, 2), ioErr: partialErr}
					c := NewConnRateLimiter(f, ratelimit.NewBucketWithRate(1, 1))
					type result struct {
						n   int
						err error
					}
					done := make(chan result, 1)
					go func() {
						var n int
						var err error
						if read {
							n, err = c.Read(make([]byte, 8))
						} else {
							n, err = c.Write(make([]byte, 8))
						}
						done <- result{n, err}
					}()
					<-f.calls
					time.Sleep(5 * time.Millisecond)
					want := error(net.ErrClosed)
					switch mode {
					case "close":
						c.Close()
						c.Close()
						if f.closed.Load() != 1 {
							t.Fatal("duplicate underlying close")
						}
					case "deadline":
						want = os.ErrDeadlineExceeded
						if read {
							c.SetReadDeadline(time.Now())
						} else {
							c.SetWriteDeadline(time.Now())
						}
					case "half-close":
						if read {
							c.CloseRead()
						} else {
							c.CloseWrite()
						}
					}
					if partialErr != nil {
						want = partialErr
					}
					select {
					case got := <-done:
						if got.n != 4 || !errors.Is(got.err, want) {
							t.Fatalf("got %d %v want 4 %v", got.n, got.err, want)
						}
					case <-time.After(500 * time.Millisecond):
						t.Fatal("rate wait stuck")
					}
					if mode == "half-close" {
						if f.closed.Load() != 0 {
							t.Fatal("half close closed both directions")
						}
						if read && f.readClosed.Load() != 1 || !read && f.writeClosed.Load() != 1 {
							t.Fatal("half close not forwarded")
						}
						c.limiter = ratelimit.NewBucketWithRate(1000000, 1000000)
						if read {
							_, err := c.Write([]byte("data"))
							if err != partialErr {
								t.Fatal(err)
							}
						} else {
							_, err := c.Read(make([]byte, 4))
							if err != partialErr {
								t.Fatal(err)
							}
						}
					}
				})
			}
		}
	}
}
func TestTCPRateDeadlineExtendAndClear(t *testing.T) {
	f := &tcpRateFixture{calls: make(chan struct{}, 2)}
	c := NewConnRateLimiter(f, ratelimit.NewBucketWithRate(100, 1))
	c.SetReadDeadline(time.Now().Add(5 * time.Millisecond))
	// Move the deadline before entering I/O. A past write deadline must not affect reads.
	c.SetReadDeadline(time.Time{})
	c.SetWriteDeadline(time.Now().Add(-time.Second))
	if n, e := c.Read(make([]byte, 4)); n != 4 || e != nil {
		t.Fatal(n, e)
	}
	if n, e := c.Write([]byte("data")); n != 0 || !errors.Is(e, os.ErrDeadlineExceeded) {
		t.Fatal(n, e)
	}
	c.Close()
	if n, e := c.Read(make([]byte, 4)); n != 0 || !errors.Is(e, net.ErrClosed) {
		t.Fatal(n, e)
	}
}
