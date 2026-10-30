package node

import (
	"errors"
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	vcore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	"net/http"
	"net/http/httptest"
	"testing"
)

type syncTestCore struct {
	vcore.Core
	live                             map[string]int
	deleteCalls, addCalls            int
	failDelete, failAdd, failCleanup bool
	addingFailed                     bool
}

func (f *syncTestCore) DelUsers(users []panel.UserInfo, _ string, _ *panel.NodeInfo) error {
	f.deleteCalls++
	if f.addingFailed && f.failCleanup {
		f.failCleanup = false
		return errors.New("injected cleanup failure")
	}
	for _, u := range users {
		delete(f.live, u.Uuid)
		if f.failDelete {
			f.failDelete = false
			return errors.New("injected partial deletion")
		}
	}
	return nil
}
func (f *syncTestCore) AddUsers(p *vcore.AddUsersParams) (int, error) {
	f.addCalls++
	for i, u := range p.Users {
		if _, exists := f.live[u.Uuid]; exists {
			return i, errors.New("duplicate authorization")
		}
		f.live[u.Uuid] = u.Id
		if f.failAdd {
			f.failAdd = false
			f.addingFailed = true
			return i + 1, errors.New("injected partial addition")
		}
	}
	f.addingFailed = false
	return len(p.Users), nil
}
func TestUserSyncRetriesPartialChangesBeforeETagCommit(t *testing.T) {
	for _, partialAdd := range []bool{false, true} {
		t.Run(fmt.Sprint(partialAdd), func(t *testing.T) {
			requests, notModified := 0, 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				switch r.URL.Path {
				case "/api/v1/server/UniProxy/alivelist":
					fmt.Fprint(w, `{"alive":{}}`)
				case "/api/v1/server/UniProxy/config":
					w.WriteHeader(304)
				case "/api/v1/server/UniProxy/user":
					requests++
					if r.Header.Get("If-None-Match") == "next" {
						notModified++
						w.WriteHeader(304)
						return
					}
					w.Header().Set("ETag", "next")
					if partialAdd {
						fmt.Fprint(w, `{"users":[{"id":2,"uuid":"new-2"},{"id":3,"uuid":"new-3"}]}`)
					} else {
						fmt.Fprint(w, `{"users":[]}`)
					}
				}
			}))
			defer server.Close()
			api, e := panel.New(&conf.ApiConfig{APIHost: server.URL, NodeType: "anytls", NodeID: 1})
			if e != nil {
				t.Fatal(e)
			}
			users := []panel.UserInfo{{Id: 1, Uuid: "old"}}
			limiter.Init()
			l := limiter.AddLimiter("sync", &conf.LimitConfig{}, users, nil)
			defer limiter.DeleteLimiter("sync")
			f := &syncTestCore{live: map[string]int{"old": 1}, failDelete: !partialAdd, failAdd: partialAdd, failCleanup: partialAdd}
			c := &Controller{server: f, apiClient: api, tag: "sync", info: &panel.NodeInfo{}, Options: &conf.Options{}, limiter: l, userList: users}
			if e = c.nodeInfoMonitor(); e != nil {
				t.Fatal(e)
			}
			if partialAdd && (len(c.userList) != 0 || len(c.pendingUserCleanup) != 2) {
				t.Fatal("completed deletion rolled back or partial additions untracked")
			}
			for i := 0; i < 2; i++ {
				if e = c.nodeInfoMonitor(); e != nil {
					t.Fatal(e)
				}
			}
			if requests != 3 || notModified != 1 || len(c.pendingUserCleanup) != 0 {
				t.Fatalf("requests=%d 304=%d cleanup=%d", requests, notModified, len(c.pendingUserCleanup))
			}
			if _, ok := f.live["old"]; ok {
				t.Fatal("revoked user restored")
			}
			if partialAdd {
				if len(f.live) != 2 || f.live["new-2"] != 2 || f.live["new-3"] != 3 || f.addCalls != 2 {
					t.Fatal("partial add retry failed")
				}
			} else if len(f.live) != 0 || f.deleteCalls != 2 {
				t.Fatal("deletion retry failed")
			}
			t.Log("partial failure retried; only fully applied response returns 304; no duplicate authorization")
		})
	}
}
func TestUserSyncIdentityChangeReplacesAccountingOwner(t *testing.T) {
	old := []panel.UserInfo{{Id: 1, Uuid: "same", SpeedLimit: 23}}
	next := []panel.UserInfo{{Id: 2, Uuid: "same", SpeedLimit: 23}}
	deleted, added := compareUserList(old, next)
	if len(deleted) != 1 || len(added) != 1 {
		t.Fatal("UID change ignored")
	}
	// The old concatenated key also collided for (a1,23) and (a12,3).
	deleted, added = compareUserList([]panel.UserInfo{{Id: 1, Uuid: "a1", SpeedLimit: 23}}, []panel.UserInfo{{Id: 1, Uuid: "a12", SpeedLimit: 3}})
	if len(deleted) != 1 || len(added) != 1 {
		t.Fatal("identity tuple collision")
	}
}
func BenchmarkUserSyncUnchanged10000(b *testing.B) {
	users := make([]panel.UserInfo, 10000)
	for i := range users {
		users[i] = panel.UserInfo{Id: i + 1, Uuid: fmt.Sprintf("synthetic-%d", i)}
	}
	limiter.Init()
	l := limiter.AddLimiter("bench", &conf.LimitConfig{}, users, nil)
	defer limiter.DeleteLimiter("bench")
	api, _ := panel.New(&conf.ApiConfig{APIHost: "http://127.0.0.1:1", NodeType: "anytls"})
	c := &Controller{apiClient: api, userList: users, tag: "bench", limiter: l, server: &syncTestCore{}, Options: &conf.Options{}}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if e := c.applyUserSnapshot(users); e != nil {
			b.Fatal(e)
		}
	}
}
