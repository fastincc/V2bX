//go:build linux || darwin

package hy2

import (
	"encoding/json"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/limiter"
	"os"
	"runtime"
	"strconv"
	"syscall"
	"testing"
	"time"
)

func TestHy2LeaseChurnProfile(t *testing.T) {
	if os.Getenv("HY2_LEASE_CHURN") != "1" {
		t.Skip("set HY2_LEASE_CHURN=1")
	}
	f := newHy2SessionFixture(t, 1)
	a := f.h.Hy2nodes[f.tag].Auth
	l, _ := limiter.GetLimiter(f.tag)
	var samples []map[string]any
	rounds := 6
	if raw := os.Getenv("HY2_CHURN_ROUNDS"); raw != "" {
		n, e := strconv.Atoi(raw)
		if e != nil || n < 1 || n > 100 {
			t.Fatal("rounds")
		}
		rounds = n
	}
	settle := 100 * time.Millisecond
	if raw := os.Getenv("HY2_CHURN_SETTLE"); raw != "" {
		d, e := time.ParseDuration(raw)
		if e != nil || d < 0 || d > 5*time.Second {
			t.Fatal("settle")
		}
		settle = d
	}
	for round := 0; round < rounds; round++ {
		start := time.Now()
		var before, after syscall.Rusage
		syscall.Getrusage(syscall.RUSAGE_SELF, &before)
		for i := 0; i < 50; i++ {
			c, e := f.dial("127.0.0.1")
			if e != nil {
				t.Fatal(e)
			}
			s, e := c.TCP(f.tcp)
			if e != nil {
				c.Close()
				t.Fatal(e)
			}
			if e = hyExchange(s, make([]byte, 4096)); e != nil {
				t.Fatal(e)
			}
			s.Close()
			c.Close()
		}
		deadline := time.Now().Add(5 * time.Second)
		for {
			a.mutex.Lock()
			left := len(a.sessions) + len(a.streams)
			a.mutex.Unlock()
			if left == 0 {
				break
			}
			if time.Now().After(deadline) {
				t.Fatal("handles not drained", left)
			}
			time.Sleep(time.Millisecond)
		}
		var up, down int64
		if e := f.h.PersistTrafficSnapshot(f.tag, func(rows []panel.UserTraffic) error {
			for _, r := range rows {
				up += r.Upload
				down += r.Download
			}
			return nil
		}); e != nil {
			t.Fatal(e)
		}
		if up != 50*4096 || down != up {
			t.Fatal("churn accounting", up, down)
		}
		l.GetOnlineDevice()
		rows, _ := l.GetOnlineDevice()
		if len(*rows) != 0 {
			t.Fatal("active lease leaked")
		}
		runtime.GC()
		time.Sleep(settle)
		var mem runtime.MemStats
		runtime.ReadMemStats(&mem)
		syscall.Getrusage(syscall.RUSAGE_SELF, &after)
		cpu := func(r syscall.Rusage) float64 {
			return float64(r.Utime.Sec+r.Stime.Sec) + float64(r.Utime.Usec+r.Stime.Usec)/1e6
		}
		rss := after.Maxrss
		if runtime.GOOS == "linux" {
			rss *= 1024
		}
		samples = append(samples, map[string]any{"round": round, "connections": 50, "heap_bytes": mem.HeapAlloc, "goroutines": runtime.NumGoroutine(), "max_rss_bytes": rss, "seconds": time.Since(start).Seconds(), "cpu_seconds": cpu(after) - cpu(before), "bytes_each_direction": up, "live_handles": 0, "active_devices": 0})
	}
	b, _ := json.Marshal(samples)
	t.Log("HY2_CHURN " + string(b))
}
