package dynamic

import (
	"context"
	"errors"
	"os"
	"sync"
	"testing"
	"time"
)

func testPolicy() *Policy {
	return &Policy{Version: 1, Revision: 1, Enabled: true, WindowSeconds: 3, ThresholdBytes: 1000, SpeedMbps: 1, DurationSeconds: 5}
}
func TestDynamicWindowExpiryAndRevision(t *testing.T) {
	c := New(0)
	defer c.Close()
	now := time.Now()
	p := testPolicy()
	if e := c.Apply(p, now); e != nil {
		t.Fatal(e)
	}
	c.Observe(600)
	c.Tick(now.Add(time.Second))
	c.Observe(399)
	c.Tick(now.Add(2 * time.Second))
	if c.Status().Limited {
		t.Fatal("premature trigger")
	}
	// At t=4 the first bucket is outside the three-second window.
	c.Observe(500)
	c.Tick(now.Add(4 * time.Second))
	if c.Status().WindowBytes != 899 {
		t.Fatal(c.Status())
	}
	c.Observe(101)
	if c.Tick(now.Add(4*time.Second)) != "triggered" {
		t.Fatal(c.Status())
	}
	until := c.Status().Until
	if e := c.Apply(p, now.Add(5*time.Second)); e != nil {
		t.Fatal(e)
	}
	c.Observe(100000)
	c.Tick(now.Add(5 * time.Second))
	if c.Status().Until != until {
		t.Fatal("extended punishment")
	}
	if c.Tick(until) != "expired" || c.Status().Limited || c.Status().WindowBytes != 0 {
		t.Fatal(c.Status())
	}
	c.Observe(1000)
	c.Tick(until.Add(time.Second))
	if !c.Status().Limited {
		t.Fatal("cannot retrigger")
	}
	v := *p
	v.Revision = 2
	v.Enabled = false
	if e := c.Apply(&v, until); e != nil {
		t.Fatal(e)
	}
	if c.Status().Enabled || c.Status().Limited {
		t.Fatal(c.Status())
	}
	if e := c.Apply(p, until); e == nil {
		t.Fatal("stale accepted")
	}
	v.Enabled = true
	if e := c.Apply(&v, until); e == nil {
		t.Fatal("same revision conflict accepted")
	}
	if e := c.Apply(nil, until); e != nil {
		t.Fatal(e)
	}
	if c.Status().Enabled {
		t.Fatal("omission not applied")
	}
}

func TestDynamicRuleChangeAndIsolation(t *testing.T) {
	a, b := New(0), New(0)
	defer a.Close()
	defer b.Close()
	now := time.Now()
	p := testPolicy()
	a.Apply(p, now)
	b.Apply(p, now)
	a.Observe(1000)
	a.Tick(now)
	if b.Tick(now) != "" || b.Status().Limited {
		t.Fatal("cross-user leak")
	}
	deadline := a.Status().Until
	v := *p
	v.Revision = 2
	v.WindowSeconds = 10
	v.SpeedMbps = 2
	v.DurationSeconds = 100
	a.Apply(&v, now.Add(time.Second))
	if a.Status().Until != deadline {
		t.Fatal("policy update extended punishment")
	}
	v.Revision = 3
	v.DurationSeconds = 1
	a.Apply(&v, now.Add(time.Second))
	if a.Status().Until != now.Add(2*time.Second) {
		t.Fatal("shorter duration ignored")
	}
	v.Revision = 4
	v.SpeedMbps = -1
	if a.Apply(&v, now) == nil || !a.Status().Enabled {
		t.Fatal("invalid policy erased valid state")
	}
	a.Close()
	if a.Apply(p, now) == nil {
		t.Fatal("retired generation revived")
	}
}

