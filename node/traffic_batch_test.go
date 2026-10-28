package node

import (
	"encoding/json"
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/conf"
	vcore "github.com/InazumaV/V2bX/core"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"
	"time"
)

type batchCore struct {
	vcore.Core
	counter *counter.TrafficCounter
}

func (c *batchCore) PersistTrafficSnapshot(_ string, persist func([]panel.UserTraffic) error) error {
	return counter.PersistSnapshot(c.counter, func(string) int { return 1 }, func(string) int64 { return 0 }, persist)
}
func TestBatchOutageLostAckAndRestart(t *testing.T) {
	mode := "offline"
	charged := int64(0)
	requests := 0
	seen := map[string]string{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		id := r.Header.Get("X-Traffic-Batch-ID")
		if len(id) != 32 {
			t.Error("batch id missing")
		}
		if mode == "offline" {
			w.WriteHeader(503)
			return
		}
		var data map[int][]int64
		if e := json.NewDecoder(r.Body).Decode(&data); e != nil {
			t.Error(e)
		}
		payload, _ := json.Marshal(data)
		if old, ok := seen[id]; ok {
			if old != string(payload) {
				t.Error("changed replay payload")
			}
		} else {
			seen[id] = string(payload)
			charged += data[1][0] + data[1][1]
		}
		if mode == "dropack" {
			w.WriteHeader(503)
			return
		}
		if mode == "legacy" {
			fmt.Fprint(w, `{"data":true}`)
			return
		}
		fmt.Fprintf(w, `{"data":true,"batch_id":%q}`, id)
	}))
	defer srv.Close()
	client, e := panel.New(&conf.ApiConfig{APIHost: srv.URL, Key: "synthetic", NodeType: "anytls", NodeID: 1})
	if e != nil {
		t.Fatal(e)
	}
	source := &batchCore{counter: counter.NewTrafficCounter()}
	source.counter.Tx("user", 123)
	source.counter.Rx("user", 456)
	c := &Controller{server: source, apiClient: client, Options: &conf.Options{TrafficBatchMode: true, TrafficSpoolDirectory: filepath.Join(t.TempDir(), "spool")}}
	if e = c.openTrafficSpool(); e != nil {
		t.Fatal(e)
	}
	if _, e = c.collectAndReportTraffic(); e == nil {
		t.Fatal("offline accepted")
	}
	id := c.trafficSpool.Pending().ID
	if source.counter.GetUpCount("user") != 0 {
		t.Fatal("persisted bytes not subtracted")
	}
	mode = "dropack"
	for i := 0; i < 3; i++ {
		if _, e = c.collectAndReportTraffic(); e == nil {
			t.Fatal("lost ack accepted")
		}
	}
	if charged != 579 {
		t.Fatalf("duplicate billing: %d", charged)
	}
	c.trafficSpool.Close()
	c.trafficSpool = nil
	if e = c.openTrafficSpool(); e != nil {
		t.Fatal(e)
	}
	defer c.trafficSpool.Close()
	if c.trafficSpool.Pending().ID != id {
		t.Fatal("restart changed id")
	}
	mode = "legacy"
	if _, e = c.collectAndReportTraffic(); e == nil {
		t.Fatal("legacy ack accepted")
	}
	mode = "normal"
	if _, e = c.collectAndReportTraffic(); e != nil {
		t.Fatal(e)
	}
	if c.trafficSpool.Pending() != nil || charged != 579 || requests < 6 {
		t.Fatal("recovery failed")
	}
}

func TestCheckpointWhileAcknowledgementBlocked(t *testing.T) {
	entered := make(chan struct{})
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { close(entered); <-release; w.WriteHeader(503) }))
	defer srv.Close()
	client, _ := panel.New(&conf.ApiConfig{APIHost: srv.URL, Key: "synthetic", NodeType: "anytls", NodeID: 1, Timeout: 10})
	source := &batchCore{counter: counter.NewTrafficCounter()}
	source.counter.Tx("user", 100)
	c := &Controller{server: source, apiClient: client, info: &panel.NodeInfo{PushInterval: time.Hour}, lastTrafficSeal: time.Now(), Options: &conf.Options{TrafficBatchMode: true, TrafficSpoolDirectory: filepath.Join(t.TempDir(), "spool")}}
	if e := c.openTrafficSpool(); e != nil {
		t.Fatal(e)
	}
	defer c.trafficSpool.Close()
	done := make(chan struct{})
	go func() { defer close(done); c.collectAndReportTraffic() }()
	<-entered
	source.counter.Tx("user", 71)
	if e := c.checkpointTrafficTask(); e != nil {
		t.Fatal(e)
	}
	if source.counter.GetUpCount("user") != 0 {
		t.Fatal("network ack blocked checkpoint")
	}
	n, active := c.trafficSpool.Counts()
	if n != 1 || !active {
		t.Fatal("new traffic not journaled")
	}
	close(release)
	<-done
	if e := c.trafficSpool.Seal(); e != nil {
		t.Fatal(e)
	}
	c.trafficSpool.Acknowledge(c.trafficSpool.Pending().ID)
	if c.trafficSpool.Pending().Traffic[0].Upload != 71 {
		t.Fatal("wrong journal increment")
	}
}
