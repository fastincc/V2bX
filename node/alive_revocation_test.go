package node

import (
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	vcore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	"net/http"
	"net/http/httptest"
	"testing"
)

type revocationCore struct {
	vcore.Core
	removed []panel.UserInfo
}

func (c *revocationCore) DelUsers(u []panel.UserInfo, _ string, _ *panel.NodeInfo) error {
	c.removed = append(c.removed, u...)
	return nil
}
func TestAliveOutageStillRevokesUsers(t *testing.T) {
	for _, empty := range []bool{false, true} {
		t.Run(fmt.Sprint(empty), func(t *testing.T) {
			userRequests, configRequests := 0, 0
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch r.URL.Path {
				case "/api/v1/server/UniProxy/alivelist":
					w.WriteHeader(503)
				case "/api/v1/server/UniProxy/config":
					configRequests++
					w.WriteHeader(304)
				case "/api/v1/server/UniProxy/user":
					userRequests++
					if r.Header.Get("If-None-Match") != "" {
						t.Error("partially applied response was cached")
					}
					w.Header().Set("ETag", "synthetic-v2")
					w.Header().Set("Content-Type", "application/json")
					if empty {
						fmt.Fprint(w, `{"users":[]}`)
					} else {
						fmt.Fprint(w, `{"users":[{"id":2,"uuid":"retained","device_limit":1},{"id":3,"uuid":"deferred"}]}`)
					}
				}
			}))
			defer srv.Close()
			users := []panel.UserInfo{{Id: 1, Uuid: "revoked"}, {Id: 2, Uuid: "retained", DeviceLimit: 1}}
			limiter.Init()
			l := limiter.AddLimiter("outage", &conf.LimitConfig{}, users, map[int]int{2: 1})
			t.Cleanup(func() { limiter.DeleteLimiter("outage") })
			api, e := panel.New(&conf.ApiConfig{APIHost: srv.URL, Key: "synthetic", NodeType: "anytls", NodeID: 1})
			if e != nil {
				t.Fatal(e)
			}
			core := &revocationCore{}
			c := &Controller{server: core, apiClient: api, tag: "outage", userList: users, limiter: l, info: &panel.NodeInfo{}, Options: &conf.Options{}}
			for i := 0; i < 2; i++ {
				if e = c.nodeInfoMonitor(); e != nil {
					t.Fatal(e)
				}
			}
			want := 1
			if empty {
				want = 2
			}
			if len(core.removed) != want || len(c.userList) != 2-want || userRequests != 2 || configRequests != 0 {
				t.Fatalf("wrong removals/cache state: removed=%d remaining=%d requests=%d config=%d", len(core.removed), len(c.userList), userRequests, configRequests)
			}
			if !empty && l.AliveList[2] != 1 {
				t.Fatal("snapshot discarded")
			}
		})
	}
}
