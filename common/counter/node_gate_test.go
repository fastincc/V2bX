package counter

import (
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestNodeGateIsolationAndRetirement(t *testing.T) {
	var g NodeGate
	unlock := g.Lock("A")
	done := make(chan struct{})
	go func() { defer close(done); defer g.Lock("A")() }()
	other := make(chan struct{})
	go func() { defer close(other); defer g.Lock("B")() }()
	select {
	case <-other:
	case <-time.After(time.Second):
		unlock()
		t.Fatal("other node blocked")
	}
	select {
	case <-done:
		unlock()
		t.Fatal("same node not serialized")
	default:
	}
	unlock()
	<-done
	var wg sync.WaitGroup
	var active atomic.Int32
	for i := 0; i < 200; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			defer g.Lock("A")()
			if active.Add(1) != 1 {
				t.Error("concurrent owner")
			}
			active.Add(-1)
		}()
	}
	wg.Wait()
	g.mu.Lock()
	defer g.mu.Unlock()
	if len(g.nodes) != 0 {
		t.Fatal("gate entries leaked")
	}
}
func TestNodeGateCloseWaitsForPersistence(t *testing.T) {
	var g NodeGate
	unlock := g.Lock("A")
	done := make(chan struct{})
	go func() { defer close(done); defer g.LockAll()() }()
	select {
	case <-done:
		unlock()
		t.Fatal("close did not wait")
	case <-time.After(20 * time.Millisecond):
	}
	unlock()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("close stuck")
	}
}
