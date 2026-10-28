//go:build sing

package sing

import (
	"errors"
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/conf"
	vcore "github.com/InazumaV/V2bX/core"
	"testing"
)

func TestRetiredGenerationsReclaimOnlyAfterTailDurable(t *testing.T) {
	vc, e := New(&conf.CoreConfig{SingConfig: conf.NewSingConfig()})
	if e != nil {
		t.Fatal(e)
	}
	b := vc.(*Sing)
	defer b.Close()
	info := &panel.NodeInfo{Type: "anytls", Common: &panel.CommonNode{}, AnyTls: &panel.AnyTlsNode{}}
	opt := &conf.Options{ListenIP: "127.0.0.1", SingOptions: conf.NewSingOptions(), CertConfig: conf.NewCertConfig()}
	if e = b.AddNode("A", info, opt); e != nil {
		t.Fatal(e)
	}
	users := []panel.UserInfo{{Id: 1, Uuid: "synthetic"}}
	add := func() {
		if _, e = b.AddUsers(&vcore.AddUsersParams{Tag: "A", Users: users, NodeInfo: info}); e != nil {
			t.Fatal(e)
		}
	}
	add()
	counts := counter.LoadOrCreate(&b.hookServer.counter, "A")
	old := counts.GetCounter("synthetic")
	old.Owners.Add(1)
	old.UpCounter.Add(13)
	b.DelUsers(users, "A", info)
	add()
	newKey := b.users.active[userKey{"A", "synthetic"}]
	if newKey == "synthetic" {
		t.Fatal("generation reused")
	}
	counts.Tx(newKey, 7)
	b.ReclaimTraffic("A")
	if _, ok := b.users.uidMap[userKey{"A", "synthetic"}]; !ok {
		t.Fatal("live record reclaimed")
	}
	old.UpCounter.Add(3)
	old.Owners.Add(-1)
	if e = b.PersistTrafficSnapshot("A", func([]panel.UserTraffic) error { return errors.New("disk failed") }); e == nil {
		t.Fatal("failure ignored")
	}
	if old.UpCounter.Load() != 16 {
		t.Fatal("failed save subtracted tail")
	}
	var rows []panel.UserTraffic
	if e = b.PersistTrafficSnapshot("A", func(r []panel.UserTraffic) error { rows = r; return nil }); e != nil {
		t.Fatal(e)
	}
	if len(rows) != 1 || rows[0].Upload != 23 || rows[0].UID != 1 {
		t.Fatal("wrong generation total", rows)
	}
	if _, ok := b.users.uidMap[userKey{"A", "synthetic"}]; ok {
		t.Fatal("retired metadata retained")
	}
	if counts.Len() != 1 {
		t.Fatal("retired counter retained")
	}
	for i := 0; i < 1000; i++ {
		b.DelUsers(users, "A", info)
		users[0].Uuid = fmt.Sprint("fixture-", i)
		add()
		if e = b.PersistTrafficSnapshot("A", func([]panel.UserTraffic) error { return nil }); e != nil {
			t.Fatal(e)
		}
	}
	if len(b.users.uidMap) > 1 || len(b.users.retired) != 0 {
		t.Fatal("metadata grew with churn", len(b.users.uidMap), len(b.users.retired))
	}
	t.Log("1000 credential rotations: one active mapping, zero retired mappings")
}
