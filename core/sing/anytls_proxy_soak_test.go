package sing

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"os"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/common/counter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
)

// Keep bounded log memory during the soak; exercise real Info log formatting.
type dualLogWriter struct {
	auditEvents int
	mu          sync.Mutex
	pairs       int
	invalid     bool
}

func (w *dualLogWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := string(p)
	if at := strings.Index(s, "AUDIT_ACCESS "); at >= 0 {
		w.auditEvents++
		var e struct {
			Authenticated bool    `json:"authenticated"`
			UserID        *string `json:"user_id"`
		}
		if json.Unmarshal([]byte(s[at+13:]), &e) != nil || !e.Authenticated || e.UserID == nil {
			w.invalid = true
		}
	}

	if strings.Contains(s, "AnyTLS PROXY addresses:") {
		w.pairs++
		if !strings.Contains(s, "relay=127.0.0.1:") || !strings.Contains(s, "client=198.51.100.9:12345") || !strings.Contains(s, "authentication pending") {
			w.invalid = true
		}
	}
	if strings.Contains(s, "fixture-password-") || strings.Contains(s, "test-password") {
		w.invalid = true
	}
	return len(p), nil
}
func (w *dualLogWriter) state() (int, bool) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.pairs, w.invalid
}
func testDualLogger(t testing.TB, w *dualLogWriter) log.ContextLogger {
	t.Helper()
	f, e := log.New(log.Options{Context: context.Background(), Options: option.LogOptions{Level: "info", DisableColor: true}, DefaultWriter: w})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { f.Close() })
	return f.Logger()
}
func TestAnyTLSDualAddressLog(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		t.Run(fmt.Sprint(enabled), func(t *testing.T) {
			w := new(dualLogWriter)
			f := newReviewFixtureUsers(t, enabled, []option.AnyTLSUser{{Name: "test-user", Password: "test-password"}}, testDualLogger(t, w))
			cl := f.client(t)
			defer cl.Close()
			for i := 0; i < 3; i++ {
				s, e := cl.CreateProxy(context.Background(), M.ParseSocksaddr("echo.invalid:80"))
				if e != nil {
					t.Fatal(e)
				}
				reviewEcho(t, s, []byte("ping"))
				s.Close()
				<-f.router.done
				<-f.router.sources
			}
			n, bad := w.state()
			want := 0
			if enabled {
				want = 1
			}
			if n != want || bad {
				t.Fatalf("pairs=%d want=%d invalid=%v", n, want, bad)
			}
		})
	}
}

func soakExchange(s net.Conn, p []byte) error {
	if e := s.SetDeadline(time.Now().Add(5 * time.Second)); e != nil {
		return e
	}
	if _, e := s.Write(p); e != nil {
		return e
	}
	b := make([]byte, len(p))
	if _, e := io.ReadFull(s, b); e != nil {
		return e
	}
	if !bytes.Equal(p, b) {
		return fmt.Errorf("echo corruption")
	}
	return nil
}

