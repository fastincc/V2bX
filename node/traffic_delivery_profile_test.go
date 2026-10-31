//go:build linux || darwin

package node

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"runtime"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
)

func TestDeliveryThroughputProfile(t *testing.T) {
	if os.Getenv("DELIVERY_PROFILE") != "1" {
		t.Skip("set DELIVERY_PROFILE=1")
	}
	var mu sync.Mutex
	ids := map[string]bool{}
	var up, down int64
	var calls int
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var rows map[string][]int64
		if e := json.NewDecoder(r.Body).Decode(&rows); e != nil {
			t.Error(e)
		}
		id := r.Header.Get("X-Traffic-Batch-ID")
		mu.Lock()
		calls++
		if !ids[id] {
			ids[id] = true
			for _, v := range rows {
				up += v[0]
				down += v[1]
			}
		}
		mu.Unlock()
		fmt.Fprintf(w, `{"data":true,"batch_id":%q}`, id)
	}))
	defer srv.Close()
	d, s := deliveryFixture(t, srv.URL, 1)
	fastDelivery(d)
	const batches = 128
	for i := 0; i < batches; i++ {
		enqueueDelivery(t, s, panel.UserTraffic{UID: 7, Upload: 1024, Download: 2048})
	}
	var before, after syscall.Rusage
	syscall.Getrusage(syscall.RUSAGE_SELF, &before)
	start := time.Now()
	go d.run()
	awaitDelivered(t, s, 10*time.Second)
	elapsed := time.Since(start)
	syscall.Getrusage(syscall.RUSAGE_SELF, &after)
	mu.Lock()
	defer mu.Unlock()
	if calls != batches || len(ids) != batches || up != batches*1024 || down != batches*2048 {
		t.Fatal("queue reconciliation")
	}
	cpu := func(r syscall.Rusage) float64 {
		return float64(r.Utime.Sec+r.Stime.Sec) + float64(r.Utime.Usec+r.Stime.Usec)/1e6
	}
	rss := after.Maxrss
	if runtime.GOOS == "linux" {
		rss *= 1024
	}
	payload, _ := json.Marshal(map[string]any{"seconds": elapsed.Seconds(), "batches": batches, "calls": calls, "cpu_seconds": cpu(after) - cpu(before), "max_rss_bytes": rss, "accounting_exact": true, "upload": up, "download": down})
	t.Logf("DELIVERY_PROFILE %s", payload)
}
