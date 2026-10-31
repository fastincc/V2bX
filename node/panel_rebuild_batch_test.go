package node

import (
	"errors"
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/limiter"
	"testing"
)

type batchRebuildCore struct {
	*rebuildTestCore
	calls, failTimes, failAfter int
	sizes                       []int
}

func (f *batchRebuildCore) DelUsers(users []panel.UserInfo, _ string, _ *panel.NodeInfo) error {
	f.calls++
	f.sizes = append(f.sizes, len(users))
	n := len(users)
	fail := f.failTimes > 0
	if fail {
		f.failTimes--
		n = min(n, f.failAfter)
	}
	removed := map[string]bool{}
	for _, u := range users[:n] {
		removed[u.Uuid] = true
	}
	keep := make([]panel.UserInfo, 0, len(f.users))
	for _, u := range f.users {
		if !removed[u.Uuid] {
			keep = append(keep, u)
		}
	}
	f.users = keep
	if fail {
		return errors.New("injected ambiguous batch deletion")
	}
	return nil
}
func batchRebuildFixture(t *testing.T, n int) (*Controller, *batchRebuildCore, *panel.NodeInfo) {
	c, base, next := rebuildFixture(t, "")
	f := &batchRebuildCore{rebuildTestCore: base}
	c.server = f
	users := make([]panel.UserInfo, n)
	for i := range users {
		users[i] = panel.UserInfo{Id: i + 1, Uuid: fmt.Sprintf("synthetic-%d", i)}
	}
	c.userList = users
	f.users = append([]panel.UserInfo(nil), users...)
	limiter.DeleteLimiter(c.tag)
	c.limiter = limiter.AddLimiter(c.tag, &conf.LimitConfig{}, users, nil)
	return c, f, next
}
func TestPanelRebuildUsesSingleBatch(t *testing.T) {
	c, f, next := batchRebuildFixture(t, 1000)
	desired := append([]panel.UserInfo(nil), c.userList[900:]...)
	if e := c.rebuildFromPanel(next, desired, nil); e != nil {
		t.Fatal(e)
	}
	if f.calls != 1 || len(f.sizes) != 1 || f.sizes[0] != 900 || len(c.userList) != 100 || len(f.users) != 100 {
		t.Fatalf("calls=%d sizes=%v retained=%d", f.calls, f.sizes, len(c.userList))
	}

}
func TestPanelRebuildAmbiguousBatchNeverRestoresRevokedUsers(t *testing.T) {
	for _, removed := range []int{0, 1, 2} {
		t.Run(fmt.Sprint(removed), func(t *testing.T) {
			c, f, next := batchRebuildFixture(t, 3)
			f.failTimes = 2
			f.failAfter = removed
			old := c.info
			desired := append([]panel.UserInfo(nil), c.userList[2:]...)
			if e := c.trafficSpool.Save([]panel.UserTraffic{{UID: 1, Upload: 5, Download: 7}}); e != nil {
				t.Fatal(e)
			}
			if e := c.rebuildFromPanel(next, desired, nil); e == nil {
				t.Fatal("partial failure hidden")
			}
			if c.rebuildRecovery == nil || len(c.rebuildRecovery.revocations) != 2 || c.rebuildRecovery.resumeOnly || !f.paused || len(c.userList) != 1 {
				t.Fatal("partial revocations not quarantined")
			}
			if _, rejected := c.limiter.CheckLimit(format.UserTag(c.tag, "synthetic-0"), "192.0.2.1", true, true); !rejected {
				t.Fatal("revoked user admitted")
			}
			// Recovery is completed before asking the unavailable panel for another ETag.
			c.nodeInfoMonitor()
			if c.rebuildRecovery == nil || !f.paused {
				t.Fatal("failed cleanup resumed node")
			}
			c.nodeInfoMonitor()
			if c.rebuildRecovery != nil || f.paused || f.port != 1 || c.info != old || len(f.users) != 1 || f.users[0].Id != 3 {
				t.Fatal("unsafe or incomplete recovery", f.users)
			}
			batch := c.trafficSpool.Pending()
			if batch == nil || len(batch.Traffic) != 1 || batch.Traffic[0].Upload != 5 || batch.Traffic[0].Download != 7 {
				t.Fatal("recovery lost durable tail", batch)
			}
			if f.calls != 3 {
				t.Fatal("unexpected retry count", f.calls)
			}
		})
	}
}
func TestPanelRebuildPendingRevocationSurvivesSaveFailure(t *testing.T) {
	c, f, next := batchRebuildFixture(t, 3)
	f.failTimes = 1
	f.failAfter = 1
	f.failure = "save"
	desired := append([]panel.UserInfo(nil), c.userList[2:]...)
	if e := c.rebuildFromPanel(next, desired, nil); e == nil {
		t.Fatal("missing delete failure")
	}
	if e := c.recoverPanelRebuild(); e == nil || c.rebuildRecovery == nil || !f.paused {
		t.Fatal("failed save permitted restore")
	}
	if e := c.recoverPanelRebuild(); e != nil {
		t.Fatal(e)
	}
	if len(f.users) != 1 || f.users[0].Id != 3 || c.rebuildRecovery != nil {
		t.Fatal("revoked identity restored")
	}
}

func TestPanelRebuildUIDReplacementFailureKeepsOldOwnerRevoked(t *testing.T) {
	c, f, next := batchRebuildFixture(t, 3)
	f.failure = "users"
	desired := append([]panel.UserInfo(nil), c.userList...)
	desired[0].Id = 99
	if e := c.rebuildFromPanel(next, desired, nil); e == nil {
		t.Fatal("missing candidate installation failure")
	}
	if f.calls != 1 || len(c.userList) != 2 || len(f.users) != 2 {
		t.Fatal("replacement rollback restored removed ownership")
	}
	for _, u := range f.users {
		if u.Id == 1 || u.Id == 99 {
			t.Fatal("old or partially installed owner survived rollback")
		}
	}
}
