package task

import (
	"sync"
	"time"
)

type Task struct {
	Interval time.Duration
	Execute  func() error
	access   sync.Mutex
	running  bool
	stop     chan struct{}
	done     chan struct{}
}

func (t *Task) Start(first bool) error {
	t.access.Lock()
	defer t.access.Unlock()
	if t.running {
		return nil
	}
	t.running = true
	stop, done := make(chan struct{}), make(chan struct{})
	t.stop, t.done = stop, done
	interval := t.Interval
	if interval <= 0 {
		interval = time.Second
	}
	execute := t.Execute
	go func() {
		defer close(done)
		defer func() {
			t.access.Lock()
			if t.stop == stop {
				t.running = false
			}
			t.access.Unlock()
		}()
		if first {
			select {
			case <-stop:
				return
			default:
			}
			if execute() != nil {
				return
			}
		}
		timer := time.NewTimer(interval)
		defer timer.Stop()
		for {
			select {
			case <-stop:
				return
			case <-timer.C:
			}
			select {
			case <-stop:
				return
			default:
			}
			if execute() != nil {
				return
			}
			timer.Reset(interval)
		}
	}()
	return nil
}
func (t *Task) Close() {
	t.access.Lock()
	if t.running {
		t.running = false
		close(t.stop)
	}
	t.access.Unlock()
}

// Wait is for external lifecycle owners, never the task's own callback.
func (t *Task) Wait() {
	t.access.Lock()
	done := t.done
	t.access.Unlock()
	if done != nil {
		<-done
	}
}
func (t *Task) SetInterval(interval time.Duration) {
	t.Close()
	t.access.Lock()
	t.Interval = interval
	t.access.Unlock()
	_ = t.Start(false)
}
