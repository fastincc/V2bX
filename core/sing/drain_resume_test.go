//go:build sing

package sing

import (
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"sync"
	"testing"
)

func TestDrainCanResumeAfterStorageFailure(t *testing.T) {
	b := &Sing{users: &UserMap{active: map[userKey]string{{"n", "synthetic"}: "synthetic"}, uidMap: map[userKey]int{{"n", "synthetic"}: 1}, retired: map[userKey]bool{}}, hookServer: &HookServer{counter: sync.Map{}}}
	counts := counter.LoadOrCreate(&b.hookServer.counter, "n")
	counts.Tx("synthetic", 123)
	if e := b.DrainTraffic("n"); e != nil {
		t.Fatal(e)
	}
	if len(b.users.active) != 0 {
		t.Fatal("not quiesced")
	}
	if e := b.ResumeTraffic("n"); e != nil {
		t.Fatal(e)
	}
	if b.users.active[userKey{"n", "synthetic"}] != "synthetic" || counts.GetUpCount("synthetic") != 123 {
		t.Fatal("resume discarded state")
	}
	var got int64
	b.PersistTrafficSnapshot("n", func(rows []panel.UserTraffic) error {
		for _, r := range rows {
			got += r.Upload
		}
		return nil
	})
	if got != 123 {
		t.Fatal("tail changed")
	}
}
