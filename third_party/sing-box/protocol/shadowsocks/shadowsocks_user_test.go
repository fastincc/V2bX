package shadowsocks

import (
	"context"
	"encoding/base64"
	"fmt"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	"github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	"sync"
	"testing"
)

func testInbound(t testing.TB) *MultiInbound {
	t.Helper()
	s, e := shadowaead.NewMultiService[userIdentity]("aes-128-gcm", 60, nil)
	if e != nil {
		t.Fatal(e)
	}
	return &MultiInbound{service: s}
}
func identityFor(h *MultiInbound, name string) userIdentity {
	h.usersMu.RLock()
	defer h.usersMu.RUnlock()
	for id := range h.identities {
		if id.Name == name {
			return id
		}
	}
	return userIdentity{}
}
func TestUserSnapshotStableIdentityAndRevocation(t *testing.T) {
	h := testInbound(t)
	if e := h.AddUsers([]option.ShadowsocksUser{{Name: "a", Password: "synthetic-a"}, {Name: "b", Password: "synthetic-b"}, {Name: "c", Password: "synthetic-c"}}); e != nil {
		t.Fatal(e)
	}
	a, b, c := identityFor(h, "a"), identityFor(h, "b"), identityFor(h, "c")
	if e := h.DelUsers([]string{"b"}); e != nil {
		t.Fatal(e)
	}
	if h.authorized(b) || !h.authorized(a) || !h.authorized(c) || identityFor(h, "c") != c {
		t.Fatal("identity moved or revocation failed")
	}
	if e := h.AddUsers([]option.ShadowsocksUser{{Name: "b", Password: "synthetic-b"}}); e != nil {
		t.Fatal(e)
	}
	if h.authorized(b) || identityFor(h, "b") == b {
		t.Fatal("old handshake reactivated")
	}
	if e := h.AddUsers([]option.ShadowsocksUser{{Name: "c", Password: "changed"}}); e != nil {
		t.Fatal(e)
	}
	if h.authorized(c) {
		t.Fatal("old credentials retained")
	}
}
func TestUserSnapshotInvalidUpdateRetainsAllState(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("synthetic-key-16" + "x"))
	s, e := shadowaead_2022.NewMultiServiceWithPassword[userIdentity]("2022-blake3-aes-128-gcm", key, 60, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	h := &MultiInbound{service: s}
	if e = h.UpdateUsers([]string{"a"}, []string{key}); e != nil {
		t.Fatal(e)
	}
	id := identityFor(h, "a")
	if e = h.UpdateUsers([]string{"a", "b"}, []string{key, "not-base64!"}); e == nil {
		t.Fatal("invalid accepted")
	}
	if !h.authorized(id) || len(h.users) != 1 {
		t.Fatal("failed update mutated published state")
	}
}
func TestUserSnapshotConcurrentPackets(t *testing.T) {
	h := testInbound(t)
	if e := h.AddUsers([]option.ShadowsocksUser{{Name: "a", Password: "synthetic"}}); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 10000; i++ {
			_ = h.AddUsers([]option.ShadowsocksUser{{Name: "b", Password: "synthetic-b"}})
			_ = h.DelUsers([]string{"b"})
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 20000; i++ {
			b := buf.New()
			_ = h.service.NewPacket(context.Background(), nil, b, M.Metadata{})
			b.Release()
			id := identityFor(h, "a")
			if !h.authorized(id) {
				t.Error("retained identity lost")
				return
			}
		}
	}()
	wg.Wait()
}
func BenchmarkUserSnapshotUpdate1000(b *testing.B) {
	h := testInbound(b)
	users := make([]option.ShadowsocksUser, 1000)
	for i := range users {
		users[i] = option.ShadowsocksUser{Name: fmt.Sprint(i), Password: fmt.Sprintf("synthetic-%d", i)}
	}
	b.ReportAllocs()
	for i := 0; i < b.N; i++ {
		if e := h.AddUsers(users); e != nil {
			b.Fatal(e)
		}
	}
}

func TestUserSnapshot2022ConcurrentInvalidPackets(t *testing.T) {
	key := base64.StdEncoding.EncodeToString([]byte("synthetic-key-16x"))
	service, e := shadowaead_2022.NewMultiServiceWithPassword[userIdentity]("2022-blake3-aes-128-gcm", key, 60, nil, nil)
	if e != nil {
		t.Fatal(e)
	}
	h := &MultiInbound{service: service}
	if e = h.UpdateUsers([]string{"a"}, []string{key}); e != nil {
		t.Fatal(e)
	}
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		for i := 0; i < 10000; i++ {
			if e := h.UpdateUsers([]string{"a"}, []string{key}); e != nil {
				t.Error(e)
				return
			}
		}
	}()
	go func() {
		defer wg.Done()
		for i := 0; i < 10000; i++ {
			b := buf.New()
			b.Write(make([]byte, 64))
			_ = h.service.NewPacket(context.Background(), nil, b, M.Metadata{})
			b.Release()
		}
	}()
	wg.Wait()
}

func TestUserSnapshotFinalAdmissionRejectsRetiredHandshake(t *testing.T) {
	h := testInbound(t)
	u := []option.ShadowsocksUser{{Name: "a", Password: "synthetic"}}
	if e := h.AddUsers(u); e != nil {
		t.Fatal(e)
	}
	old := h.authenticatedContext(context.Background(), identityFor(h, "a"))
	if !UserAuthorized(old) {
		t.Fatal("valid handshake denied")
	}
	if e := h.DelUsers([]string{"a"}); e != nil {
		t.Fatal(e)
	}
	if e := h.AddUsers(u); e != nil {
		t.Fatal(e)
	}
	if UserAuthorized(old) {
		t.Fatal("retired handshake acquired replacement identity")
	}
	if !UserAuthorized(h.authenticatedContext(context.Background(), identityFor(h, "a"))) {
		t.Fatal("new identity denied")
	}
	if !UserAuthorized(context.Background()) {
		t.Fatal("unrelated protocol affected")
	}
}
