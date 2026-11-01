//go:build linux

package visionsplice

import (
	"bytes"
	"context"
	"encoding/json"
	"github.com/InazumaV/V2bX/common/dynamic"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

func pair(t *testing.T) (*net.TCPConn, *net.TCPConn) {
	t.Helper()
	l, e := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if e != nil {
		t.Fatal(e)
	}
	defer l.Close()
	a, e := net.DialTCP("tcp", nil, l.Addr().(*net.TCPAddr))
	if e != nil {
		t.Fatal(e)
	}
	b, e := l.AcceptTCP()
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { a.Close(); b.Close() })
	return a, b
}
func testPolicy(rev int64, speed int, enabled bool) *dynamic.Policy {
	return &dynamic.Policy{Version: 1, Revision: rev, Enabled: enabled, WindowSeconds: 60, ThresholdBytes: 1, SpeedMbps: speed, DurationSeconds: 30}
}
func TestVisionSpliceExistingStreamAndAccounting(t *testing.T) {
	in, src := pair(t)
	dst, out := pair(t)
	h := dynamic.New(0)
	defer h.Close()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var stats Stats
	var observed atomic.Int64
	done := make(chan error, 1)
	go func() {
		n, e := Copy(ctx, dst, src, Options{Stats: &stats, Limited: h.ActiveLimit, Before: func(ctx context.Context, n int64) error {
			return h.Wait(n, func() (time.Time, <-chan struct{}, error) { d, _ := ctx.Deadline(); return d, ctx.Done(), ctx.Err() })
		}, Progress: func(n int64) { observed.Add(n); h.Observe(n) }})
		if n != observed.Load() {
			t.Error("partial accounting mismatch")
		}
		done <- e
	}()
	exchange := func(n int) time.Duration {
		t.Helper()
		p := bytes.Repeat([]byte{91}, n)
		errc := make(chan error, 1)
		start := time.Now()
		go func() { _, e := in.Write(p); errc <- e }()
		out.SetReadDeadline(time.Now().Add(5 * time.Second))
		got := make([]byte, n)
		_, e := io.ReadFull(out, got)
		if e != nil {
			t.Fatal(e)
		}
		if e = <-errc; e != nil || !bytes.Equal(p, got) {
			t.Fatal("payload", e)
		}
		return time.Since(start)
	}
	exchange(4096)
	h.Apply(testPolicy(1, 1, true), time.Now())
	h.Observe(1)
	h.Tick(time.Now())
	a := exchange(125000)
	if a < 850*time.Millisecond || a > 2*time.Second {
		t.Fatal("1 Mbps", a)
	}
	h.Apply(testPolicy(2, 4, true), time.Now())
	b := exchange(125000)
	if b < 180*time.Millisecond || b > 800*time.Millisecond {
		t.Fatal("4 Mbps", b)
	}
	h.Apply(testPolicy(3, 4, false), time.Now())
	c := exchange(125000)
	if c > 500*time.Millisecond {
		t.Fatal("disabled", c)
	}
	in.CloseWrite()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
	if observed.Load() != 379096 || stats.Bytes.Load() != 379096 || stats.Calls.Load() == 0 {
		t.Fatal("splice/count", observed.Load(), stats.Bytes.Load())
	}
	t.Logf("VISION_SPLICE calls=%d one_mbps=%s four_mbps=%s off=%s bytes=%d", stats.Calls.Load(), a, b, c, observed.Load())
}
func TestVisionSpliceCancellationAndFDReclaim(t *testing.T) {
	before, _ := os.ReadDir("/proc/self/fd")
	for i := 0; i < 50; i++ {
		in, src := pair(t)
		dst, out := pair(t)
		ctx, cancel := context.WithCancel(context.Background())
		done := make(chan struct{})
		go func() { Copy(ctx, dst, src, Options{}); close(done) }()
		cancel()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("cancelled read stuck")
		}
		in.Close()
		src.Close()
		dst.Close()
		out.Close()
	}
	after, _ := os.ReadDir("/proc/self/fd")
	if len(after) > len(before)+2 {
		t.Fatal("fd leak", len(before), len(after))
	}
}
func TestVisionSpliceProfile(t *testing.T) {
	text := os.Getenv("VISION_PROFILE_DURATION")
	if text == "" {
		t.Skip("opt-in profile")
	}
	d, e := time.ParseDuration(text)
	if e != nil {
		t.Fatal(e)
	}
	mode := os.Getenv("VISION_PROFILE_MODE")
	parallel, _ := strconv.Atoi(os.Getenv("VISION_PROFILE_PARALLEL"))
	if parallel < 1 || parallel > 16 {
		t.Fatal("parallel")
	}
	h := dynamic.New(0)
	defer h.Close()
	if mode == "observe" {
		p := testPolicy(1, 20, true)
		p.ThresholdBytes = 1 << 60
		h.Apply(p, time.Now())
	}
	var total, wire atomic.Int64
	var stats Stats
	done := make(chan error, parallel)
	var before, after syscall.Rusage
	type connections struct{ in, src, dst, out *net.TCPConn }
	pairs := make([]connections, parallel)
	for i := range pairs {
		a, b := pair(t)
		c, d := pair(t)
		pairs[i] = connections{a, b, c, d}
	}
	syscall.Getrusage(syscall.RUSAGE_SELF, &before)
	start := time.Now()
	for _, p := range pairs {
		go func(p connections) {
			reader := make(chan int64, 1)
			go func() { n, _ := io.Copy(io.Discard, p.out); reader <- n }()
			copied := make(chan error, 1)
			go func() {
				var n int64
				var e error
				if mode == "legacy" {
					n, e = p.dst.ReadFrom(p.src)
				} else {
					n, e = Copy(context.Background(), p.dst, p.src, Options{Stats: &stats, Limited: h.ActiveLimit, Before: func(_ context.Context, n int64) error { return h.Wait(n, nil) }, Progress: h.Observe})
				}
				total.Add(n)
				p.dst.CloseWrite()
				copied <- e
			}()
			payload := bytes.Repeat([]byte{113}, 64<<10)
			p.in.SetWriteDeadline(start.Add(d))
			for time.Now().Before(start.Add(d)) {
				if _, e := p.in.Write(payload); e != nil {
					break
				}
			}
			p.in.CloseWrite()
			err := <-copied
			wire.Add(<-reader)
			done <- err
		}(p)
	}
	for i := 0; i < parallel; i++ {
		if e := <-done; e != nil {
			t.Fatal(e)
		}
	}
	seconds := time.Since(start).Seconds()
	syscall.Getrusage(syscall.RUSAGE_SELF, &after)
	if total.Load() != wire.Load() {
		t.Fatal("profile accounting")
	}
	if mode != "legacy" && (stats.Calls.Load() == 0 || stats.Bytes.Load() != wire.Load()) {
		t.Fatal("not splice")
	}
	cpu := func(r syscall.Rusage) float64 {
		return float64(r.Utime.Sec+r.Stime.Sec) + float64(r.Utime.Usec+r.Stime.Usec)/1e6
	}
	rss := after.Maxrss
	if runtime.GOOS == "linux" {
		rss *= 1024
	}
	b, _ := json.Marshal(map[string]any{"mode": mode, "parallel": parallel, "seconds": seconds, "mib_s": float64(wire.Load()) / 1048576 / seconds, "cpu_seconds": cpu(after) - cpu(before), "rss_bytes": rss, "bytes": wire.Load(), "splice_calls": stats.Calls.Load(), "reconciled": true})
	t.Log("VISION_PROFILE " + string(b))
}
