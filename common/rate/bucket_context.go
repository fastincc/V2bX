package rate

import (
	"context"
	"github.com/juju/ratelimit"
	"time"
)

// WaitBucketContext uses the same fixed bucket as ordinary writers. It does
// not reserve future tokens, so a cancelled raw-copy wait leaves no debt.
func WaitBucketContext(ctx context.Context, b *ratelimit.Bucket, n int64) error {
	if b == nil {
		return ctx.Err()
	}
	for n > 0 {
		if e := ctx.Err(); e != nil {
			return e
		}
		chunk := min(n, min(b.Capacity(), int64(16<<10)))
		if _, ok := b.TakeMaxDuration(chunk, 0); ok {
			n -= chunk
			continue
		}
		delay := max(time.Millisecond, time.Duration(float64(chunk-b.Available())/b.Rate()*float64(time.Second)))
		timer := time.NewTimer(delay)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return ctx.Err()
}
