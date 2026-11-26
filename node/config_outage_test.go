package node

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"sync/atomic"
	"testing"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
)

// Drive the ordinary monitor entry point, including the HTTP cache boundary.
func TestConfigOutageIdentityAndETag(t *testing.T) {
	var healthy atomic.Bool
	var userCalls, cacheHits atomic.Int32
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/api/v1/server/UniProxy/alivelist":
			fmt.Fprint(w, `{"alive":{}}`)
		case "/api/v1/server/UniProxy/config":
			if healthy.Load() {
				w.WriteHeader(http.StatusNotModified)
			} else {
				w.WriteHeader(http.StatusServiceUnavailable)
			}
		case "/api/v1/server/UniProxy/user":
			userCalls.Add(1)
			if r.Header.Get("If-None-Match") == "changed" {
				cacheHits.Add(1)
				w.WriteHeader(http.StatusNotModified)
				return
			}
			w.Header().Set("ETag", "changed")
			fmt.Fprint(w, `{"users":[{"id":1,"uuid":"synthetic-0","speed_limit":9},{"id":24,"uuid":"synthetic-1"},{"id":3,"uuid":"rotated"},{"id":5,"uuid":"new"}]}`)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
		}
	}))
	defer server.Close()
	c, _, _ := batchRebuildFixture(t, 4)
	api, err := panel.New(&conf.ApiConfig{APIHost: server.URL, NodeType: "shadowsocks", NodeID: 1})
	if err != nil {
		t.Fatal(err)
	}
	c.apiClient = api
	f := &syncTestCore{live: map[string]int{}}
	for _, u := range c.userList {
		f.live[u.Uuid] = u.Id
	}
	c.server = f
	for i := 0; i < 2; i++ {
		if err = c.nodeInfoMonitor(); err != nil {
			t.Fatal(err)
		}
		if len(c.userList) != 1 || c.userList[0].Id != 1 || c.userList[0].SpeedLimit != 0 || len(f.live) != 1 || f.live["synthetic-0"] != 1 || f.addCalls != 0 || f.deleteCalls != 1 {
			t.Fatalf("unexpected outage state users=%d survivors=%d add=%d del=%d", len(c.userList), len(f.live), f.addCalls, f.deleteCalls)
		}
	}
	healthy.Store(true)
	for i := 0; i < 2; i++ {
		if err = c.nodeInfoMonitor(); err != nil {
			t.Fatal(err)
		}
	}
	if userCalls.Load() != 4 || cacheHits.Load() != 1 || !reflect.DeepEqual(f.live, map[string]int{"synthetic-0": 1, "synthetic-1": 24, "rotated": 3, "new": 5}) || c.userList[0].SpeedLimit != 9 {
		t.Fatalf("inconsistent healthy recovery requests=%d cached=%d users=%d", userCalls.Load(), cacheHits.Load(), len(f.live))
	}
}

func TestConfigOutageSnapshotControls(t *testing.T) {
	for _, tc := range []struct {
		name      string
		users     []panel.UserInfo
		wantError bool
	}{
		{"not-modified", nil, false},
		{"empty", []panel.UserInfo{}, false},
		{"blank-credential", []panel.UserInfo{{Id: 1, Uuid: " "}}, true},
		{"missing-owner", []panel.UserInfo{{Uuid: "unknown"}}, true},
		{"conflicting-owner", []panel.UserInfo{{Id: 1, Uuid: "duplicate"}, {Id: 3, Uuid: "duplicate"}}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, f, _ := batchRebuildFixture(t, 3)
			err := c.applyRevocationsOnly(tc.users)
			if (err != nil) != tc.wantError {
				t.Fatalf("unexpected error %v", err)
			}
			want := 3
			if tc.name == "empty" {
				want = 0
			}
			if len(f.users) != want || len(c.userList) != want {
				t.Fatal("untrusted or missing snapshot changed authorization")
			}
		})
	}
}

func TestConfigOutageUserResponsePreservesAppliedUsers(t *testing.T) {
	for _, tc := range []struct {
		name   string
		status int
		body   string
	}{
		{"unchanged", http.StatusNotModified, ""},
		{"unavailable", http.StatusServiceUnavailable, ""},
		{"truncated", http.StatusOK, `{"users":[]`},
		{"wrong-root", http.StatusOK, `{"error":{"users":[]}}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, f, _ := batchRebuildFixture(t, 2)
			var userRequests atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/server/UniProxy/alivelist":
					fmt.Fprint(w, `{"alive":{}}`)
				case "/api/v1/server/UniProxy/config":
					w.WriteHeader(http.StatusServiceUnavailable)
				case "/api/v1/server/UniProxy/user":
					userRequests.Add(1)
					w.WriteHeader(tc.status)
					if tc.body != "" {
						fmt.Fprint(w, tc.body)
					}
				default:
					t.Errorf("unexpected request %s", r.URL.Path)
				}
			}))
			defer server.Close()
			api, err := panel.New(&conf.ApiConfig{APIHost: server.URL, NodeType: "shadowsocks", NodeID: 1})
			if err != nil {
				t.Fatal(err)
			}
			c.apiClient = api
			if err := c.nodeInfoMonitor(); err != nil {
				t.Fatal(err)
			}
			if userRequests.Load() != 1 || f.calls != 0 || len(f.users) != 2 || len(c.userList) != 2 {
				t.Fatal("inconclusive response changed applied users or skipped the user request")
			}
		})
	}
}

func TestConfigOutagePreflightRevocations(t *testing.T) {
	for _, failure := range []string{"rule", "certificate"} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/partial=%v", failure, partial), func(t *testing.T) {
				c, f, next := batchRebuildFixture(t, 3)
				if failure == "rule" {
					next.Rules.Regexp = []string{"["}
				} else {
					next.Security = panel.Tls
				}
				if partial {
					f.failTimes, f.failAfter = 1, 1
				}
				desired := []panel.UserInfo{c.userList[2], {Id: 4, Uuid: "deferred"}}
				desired[0].SpeedLimit = 16
				if err := c.rebuildFromPanel(next, desired, nil); err == nil {
					t.Fatal("accepted bad configuration")
				}
				if partial {
					if c.rebuildRecovery == nil || !f.paused {
						t.Fatal("ambiguous deletion not quarantined")
					}
					if err := c.recoverPanelRebuild(); err != nil {
						t.Fatal(err)
					}
				}
				if f.port != 1 || f.paused || len(c.userList) != 1 || c.userList[0].Id != 3 || c.userList[0].SpeedLimit != 0 || !reflect.DeepEqual(f.users, c.userList) {
					t.Fatal("failed preflight restored a removed identity or installed deferred changes")
				}
			})
		}
	}
}

func BenchmarkConfigOutageUnchanged10000(b *testing.B) {
	users := make([]panel.UserInfo, 10000)
	for i := range users {
		users[i] = panel.UserInfo{Id: i + 1, Uuid: fmt.Sprintf("synthetic-%d", i)}
	}
	api, err := panel.New(&conf.ApiConfig{APIHost: "http://127.0.0.1:1", NodeType: "shadowsocks", NodeID: 1})
	if err != nil {
		b.Fatal(err)
	}
	c := &Controller{apiClient: api, userList: users}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := c.applyRevocationsOnly(users); err != nil {
			b.Fatal(err)
		}
	}
}
