package node

import (
	"errors"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	vcore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	"net/http"
	"net/http/httptest"
	"testing"
)

type rebuildTestCore struct {
	vcore.Core
	live, paused  bool
	port          int
	users         []panel.UserInfo
	failure       string
	triggered     bool
	failRestore   bool
	saves, drains int
}

func (f *rebuildTestCore) HasNode(string) bool { return f.live }
func (f *rebuildTestCore) fail(stage string) error {
	if f.failure == stage && !f.triggered {
		f.triggered = true
		return errors.New("injected " + stage)
	}
	return nil
}
func (f *rebuildTestCore) DrainTraffic(string) error {
	f.drains++
	f.paused = true
	return f.fail("drain")
}
func (f *rebuildTestCore) ResumeTraffic(string) error {
	if e := f.fail("resume"); e != nil {
		return e
	}
	f.paused = false
	return nil
}
func (f *rebuildTestCore) PersistTrafficSnapshot(_ string, save func([]panel.UserTraffic) error) error {
	f.saves++
	if e := f.fail("save"); e != nil {
		return e
	}
	return save(nil)
}
func (f *rebuildTestCore) DelNode(string) error {
	if e := f.fail("delete"); e != nil {
		return e
	}
	f.live = false
	f.users = nil
	return nil
}
func (f *rebuildTestCore) AddNode(_ string, n *panel.NodeInfo, _ *conf.Options) error {
	if n.Common.ServerPort == 2 {
		if e := f.fail("create"); e != nil {
			return e
		}
	}
	if n.Common.ServerPort == 1 && f.failRestore {
		f.failRestore = false
		return errors.New("injected restore failure")
	}
	f.live = true
	f.paused = false
	f.port = n.Common.ServerPort
	return nil
}
func (f *rebuildTestCore) AddUsers(p *vcore.AddUsersParams) (int, error) {
	f.users = append([]panel.UserInfo(nil), p.Users...)
	if p.NodeInfo.Common.ServerPort == 2 {
		if e := f.fail("users"); e != nil {
			return 1, e
		}
	}
	return len(p.Users), nil
}
func (f *rebuildTestCore) DelUsers(users []panel.UserInfo, _ string, _ *panel.NodeInfo) error {
	for _, u := range users {
		var keep []panel.UserInfo
		for _, old := range f.users {
			if old.Uuid != u.Uuid {
				keep = append(keep, old)
			}
		}
		f.users = keep
	}
	return nil
}
func rebuildFixture(t *testing.T, stage string) (*Controller, *rebuildTestCore, *panel.NodeInfo) {
	t.Helper()
	limiter.Init()
	users := []panel.UserInfo{{Id: 1, Uuid: "synthetic-a"}, {Id: 2, Uuid: "synthetic-b"}}
	f := &rebuildTestCore{live: true, port: 1, users: users, failure: stage}
	api, e := panel.New(&conf.ApiConfig{APIHost: "http://127.0.0.1:1", NodeType: "shadowsocks", NodeID: 1})
	if e != nil {
		t.Fatal(e)
	}
	c := &Controller{server: f, apiClient: api, tag: "rebuild", userList: users, info: &panel.NodeInfo{Common: &panel.CommonNode{ServerPort: 1}}, Options: &conf.Options{TrafficSpoolDirectory: t.TempDir()}, reliableTrafficEnabled: true}
	c.limiter = limiter.AddLimiter(c.tag, &conf.LimitConfig{}, users, nil)
	t.Cleanup(func() { limiter.DeleteLimiter(c.tag) })
	if e = c.openTrafficSpool(); e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { c.trafficSpool.Close() })
	return c, f, &panel.NodeInfo{Common: &panel.CommonNode{ServerPort: 2}}
}
func TestPanelRebuildFailureRestoresAppliedNode(t *testing.T) {
	for _, stage := range []string{"drain", "save", "delete", "create", "users"} {
		t.Run(stage, func(t *testing.T) {
			c, f, next := rebuildFixture(t, stage)
			old := c.info
			if e := c.rebuildFromPanel(next, nil, nil); e == nil {
				t.Fatal("failure hidden")
			}
			if !f.live || f.paused || f.port != 1 || c.info != old || c.rebuildRecovery != nil || len(f.users) != 2 {
				t.Fatalf("not restored live=%v paused=%v port=%d recovery=%v users=%d", f.live, f.paused, f.port, c.rebuildRecovery != nil, len(f.users))
			}
			if f.drains == 0 {
				t.Fatal("drain not exercised")
			}
		})
	}
}
func TestPanelRebuildRecoveryRetriesBeforePanel(t *testing.T) {
	c, f, next := rebuildFixture(t, "create")
	f.failRestore = true
	if e := c.rebuildFromPanel(next, nil, nil); e == nil || c.rebuildRecovery == nil {
		t.Fatal("missing pending recovery")
	}
	calls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; w.WriteHeader(503) }))
	defer srv.Close()
	api, e := panel.New(&conf.ApiConfig{APIHost: srv.URL, NodeType: "shadowsocks", NodeID: 1})
	if e != nil {
		t.Fatal(e)
	}
	c.apiClient = api
	if e = c.nodeInfoMonitor(); e != nil {
		t.Fatal(e)
	}
	if calls != 0 || !f.live || f.port != 1 || c.rebuildRecovery != nil {
		t.Fatal("recovery depended on panel or failed")
	}
}
func TestPanelRebuildRollbackDoesNotRestoreRevokedUser(t *testing.T) {
	c, f, next := rebuildFixture(t, "users")
	desired := []panel.UserInfo{c.userList[1], {Id: 3, Uuid: "synthetic-c"}}
	if e := c.rebuildFromPanel(next, desired, nil); e == nil {
		t.Fatal("failure hidden")
	}
	if len(c.userList) != 1 || len(f.users) != 1 || f.users[0].Id != 2 {
		t.Fatal("revoked/new user restored on rollback")
	}
	if f.saves < 2 {
		t.Fatal("partial candidate traffic was not captured before removal")
	}
}
func TestPanelRebuildSuccessCommitsAndPreservesLimiter(t *testing.T) {
	c, f, next := rebuildFixture(t, "")
	l := c.limiter
	if e := c.rebuildFromPanel(next, nil, nil); e != nil {
		t.Fatal(e)
	}
	if c.info != next || f.port != 2 || c.limiter != l || c.rebuildRecovery != nil || f.paused {
		t.Fatal("candidate not committed")
	}
}
func TestPanelRebuildSealFailureResumesWithoutLosingQueue(t *testing.T) {
	c, f, next := rebuildFixture(t, "")
	c.trafficSpool.Close()
	c.TrafficSpoolMaxBatches = 1
	if e := c.openTrafficSpool(); e != nil {
		t.Fatal(e)
	}
	if e := c.trafficSpool.Save([]panel.UserTraffic{{UID: 1, Upload: 5}}); e != nil {
		t.Fatal(e)
	}
	if e := c.trafficSpool.Seal(); e != nil {
		t.Fatal(e)
	}
	if e := c.trafficSpool.Save([]panel.UserTraffic{{UID: 1, Upload: 7}}); e != nil {
		t.Fatal(e)
	}
	if e := c.rebuildFromPanel(next, nil, nil); e == nil {
		t.Fatal("capacity failure hidden")
	}
	n, active := c.trafficSpool.Counts()
	if f.paused || !f.live || f.port != 1 || n != 1 || !active {
		t.Fatal("failed seal lost queue or admission")
	}
}
