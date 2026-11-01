package rate

import (
	"context"
	"github.com/juju/ratelimit"
	"testing"
	"time"
)

func TestVisionFixedBucketSharedAndCancellation(t *testing.T) {
	b := ratelimit.NewBucketWithRate(125000, 125000)
	b.Take(125000)
	done := make(chan error, 2)
	start := time.Now()
	for i := 0; i < 2; i++ {
		go func() { done <- WaitBucketContext(context.Background(), b, 62500) }()
	}
	for i := 0; i < 2; i++ {
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	}
	if d := time.Since(start); d < 850*time.Millisecond || d > 2*time.Second {
		t.Fatal("not shared", d)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if e := WaitBucketContext(ctx, b, 10<<20); e == nil {
		t.Fatal("cancel ignored")
	}
	start = time.Now()
	if e := WaitBucketContext(context.Background(), b, 1250); e != nil || time.Since(start) > 100*time.Millisecond {
		t.Fatal("future quota debt", e)
	}
}
