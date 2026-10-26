package node

import (
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
)

func TestDeviceSnapshotFailureDoesNotConsumeConfiguration(t *testing.T) {
	var other atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v1/server/UniProxy/alivelist" {
			other.Add(1)
		}
		w.WriteHeader(503)
	}))
	defer srv.Close()
	api, err := panel.New(&conf.ApiConfig{APIHost: srv.URL, NodeType: "anytls", NodeID: 1, Key: "test-only"})
	if err != nil {
		t.Fatal(err)
	}
	c := &Controller{apiClient: api}
	if err := c.nodeInfoMonitor(); err != nil {
		t.Fatal(err)
	}
	if other.Load() != 0 {
		t.Fatal("configuration/user ETags consumed during snapshot failure")
	}
}
