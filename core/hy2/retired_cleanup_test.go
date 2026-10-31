package hy2

import (
	"fmt"
	"runtime"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	vcore "github.com/InazumaV/V2bX/core"
)

func retiredFixture() (*Hysteria2, *V2bX, *HookServer) {
	a := &V2bX{usersMap: map[string]int{}}
	hook := &HookServer{Auth: a, Tag: "retire"}
	h := &Hysteria2{Hy2nodes: map[string]Hysteria2node{"retire": {Auth: a, TrafficLogger: hook}}}
	return h, a, hook
}
func retireCycles(t testing.TB, h *Hysteria2, n int) {
	t.Helper()
	u := []panel.UserInfo{{Id: 41, Uuid: "synthetic-retire"}}
	for i := 0; i < n; i++ {
		if _, e := h.AddUsers(&vcore.AddUsersParams{Tag: "retire", Users: u}); e != nil {
			t.Fatal(e)
		}
		if e := h.DelUsers(u, "retire", nil); e != nil {
			t.Fatal(e)
		}
	}
}
func TestRetiredWithoutCounter(t *testing.T) {
	for _, mode := range []string{"legacy", "reliable"} {
		for _, n := range []int{10000, 100000} {
			t.Run(fmt.Sprintf("%s/%d", mode, n), func(t *testing.T) {
				h, a, hook := retiredFixture()
				a.addLocked("active", 7)
				retireCycles(t, h, n)
				calls := 0
				start := time.Now()
				for len(a.identities) > 1 {
					before := len(a.identities)
					if mode == "legacy" {
						if _, e := h.GetUserTrafficSlice("retire", true); e != nil {
							t.Fatal(e)
						}
					} else {
						if e := h.PersistTrafficSnapshot("retire", func([]panel.UserTraffic) error { t.Fatal("empty cleanup persisted data"); return nil }); e != nil {
							t.Fatal(e)
						}
					}
					delta := before - len(a.identities)
					if delta <= 0 || delta > 256 {
						t.Fatalf("invalid bounded progress: %d", delta)
					}
					calls++
				}
				if len(a.active) != 1 || a.retired.Len() != 0 {
					t.Fatal("active user changed or queue retained")
				}
				if _, exists := hook.Counter.Load("retire"); exists {
					t.Fatal("cleanup created counter")
				}
				t.Logf("mode=%s retired=%d collections=%d elapsed=%s", mode, n, calls, time.Since(start))
			})
		}
	}
}
func TestRetiredPendingTailsDoNotStarve(t *testing.T) {
	h, a, hook := retiredFixture()
	c := counter.LoadOrCreate(&hook.Counter, "retire")
	var oldIDs []string
	for i := 0; i < 300; i++ {
		raw := fmt.Sprintf("pending-%d", i)
		u := []panel.UserInfo{{Id: 100 + i, Uuid: raw}}
		h.AddUsers(&vcore.AddUsersParams{Tag: "retire", Users: u})
		id := a.active[raw]
		oldIDs = append(oldIDs, id)
		c.Tx(id, 1)
		h.DelUsers(u, "retire", nil)
	}
	retireCycles(t, h, 600)
	for i := 0; i < 5; i++ {
		h.GetUserTrafficSlice("retire", false)
	}
	if len(a.identities) != 300 {
		t.Fatalf("pending tails starved zero counters: %d", len(a.identities))
	}
	for _, id := range oldIDs {
		if c.GetUpCount(id) != 1 || a.uidLocked(id) == 0 {
			t.Fatal("tail lost")
		}
	}
	var total int64
	if e := h.PersistTrafficSnapshot("retire", func(rows []panel.UserTraffic) error {
		for _, r := range rows {
			total += r.Upload
		}
		return nil
	}); e != nil {
		t.Fatal(e)
	}
	for i := 0; i < 3; i++ {
		h.PersistTrafficSnapshot("retire", func([]panel.UserTraffic) error { t.Fatal("duplicate persistence"); return nil })
	}
	if total != 300 || len(a.identities) != 0 || a.retired.Len() != 0 {
		t.Fatal("tail reconciliation or cleanup")
	}
}
func TestRetiredCleanupHeapPlateau(t *testing.T) {
	var values []uint64
	h, a, _ := retiredFixture()
	for round := 0; round < 8; round++ {
		retireCycles(t, h, 10000)
		for a.retired.Len() > 0 {
			h.PersistTrafficSnapshot("retire", func([]panel.UserTraffic) error { return nil })
		}
		if len(a.identities) != 0 {
			t.Fatal("records retained")
		}
		runtime.GC()
		var m runtime.MemStats
		runtime.ReadMemStats(&m)
		values = append(values, m.HeapAlloc)
		runtime.KeepAlive(h)
	}
	t.Logf("post-GC heap bytes across 8 successive 10000-cycle runs on one node: %v", values)
}
func BenchmarkRetiredIdleCollection(b *testing.B) {
	for _, n := range []int{0, 100000} {
		b.Run(fmt.Sprint(n), func(b *testing.B) {
			h, a, hook := retiredFixture()
			counter.LoadOrCreate(&hook.Counter, "retire")
			for i := 0; i < n; i++ {
				a.addLocked(fmt.Sprint(i), i+1)
			}
			b.ResetTimer()
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				h.PersistTrafficSnapshot("retire", func([]panel.UserTraffic) error { return nil })
			}
		})
	}
}
