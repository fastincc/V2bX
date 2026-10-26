//go:build sing

package sing

import (
	"bytes"
	"context"
	"fmt"
	M "github.com/sagernet/sing/common/metadata"
	"os"
	"runtime"
	"runtime/pprof"
	"testing"
	"time"
)

func TestAnyTLSDeviceReviewClosedFixtureCleanup(t *testing.T) {
	if os.Getenv("ANYTLS_CLEANUP_REVIEW") != "1" {
		t.Skip("enable bounded client cleanup review")
	}
	baseline := runtime.NumGoroutine()
	for round := 0; round < 5; round++ {
		t.Run(fmt.Sprint(round), func(t *testing.T) {
			f := newReviewFixture(t, true)
			c := f.client(t)
			defer c.Close()
			for i := 0; i < 16; i++ {
				s, e := c.CreateProxy(context.Background(), M.ParseSocksaddr("echo.invalid:80"))
				if e != nil {
					t.Fatal(e)
				}
				reviewEcho(t, s, bytes.Repeat([]byte("cleanup"), 1024))
				s.Close()
				select {
				case <-f.router.done:
				case <-time.After(3 * time.Second):
					t.Fatal("stream did not finish")
				}
				<-f.router.sources
			}
		})
		time.Sleep(200 * time.Millisecond)
		t.Logf("round=%d goroutines=%d", round, runtime.NumGoroutine())
	}
	// The fixture client checks cancellation on its one-minute idle sweep.
	// Wait through that documented interval rather than labeling sleeping client
	// sweepers as leaked inbound goroutines.
	deadline := time.Now().Add(65 * time.Second)
	for runtime.NumGoroutine() > baseline && time.Now().Before(deadline) {
		runtime.GC()
		time.Sleep(200 * time.Millisecond)
	}
	after := runtime.NumGoroutine()
	t.Logf("baseline=%d settled=%d", baseline, after)
	if after > baseline {
		var dump bytes.Buffer
		pprof.Lookup("goroutine").WriteTo(&dump, 1)
		t.Log(dump.String())
		t.Fatal("closed fixtures left unexpected goroutines")
	}
}
