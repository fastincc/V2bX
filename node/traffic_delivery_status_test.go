package node

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/trafficbatch"
	log "github.com/sirupsen/logrus"
	logtest "github.com/sirupsen/logrus/hooks/test"
)

func TestDeliveryExplicitStates(t *testing.T) {
	for _, mode := range []string{"empty", "paused", "backoff", "canceled", "failed", "wrong_ack", "ack_storage", "confirmed", "cancel_inflight"} {
		t.Run(mode, func(t *testing.T) {
			var calls atomic.Int32
			var spool *trafficbatch.Store
			var sender *trafficDelivery
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				calls.Add(1)
				io.Copy(io.Discard, r.Body)
				switch mode {
				case "failed":
					w.WriteHeader(503)
				case "wrong_ack":
					fmt.Fprint(w, `{"data":true,"batch_id":"wrong"}`)
				case "ack_storage":
					spool.Close()
					fmt.Fprintf(w, `{"data":true,"batch_id":%q}`, r.Header.Get("X-Traffic-Batch-ID"))
				case "cancel_inflight":
					sender.cancel()
					<-r.Context().Done()
				default:
					fmt.Fprintf(w, `{"data":true,"batch_id":%q}`, r.Header.Get("X-Traffic-Batch-ID"))
				}
			}))
			defer srv.Close()
			d, s := deliveryFixture(t, srv.URL, 1)
			spool = s
			sender = d
			defer func() { d.cancel(); go d.run() }()
			if mode != "empty" {
				enqueueDelivery(t, s, panel.UserTraffic{UID: 7, Upload: 9})
			}
			want := deliveryConfirmed
			wantCalls := int32(1)
			wantPending := 1
			switch mode {
			case "empty":
				want = deliveryIdle
				wantCalls = 0
				wantPending = 0
			case "paused":
				d.paused = true
				want = deliveryPaused
				wantCalls = 0
			case "backoff":
				d.retryAt = time.Now().Add(time.Hour)
				want = deliveryDeferred
				wantCalls = 0
			case "canceled":
				d.cancel()
				want = deliveryCanceled
				wantCalls = 0
			case "cancel_inflight":
				want = deliveryCanceled
			case "failed", "wrong_ack":
				want = deliveryFailed
			case "ack_storage":
				want = deliveryAckPending
			case "confirmed":
				wantPending = 0
			}
			result := d.attempt(false)
			n, _ := s.Counts()
			if result.state != want || calls.Load() != wantCalls || n != wantPending {
				t.Fatalf("state=%s calls=%d pending=%d", result.state, calls.Load(), n)
			}
			if (len(result.rows) > 0) != (want == deliveryConfirmed) {
				t.Fatal("unconfirmed data exposed as successful")
			}
			if (result.err != nil) != (want == deliveryFailed || want == deliveryAckPending) {
				t.Fatal("incorrect error state")
			}
			if mode == "ack_storage" && !d.paused {
				t.Fatal("sender did not pause")
			}
		})
	}
}
func TestDeliverySkippedStateLogsOnce(t *testing.T) {
	old := log.StandardLogger().Hooks
	hook := logtest.NewGlobal()
	defer log.StandardLogger().ReplaceHooks(old)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { t.Error("unexpected HTTP") }))
	defer srv.Close()
	d, s := deliveryFixture(t, srv.URL, 1)
	defer func() { d.cancel(); go d.run() }()
	enqueueDelivery(t, s, panel.UserTraffic{UID: 1, Upload: 1})
	d.paused = true
	for i := 0; i < 100; i++ {
		d.attempt(false)
	}
	d.paused = false
	d.retryAt = time.Now().Add(time.Hour)
	for i := 0; i < 100; i++ {
		d.attempt(false)
	}
	states := map[deliveryState]int{}
	for _, entry := range hook.AllEntries() {
		if state, ok := entry.Data["delivery_state"].(deliveryState); ok {
			states[state]++
		}
	}
	if states[deliveryPaused] != 1 || states[deliveryDeferred] != 1 {
		t.Fatalf("repeated logs: %v", states)
	}
}
