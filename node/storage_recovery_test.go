package node

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/common/trafficbatch"
)

func TestStorageRecoveryLocalAckWithoutSecondPost(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	var calls atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		fmt.Fprintf(w, `{"data":true,"batch_id":%q}`, r.Header.Get("X-Traffic-Batch-ID"))
	}))
	defer srv.Close()
	d, s := deliveryFixture(t, srv.URL, 27, dir)
	defer close(d.done) // attempts are driven synchronously here
	row := panel.UserTraffic{UID: 9, Upload: 100, Download: 202}
	enqueueDelivery(t, s, row)
	// Make unlink temporarily fail after the panel replies. All files and
	// the fake obstruction live inside this test's private directory.
	matches, err := filepath.Glob(filepath.Join(dir, "batch-*.json"))
	if err != nil || len(matches) != 1 {
		t.Fatal("missing local head", err)
	}
	headPath := matches[0]
	hidden := headPath + ".hold"
	if err := os.Rename(headPath, hidden); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(headPath, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(headPath, "hold"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	first := d.attempt(false)
	if first.state != deliveryAckPending || d.paused || len(first.rows) != 0 {
		t.Fatalf("result=%+v paused=%v", first, d.paused)
	}
	if second := d.attempt(false); second.state != deliveryDeferred {
		t.Fatalf("no backoff: %s", second.state)
	}
	if err := os.Remove(filepath.Join(headPath, "hold")); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(headPath); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(hidden, headPath); err != nil {
		t.Fatal(err)
	}
	d.retryAt = time.Time{}
	last := d.attempt(false)
	if last.err != nil || last.state != deliveryConfirmed || len(last.rows) != 1 || last.rows[0] != row || s.Pending() != nil || calls.Load() != 1 {
		t.Fatalf("state=%s calls=%d err=%v", last.state, calls.Load(), last.err)
	}
}

func TestStorageRecoveryScheduledBackoffAndFinalFlush(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "spool")
	spool, err := trafficbatch.Open(dir, "synthetic")
	if err != nil {
		t.Fatal(err)
	}
	defer spool.Close()
	counts := counter.NewTrafficCounter()
	c := &Controller{server: &batchCore{counter: counts}, trafficSpool: spool}
	counts.Tx("test", 11)
	path := filepath.Join(dir, "active.json")
	if err := os.Mkdir(path, 0700); err != nil {
		t.Fatal(err)
	}
	for _, want := range []time.Duration{2 * time.Second, 4 * time.Second, 8 * time.Second, 8 * time.Second} {
		c.checkpointRetry.next = time.Time{} // advance this test's scheduler
		if err := c.scheduledCheckpoint(time.Hour); err == nil || c.checkpointRetry.delay != want {
			t.Fatalf("delay=%s err=%v", c.checkpointRetry.delay, err)
		}
		if err := c.scheduledCheckpoint(time.Hour); err != nil {
			t.Fatal("scheduled retry did not defer")
		}
	}
	counts.Rx("test", 19)
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	// An immediate lifecycle checkpoint bypasses regular backoff and catches
	// both original counters and subsequent writes in the same final flush.
	if err := c.captureTraffic(); err != nil {
		t.Fatal(err)
	}
	if err := spool.Seal(); err != nil {
		t.Fatal(err)
	}
	want := panel.UserTraffic{UID: 1, Upload: 11, Download: 19}
	if got := spool.Pending(); got == nil || len(got.Traffic) != 1 || got.Traffic[0] != want {
		t.Fatalf("final flush: %#v", got)
	}
	c.checkpointRetry.next = time.Now().Add(-time.Second)
	if err := c.scheduledCheckpoint(time.Hour); err != nil || c.checkpointRetry.delay != 0 || !c.checkpointRetry.next.IsZero() {
		t.Fatal("backoff did not reset", err)
	}
}
