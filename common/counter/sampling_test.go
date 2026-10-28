package counter

import (
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
)

func TestConcurrentSamplingConservesBytes(t *testing.T) {
	s := &TrafficStorage{}
	var producers, samplers sync.WaitGroup
	var up, down atomic.Int64
	done := make(chan struct{})
	for i := 0; i < 4; i++ {
		producers.Add(1)
		go func() {
			defer producers.Done()
			for j := 0; j < 100000; j++ {
				s.UpCounter.Add(3)
				s.DownCounter.Add(7)
				if j%100 == 0 {
					runtime.Gosched()
				}
			}
		}()
	}
	for i := 0; i < 3; i++ {
		samplers.Add(1)
		go func() {
			defer samplers.Done()
			for {
				u, d, _ := s.Sample(10, true)
				up.Add(u)
				down.Add(d)
				select {
				case <-done:
					return
				default:
				}
			}
		}()
	}
	producers.Wait()
	close(done)
	samplers.Wait()
	u, d, _ := s.Sample(-1, true)
	up.Add(u)
	down.Add(d)
	if up.Load() != 1200000 || down.Load() != 2800000 {
		t.Fatalf("lost or duplicated bytes: up=%d down=%d", up.Load(), down.Load())
	}
}
func TestSampleThresholdAndPeek(t *testing.T) {
	s := &TrafficStorage{}
	s.UpCounter.Add(4)
	s.DownCounter.Add(6)
	if _, _, ok := s.Sample(10, true); ok {
		t.Fatal("threshold semantics changed")
	}
	for i := 0; i < 2; i++ {
		u, d, ok := s.Sample(9, false)
		if !ok || u != 4 || d != 6 {
			t.Fatal("peek drained sample")
		}
	}
	u, d, ok := s.Sample(9, true)
	if !ok || u != 4 || d != 6 {
		t.Fatal("wrong drain")
	}
	if _, _, ok := s.Sample(0, true); ok {
		t.Fatal("duplicate drain")
	}
}

func TestConcurrentNodeCounterInitialization(t *testing.T) {
	var counters sync.Map
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); LoadOrCreate(&counters, "node").Tx("user", 1) }()
	}
	wg.Wait()
	if LoadOrCreate(&counters, "node").GetUpCount("user") != 100 {
		t.Fatal("first stream counters replaced")
	}
}
