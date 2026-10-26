package limiter

import (
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"sync"
	"testing"
	"time"
)

func TestDeviceReviewMultipleStreamsHoldOneSlot(t *testing.T) {
	l, u := deviceFixture(1)
	l.CheckLimit(u, "192.0.2.1", true, true)
	a, b := l.TrackDevice(u, "192.0.2.1"), l.TrackDevice(u, "::ffff:192.0.2.1")
	a()
	a()
	l.deviceLeases[u]["192.0.2.1"].expires = time.Now().Add(-time.Hour)
	if _, r := l.CheckLimit(u, "192.0.2.2", true, true); !r {
		t.Fatal("one close released another active stream")
	}
	b()
	l.deviceLeases[u]["192.0.2.1"].expires = time.Now().Add(-time.Hour)
	if _, r := l.CheckLimit(u, "192.0.2.2", true, true); r {
		t.Fatal("last stream failed to release")
	}
}
func TestDeviceReviewSnapshotDowngradeRecoveryAndUnion(t *testing.T) {
	l, u := deviceFixture(2)
	l.UpdateAliveSnapshot(map[int]int{1: 1}, &panel.DeviceSnapshot{Version: 1, TTL: 180, IPs: map[int]map[string]int{1: {"2001:db8::1": 180}}})
	if _, r := l.CheckLimit(u, "192.0.2.1", true, true); r {
		t.Fatal("free slot rejected")
	}
	if _, r := l.CheckLimit(u, "192.0.2.2", true, true); !r {
		t.Fatal("local/global union ignored")
	}
	l.UpdateAliveSnapshot(map[int]int{}, nil)
	if _, r := l.CheckLimit(u, "192.0.2.2", true, true); !r {
		t.Fatal("missing snapshot erased known state")
	}
	l.snapshotUntil = time.Now().Add(-time.Second)
	l.globalIPs[1]["2001:db8::1"] = time.Now().Add(-time.Second)
	if _, r := l.CheckLimit(u, "192.0.2.2", true, true); !r {
		t.Fatal("expired snapshot failed open")
	}
	l.UpdateAliveSnapshot(map[int]int{}, &panel.DeviceSnapshot{Version: 1, TTL: 180, IPs: map[int]map[string]int{}})
	if _, r := l.CheckLimit(u, "192.0.2.2", true, true); r {
		t.Fatal("recovery failed to reopen free slot")
	}
}
func TestDeviceReviewSnapshotRaceAndConcurrentSlots(t *testing.T) {
	l, u := deviceFixture(2)
	snapshot := &panel.DeviceSnapshot{Version: 1, TTL: 180, IPs: map[int]map[string]int{1: {"192.0.2.1": 180}}}
	l.UpdateAliveSnapshot(map[int]int{1: 1}, snapshot)
	var wg sync.WaitGroup
	accepted := make(chan string, 64)
	for i := 0; i < 64; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			ip := fmt.Sprintf("198.51.100.%d", i+1)
			if _, r := l.CheckLimit(u, ip, true, true); !r {
				accepted <- ip
			}
		}(i)
	}
	wg.Add(1)
	go func() {
		defer wg.Done()
		for i := 0; i < 100; i++ {
			l.UpdateAliveSnapshot(map[int]int{1: 1}, snapshot)
			l.GetOnlineDevice()
		}
	}()
	wg.Wait()
	close(accepted)
	if len(accepted) != 1 {
		t.Fatalf("remaining one slot admitted %d", len(accepted))
	}
}
