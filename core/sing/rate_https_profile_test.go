//go:build linux || darwin

package sing

import (
	"context"
	"crypto/tls"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

type httpsRateMeter struct {
	net.Conn
	up, down *atomic.Int64
}

func (c *httpsRateMeter) Read(p []byte) (int, error) {
	n, e := c.Conn.Read(p)
	c.up.Add(int64(n))
	return n, e
}
func (c *httpsRateMeter) Write(p []byte) (int, error) {
	n, e := c.Conn.Write(p)
	c.down.Add(int64(n))
	return n, e
}

type httpsRateRouter struct {
	adapter.Router
	hook           *HookServer
	target         string
	up, down       atomic.Int64
	mu             sync.Mutex
	conns          []net.Conn
	wg             sync.WaitGroup
	sourceMismatch atomic.Bool
}

func (r *httpsRateRouter) RouteConnectionEx(ctx context.Context, c net.Conn, m adapter.InboundContext, closed N.CloseHandlerFunc) {
	if m.Source.Addr.String() != "198.51.100.9" {
		r.sourceMismatch.Store(true)
	}
	c = &httpsRateMeter{Conn: c, up: &r.up, down: &r.down}
	c = r.hook.RoutedConnection(ctx, c, m, nil, nil)
	r.mu.Lock()
	r.conns = append(r.conns, c)
	r.mu.Unlock()
	r.wg.Add(1)
	go func() {
		defer r.wg.Done()
		defer c.Close()
		if closed != nil {
			defer closed(nil)
		}
		remote, e := net.DialTimeout("tcp", r.target, time.Second)
		if e != nil {
			return
		}
		defer remote.Close()
		finished := make(chan struct{})
		go func() { io.Copy(remote, c); remote.Close(); c.Close(); close(finished) }()
		io.Copy(c, remote)
		remote.Close()
		c.Close()
		<-finished
	}()
}
func (r *httpsRateRouter) shutdown() {
	r.mu.Lock()
	for _, c := range r.conns {
		c.Close()
	}
	r.mu.Unlock()
	r.wg.Wait()
}

// Bounded, opt-in local TLS/AnyTLS/PROXY-v2/real HTTPS profile. Both variants
// execute the same fixture; no Internet requests or deployment are involved.
func TestRateHTTPSProfile(t *testing.T) {
	text := os.Getenv("RATE_HTTPS_DURATION")
	if text == "" {
		t.Skip("set RATE_HTTPS_DURATION")
	}
	duration, e := time.ParseDuration(text)
	if e != nil || duration < time.Second || duration > time.Minute {
		t.Fatal("duration")
	}
	mbps, _ := strconv.Atoi(os.Getenv("RATE_HTTPS_MBPS"))
	if mbps < 1 || mbps > 1000 {
		t.Fatal("rate")
	}
	load, _ := strconv.Atoi(os.Getenv("RATE_HTTPS_LOAD"))
	if load < 0 || load > 8 {
		t.Fatal("load")
	}
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/generate_204" {
			w.WriteHeader(204)
			return
		}
		w.Header().Set("Content-Length", "1048576")
		block := make([]byte, 4096)
		for i := 0; i < 256; i++ {
			if _, e := w.Write(block); e != nil {
				return
			}
		}
	}))
	defer target.Close()
	capture := new(auditCapture)
	f := newReviewFixtureAudit(t, true, []option.AnyTLSUser{{Name: "rate-user", Password: "synthetic-rate", AuditID: "123"}}, auditLogger(t, capture), true)
	limiter.DeleteLimiter("review")
	limiter.AddLimiter("review", &conf.LimitConfig{}, []panel.UserInfo{{Id: 123, Uuid: "rate-user", SpeedLimit: mbps, DeviceLimit: 1}}, map[int]int{})
	users := &UserMap{uidMap: map[userKey]int{{"review", "rate-user"}: 123}, active: map[userKey]string{{"review", "rate-user"}: "rate-user"}}
	f.router.hook.users = users
	router := &httpsRateRouter{hook: &f.router.hook, target: strings.TrimPrefix(target.URL, "https://")}
	f.router.forward = router
	client := f.clientPassword(t, "synthetic-rate")
	defer client.Close()
	// Trust only the local httptest certificate; the outer AnyTLS transport also
	// verifies its generated fixture certificate.
	transport := &http.Transport{TLSClientConfig: &tls.Config{RootCAs: target.Client().Transport.(*http.Transport).TLSClientConfig.RootCAs}, DialContext: func(ctx context.Context, _, addr string) (net.Conn, error) {
		return client.CreateProxy(ctx, M.ParseSocksaddr(addr))
	}, MaxIdleConnsPerHost: 16}
	httpClient := &http.Client{Transport: transport, Timeout: 8 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), duration)
	defer cancel()
	var before, after syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &before)
	started := time.Now()
	var bulk atomic.Int64
	var wg sync.WaitGroup
	for i := 0; i < load; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for ctx.Err() == nil {
				req, _ := http.NewRequestWithContext(ctx, "GET", target.URL+"/bulk", nil)
				resp, e := httpClient.Do(req)
				if e != nil {
					if ctx.Err() == nil {
						t.Error(e)
					}
					return
				}
				n, e := io.Copy(io.Discard, resp.Body)
				bulk.Add(n)
				resp.Body.Close()
				if e != nil && ctx.Err() == nil {
					t.Error(e)
					return
				}
			}
		}()
	}
	latencies := []float64{}
	for ctx.Err() == nil {
		req, _ := http.NewRequestWithContext(ctx, "GET", target.URL+"/generate_204", nil)
		start := time.Now()
		resp, e := httpClient.Do(req)
		if e != nil {
			if ctx.Err() == nil {
				t.Error(e)
			}
			break
		}
		resp.Body.Close()
		if resp.StatusCode != 204 {
			t.Fatal(resp.StatusCode)
		}
		latencies = append(latencies, float64(time.Since(start).Microseconds())/1000)
		select {
		case <-ctx.Done():
		case <-time.After(25 * time.Millisecond):
		}
	}
	wg.Wait()
	transport.CloseIdleConnections()
	client.Close()
	router.shutdown()
	elapsed := time.Since(started)
	syscall.Getrusage(syscall.RUSAGE_SELF, &after)
	if len(latencies) == 0 {
		t.Fatal("no successful HTTPS204")
	}
	l, _ := limiter.GetLimiter("review")
	online, _ := l.GetOnlineDevice()
	for _, row := range *online {
		if row.IP != "198.51.100.9" {
			t.Fatal("device IP mismatch")
		}
	}
	// Active device admission remains enforced during rate-limited traffic.
	if _, reject := l.CheckLimit("review|rate-user", "198.51.100.10", true, true); !reject {
		t.Fatal("second device accepted")
	}
	if router.sourceMismatch.Load() {
		t.Fatal("real IP mismatch")
	}
	core := &Sing{users: users, hookServer: &f.router.hook}
	var rows []panel.UserTraffic
	if e := core.PersistTrafficSnapshot("review", func(v []panel.UserTraffic) error { rows = v; return nil }); e != nil {
		t.Fatal(e)
	}
	if len(rows) != 1 || rows[0].UID != 123 || rows[0].Upload != router.up.Load() || rows[0].Download != router.down.Load() {
		t.Fatalf("accounting mismatch rows=%v wire=%d/%d", rows, router.up.Load(), router.down.Load())
	}
	if len(capture.events(t)) == 0 {
		t.Fatal("audit missing")
	}
	sort.Float64s(latencies)
	cpu := func(r syscall.Rusage) float64 {
		return float64(r.Utime.Sec+r.Stime.Sec) + float64(r.Utime.Usec+r.Stime.Usec)/1e6
	}
	rss := after.Maxrss
	if runtime.GOOS == "linux" {
		rss *= 1024
	}
	data, _ := json.Marshal(map[string]any{"mbps": mbps, "load": load, "elapsed_seconds": elapsed.Seconds(), "cpu_seconds": cpu(after) - cpu(before), "max_rss_bytes": rss, "https_samples": len(latencies), "https_p50_ms": latencies[len(latencies)/2], "https_p95_ms": latencies[min(len(latencies)-1, len(latencies)*95/100)], "bulk_bytes": bulk.Load(), "upload": router.up.Load(), "download": router.down.Load(), "wire_mbps": float64(router.up.Load()+router.down.Load()) * 8 / 1e6 / elapsed.Seconds(), "accounting_exact": true})
	t.Log("RATE_HTTPS_PROFILE " + string(data))
}
