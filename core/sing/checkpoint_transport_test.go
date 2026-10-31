//go:build linux || darwin

package sing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/trafficbatch"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Opt-in, bounded real TLS/AnyTLS/PROXY-v2/hook/leased-counter/durable-store
// comparison. Same fixture can run on -12 via overlay; it has no candidate APIs.
func TestCheckpointTransportProfile(t *testing.T) {
	durationText := os.Getenv("CHECKPOINT_PROFILE_DURATION")
	if durationText == "" {
		t.Skip("set CHECKPOINT_PROFILE_DURATION")
	}
	duration, e := time.ParseDuration(durationText)
	if e != nil || duration < time.Second || duration > 5*time.Minute {
		t.Fatal("invalid duration")
	}
	parallel, _ := strconv.Atoi(os.Getenv("CHECKPOINT_PROFILE_PARALLEL"))
	if parallel == 0 {
		parallel = 1
	}
	if parallel < 1 || parallel > 32 {
		t.Fatal("invalid parallelism")
	}
	slowMS, _ := strconv.Atoi(os.Getenv("CHECKPOINT_PROFILE_SLOW_MS"))
	if slowMS < 0 || slowMS > 1000 {
		t.Fatal("invalid disk delay")
	}
	writer := new(dualLogWriter)
	f := newReviewFixtureAudit(t, true, []option.AnyTLSUser{{Name: "test-user", Password: "test-password", AuditID: "123"}}, testDualLogger(t, writer), true)
	users := &UserMap{uidMap: map[userKey]int{{"review", "test-user"}: 123}, active: map[userKey]string{{"review", "test-user"}: "test-user"}}
	f.router.hook.users = users
	b := &Sing{users: users, hookServer: &f.router.hook}
	store, e := trafficbatch.Open(filepath.Join(t.TempDir(), "queue"), "profile")
	if e != nil {
		t.Fatal(e)
	}
	defer store.Close()
	stop := make(chan struct{})
	stopped := make(chan struct{})
	var saves atomic.Int64
	interval := time.Second
	if slowMS > 0 {
		interval = 100 * time.Millisecond
	}
	go func() {
		defer close(stopped)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-stop:
				return
			case <-ticker.C:
				if err := b.PersistTrafficSnapshot("review", func(rows []panel.UserTraffic) error {
					if slowMS > 0 {
						time.Sleep(time.Duration(slowMS) * time.Millisecond)
					}
					saves.Add(1)
					return store.Save(rows)
				}); err != nil {
					t.Error(err)
					return
				}
			}
		}
	}()
	var before, after syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &before)
	var total atomic.Int64
	var streams atomic.Int64
	var wg sync.WaitGroup
	var latencyMu sync.Mutex
	var latencies []float64
	start := time.Now()
	end := start.Add(duration)
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			cl := f.client(t)
			defer cl.Close()
			payload := bytes.Repeat([]byte{42}, 32768)
			reply := make([]byte, len(payload))
			for time.Now().Before(end) {
				begun := time.Now()
				s, err := cl.CreateProxy(context.Background(), M.ParseSocksaddr("echo.invalid:80"))
				if err != nil {
					t.Error(err)
					return
				}
				s.SetDeadline(time.Now().Add(5 * time.Second))
				if _, err = s.Write(payload[:32]); err == nil {
					_, err = io.ReadFull(s, reply[:32])
				}
				if err != nil {
					s.Close()
					t.Error(err)
					return
				}
				latencyMu.Lock()
				latencies = append(latencies, float64(time.Since(begun).Microseconds())/1000)
				latencyMu.Unlock()
				total.Add(32)
				for j := 0; j < 8; j++ {
					if _, err = s.Write(payload); err == nil {
						_, err = io.ReadFull(s, reply)
					}
					if err != nil || !bytes.Equal(payload, reply) {
						s.Close()
						t.Error("payload transfer", err)
						return
					}
					total.Add(int64(len(payload)))
				}
				s.Close()
				select {
				case <-f.router.done:
				case <-time.After(5 * time.Second):
					t.Error("stream did not close")
					return
				}
				if source := <-f.router.sources; source != "198.51.100.9" {
					t.Error("real IP changed", source)
					return
				}
				streams.Add(1)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start)
	close(stop)
	<-stopped
	if err := b.PersistTrafficSnapshot("review", store.Save); err != nil {
		t.Fatal(err)
	}
	if e = store.Seal(); e != nil {
		t.Fatal(e)
	}
	var up, down int64
	for batch := store.Pending(); batch != nil; batch = store.Pending() {
		for _, r := range batch.Traffic {
			if r.UID != 123 {
				t.Fatal("wrong UID")
			}
			up += r.Upload
			down += r.Download
		}
		if e = store.Acknowledge(batch.ID); e != nil {
			t.Fatal(e)
		}
	}
	if up != total.Load() || down != total.Load() || total.Load() == 0 {
		t.Fatalf("reconciliation up=%d down=%d expected=%d", up, down, total.Load())
	}
	writer.mu.Lock()
	events, bad := writer.auditEvents, writer.invalid
	writer.mu.Unlock()
	if bad || int64(events) != streams.Load() {
		t.Fatal("audit mismatch", events, streams.Load(), bad)
	}
	syscall.Getrusage(syscall.RUSAGE_SELF, &after)
	cpu := func(r syscall.Rusage) float64 {
		return float64(r.Utime.Sec+r.Stime.Sec) + float64(r.Utime.Usec+r.Stime.Usec)/1e6
	}
	rss := after.Maxrss
	if runtime.GOOS == "linux" {
		rss *= 1024
	}
	sort.Float64s(latencies)
	quantile := func(q float64) float64 {
		if len(latencies) == 0 {
			return 0
		}
		return latencies[int(float64(len(latencies)-1)*q)]
	}
	var mem runtime.MemStats
	runtime.GC()
	runtime.ReadMemStats(&mem)
	out := map[string]any{"parallel": parallel, "slow_ms": slowMS, "seconds": elapsed.Seconds(), "streams": streams.Load(), "bytes_each_direction": up, "aggregate_mib_s": float64(up+down) / (1 << 20) / elapsed.Seconds(), "p50_ms": quantile(.5), "p95_ms": quantile(.95), "p99_ms": quantile(.99), "cpu_seconds": cpu(after) - cpu(before), "max_rss_bytes": rss, "heap_after_gc": mem.HeapAlloc, "checkpoints": saves.Load(), "real_ip_and_audit": true, "reconciled": true}
	encoded, _ := json.Marshal(out)
	t.Log(fmt.Sprintf("PROFILE %s", encoded))
}
