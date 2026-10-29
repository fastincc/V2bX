package anytls

import (
	anytls "github.com/anytls/sing-anytls"
	"github.com/sagernet/sing-box/option"
	"maps"
	"net"
)

type sessionStateKey struct{}
type sessionState struct {
	conn   net.Conn
	user   string            // protected by Inbound.usersMu
	epochs map[string]uint64 // immutable authentication snapshot
}

// Publish a fresh service instead of racing sing-anytls UpdateUsers with handshakes.
func (h *Inbound) publishUsers(users map[string]option.AnyTLSUser) error {
	list := make([]anytls.User, 0, len(users))
	for _, user := range users {
		list = append(list, anytls.User{Name: user.Name, Password: user.Password})
	}
	service, err := anytls.NewService(anytls.ServiceConfig{Users: list, PaddingScheme: h.paddingScheme, Handler: (*inboundHandler)(h), Logger: h.logger})
	if err != nil {
		return err
	}
	h.service = service
	h.users = users
	return nil
}

func (h *Inbound) AddUsers(users []option.AnyTLSUser) error {
	h.usersMu.Lock()
	next, epochs := maps.Clone(h.users), maps.Clone(h.epochs)
	var closeConns []net.Conn
	for _, user := range users {
		previous, exists := next[user.Name]
		if !exists || previous.Password != user.Password {
			h.nextEpoch++
			epochs[user.Name] = h.nextEpoch
			for conn, state := range h.sessions {
				if state.user == user.Name {
					closeConns = append(closeConns, conn)
				}
			}
		}
		next[user.Name] = user
	}
	if err := h.publishUsers(next); err != nil {
		h.usersMu.Unlock()
		return err
	}
	h.epochs = epochs
	for _, user := range users {
		h.setAuditUser(user.Name, user.AuditID)
	}
	h.usersMu.Unlock()
	for _, conn := range closeConns {
		_ = conn.Close()
	}
	return nil
}

func (h *Inbound) DelUsers(names []string) error {
	if len(names) == 0 {
		return nil
	}
	h.usersMu.Lock()
	next, epochs := maps.Clone(h.users), maps.Clone(h.epochs)
	remove := make(map[string]struct{}, len(names))
	for _, name := range names {
		delete(next, name)
		delete(epochs, name)
		remove[name] = struct{}{}
	}
	if err := h.publishUsers(next); err != nil {
		h.usersMu.Unlock()
		return err
	}
	h.epochs = epochs
	var closeConns []net.Conn
	for conn, state := range h.sessions {
		_, revoked := remove[state.user]
		// Pending authentication has no trusted identity yet. Close it on revocation;
		// it can reconnect against the current authorization snapshot.
		if revoked || state.user == "" {
			closeConns = append(closeConns, conn)
		}
	}
	for _, name := range names {
		h.auditUsers.Delete(name)
	}
	h.usersMu.Unlock()
	for _, conn := range closeConns {
		_ = conn.Close()
	}
	return nil
}
