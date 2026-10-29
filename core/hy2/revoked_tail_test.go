package hy2

import (
	"errors"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	vcore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	"go.uber.org/zap"
	"sync"
	"testing"
)

func TestRevokedTailGenerationAndPersistence(t *testing.T) {
	limiter.Init()
	tag := "tail"
	raw := "synthetic-secret"
	user := panel.UserInfo{Id: 41, Uuid: raw}
	limiter.AddLimiter(tag, &conf.LimitConfig{}, []panel.UserInfo{user}, nil)
	defer limiter.DeleteLimiter(tag)
	auth := &V2bX{usersMap: map[string]int{}}
	hook := &HookServer{Auth: auth, Tag: tag, logger: zap.NewNop(), ReportMinTrafficBytes: 1 << 20}
	h := &Hysteria2{Hy2nodes: map[string]Hysteria2node{tag: {Auth: auth, TrafficLogger: hook}}}
	add := func(uid int) {
		if _, err := h.AddUsers(&vcore.AddUsersParams{Tag: tag, Users: []panel.UserInfo{{Id: uid, Uuid: raw}}}); err != nil {
			t.Fatal(err)
		}
	}
	add(41)
	old := hyTestID(auth, raw)
	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < 1000; j++ {
				if !hook.LogTraffic(old, 1, 2) {
					t.Error("authorized traffic denied")
					return
				}
			}
		}()
	}
	wg.Wait()
	if err := h.DelUsers([]panel.UserInfo{user}, tag, nil); err != nil {
		t.Fatal(err)
	}
	if hook.LogTraffic(old, 999, 999) {
		t.Fatal("revoked bytes accepted")
	}
	add(42)
	current := hyTestID(auth, raw)
	if current == old || hook.LogTraffic(old, 999, 999) {
		t.Fatal("old generation reactivated")
	}
	if !hook.LogTraffic(current, 7, 11) {
		t.Fatal("re-add denied")
	}
	failure := errors.New("synthetic disk failure")
	if err := h.PersistTrafficSnapshot(tag, func([]panel.UserTraffic) error { return failure }); err != failure {
		t.Fatal(err)
	}
	var rows []panel.UserTraffic
	if err := h.PersistTrafficSnapshot(tag, func(v []panel.UserTraffic) error { rows = v; return nil }); err != nil {
		t.Fatal(err)
	}
	got := map[int]panel.UserTraffic{}
	for _, v := range rows {
		got[v.UID] = v
	}
	if got[41].Upload != 8000 || got[41].Download != 16000 || got[42].Upload != 7 || got[42].Download != 11 {
		t.Fatalf("wrong tail: %+v", got)
	}
	calls := 0
	h.PersistTrafficSnapshot(tag, func([]panel.UserTraffic) error { calls++; return nil })
	if calls != 0 {
		t.Fatal("repeated durable bytes")
	}
	// Retired traffic must bypass the ordinary minimum in legacy mode as well.
	hook.LogTraffic(current, 3, 5)
	h.DelUsers([]panel.UserInfo{user}, tag, nil)
	rows, err := h.GetUserTrafficSlice(tag, true)
	if err != nil || len(rows) != 1 || rows[0].Upload != 3 || rows[0].Download != 5 {
		t.Fatal("legacy tail lost", err, rows)
	}
	if len(auth.identities) != 0 {
		t.Fatal("retired generations not reclaimed")
	}
}
