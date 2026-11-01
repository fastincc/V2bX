package dynamic

import (
	"errors"
	"math"
	"net"
	"os"
	"sync"
	"sync/atomic"
	"time"
)

type observation struct{ pending atomic.Int64 }
type slot struct {
	second int64
	bytes  int64
}

// Control is stable for one authorization generation, including across panel
// policy changes. It never owns or resets the billing counters/device records.
type Control struct {
	mu         sync.Mutex
	policy     *Policy
	last       Policy
	fixedMbps  int
	obs        atomic.Pointer[observation]
	gate       atomic.Pointer[gate]
	done       chan struct{}
	once       sync.Once
	closed     bool
	epoch      time.Time
	lastSecond int64
	slots      []slot
	total      int64
	until      time.Time
}

func New(fixedMbps int) *Control { return &Control{fixedMbps: fixedMbps, done: make(chan struct{})} }

// Observe is the only statistics operation in the packet path. Saturation is
// safe because no accepted threshold exceeds 2^60.
func (c *Control) Observe(n int64) {
	if c == nil || n <= 0 {
		return
	}
	o := c.obs.Load()
	if o == nil {
		return
	}
	for {
		old := o.pending.Load()
		next := old + n
		if next < old || next > 1<<60 {
			next = 1 << 60
		}
		if o.pending.CompareAndSwap(old, next) {
			return
		}
	}
}

func (c *Control) Apply(p *Policy, now time.Time) error {
	if err := p.Validate(); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed {
		return net.ErrClosed
	}
	if p == nil {
		c.policy = nil
		c.obs.Store(nil)
		c.resetWindow(now)
		c.until = time.Time{}
		c.setGate(nil)
		return nil
	}
	if p.Revision < c.last.Revision {
		return errors.New("stale dynamic policy")
	}
	if p.Revision == c.last.Revision {
		if *p != c.last {
			return errors.New("conflicting dynamic policy revision")
		}
		return nil
	}
	old := c.policy
	v := *p
	c.last = v
	c.policy = &v
	if !v.Enabled {
		c.obs.Store(nil)
		c.resetWindow(now)
		c.until = time.Time{}
		c.setGate(nil)
		return nil
	}
	if old == nil || !old.Enabled || old.WindowSeconds != v.WindowSeconds || old.ThresholdBytes != v.ThresholdBytes {
		c.obs.Store(&observation{})
		c.resetWindow(now)
	}
	if !c.until.IsZero() {
		if !now.Before(c.until) {
			c.until = time.Time{}
			// A policy update can win the race with the expiry tick. Discard
			// observations from the old penalty period in either ordering.
			c.obs.Store(&observation{})
			c.resetWindow(now)
			c.setGate(nil)
		} else {
			if cap := now.Add(time.Duration(v.DurationSeconds) * time.Second); cap.Before(c.until) {
				c.until = cap
			}
			if old == nil || old.SpeedMbps != v.SpeedMbps {
				c.activateGate(v.SpeedMbps)
			}
		}
	}
	return nil
}

func (c *Control) resetWindow(now time.Time) {
	c.slots = nil
	c.total = 0
	c.epoch = now
	c.lastSecond = 0
}
func (c *Control) setGate(g *gate) {
	if old := c.gate.Swap(g); old != nil {
		close(old.changed)
	}
}
func (c *Control) activateGate(mbps int) {
	// Own the effective ceiling while active, including direct reader paths.
	// Existing fixed buckets remain intact and never receive extra tokens.
	if c.fixedMbps > 0 && c.fixedMbps < mbps {
		mbps = c.fixedMbps
	}
	c.setGate(newGate(int64(mbps) * 1000000 / 8))
}

// Tick is driven once per second by one node task, never one goroutine/user.
// The return value reports a transition only; callers may log a safe user ID.
func (c *Control) Tick(now time.Time) string {
	if c == nil || c.obs.Load() == nil {
		return ""
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.closed || c.policy == nil || !c.policy.Enabled {
		return ""
	}
	o := c.obs.Load()
	delta := o.pending.Swap(0)
	if !c.until.IsZero() {
		if now.Before(c.until) {
			return ""
		}
		c.until = time.Time{}
		c.resetWindow(now)
		c.setGate(nil)
		return "expired"
	}
	sec := int64(now.Sub(c.epoch) / time.Second)
	if sec < c.lastSecond {
		sec = c.lastSecond
	}
	w := c.policy.WindowSeconds
	if len(c.slots) > 0 {
		if sec-c.lastSecond >= int64(w) {
			c.slots = nil
			c.total = 0
		} else {
			for s := c.lastSecond + 1; s <= sec; s++ {
				i := int(s % int64(w))
				c.total -= c.slots[i].bytes
				c.slots[i] = slot{second: s}
			}
		}
	}
	c.lastSecond = sec
	if delta > 0 {
		if len(c.slots) == 0 {
			c.slots = make([]slot, w)
		}
		i := int(sec % int64(w))
		c.slots[i].second = sec
		c.slots[i].bytes += delta
		c.total += delta
	}
	if c.total >= c.policy.ThresholdBytes {
		c.until = now.Add(time.Duration(c.policy.DurationSeconds) * time.Second)
		c.activateGate(c.policy.SpeedMbps)
		return "triggered"
	}
	if c.total == 0 {
		c.slots = nil
	}
	return ""
}

func (c *Control) Close() {
	if c == nil {
		return
	}
	c.once.Do(func() {
		c.mu.Lock()
		defer c.mu.Unlock()
		c.closed = true
		c.obs.Store(nil)
		c.slots = nil
		c.setGate(nil)
		close(c.done)
	})
}

type Status struct {
	Enabled, Limited bool
	Revision         int64
	WindowBytes      int64
	Until            time.Time
}

func (c *Control) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	return Status{Enabled: c.policy != nil && c.policy.Enabled && !c.closed, Limited: !c.until.IsZero(), Revision: c.last.Revision, WindowBytes: c.total, Until: c.until}
}

