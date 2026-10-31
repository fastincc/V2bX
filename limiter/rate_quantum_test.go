package limiter

import (
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/conf"
	"testing"
	"time"
)

func TestRateQuantumAndSharedBucket(t *testing.T) {
	for _, mbps := range []int{1, 10, 1000} {
		Init()
		l := AddLimiter("rate", &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "synthetic", SpeedLimit: mbps}}, map[int]int{})
		key := format.UserTag("rate", "synthetic")
		a, deny := l.CheckLimit(key, "192.0.2.1", true, true)
		if deny || a == nil {
			t.Fatal("admission")
		}
		b, deny := l.CheckLimit(key, "192.0.2.1", false, true)
		if deny || b != a {
			t.Fatal("TCP/UDP bucket must be shared")
		}
		bytes := int64(mbps) * 125000
		if a.Capacity() != bytes || a.Rate() != float64(bytes) {
			t.Fatal("configured rate/capacity changed")
		}
		if d := a.Take(bytes); d > 0 {
			t.Fatal("initial burst lost", d)
		}
		d := a.Take(1)
		if d <= 0 || d > 15*time.Millisecond {
			t.Fatal("one-byte refill too coarse", d)
		}
		// A full second of additional debt must still require about a second.
		d = a.Take(bytes)
		if d < 950*time.Millisecond || d > 1020*time.Millisecond {
			t.Fatal("average bandwidth changed", d)
		}
	}
	Init()
	l := AddLimiter("unlimited", &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "synthetic"}}, map[int]int{})
	if b, deny := l.CheckLimit(format.UserTag("unlimited", "synthetic"), "192.0.2.1", true, true); deny || b != nil {
		t.Fatal("unlimited now wrapped")
	}
}

func TestRateAverageAfterBurst(t *testing.T) {
	for _, mbps := range []int{1, 10, 1000} {
		Init()
		l := AddLimiter("average", &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "synthetic", SpeedLimit: mbps}}, map[int]int{})
		b, _ := l.CheckLimit(format.UserTag("average", "synthetic"), "192.0.2.1", true, true)
		b.Take(b.Capacity())
		started := time.Now()
		// Ten concurrent reservations share one user's refill budget.
		done := make(chan struct{}, 10)
		for i := 0; i < 10; i++ {
			go func() { b.Wait(b.Capacity() / 10); done <- struct{}{} }()
		}
		for i := 0; i < 10; i++ {
			<-done
		}
		elapsed := time.Since(started)
		if elapsed < 950*time.Millisecond || elapsed > 1200*time.Millisecond {
			t.Fatal("shared sustained bandwidth drift", mbps, elapsed)
		}
	}
}
