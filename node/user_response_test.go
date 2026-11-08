package node

import (
	"bytes"
	"encoding/json/v2"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/dynamic"
	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/vmihailenco/msgpack/v5"
)

// Exercise the public HTTP client through the monitor, using synthetic core
// authorization and an in-memory connection. These assertions establish state
// preservation, not actual protocol forwarding or PROXY-header extraction.
func TestUserResponseInvalidSnapshotPreservesAppliedState(t *testing.T) {
	for _, wire := range []string{"json", "msgpack"} {
		for _, bad := range userResponseInvalidBodies(t, wire) {
			for _, aliveFailure := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/alive_failure=%t", wire, bad.name, aliveFailure), func(t *testing.T) {
					userResponsePreservesState(t, wire, bad.body, aliveFailure)
				})
			}
		}
	}
}

type invalidUserResponse struct {
	name string
	body []byte
}

func userResponseInvalidBodies(t *testing.T, wire string) []invalidUserResponse {
	t.Helper()
	if wire == "json" {
		return []invalidUserResponse{
			{"missing", []byte(`{}`)},
			{"root_null", []byte(`null`)},
			{"users_null", []byte(`{"users":null}`)},
			{"nested_users", []byte(`{"error":{"users":[]}}`)},
			{"missing_object_end", []byte(`{"users":[]`)},
			{"invalid_tail", []byte(`{"users":[],"meta":`)},
			{"duplicate_users", []byte(`{"users":[],"users":[]}`)},
			{"trailing_document", []byte(`{"users":[]}{"error":"synthetic"}`)},
			{"bad_later_row", []byte(`{"users":[{"id":11,"uuid":"synthetic-user"},{"id":"invalid"}]}`)},
		}
	}
	encode := func(value any) []byte {
		t.Helper()
		body, err := msgpack.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	empty := encode(map[string]any{"users": []any{}})
	var duplicate bytes.Buffer
	enc := msgpack.NewEncoder(&duplicate)
	if err := enc.EncodeMapLen(2); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 2; i++ {
		if err := enc.EncodeString("users"); err != nil {
			t.Fatal(err)
		}
		if err := enc.EncodeArrayLen(0); err != nil {
			t.Fatal(err)
		}
	}
	return []invalidUserResponse{
		{"missing", encode(map[string]any{})},
		{"root_null", encode(nil)},
		{"users_null", encode(map[string]any{"users": nil})},
		{"nested_users", encode(map[string]any{"error": map[string]any{"users": []any{}}})},
		{"truncated_array", empty[:len(empty)-1]},
		{"invalid_tail", append(append([]byte(nil), empty...), 0xc1)},
		{"duplicate_users", duplicate.Bytes()},
		{"trailing_document", append(append([]byte(nil), empty...), encode(map[string]any{"error": "synthetic"})...)},
		{"bad_later_row", encode(map[string]any{"users": []any{map[string]any{"id": 11, "uuid": "synthetic-user"}, map[string]any{"id": "invalid"}}})},
	}
}

func userResponsePreservesState(t *testing.T, wire string, bad []byte, aliveFailure bool) {
	t.Helper()
	const tag, identity, sourceIP = "response-snapshot", "synthetic-user", "192.0.2.1"
	users := []panel.UserInfo{{Id: 11, Uuid: identity, DeviceLimit: 1, DynamicSpeedLimit: &dynamic.Policy{
		Version: 1, Revision: 7, Enabled: true, WindowSeconds: 60,
		ThresholdBytes: 1 << 60, SpeedMbps: 1, DurationSeconds: 10,
	}}}
	encode := func(value any) []byte {
		t.Helper()
		var body []byte
		var err error
		if wire == "json" {
			body, err = json.Marshal(value)
		} else {
			body, err = msgpack.Marshal(value)
		}
		if err != nil {
			t.Fatal(err)
		}
		return body
	}
	initial := encode(panel.UserListBody{Users: users})
	empty := encode(map[string]any{"users": []panel.UserInfo{}, "future_metadata": map[string]any{"version": 1}})
	var phase atomic.Int32
	var outage atomic.Bool
	var requests, notModified atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/api/v1/server/UniProxy/alivelist":
			if outage.Load() {
				w.WriteHeader(http.StatusServiceUnavailable)
			} else {
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"alive":{}}`)
			}
		case "/api/v1/server/UniProxy/config":
			w.WriteHeader(http.StatusNotModified)
		case "/api/v1/server/UniProxy/user":
			requests.Add(1)
			if wire == "json" {
				w.Header().Set("Content-Type", "application/json")
			} else {
				w.Header().Set("Content-Type", "application/x-msgpack")
			}
			etag, body := "good-start", initial
			switch phase.Load() {
			case 1:
				etag, body = "bad-body", bad
			case 2:
				etag, body = "good-empty", empty
			}
			if r.Header.Get("If-None-Match") == "bad-body" {
				t.Error("invalid response ETag was committed")
			}
			if outage.Load() && r.Header.Get("If-None-Match") == "good-empty" {
				t.Error("outage response was committed before full application")
			}
			w.Header().Set("ETag", etag)
			if r.Header.Get("If-None-Match") == etag {
				notModified.Add(1)
				w.WriteHeader(http.StatusNotModified)
				return
			}
			_, _ = w.Write(body)
		default:
			t.Errorf("unexpected path: %s", r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	api, err := panel.New(&conf.ApiConfig{APIHost: server.URL, NodeType: "anytls", NodeID: 1})
	if err != nil {
		t.Fatal(err)
	}
	limiter.Init()
	l := limiter.AddLimiter(tag, &conf.LimitConfig{}, users, map[int]int{})
	defer limiter.DeleteLimiter(tag)
	core := &syncTestCore{live: map[string]int{identity: 11}}
	c := &Controller{server: core, apiClient: api, tag: tag, userList: users, limiter: l,
		info: &panel.NodeInfo{}, Options: &conf.Options{}, traffic: map[string]int64{identity: 1234567}}
	snapshot, err := api.GetUserList()
	if err != nil {
		t.Fatal(err)
	}
	if err := c.applyUserSnapshot(snapshot); err != nil {
		t.Fatal(err)
	}
	key := format.UserTag(tag, identity)
	control := l.DynamicControl(key)
	if control == nil || !control.Status().Enabled || control.Status().Revision != 7 {
		t.Fatal("initial dynamic policy unavailable")
	}
	if bucket, rejected := l.CheckLimit(key, sourceIP, true, true); rejected || bucket != nil {
		t.Fatal("initial unlimited device admission failed")
	}
	a, b := net.Pipe()
	conn := l.TrackDeviceConn(a, key, sourceIP)
	defer conn.Close()
	defer b.Close()

	phase.Store(1)
	outage.Store(aliveFailure)
	for i := 0; i < 2; i++ {
		before := requests.Load()
		if err := c.nodeInfoMonitor(); err != nil {
			t.Fatal(err)
		}
		if requests.Load() != before+1 || notModified.Load() != 0 {
			t.Fatal("bad response suppressed retry")
		}
		if !reflect.DeepEqual(c.userList, users) || core.live[identity] != 11 || len(core.live) != 1 || core.addCalls != 0 || core.deleteCalls != 0 {
			t.Fatal("invalid response changed applied authorization")
		}
		if c.traffic[identity] != 1234567 || len(c.traffic) != 1 || l.UUIDtoUID[identity] != 11 {
			t.Fatal("invalid response reset traffic or accounting owner")
		}
		if l.DynamicControl(key) != control || !control.Status().Enabled || control.Status().Revision != 7 {
			t.Fatal("invalid response replaced or disabled existing dynamic control")
		}
		if bucket, rejected := l.CheckLimit(key, "::ffff:"+sourceIP, true, true); rejected || bucket != nil {
			t.Fatal("same source no longer reuses the unlimited device slot")
		}
		if _, rejected := l.CheckLimit(key, "192.0.2.2", true, true); !rejected {
			t.Fatal("invalid response freed the occupied device slot")
		}
		online, err := l.GetOnlineDevice()
		if err != nil || len(*online) != 1 || (*online)[0].UID != 11 || (*online)[0].IP != sourceIP {
			t.Fatal("accepted source IP or online identity changed")
		}
		userResponsePipeRoundTrip(t, conn, b)
	}

	phase.Store(2)
	if err := c.nodeInfoMonitor(); err != nil {
		t.Fatal(err)
	}
	if len(c.userList) != 0 || len(core.live) != 0 || core.deleteCalls != 1 || core.addCalls != 0 {
		t.Fatal("explicit empty list did not revoke once")
	}
	if l.DynamicControl(key) != nil {
		t.Fatal("valid revocation retained dynamic control")
	}
	if _, rejected := l.CheckLimit(key, sourceIP, true, true); !rejected {
		t.Fatal("revoked identity can still gain admission")
	}
	if aliveFailure {
		if err := c.nodeInfoMonitor(); err != nil {
			t.Fatal(err)
		}
		if notModified.Load() != 0 {
			t.Fatal("partially applied empty list incorrectly cached during outage")
		}
		outage.Store(false)
		if err := c.nodeInfoMonitor(); err != nil {
			t.Fatal(err)
		}
	}
	if err := c.nodeInfoMonitor(); err != nil {
		t.Fatal(err)
	}
	if notModified.Load() != 1 || core.deleteCalls != 1 {
		t.Fatal("healthy empty snapshot was not cached, or revocation was duplicated")
	}
}

func userResponsePipeRoundTrip(t *testing.T, conn, peer net.Conn) {
	t.Helper()
	for _, ends := range [][2]net.Conn{{conn, peer}, {peer, conn}} {
		deadline := time.Now().Add(2 * time.Second)
		if err := ends[0].SetDeadline(deadline); err != nil {
			t.Fatal(err)
		}
		if err := ends[1].SetDeadline(deadline); err != nil {
			t.Fatal(err)
		}
		payload := []byte("synthetic existing stream")
		written := make(chan error, 1)
		go func() {
			_, err := ends[0].Write(payload)
			written <- err
		}()
		got := make([]byte, len(payload))
		_, readErr := io.ReadFull(ends[1], got)
		writeErr := <-written
		if readErr != nil || writeErr != nil || !bytes.Equal(got, payload) {
			t.Fatal("invalid snapshot interrupted the existing device-tracked stream")
		}
	}
}
