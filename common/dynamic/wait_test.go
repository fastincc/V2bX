package dynamic

import (
	"errors"
	"net"
	"os"
	"testing"
	"time"
)

func TestDynamicWaitWithoutLimitSkipsState(t *testing.T) {
	for _, name := range []string{"nil", "off", "observe", "disabled", "closed"} {
		t.Run(name, func(t *testing.T) {
			var c *Control
			if name != "nil" {
				c = New(0)
				defer c.Close()
				if name == "observe" || name == "disabled" {
					p := testPolicy()
					p.Enabled = name == "observe"
					if err := c.Apply(p, time.Now()); err != nil {
						t.Fatal(err)
					}
				}
				if name == "closed" {
					c.Close()
				}
			}
			calls := 0
			state := func() (time.Time, <-chan struct{}, error) {
				calls++
				return time.Time{}, nil, net.ErrClosed
			}
			for _, n := range []int64{-1, 0, 32768} {
				if err := c.Wait(n, state); err != nil {
					t.Fatalf("Wait(%d): %v", n, err)
				}
			}
			if calls != 0 {
				t.Fatalf("no-limit wait called state %d times", calls)
			}
		})
	}
}

func TestDynamicWaitNonpositiveWithLimitSkipsStateAndQuota(t *testing.T) {
	c := triggered(t)
	g := c.gate.Load()
	beforeTokens, beforeLast := g.tokens, g.last
	for _, n := range []int64{-1, 0} {
		if err := c.Wait(n, func() (time.Time, <-chan struct{}, error) {
			t.Error("nonpositive wait called state")
			return time.Time{}, nil, net.ErrClosed
		}); err != nil {
			t.Fatalf("Wait(%d): %v", n, err)
		}
	}
	if g.tokens != beforeTokens || g.last != beforeLast {
		t.Fatal("nonpositive wait touched quota")
	}
}

func TestDynamicWaitStateErrors(t *testing.T) {
	sentinel := errors.New("connection canceled")
	for _, tc := range []struct {
		name     string
		deadline time.Time
		stateErr error
		want     error
	}{
		{"state-error", time.Time{}, sentinel, sentinel},
		{"expired-deadline", time.Now().Add(-time.Second), nil, os.ErrDeadlineExceeded},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := triggered(t)
			calls := 0
			err := c.Wait(1, func() (time.Time, <-chan struct{}, error) {
				calls++
				return tc.deadline, nil, tc.stateErr
			})
			if err != tc.want || calls != 1 {
				t.Fatalf("got (%v, %d calls), want (%v, 1 call)", err, calls, tc.want)
			}
		})
	}
}

// Hold the first state callback until the public policy/lifecycle operation has
// replaced the gate. No sleeps or rate timing decide which gate Wait captured.
func TestDynamicWaitCapturedGateTransition(t *testing.T) {
	for _, transition := range []string{"disable", "replace", "close"} {
		t.Run(transition, func(t *testing.T) {
			c := triggered(t)
			old := c.gate.Load()
			// Keep the old gate's turn unavailable, so changed/done is the
			// only wake-up after the first callback returns.
			<-old.turn
			entered := make(chan struct{})
			resume := make(chan struct{})
			released := false
			defer func() {
				if !released {
					close(resume)
				}
			}()
			sentinel := errors.New("new gate reached")
			type result struct {
				err   error
				calls int
			}
			finished := make(chan result, 1)
			go func() {
				calls := 0
				err := c.Wait(1, func() (time.Time, <-chan struct{}, error) {
					calls++
					if calls == 1 {
						close(entered)
						<-resume
						return time.Time{}, nil, nil
					}
					return time.Time{}, nil, sentinel
				})
				finished <- result{err, calls}
			}()
			select {
			case <-entered:
			case <-time.After(time.Second):
				t.Fatal("wait did not capture the original gate")
			}
			switch transition {
			case "disable":
				if err := c.Apply(nil, time.Now()); err != nil {
					t.Fatal(err)
				}
			case "replace":
				p := testPolicy()
				p.Revision++
				p.SpeedMbps++
				// The transition test must stay inside the penalty period,
				// even if a race-enabled runner is descheduled for a while.
				beforeExpiry := c.Status().Until.Add(-time.Second)
				if err := c.Apply(p, beforeExpiry); err != nil {
					t.Fatal(err)
				}
				if c.gate.Load() == old {
					t.Fatal("policy update did not replace gate")
				}
			case "close":
				c.Close()
			}
			close(resume)
			released = true
			select {
			case got := <-finished:
				switch transition {
				case "disable":
					if got.err != nil || got.calls != 1 {
						t.Fatalf("disable: %+v", got)
					}
				case "replace":
					if got.err != sentinel || got.calls != 2 {
						t.Fatalf("replacement was not retried: %+v", got)
					}
				case "close":
					// Close clears the gate before signaling done. The old
					// changed/done select has always allowed either result.
					if (got.err != nil && got.err != net.ErrClosed) || got.calls != 1 {
						t.Fatalf("close: %+v", got)
					}
				}
			case <-time.After(time.Second):
				t.Fatal("gate transition did not release wait")
			}
		})
	}
}

func TestDynamicWaitPartialQuotaRefund(t *testing.T) {
	c := triggered(t)
	g := c.gate.Load()
	g.tokens = g.capacity
	quantum := max(int64(1), min(int64(g.rate/100), 16*1024))
	sentinel := errors.New("cancel after one quantum")
	calls := 0
	err := c.Wait(2*quantum, func() (time.Time, <-chan struct{}, error) {
		calls++
		if calls > 1 {
			return time.Time{}, nil, sentinel
		}
		return time.Time{}, nil, nil
	})
	if err != sentinel || calls != 2 {
		t.Fatalf("got (%v, %d calls), want cancellation after one quantum", err, calls)
	}
	if g.tokens != g.capacity {
		t.Fatalf("spent quota was not fully refunded: got %f, want %f", g.tokens, g.capacity)
	}
}