func state(ctx context.Context) WaitState {
	return func() (time.Time, <-chan struct{}, error) { d, _ := ctx.Deadline(); return d, ctx.Done(), ctx.Err() }
}
func triggered(t *testing.T) *Control {
	t.Helper()
	c := New(0)
	p := testPolicy()
	now := time.Now()
	c.Apply(p, now)
	c.Observe(1000)
	c.Tick(now)
	t.Cleanup(c.Close)
	return c
}

func TestDynamicWaitCancelDisableAndNoDebt(t *testing.T) {
	c := triggered(t)
	ctx, cancel := context.WithTimeout(context.Background(), 25*time.Millisecond)
	defer cancel()
	if e := c.Wait(10*1024*1024, state(ctx)); !errors.Is(e, context.DeadlineExceeded) && !errors.Is(e, os.ErrDeadlineExceeded) {
		t.Fatal(e)
	}
	start := time.Now()
	if e := c.Wait(1250, nil); e != nil {
		t.Fatal(e)
	}
	if time.Since(start) > 100*time.Millisecond {
		t.Fatal("cancelled traffic left quota debt")
	}
	done := make(chan error, 1)
	go func() { done <- c.Wait(10*1024*1024, nil) }()
	time.Sleep(20 * time.Millisecond)
	c.Apply(nil, time.Now())
	select {
	case e := <-done:
		if e != nil {
			t.Fatal(e)
		}
	case <-time.After(time.Second):
		t.Fatal("disable did not wake long stream")
	}
}

func TestDynamicSharedRateAndFairness(t *testing.T) {
	c := triggered(t)
	start := time.Now()
	var wg sync.WaitGroup
	for i := 0; i < 4; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if e := c.Wait(12500, nil); e != nil {
				t.Error(e)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	if elapsed < 350*time.Millisecond || elapsed > 2*time.Second {
		t.Fatal("not one shared 1 Mbps limit", elapsed)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go c.Wait(10*1024*1024, state(ctx))
	time.Sleep(15 * time.Millisecond)
	start = time.Now()
	if e := c.Wait(100, nil); e != nil {
		t.Fatal(e)
	}
	if time.Since(start) > 200*time.Millisecond {
		t.Fatal("large write starved small request")
	}
}

func TestDynamicConcurrentObserveAndUpdate(t *testing.T) {
	c := New(0)
	defer c.Close()
	p := testPolicy()
	c.Apply(p, time.Now())
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 10000; j++ {
				c.Observe(100)
			}
		}()
	}
	for i := 0; i < 100; i++ {
		v := *p
		v.Revision = int64(i + 2)
		c.Apply(&v, time.Now())
		c.Tick(time.Now())
		c.Status()
	}
	wg.Wait()
}

func TestDynamicExpiryUpdateDoesNotReusePenaltyTraffic(t *testing.T) {
	c := New(0)
	defer c.Close()
	now := time.Now()
	p := testPolicy()
	c.Apply(p, now)
	c.Observe(1000)
	c.Tick(now)
	until := c.Status().Until
	c.Observe(1000000)
	v := *p
	v.Revision = 2
	v.SpeedMbps = 2
	if e := c.Apply(&v, until); e != nil {
		t.Fatal(e)
	}
	c.Tick(until.Add(time.Second))
	if c.Status().Limited || c.Status().WindowBytes != 0 {
		t.Fatal("old penalty traffic retriggered after expiry", c.Status())
	}
	c.Observe(1000)
	c.Tick(until.Add(2 * time.Second))
	if !c.Status().Limited {
		t.Fatal("new period traffic ignored")
	}
}

func BenchmarkDynamicDisabled(b *testing.B) {
	c := New(0)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		c.Observe(32768)
		c.Wait(32768, nil)
	}
}
func BenchmarkDynamicObserve(b *testing.B) {
	c := New(0)
	c.Apply(testPolicy(), time.Now())
	b.ReportAllocs()
	b.RunParallel(func(pb *testing.PB) {
		for pb.Next() {
			c.Observe(32768)
		}
	})
}
