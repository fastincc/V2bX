package rate

import (
	"errors"
	"github.com/juju/ratelimit"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	"net"
	"os"
	"sync/atomic"
	"testing"
	"time"
)

type packetFixture struct {
	writes atomic.Int64
	closed atomic.Int64
}

func (f *packetFixture) ReadPacket(b *buf.Buffer) (M.Socksaddr, error) {
	b.Write([]byte("packet"))
	return M.ParseSocksaddr("example.test:53"), nil
}
func (f *packetFixture) WritePacket(b *buf.Buffer, _ M.Socksaddr) error {
	f.writes.Add(int64(b.Len()))
	b.Release()
	return nil
}
func (f *packetFixture) Close() error                     { f.closed.Add(1); return nil }
func (f *packetFixture) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (f *packetFixture) SetDeadline(time.Time) error      { return nil }
func (f *packetFixture) SetReadDeadline(time.Time) error  { return nil }
func (f *packetFixture) SetWriteDeadline(time.Time) error { return nil }
func TestPacketRateCancellationAndDeadline(t *testing.T) {
	for _, mode := range []string{"close", "deadline"} {
		t.Run(mode, func(t *testing.T) {
			f := new(packetFixture)
			c := NewPacketConnRateLimiter(f, ratelimit.NewBucketWithRate(1, 1))
			b := buf.New()
			b.Write(make([]byte, 1024))
			done := make(chan error, 1)
			go func() { done <- c.WritePacket(b, M.Socksaddr{}) }()
			time.Sleep(10 * time.Millisecond)
			start := time.Now()
			if mode == "close" {
				c.Close()
			} else {
				c.SetWriteDeadline(time.Now().Add(10 * time.Millisecond))
			}
			select {
			case e := <-done:
				if !errors.Is(e, net.ErrClosed) && !errors.Is(e, os.ErrDeadlineExceeded) {
					t.Fatal(e)
				}
			case <-time.After(time.Second):
				t.Fatal("rate wait did not cancel")
			}
			if time.Since(start) > time.Second/2 || f.writes.Load() != 0 {
				t.Fatal("canceled packet forwarded")
			}
			c.Close()
		})
	}
}
func TestPacketRatePreservesBoundaryAndSharedBucket(t *testing.T) {
	f := new(packetFixture)
	bucket := ratelimit.NewBucketWithRate(1000, 1)
	c := NewPacketConnRateLimiter(f, bucket)
	defer c.Close()
	start := time.Now()
	b := buf.New()
	b.Write(make([]byte, 100))
	if e := c.WritePacket(b, M.Socksaddr{}); e != nil {
		t.Fatal(e)
	}
	r := buf.New()
	defer r.Release()
	addr, e := c.ReadPacket(r)
	if e != nil || r.Len() != 6 || addr.Port != 53 || f.writes.Load() != 100 {
		t.Fatal("datagram changed")
	}
	if time.Since(start) < 90*time.Millisecond {
		t.Fatal("rate not applied")
	}
}
