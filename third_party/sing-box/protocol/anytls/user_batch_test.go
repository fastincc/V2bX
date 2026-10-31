package anytls

import (
	"github.com/anytls/sing-anytls/padding"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"net"
	"sync/atomic"
	"testing"
)

type userBatchConn struct {
	net.Conn
	closed  atomic.Int32
	onClose func()
}

func (c *userBatchConn) Close() error {
	c.closed.Add(1)
	if c.onClose != nil {
		c.onClose()
	}
	return nil
}
func batchInbound() *Inbound {
	return &Inbound{users: map[string]option.AnyTLSUser{}, epochs: map[string]uint64{}, sessions: map[net.Conn]*sessionState{}, paddingScheme: padding.DefaultPaddingScheme, logger: log.NewNOPFactory().Logger()}
}
func TestUserBatchPreservesAuthorizationEpochs(t *testing.T) {
	h := batchInbound()
	initial := []option.AnyTLSUser{{Name: "keep", Password: "synthetic-keep", AuditID: "1"}, {Name: "change", Password: "synthetic-before", AuditID: "2"}}
	if e := h.AddUsers(initial); e != nil {
		t.Fatal(e)
	}
	oldService, oldEpochs := h.service, h.epochs
	keep, changed, stale, pending := &userBatchConn{}, &userBatchConn{}, &userBatchConn{}, &userBatchConn{}
	// A callback which reacquires usersMu proves connection closing is outside it.
	changed.onClose = func() { h.usersMu.Lock(); h.usersMu.Unlock() }
	h.sessions[keep] = &sessionState{user: "keep", epochs: oldEpochs}
	h.sessions[changed] = &sessionState{user: "change", epochs: oldEpochs}
	h.sessions[stale] = &sessionState{user: "readded", epochs: map[string]uint64{"readded": 1}}
	h.sessions[pending] = &sessionState{epochs: oldEpochs}
	update := []option.AnyTLSUser{{Name: "keep", Password: "synthetic-keep", AuditID: "99"}, {Name: "change", Password: "synthetic-middle"}, {Name: "change", Password: "synthetic-after"}, {Name: "readded", Password: "synthetic-fresh"}, {Name: "new", Password: "synthetic-new"}}
	if e := h.AddUsers(update); e != nil {
		t.Fatal(e)
	}
	if keep.closed.Load() != 0 || pending.closed.Load() != 0 || changed.closed.Load() != 1 || stale.closed.Load() != 1 {
		t.Fatal("wrong sessions closed or duplicate close")
	}
	if h.service == oldService || h.epochs["keep"] != oldEpochs["keep"] || h.epochs["change"] == oldEpochs["change"] || oldEpochs["new"] != 0 {
		t.Fatal("immutable epoch publication changed")
	}
	if h.users["change"].Password != "synthetic-after" {
		t.Fatal("duplicate input final value changed")
	}
	if id, _ := h.auditUsers.Load("keep"); id != "99" {
		t.Fatal("audit-only update lost")
	}
	// A pending old snapshot must not authorize the changed or newly added identity.
	for _, name := range []string{"change", "new", "readded"} {
		if oldEpochs[name] == h.epochs[name] {
			t.Fatal("stale authentication retained", name)
		}
	}
}
func TestUserBatchFailedPublishLeavesActiveState(t *testing.T) {
	h := batchInbound()
	if e := h.AddUsers([]option.AnyTLSUser{{Name: "keep", Password: "synthetic", AuditID: "7"}}); e != nil {
		t.Fatal(e)
	}
	service, epochs := h.service, h.epochs
	conn := &userBatchConn{}
	h.sessions[conn] = &sessionState{user: "keep"}
	h.paddingScheme = []byte("invalid")
	if e := h.AddUsers([]option.AnyTLSUser{{Name: "keep", Password: "changed", AuditID: "9"}}); e == nil {
		t.Fatal("expected publish failure")
	}
	if h.service != service || h.users["keep"].Password != "synthetic" || h.epochs["keep"] != epochs["keep"] || conn.closed.Load() != 0 {
		t.Fatal("failed publication changed authorization")
	}
	if id, _ := h.auditUsers.Load("keep"); id != "7" {
		t.Fatal("failed publication changed audit identity")
	}
}
