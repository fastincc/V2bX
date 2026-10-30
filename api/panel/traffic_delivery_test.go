package panel

import (
	"context"
	"fmt"
	"github.com/InazumaV/V2bX/conf"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestTrafficDeliverySingleAttemptAndCancellation(t *testing.T) {
	var requests atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		conn, _, _ := w.(http.Hijacker).Hijack()
		conn.Close()
	}))
	defer srv.Close()
	c, _ := New(&conf.ApiConfig{APIHost: srv.URL, NodeType: "anytls", Key: "synthetic", NodeID: 1})
	if err := c.ReportTrafficBatch("batch", []UserTraffic{{UID: 1, Upload: 1}}); err == nil {
		t.Fatal("missing ack accepted")
	}
	if requests.Load() != 1 {
		t.Fatalf("hidden retries: %d", requests.Load())
	}
	if c.client.RetryCount != 3 || c.trafficClient.RetryCount != 0 {
		t.Fatal("other endpoint retry policy changed")
	}
	entered := make(chan struct{})
	slow := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		io.Copy(io.Discard, r.Body)
		close(entered)
		<-r.Context().Done()
	}))
	defer slow.Close()
	c, _ = New(&conf.ApiConfig{APIHost: slow.URL, NodeType: "anytls", Key: "synthetic", NodeID: 1, Timeout: 30})
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- c.ReportTrafficBatchContext(ctx, "batch", []UserTraffic{{UID: 1, Upload: 1}}) }()
	<-entered
	cancel()
	select {
	case err := <-done:
		if err == nil {
			t.Fatal("cancel acknowledged")
		}
	case <-time.After(time.Second):
		t.Fatal("HTTP not canceled")
	}
}
func TestTrafficDeliveryRetryAfterAndAck(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Retry-After", "2")
		w.WriteHeader(429)
		fmt.Fprint(w, `{"data":true,"batch_id":"wrong"}`)
	}))
	defer srv.Close()
	c, _ := New(&conf.ApiConfig{APIHost: srv.URL, NodeType: "anytls", NodeID: 1})
	err := c.ReportTrafficBatch("batch", []UserTraffic{{UID: 1, Upload: 1}})
	if err == nil || TrafficRetryAfter(err) != 2*time.Second {
		t.Fatal("rate limit ignored")
	}
	now := time.Now().Truncate(time.Second)
	if retryAfter(now.Add(3*time.Second).UTC().Format(http.TimeFormat), now) != 3*time.Second {
		t.Fatal("HTTP-date ignored")
	}
	for _, value := range []string{"-1", "invalid", "9223372036854775807"} {
		if retryAfter(value, now) != 0 {
			t.Fatal("invalid delay")
		}
	}
}
