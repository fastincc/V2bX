package panel

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/InazumaV/V2bX/common/dynamic"
	"github.com/InazumaV/V2bX/conf"
	"github.com/vmihailenco/msgpack/v5"
)

func TestDynamicPolicyWireAndETag(t *testing.T) {
	for _, format := range []string{"json", "msgpack"} {
		t.Run(format, func(t *testing.T) {
			policy := map[string]any{"version": 1, "revision": int64(3), "enabled": true, "window_seconds": 60, "threshold_bytes": int64(1000000000), "speed_mbps": 20, "duration_seconds": 300}
			stage := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(dynamic.CapabilityHeader) != "1" {
					t.Error("missing capability")
				}
				if r.Header.Get("If-None-Match") == "rule-3" && stage == 0 {
					w.WriteHeader(304)
					return
				}
				user := map[string]any{"id": 1, "uuid": "synthetic-user", "speed_limit": 100, "device_limit": 2}
				if stage == 0 {
					user["dynamic_speed_limit"] = policy
					w.Header().Set("ETag", "rule-3")
				}
				if stage == 1 {
					user["dynamic_speed_limit"] = map[string]any{"enabled": true, "speed_mbps": "invalid"}
				}
				body := map[string]any{"users": []any{user}}
				if format == "json" {
					w.Header().Set("Content-Type", "application/json")
					json.NewEncoder(w).Encode(body)
				} else {
					w.Header().Set("Content-Type", "application/x-msgpack")
					msgpack.NewEncoder(w).Encode(body)
				}
			}))
			defer s.Close()
			c, e := New(&conf.ApiConfig{APIHost: s.URL, NodeType: "anytls"})
			if e != nil {
				t.Fatal(e)
			}
			u, e := c.GetUserList()
			if e != nil || len(u) != 1 {
				t.Fatal(e, u)
			}
			p := u[0].DynamicSpeedLimit
			if p == nil || p.Validate() != nil || p.Revision != 3 || p.ThresholdBytes != 1000000000 {
				t.Fatal(p)
			}
			c.CommitUserList()
			u, e = c.GetUserList()
			if e != nil || u != nil {
				t.Fatal("304 must preserve state", e)
			}
			stage = 1
			u, e = c.GetUserList()
			if e != nil || len(u) != 1 || u[0].DynamicSpeedLimit.Validate() == nil {
				t.Fatal("invalid extension must not revoke users", e)
			}
			stage = 2
			u, e = c.GetUserList()
			if e != nil || len(u) != 1 || u[0].DynamicSpeedLimit != nil {
				t.Fatal("legacy response", e)
			}
		})
	}
}

func TestDynamicMalformedEnableDoesNotDisableRule(t *testing.T) {
	for _, value := range []map[string]any{{"version": 1, "revision": 2}, {"version": 1, "revision": 2, "enabled": nil}, {"version": 1, "revision": 2, "enabled": "false"}} {
		for _, format := range []string{"json", "msgpack"} {
			body := map[string]any{"id": 1, "uuid": "synthetic", "dynamic_speed_limit": value}
			var user UserInfo
			var e error
			if format == "json" {
				data, _ := json.Marshal(body)
				e = json.Unmarshal(data, &user)
			} else {
				data, _ := msgpack.Marshal(body)
				e = msgpack.Unmarshal(data, &user)
			}
			if e != nil || user.Id != 1 || user.DynamicSpeedLimit == nil || user.DynamicSpeedLimit.Validate() == nil {
				t.Fatalf("%s malformed extension not isolated", format)
			}
		}
	}
}
