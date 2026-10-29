package task

import (
	"sync/atomic"
	"testing"
	"time"
)

func TestTaskRestartKeepsOneGeneration(t *testing.T) {
	var calls atomic.Int32
	entered := make(chan struct{}, 5)
	release := make(chan struct{})
	ts := &Task{Interval: 10 * time.Millisecond}
	ts.Execute = func() error {
		n := calls.Add(1)
		if n == 1 {
			ts.SetInterval(10 * time.Millisecond)
			return nil
		}
		entered <- struct{}{}
		<-release
		return nil
	}
	ts.Start(false)
	select {
	case <-entered:
	case <-time.After(time.Second):
		t.Fatal("not running")
	}
	select {
	case <-entered:
		t.Fatal("old task generation continued")
	case <-time.After(50 * time.Millisecond):
	}
	ts.Close()
	close(release)
	ts.Wait()
}
