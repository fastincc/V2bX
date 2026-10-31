package hy2

import (
	"context"
	"errors"
	"github.com/apernet/hysteria/core/v2/server"
	"github.com/juju/ratelimit"
	"time"
)

var errRateClosed = errors.New("traffic pacing stopped")
var _ server.TrafficPacerProvider = (*HookServer)(nil)

// One quota per user, shared across TCP/UDP, directions and physical connections.
// The limiter owns the bucket; generations own cancellation, never credentials
// or accounting counters. All waits happen outside authentication/device locks.
type hyRate struct {
	bucket *ratelimit.Bucket
	done   chan struct{}
	turn   chan struct{}
}
type hyPacer struct {
	auth *V2bX
	id   string
	rate *hyRate
}
type deniedPacer struct{}

func (deniedPacer) Wait(context.Context, int) error { return errRateClosed }

func (h *HookServer) TrafficPacer(id string) server.TrafficPacer {
	a := h.Auth
	if a == nil {
		return deniedPacer{}
	}
	a.mutex.Lock()
	defer a.mutex.Unlock()
	s := a.sessions[id]
	if s == nil || s.denied || s.closed || a.paused {
		return deniedPacer{}
	}
	entry, ok := a.identities[s.generation]
	if !ok || entry.retired || a.active[entry.credential] != s.generation {
		return deniedPacer{}
	}
	if s.bucket == nil {
		return nil
	}
	if entry.rate == nil {
		entry.rate = &hyRate{bucket: s.bucket, done: make(chan struct{}), turn: make(chan struct{}, 1)}
		entry.rate.turn <- struct{}{}
		a.identities[s.generation] = entry
	}
	return &hyPacer{auth: a, id: id, rate: entry.rate}
}

func (p *hyPacer) Wait(ctx context.Context, n int) error {
	// Serialize only this user's quota reservations. Channel handoff lets a
	// waiting large write progress even under continuous small-packet traffic,
	// and avoids waking every flow for the same refill. Cancellation can leave
	// the queue immediately; no authentication/device lock is held.
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-p.rate.done:
		return errRateClosed
	case <-p.rate.turn:
	}
	defer func() { p.rate.turn <- struct{}{} }()
	b := p.rate.bucket
	// Bound each reservation to the bucket capacity, including a large sniff
	// prefix. Never reserve future tokens: cancellation cannot leave token debt.
	for left := int64(n); left > 0; {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-p.rate.done:
			return errRateClosed
		default:
		}
		chunk := min(left, b.Capacity())
		if _, ok := b.TakeMaxDuration(chunk, 0); ok {
			left -= chunk
			continue
		}
		delay := time.Duration(float64(chunk-b.Available()) / b.Rate() * float64(time.Second))
		if delay < 10*time.Millisecond {
			delay = 10 * time.Millisecond
		}
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-p.rate.done:
			timer.Stop()
			return errRateClosed
		case <-timer.C:
		}
	}
	// A revocation/drain may race with a reservation. Recheck before forwarding,
	// without retaining any lock during network I/O.
	p.auth.mutex.RLock()
	s := p.auth.sessions[p.id]
	valid := s != nil && !s.denied && !s.closed && !p.auth.paused
	if valid {
		entry, ok := p.auth.identities[s.generation]
		valid = ok && !entry.retired && entry.rate == p.rate && p.auth.active[entry.credential] == s.generation
	}
	p.auth.mutex.RUnlock()
	if !valid {
		return errRateClosed
	}
	return ctx.Err()
}
