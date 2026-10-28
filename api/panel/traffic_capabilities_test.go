package panel

import (
	"github.com/InazumaV/V2bX/conf"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestTrafficCapabilityGate(t *testing.T) {
	for _, tc := range []struct {
		name, body string
		status     int
		ok         bool
	}{
		{"supported", `{"data":{"version":1,"enabled":true,"durable_ack":true,"deduplication":true,"receipt_retention_seconds":0}}`, 200, true},
		{"legacy", `{"data":true}`, 200, false}, {"absent", `{}`, 404, false},
		{"disabled", `{"data":{"version":1,"enabled":false,"durable_ack":true,"deduplication":true,"receipt_retention_seconds":0}}`, 200, false},
		{"expiring", `{"data":{"version":1,"enabled":true,"durable_ack":true,"deduplication":true,"receipt_retention_seconds":86400}}`, 200, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Query().Get("token") != "synthetic" || r.URL.Query().Get("node_id") != "1" {
					t.Error("auth changed")
				}
				w.WriteHeader(tc.status)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()
			c, e := New(&conf.ApiConfig{APIHost: srv.URL, Key: "synthetic", NodeType: "anytls", NodeID: 1})
			if e != nil {
				t.Fatal(e)
			}
			if (c.CheckTrafficCapabilities() == nil) != tc.ok {
				t.Fatal("capability gate mismatch")
			}
		})
	}
}
