package sing

import (
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/conf"
	vcore "github.com/InazumaV/V2bX/core"
	"testing"
)

func TestDeletingOneNodePreservesOtherNodeBilling(t *testing.T) {
	vc, e := New(&conf.CoreConfig{SingConfig: conf.NewSingConfig()})
	if e != nil {
		t.Fatal(e)
	}
	b := vc.(*Sing)
	defer b.Close()
	info := &panel.NodeInfo{Type: "anytls", Common: &panel.CommonNode{}, AnyTls: &panel.AnyTlsNode{}}
	opt := &conf.Options{ListenIP: "127.0.0.1", SingOptions: conf.NewSingOptions(), CertConfig: conf.NewCertConfig()}
	users := []panel.UserInfo{{Id: 1, Uuid: "synthetic"}}
	for _, tag := range []string{"A", "B"} {
		if e = b.AddNode(tag, info, opt); e != nil {
			t.Fatal(e)
		}
		if _, e = b.AddUsers(&vcore.AddUsersParams{Tag: tag, Users: users, NodeInfo: info}); e != nil {
			t.Fatal(e)
		}
	}
	tc := counter.NewTrafficCounter()
	tc.Tx("synthetic", 1234)
	b.hookServer.counter.Store("B", tc)
	aCounter := counter.NewTrafficCounter()
	aCounter.Tx("synthetic", 10)
	b.hookServer.counter.Store("A", aCounter)
	if e = b.DelUsers(users, "A", info); e != nil {
		t.Fatal(e)
	}
	aCounter.Tx("synthetic", 20) // final bytes from asynchronous stream cleanup
	retired, err := b.GetUserTrafficSlice("A", true)
	if err != nil || len(retired) != 1 || retired[0].UID != 1 || retired[0].Upload != 30 {
		t.Fatal("revocation discarded final traffic")
	}
	rows, e := b.GetUserTrafficSlice("B", true)
	if e != nil {
		t.Fatal(e)
	}
	if len(rows) != 1 || rows[0].UID != 1 || rows[0].Upload != 1234 {
		t.Fatalf("B accounting lost: %+v", rows)
	}
	rows, e = b.GetUserTrafficSlice("B", true)
	if e != nil || len(rows) != 0 {
		t.Fatal("duplicate traffic")
	}
}
