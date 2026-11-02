package tcpquickack

import (
	"errors"
	"sync"
	"sync/atomic"
	"testing"
)

func TestHintBulkAndFailure(t *testing.T) {
	var calls int
	h := &hint{ack: func() error { calls++; return errors.New("unsupported") }}
	for _, n := range []int{0, -1, 2049, 65536, 1 << 20} {
		h.afterRead(n, n)
	}
	if calls != 0 || h.next.Load() != 0 {
		t.Fatal("bulk path performed work")
	}
	h.afterRead(45, 45)
	h.next.Store(0)
	h.afterRead(128, 128)
	if calls != 1 || !h.disabled.Load() {
		t.Fatal("failure did not permanently fall back", calls)
	}
}

func TestHintBudgetConcurrent(t *testing.T) {
	h := new(hint)
	var calls atomic.Int32
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			if h.claim(10_000_000) {
				calls.Add(1)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 1 {
		t.Fatal("concurrent hints exceeded budget", calls.Load())
	}
	if h.claim(10_000_001) || !h.claim(10_000_000+int64(minInterval)) {
		t.Fatal("cooldown boundary")
	}
}

func BenchmarkHintBulk(b *testing.B) {
	h := new(hint)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h.afterRead(65536, 65536)
	}
}

func BenchmarkHintSmallCooldown(b *testing.B) {
	h := &hint{ack: func() error { return nil }}
	h.next.Store(1 << 62)
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		h.afterRead(128, 128)
	}
}

func TestHintCoalescedShortRead(t *testing.T) {
	var calls int
	h := &hint{ack: func() error { calls++; return nil }}
	h.afterRead(32768, 32768)
	if calls != 0 {
		t.Fatal("full bulk read triggered")
	}
	h.afterRead(32000, 32768)
	if calls != 1 {
		t.Fatal("coalesced tail was missed")
	}
}
