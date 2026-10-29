package hy2

import (
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	vcore "github.com/InazumaV/V2bX/core"
	"github.com/InazumaV/V2bX/limiter"
	"go.uber.org/zap"
	"testing"
)

func TestNodeAuthorizationIsolation(t *testing.T) {
	limiter.Init()
	h := &Hysteria2{Hy2nodes: make(map[string]Hysteria2node)}
	for _, tag := range []string{"A", "B"} {
		auth := &V2bX{usersMap: make(map[string]int)}
		h.Hy2nodes[tag] = Hysteria2node{Auth: auth, TrafficLogger: &HookServer{Tag: tag, Auth: auth, logger: zap.NewNop()}}
		limiter.AddLimiter(tag, &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "shared"}, {Id: 2, Uuid: tag}}, map[int]int{})
		t.Cleanup(func() { limiter.DeleteLimiter(tag) })
		if _, err := h.AddUsers(&vcore.AddUsersParams{Tag: tag, Users: []panel.UserInfo{{Id: 1, Uuid: "shared"}, {Id: 2, Uuid: tag}}}); err != nil {
			t.Fatal(err)
		}
	}
	for _, tag := range []string{"A", "B"} {
		other := "A"
		if tag == "A" {
			other = "B"
		}
		n := h.Hy2nodes[tag]
		if ok, _ := n.Auth.Authenticate(nil, other, 0); ok {
			t.Fatal("cross-node authentication")
		}
		if n.TrafficLogger.(*HookServer).LogTraffic(other, 10, 10) {
			t.Fatal("cross-node traffic")
		}
		if !n.TrafficLogger.(*HookServer).LogTraffic(hyTestID(n.Auth, "shared"), 10, 10) {
			t.Fatal("allowed user denied")
		}
	}
	if err := h.DelUsers([]panel.UserInfo{{Id: 1, Uuid: "shared"}}, "A", nil); err != nil {
		t.Fatal(err)
	}
	if h.Hy2nodes["A"].TrafficLogger.(*HookServer).LogTraffic("shared", 10, 10) {
		t.Fatal("revoked traffic accepted")
	}
	if !h.Hy2nodes["B"].TrafficLogger.(*HookServer).LogTraffic(hyTestID(h.Hy2nodes["B"].Auth, "shared"), 10, 10) {
		t.Fatal("other node revoked")
	}
	if _, err := h.AddUsers(&vcore.AddUsersParams{Tag: "missing"}); err == nil {
		t.Fatal("missing node accepted")
	}
}

func hyTestID(a *V2bX, raw string) string { _, id := a.Authenticate(nil, raw, 0); return id }