// Opt-in bounded soak of the actual AnyTLS TLS/auth/router/counter chain.
// No panel users, API keys, or external traffic; eight independent credentials.
func TestAnyTLSMultiUserSoak(t *testing.T) {
	durationText := os.Getenv("ANYTLS_SOAK_DURATION")
	if durationText == "" {
		t.Skip("set ANYTLS_SOAK_DURATION to enable")
	}
	duration, e := time.ParseDuration(durationText)
	if e != nil || duration < time.Second || duration > time.Hour {
		t.Fatal("invalid soak duration")
	}
	enabled := os.Getenv("ANYTLS_SOAK_DISABLED") != "1"
	const userCount = 8
	users := make([]option.AnyTLSUser, userCount)
	for i := range users {
		users[i] = option.AnyTLSUser{Name: fmt.Sprintf("fixture-user-%d", i), Password: fmt.Sprintf("fixture-password-%d", i), AuditID: strconv.Itoa(i + 123)}
	}
	w := new(dualLogWriter)
	audit := os.Getenv("ANYTLS_SOAK_AUDIT") == "1"
	f := newReviewFixtureAudit(t, enabled, users, testDualLogger(t, w), audit)
	stopDrain := make(chan struct{})
	var badSource atomic.Bool
	go func() {
		for {
			select {
			case <-stopDrain:
				return
			case source := <-f.router.sources:
				want := "127.0.0.1"
				if enabled {
					want = "198.51.100.9"
				}
				if source != want {
					badSource.Store(true)
				}
			case <-f.router.done:
			}
		}
	}()
	defer close(stopDrain)
	var totals [userCount]atomic.Int64
	var streams atomic.Int64
	end := time.Now().Add(duration)
	var wg sync.WaitGroup
	initialG := runtime.NumGoroutine()
	for u := 0; u < userCount; u++ {
		wg.Add(1)
		go func(u int) {
			defer wg.Done()
			cl := f.clientPassword(t, users[u].Password)
			defer func() { cl.Close() }()
			round := 0
			for time.Now().Before(end) {
				// Four concurrent streams per authenticated user, plus periodic reconnect.
				var batch sync.WaitGroup
				for j := 0; j < 4; j++ {
					batch.Add(1)
					go func(j int) {
						defer batch.Done()
						s, e := cl.CreateProxy(context.Background(), M.ParseSocksaddr("echo.invalid:80"))
						if e != nil {
							t.Error(e)
							return
						}
						defer s.Close()
						p := bytes.Repeat([]byte{byte(u + 1), byte(j + 1)}, 8192)
						for k := 0; k < 4; k++ {
							if e := soakExchange(s, p); e != nil {
								t.Error(e)
								return
							}
							totals[u].Add(int64(len(p)))
						}
						streams.Add(1)
					}(j)
				}
				batch.Wait()
				round++
				if round%20 == 0 {
					cl.Close()
					cl = f.clientPassword(t, users[u].Password)
				}
				time.Sleep(200 * time.Millisecond)
			}
		}(u)
	}
	finished := make(chan struct{})
	go func() { wg.Wait(); close(finished) }()
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	var m runtime.MemStats
loop:
	for {
		select {
		case <-finished:
			break loop
		case <-ticker.C:
			runtime.ReadMemStats(&m)
			t.Logf("progress streams=%d heap=%d goroutines=%d", streams.Load(), m.HeapAlloc, runtime.NumGoroutine())
		}
	}
	time.Sleep(time.Second)
	v, _ := f.router.hook.counter.Load("review")
	c := v.(*counter.TrafficCounter)
	for i, u := range users {
		want := totals[i].Load()
		if want == 0 || c.GetUpCount(u.Name) != want || c.GetDownCount(u.Name) != want {
			t.Errorf("user %d up=%d down=%d want=%d", i, c.GetUpCount(u.Name), c.GetDownCount(u.Name), want)
		}
	}
	if c.Len() != userCount || badSource.Load() {
		t.Errorf("users=%d bad source=%v", c.Len(), badSource.Load())
	}
	n, bad := w.state()
	if bad || (enabled && n < userCount) || (!enabled && n != 0) {
		t.Errorf("dual log pairs=%d invalid=%v", n, bad)
	}
	if audit {
		w.mu.Lock()
		count := w.auditEvents
		w.mu.Unlock()
		if int64(count) != streams.Load() {
			t.Errorf("audit events=%d completed streams=%d", count, streams.Load())
		}
		t.Logf("audit access events=%d", count)
	}
	runtime.GC()
	runtime.ReadMemStats(&m)
	t.Logf("complete duration=%s enabled=%v users=%d streams=%d address_pairs=%d heap_after_gc=%d goroutines_start=%d end=%d", duration, enabled, userCount, streams.Load(), n, m.HeapAlloc, initialG, runtime.NumGoroutine())
}

func TestAnyTLSLongLivedStream(t *testing.T) {
	text := os.Getenv("ANYTLS_LONG_STREAM_DURATION")
	if text == "" {
		t.Skip("set ANYTLS_LONG_STREAM_DURATION to enable")
	}
	d, e := time.ParseDuration(text)
	if e != nil || d < time.Second || d > time.Hour {
		t.Fatal("invalid duration")
	}
	w := new(dualLogWriter)
	f := newReviewFixtureUsers(t, true, []option.AnyTLSUser{{Name: "test-user", Password: "test-password"}}, testDualLogger(t, w))
	cl := f.client(t)
	defer cl.Close()
	s, e := cl.CreateProxy(context.Background(), M.ParseSocksaddr("echo.invalid:80"))
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	p := bytes.Repeat([]byte("long-stream"), 2048)
	var total int64
	end := time.Now().Add(d)
	for time.Now().Before(end) {
		if e := soakExchange(s, p); e != nil {
			t.Fatal(e)
		}
		total += int64(len(p))
		time.Sleep(100 * time.Millisecond)
	}
	s.Close()
	select {
	case <-f.router.done:
	case <-time.After(5 * time.Second):
		t.Fatal("stream not closed")
	}
	if source := <-f.router.sources; source != "198.51.100.9" {
		t.Fatal("source mismatch")
	}
	v, _ := f.router.hook.counter.Load("review")
	c := v.(*counter.TrafficCounter)
	if c.GetUpCount("test-user") != total || c.GetDownCount("test-user") != total {
		t.Fatal("long stream counter mismatch")
	}
	n, bad := w.state()
	if n != 1 || bad {
		t.Fatalf("address pairs=%d invalid=%v", n, bad)
	}
	t.Logf("one physical connection, one stream, duration=%s bytes_up=%d bytes_down=%d address_pairs=%d", d, total, total, n)
}
