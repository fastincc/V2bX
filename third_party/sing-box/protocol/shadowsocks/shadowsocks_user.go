package shadowsocks

import (
	"context"
	"fmt"
	"github.com/sagernet/sing-box/option"
)

// An identity is never recycled after removal, even for the same name/password.
// An in-flight handshake must not index a newer, reordered users slice.
type userIdentity struct {
	Name       string
	Index      int
	Generation uint64
}

func (h *MultiInbound) authorized(id userIdentity) bool {
	h.usersMu.RLock()
	defer h.usersMu.RUnlock()
	_, ok := h.identities[id]
	return ok
}

// Caller holds usersMu (or the inbound has not been published yet).
func (h *MultiInbound) replaceUsers(users []option.ShadowsocksUser) error {
	candidate := append([]option.ShadowsocksUser(nil), users...)
	ids := make([]userIdentity, len(candidate))
	passwords := make([]string, len(candidate))
	current := make(map[userIdentity]struct{}, len(candidate))
	old := make(map[string]int, len(h.users))
	for i, u := range h.users {
		if u.Name != "" {
			old[u.Name] = i
		}
	}
	identityByName := make(map[string]userIdentity, len(h.identities))
	identityByIndex := make(map[int]userIdentity)
	for id := range h.identities {
		if id.Name != "" {
			identityByName[id.Name] = id
		} else {
			identityByIndex[id.Index] = id
		}
	}
	seen := make(map[string]bool)
	for i, u := range candidate {
		if u.Name != "" && seen[u.Name] {
			return fmt.Errorf("duplicate shadowsocks user name")
		}
		seen[u.Name] = true
		previous := -1
		if u.Name != "" {
			if index, ok := old[u.Name]; ok {
				previous = index
			}
		} else if i < len(h.users) && h.users[i].Name == "" {
			previous = i
		}
		var id userIdentity
		if previous >= 0 && h.users[previous].Password == u.Password {
			if u.Name != "" {
				id = identityByName[u.Name]
			} else {
				id = identityByIndex[previous]
			}
		}
		if id.Generation == 0 {
			h.sequence++
			id = userIdentity{Name: u.Name, Index: i, Generation: h.sequence}
		}
		ids[i] = id
		passwords[i] = u.Password
		current[id] = struct{}{}
	}
	// Readers validate identities under usersMu after authentication; publication
	// of service tables and identities is therefore one admission transition.
	if err := h.service.UpdateUsersWithPasswords(ids, passwords); err != nil {
		return err
	}
	h.users = candidate
	h.identities = current
	return nil
}
func (h *MultiInbound) AddUsers(users []option.ShadowsocksUser) error {
	h.usersMu.Lock()
	defer h.usersMu.Unlock()
	candidate := append([]option.ShadowsocksUser(nil), h.users...)
	indexes := make(map[string]int, len(candidate))
	for i, u := range candidate {
		if u.Name != "" {
			indexes[u.Name] = i
		}
	}
	for _, u := range users {
		if i, ok := indexes[u.Name]; ok && u.Name != "" {
			candidate[i] = u
		} else {
			if u.Name != "" {
				indexes[u.Name] = len(candidate)
			}
			candidate = append(candidate, u)
		}
	}
	return h.replaceUsers(candidate)
}
func (h *MultiInbound) DelUsers(names []string) error {
	h.usersMu.Lock()
	defer h.usersMu.Unlock()
	deleted := make(map[string]bool, len(names))
	for _, n := range names {
		deleted[n] = true
	}
	candidate := make([]option.ShadowsocksUser, 0, len(h.users))
	for _, u := range h.users {
		if !deleted[u.Name] {
			candidate = append(candidate, u)
		}
	}
	return h.replaceUsers(candidate)
}

type admissionContextKey struct{}
type userAdmission struct {
	inbound  *MultiInbound
	identity userIdentity
}

func (h *MultiInbound) authenticatedContext(ctx context.Context, id userIdentity) context.Context {
	return context.WithValue(ctx, admissionContextKey{}, userAdmission{h, id})
}

// UserAuthorized revalidates a handshake at the accounting/admission boundary.
// Call while holding the core user lock, in the same lock order as updates.
// Non-Shadowsocks contexts keep their existing admission behavior.
func UserAuthorized(ctx context.Context) bool {
	a, ok := ctx.Value(admissionContextKey{}).(userAdmission)
	return !ok || a.inbound.authorized(a.identity)
}
