package hy2

import (
	"context"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/limiter"
	"testing"
	"time"
)

func rateUnitFixture(t *testing.T, speed int) (*V2bX, *HookServer, string) {
	t.Helper()
	a, h, _ := admissionFixture(t)
	l, _ := limiter.GetLimiter(a.tag)
	users := []panel.UserInfo{{Id: 7, Uuid: "synthetic", DeviceLimit: 1, SpeedLimit: speed}}
	l.UpdateUser(a.tag, users, users)
	ok, id := a.Authenticate(admissionAddr("192.0.2.1"), "synthetic", 0)
	if !ok {
		t.Fatal("authentication failed")
	}
	return a, h, id
}
func TestHy2RateSharedAndUnlimited(t *testing.T) {
	for _, speed := range []int{0, 1, 8} {
		t.Run(string(rune('0'+speed)), func(t *testing.T) {
			a, h, id := rateUnitFixture(t, speed)
			_, id2 := a.Authenticate(admissionAddr("192.0.2.1"), "synthetic", 0)
			first, second := h.TrafficPacer(id), h.TrafficPacer(id2)
			if speed == 0 {
				if first.(*hyPacer).rate != nil || second.(*hyPacer).rate != nil {
					t.Fatal("unlimited acquired token bucket or pacing queue")
				}
				return
			}
			p, q := first.(*hyPacer), second.(*hyPacer)
			if p.rate != q.rate || p.rate.bucket.Rate() != float64(speed*125000) {
				t.Fatal("quota not shared")
			}
			if err := p.Wait(context.Background(), speed*125000); err != nil {
				t.Fatal(err)
			}
			start := time.Now()
			if err := q.Wait(context.Background(), speed*12500); err != nil {
				t.Fatal(err)
			}
			if time.Since(start) < 80*time.Millisecond {
				t.Fatal("second connection bypassed quota")
			}
		})
	}
}
func TestHy2RateCancelDrainRevoke(t *testing.T) {
	for _, action := range []string{"cancel", "drain", "revoke", "disconnect"} {
		t.Run(action, func(t *testing.T) {
			a, h, id := rateUnitFixture(t, 1)
			p := h.TrafficPacer(id).(*hyPacer)
			p.rate.bucket.TakeAvailable(p.rate.bucket.Capacity())
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			done := make(chan error, 1)
			go func() { done <- p.Wait(ctx, 125000) }()
			time.Sleep(20 * time.Millisecond)
			core := &Hysteria2{Hy2nodes: map[string]Hysteria2node{a.tag: {Auth: a, TrafficLogger: h}}}
			start := time.Now()
			switch action {
			case "cancel":
				cancel()
			case "drain":
				if e := core.DrainTraffic(a.tag); e != nil {
					t.Fatal(e)
				}
			case "revoke":
				if e := core.DelUsers([]panel.UserInfo{{Id: 7, Uuid: "synthetic"}}, a.tag, nil); e != nil {
					t.Fatal(e)
				}
			case "disconnect":
				a.disconnect(id)
				cancel() // transport cancellation accompanies disconnect
			}
			select {
			case err := <-done:
				if err == nil {
					t.Fatal("wait succeeded after cancellation")
				}
			case <-time.After(150 * time.Millisecond):
				t.Fatal("wait did not stop promptly")
			}
			if p.rate.bucket.Available() < 0 {
				t.Fatal("cancelled wait reserved future quota")
			}
			if action == "drain" {
				if e := core.ResumeTraffic(a.tag); e != nil {
					t.Fatal(e)
				}
				q := h.TrafficPacer(id).(*hyPacer)
				if q.rate == p.rate || q.rate.bucket != p.rate.bucket {
					t.Fatal("resume reset quota or revived cancelled group")
				}
				if p.Wait(context.Background(), 1) == nil {
					t.Fatal("old wait revived")
				}
				if e := q.Wait(context.Background(), 1); e != nil {
					t.Fatal(e)
				}
			}
			if action == "revoke" {
				a.mutex.Lock()
				a.addLocked("synthetic", 7)
				a.mutex.Unlock()
				_, fresh := a.Authenticate(admissionAddr("192.0.2.1"), "synthetic", 0)
				if e := h.TrafficPacer(fresh).Wait(context.Background(), 1); e != nil {
					t.Fatal(e)
				}
				if h.TrafficPacer(id).Wait(context.Background(), 1) == nil {
					t.Fatal("revoked session revived")
				}
			}
			t.Logf("%s cancellation %s", action, time.Since(start))
		})
	}
}

func TestHy2RateUnlimitedAllocations(t *testing.T) {
	_, h, id := rateUnitFixture(t, 0)
	if n := testing.AllocsPerRun(1000, func() {
		p := h.TrafficPacer(id).(*hyPacer)
		if p.rate != nil || p.Wait(context.Background(), 32768) != nil {
			panic("unlimited pacing not a no-op")
		}
	}); n != 0 {
		t.Fatalf("unlimited stream added %.2f allocations", n)
	}
}

// Continuous small datagrams must not consume every refill while a larger TCP
// write repeatedly waits for a full buffer's quota.
func TestHy2RateSmallPacketsCannotStarveLargeWrite(t *testing.T) {
	_, h, id := rateUnitFixture(t, 1)
	p := h.TrafficPacer(id).(*hyPacer)
	p.rate.bucket.TakeAvailable(p.rate.bucket.Capacity())
	smallCtx, stop := context.WithCancel(context.Background())
	defer stop()
	started, done := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(done)
		first := true
		for {
			if e := p.Wait(smallCtx, 1250); e != nil {
				return
			}
			if first {
				close(started)
				first = false
			}
		}
	}()
	select {
	case <-started:
	case <-time.After(time.Second):
		t.Fatal("small sender not running")
	}
	largeCtx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := p.Wait(largeCtx, 32768)
	stop()
	<-done
	if err != nil {
		t.Fatalf("large write starved despite 1Mbps quota: %v", err)
	}
	t.Logf("large write admitted under small-packet load in %s", time.Since(start))
}

func TestHy2RateQueuedCancellation(t *testing.T) {
	_, h, id := rateUnitFixture(t, 1)
	p := h.TrafficPacer(id).(*hyPacer)
	p.rate.bucket.TakeAvailable(p.rate.bucket.Capacity())
	headCtx, stop := context.WithCancel(context.Background())
	defer stop()
	done := make(chan error, 1)
	go func() { done <- p.Wait(headCtx, 125000) }()
	time.Sleep(20 * time.Millisecond)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	start := time.Now()
	err := p.Wait(ctx, 1)
	if err != context.DeadlineExceeded || time.Since(start) > 150*time.Millisecond {
		t.Fatal("queued cancellation blocked", err)
	}
	stop()
	select {
	case <-done:
	case <-time.After(150 * time.Millisecond):
		t.Fatal("head cancellation blocked")
	}
	if p.rate.bucket.Available() < 0 {
		t.Fatal("queued cancellation left token debt")
	}
}
