//go:build linux || darwin

package anytls

import (
	"encoding/json"
	"fmt"
	"github.com/anytls/sing-anytls/padding"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"net"
	"os"
	"runtime"
	"syscall"
	"testing"
	"time"
)

type auditSessionConn struct {
	net.Conn
	index int
}

func TestAnyTLSAddSessionScanProfile(t *testing.T) {
	if os.Getenv("ANYTLS_TABLE_PROFILE") == "" {
		t.Skip("set ANYTLS_TABLE_PROFILE")
	}
	for _, n := range []int{1000, 5000} {
		for _, m := range []int{0, 1000, 10000} {
			t.Run(fmt.Sprintf("add-%d_sessions-%d", n, m), func(t *testing.T) {
				h := &Inbound{users: map[string]option.AnyTLSUser{}, epochs: map[string]uint64{}, sessions: map[net.Conn]*sessionState{}, paddingScheme: padding.DefaultPaddingScheme, logger: log.NewNOPFactory().Logger()}
				for i := 0; i < m; i++ {
					h.sessions[&auditSessionConn{index: i}] = &sessionState{user: "existing-unaffected"}
				}
				users := make([]option.AnyTLSUser, n)
				for i := range users {
					users[i] = option.AnyTLSUser{Name: fmt.Sprintf("new-%d", i), Password: fmt.Sprintf("synthetic-%d", i)}
				}
				var before, after runtime.MemStats
				var cpuBefore, cpuAfter syscall.Rusage
				runtime.ReadMemStats(&before)
				syscall.Getrusage(syscall.RUSAGE_SELF, &cpuBefore)
				start := time.Now()
				if e := h.AddUsers(users); e != nil {
					t.Fatal(e)
				}
				elapsed := time.Since(start)
				syscall.Getrusage(syscall.RUSAGE_SELF, &cpuAfter)
				runtime.ReadMemStats(&after)
				if len(h.users) != n || len(h.sessions) != m {
					t.Fatal("unexpected changes")
				}
				cpu := func(r syscall.Rusage) float64 {
					return float64(r.Utime.Sec+r.Stime.Sec) + float64(r.Utime.Usec+r.Stime.Usec)/1e6
				}
				rss := cpuAfter.Maxrss
				if runtime.GOOS == "linux" {
					rss *= 1024
				}
				data, _ := json.Marshal(map[string]any{"new_users": n, "sessions": m, "elapsed_ms": float64(elapsed.Microseconds()) / 1000, "cpu_seconds": cpu(cpuAfter) - cpu(cpuBefore), "allocated_bytes": after.TotalAlloc - before.TotalAlloc, "max_rss_bytes": rss})
				t.Log("ANYTLS_TABLE_PROFILE " + string(data))
			})
		}
	}
}
