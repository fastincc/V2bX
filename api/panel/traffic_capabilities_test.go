package panel

import (
	"github.com/InazumaV/V2bX/conf"
	"net/http"
	"net/http/httptest"
	"strings"
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

func TestTrafficAutoNegotiation(t *testing.T) {
	for _, tc := range []struct {
		name, body      string
		status          int
		available, fail bool
	}{
		{"enabled", `{"data":{"version":1,"enabled":true,"durable_ack":true,"deduplication":true,"receipt_retention_seconds":0}}`, 200, true, false},
		{"disabled", `{"data":{"version":1,"enabled":false,"durable_ack":true,"deduplication":true,"receipt_retention_seconds":0}}`, 200, false, false},
		{"missing", `{}`, 404, false, false}, {"method", `{}`, 405, false, false},
		{"unauthorized", `{}`, 401, false, true}, {"forbidden", `{}`, 403, false, true}, {"server", `{}`, 500, false, true},
		{"missing enabled", `{"data":{"version":1}}`, 200, false, true},
		{"null", `{"data":null}`, 200, false, true}, {"html", `<html>login</html>`, 200, false, true},
		{"expiring", `{"data":{"version":1,"enabled":true,"durable_ack":true,"deduplication":true,"receipt_retention_seconds":60}}`, 200, false, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(tc.status); w.Write([]byte(tc.body)) }))
			defer srv.Close()
			c, err := New(&conf.ApiConfig{APIHost: srv.URL, Key: "synthetic", NodeType: "anytls", NodeID: 1})
			if err != nil {
				t.Fatal(err)
			}
			available, err := c.TrafficBatchAvailable()
			if available != tc.available || (err != nil) != tc.fail {
				t.Fatalf("available=%v err=%v", available, err)
			}
		})
	}
}

func TestTrafficAutoTransportFailure(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	addr := srv.URL
	srv.Close()
	c, err := New(&conf.ApiConfig{APIHost: addr, Key: "synthetic-secret", NodeType: "anytls", NodeID: 1})
	if err != nil {
		t.Fatal(err)
	}
	c.client.SetRetryCount(0)
	available, err := c.TrafficBatchAvailable()
	if err == nil || available {
		t.Fatal("transport failure treated as unsupported")
	}
	if strings.Contains(err.Error(), "synthetic-secret") || strings.Contains(err.Error(), addr) {
		t.Fatal("transport error leaked request")
	}
}