// gate reserves only available tokens. A bounded quantum hands the turn back
// to other streams, preventing a large write from monopolizing a user's queue.
type gate struct {
	mu                     sync.Mutex
	rate, tokens, capacity float64
	last                   time.Time
	turn                   chan struct{}
	changed                chan struct{}
}

func newGate(bytesPerSecond int64) *gate {
	r := float64(bytesPerSecond)
	g := &gate{rate: r, capacity: r, tokens: 0, last: time.Now(), turn: make(chan struct{}, 1), changed: make(chan struct{})}
	g.turn <- struct{}{}
	return g
}
func (g *gate) refill() {
	now := time.Now()
	g.tokens = math.Min(g.capacity, g.tokens+now.Sub(g.last).Seconds()*g.rate)
	g.last = now
}
func (g *gate) take(n int64) time.Duration {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.refill()
	if g.tokens >= float64(n) {
		g.tokens -= float64(n)
		return 0
	}
	return max(time.Millisecond, time.Duration((float64(n)-g.tokens)/g.rate*float64(time.Second)))
}
func (g *gate) refund(n int64) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.refill()
	g.tokens = math.Min(g.capacity, g.tokens+float64(n))
}

// WaitState supplies the connection's cancel/deadline state without allocating
// a context or goroutine per packet. nil is suitable for a caller with no deadline.
type WaitState func() (time.Time, <-chan struct{}, error)

func (c *Control) Wait(n int64, state WaitState) error {
	if c == nil || n <= 0 {
		return nil
	}
	for {
		g := c.gate.Load()
		if g == nil {
			return nil
		} // unchanged/disabled fast path
		retry, err := c.waitGate(g, n, state)
		if err != nil {
			return err
		}
		if !retry {
			return nil
		}
	}
}

func (c *Control) waitGate(g *gate, n int64, state WaitState) (retry bool, err error) {
	var spent int64
	defer func() {
		if retry || err != nil {
			g.refund(spent)
		}
	}()
	for spent < n {
		deadline, changed, e := time.Time{}, (<-chan struct{})(nil), error(nil)
		if state != nil {
			deadline, changed, e = state()
		}
		if e != nil {
			return false, e
		}
		if !deadline.IsZero() && !time.Now().Before(deadline) {
			return false, os.ErrDeadlineExceeded
		}
		var timeout <-chan time.Time
		var timer *time.Timer
		if !deadline.IsZero() {
			timer = time.NewTimer(time.Until(deadline))
			timeout = timer.C
		}
		select {
		case <-c.done:
			if timer != nil {
				timer.Stop()
			}
			return false, net.ErrClosed
		case <-g.changed:
			if timer != nil {
				timer.Stop()
			}
			return true, nil
		case <-changed:
			if timer != nil {
				timer.Stop()
			}
			continue
		case <-timeout:
			return false, os.ErrDeadlineExceeded
		case <-g.turn:
		}
		if timer != nil {
			timer.Stop()
		}
		chunk := min(n-spent, max(int64(1), min(int64(g.rate/100), 16*1024)))
		delay := g.take(chunk)
		if delay == 0 {
			spent += chunk
			g.turn <- struct{}{}
			continue
		}
		if !deadline.IsZero() {
			delay = min(delay, time.Until(deadline))
		}
		timer = time.NewTimer(delay)
		select {
		case <-c.done:
			timer.Stop()
			g.turn <- struct{}{}
			return false, net.ErrClosed
		case <-g.changed:
			timer.Stop()
			g.turn <- struct{}{}
			return true, nil
		case <-changed:
			timer.Stop()
		case <-timer.C:
		}
		g.turn <- struct{}{}
	}
	select {
	case <-g.changed:
		return true, nil
	case <-c.done:
		return false, net.ErrClosed
	default:
		return false, nil
	}
}

// ActiveLimit is a lock-free diagnostic; it does not include a fixed limiter.
func (c *Control) ActiveLimit() bool { return c != nil && c.gate.Load() != nil }
