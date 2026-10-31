package rate

import (
	"errors"
	"net"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/common/counter"
	"github.com/juju/ratelimit"
)

func TestCancelledTCPWaitRetainsAccountingLease(t *testing.T) {
	for _, read := range []bool{true, false} {
		f := &tcpRateFixture{calls: make(chan struct{}, 1)}
		c := NewConnRateLimiter(f, ratelimit.NewBucketWithRate(1, 1))
		s := new(counter.TrafficStorage)
		owned := counter.NewLeasedConn(c, s)
		done := make(chan error, 1)
		go func() {
			var e error
			if read {
				_, e = owned.Read(make([]byte, 4))
			} else {
				_, e = owned.Write([]byte("data"))
			}
			done <- e
		}()
		<-f.calls
		owned.Close()
		select {
		case e := <-done:
			if !errors.Is(e, net.ErrClosed) {
				t.Fatal(e)
			}
		case <-time.After(500 * time.Millisecond):
			t.Fatal("lease blocked")
		}
		wantUp, wantDown := int64(0), int64(4)
		if read {
			wantUp, wantDown = 4, 0
		}
		if s.UpCounter.Load() != wantUp || s.DownCounter.Load() != wantDown || s.Owners.Load() != 0 {
			t.Fatal("final bytes or lease lost")
		}
	}
}
func TestTCPRateDeadlineChangeWhileWaiting(t *testing.T) {
	for _, clear := range []bool{true, false} {
		f := &tcpRateFixture{calls: make(chan struct{}, 1)}
		c := NewConnRateLimiter(f, ratelimit.NewBucketWithRate(20, 1))
		c.SetReadDeadline(time.Now().Add(80 * time.Millisecond))
		done := make(chan error, 1)
		go func() { _, e := c.Read(make([]byte, 4)); done <- e }()
		<-f.calls
		time.Sleep(5 * time.Millisecond)
		if clear {
			c.SetReadDeadline(time.Time{})
		} else {
			c.SetReadDeadline(time.Now().Add(time.Second))
		}
		select {
		case e := <-done:
			if e != nil {
				t.Fatal("stale deadline fired", e)
			}
		case <-time.After(time.Second):
			t.Fatal("wait stuck")
		}
		c.Close()
	}
}
