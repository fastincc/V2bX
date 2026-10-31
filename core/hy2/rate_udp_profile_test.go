package hy2

import (
	"bytes"
	"encoding/json"
	"os"
	"runtime"
	"strconv"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// Opt-in, finite loopback profile. Non-fragmented messages allow independent
// application/ingress reconciliation on both the baseline and candidate.
func TestHy2UDPTransportProfile(t *testing.T) {
	raw := os.Getenv("HY2_UDP_PROFILE_DURATION")
	if raw == "" {
		t.Skip("set HY2_UDP_PROFILE_DURATION")
	}
	duration, e := time.ParseDuration(raw)
	if e != nil || duration < time.Second || duration > time.Minute {
		t.Fatal("invalid duration")
	}
	parallel, e := strconv.Atoi(os.Getenv("HY2_UDP_PROFILE_PARALLEL"))
	if e != nil || parallel < 1 || parallel > 16 {
		t.Fatal("invalid parallelism")
	}
	f := newTailLifecycleFixture(t, 0, nil)
	var total atomic.Int64
	var wg sync.WaitGroup
	var before, after syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &before)
	start := time.Now()
	end := start.Add(duration)
	for i := 0; i < parallel; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c, e := f.dial("127.0.0.1")
			if e != nil {
				t.Error(e)
				return
			}
			defer c.Close()
			u, e := c.UDP()
			if e != nil {
				t.Error(e)
				return
			}
			defer u.Close()
			timeout := time.AfterFunc(duration+5*time.Second, func() { c.Close() })
			defer timeout.Stop()
			p := bytes.Repeat([]byte{7}, 1000)
			for time.Now().Before(end) {
				if e = u.Send(p, f.udp); e != nil {
					t.Error(e)
					return
				}
				b, _, e := u.Receive()
				if e != nil {
					t.Error(e)
					return
				}
				if !bytes.Equal(p, b) {
					t.Error("UDP payload changed")
					return
				}
				total.Add(1000)
			}
		}()
	}
	wg.Wait()
	elapsed := time.Since(start).Seconds()
	syscall.Getrusage(syscall.RUSAGE_SELF, &after)
	rows, e := f.h.GetUserTrafficSlice(f.tag, false)
	if e != nil || len(rows) != 1 || rows[0].Upload != total.Load() || rows[0].Download != total.Load() {
		t.Fatalf("UDP accounting %+v %v application=%d", rows, e, total.Load())
	}
	cpu := func(r syscall.Rusage) float64 {
		return float64(r.Utime.Sec+r.Stime.Sec) + float64(r.Utime.Usec+r.Stime.Usec)/1e6
	}
	rss := after.Maxrss
	if runtime.GOOS == "linux" {
		rss *= 1024
	}
	result, _ := json.Marshal(map[string]any{"parallel": parallel, "seconds": elapsed, "bytes_each_direction": total.Load(), "aggregate_mib_s": float64(total.Load()*2) / float64(1<<20) / elapsed, "cpu_seconds": cpu(after) - cpu(before), "max_rss_bytes": rss, "reconciled": true})
	t.Logf("UDP_PROFILE %s", result)
}
