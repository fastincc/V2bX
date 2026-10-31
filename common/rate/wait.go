package rate

import (
	"net"
	"os"
	"sync"
	"time"

	"github.com/juju/ratelimit"
)

// waitState owns cancellation for one I/O direction. Refill remains lazy in the
// shared bucket: there is no background timer or goroutine per user/connection.
type waitState struct {
	mu       sync.Mutex
	closed   bool
	deadline time.Time
	changed  chan struct{}
}

func newWaitState() *waitState { return &waitState{changed: make(chan struct{})} }
func (s *waitState) update(deadline time.Time, closeIO bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.deadline = deadline
	s.closed = s.closed || closeIO
	close(s.changed)
	s.changed = make(chan struct{})
}
func (s *waitState) snapshot() (time.Time, <-chan struct{}, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.closed {
		return s.deadline, s.changed, net.ErrClosed
	}
	if !s.deadline.IsZero() && !s.deadline.After(time.Now()) {
		return s.deadline, s.changed, os.ErrDeadlineExceeded
	}
	return s.deadline, s.changed, nil
}
func (s *waitState) check() error { _, _, err := s.snapshot(); return err }
func (s *waitState) wait(bucket *ratelimit.Bucket, n int64) error {
	if err := s.check(); err != nil {
		return err
	}
	ready := time.Now().Add(bucket.Take(n))
	for {
		deadline, changed, err := s.snapshot()
		if err != nil {
			return err
		}
		if !ready.After(time.Now()) {
			return nil
		}
		end := ready
		if !deadline.IsZero() && deadline.Before(end) {
			end = deadline
		}
		timer := time.NewTimer(time.Until(end))
		select {
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
	}
}
