package anytls

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"os"
	"strconv"
	"sync"
	"sync/atomic"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
)

var auditInstance struct {
	once       sync.Once
	id         string
	err        error
	transports atomic.Uint64
}

func initAuditInstance() error {
	auditInstance.once.Do(func() {
		var b [16]byte
		_, auditInstance.err = rand.Read(b[:])
		if auditInstance.err != nil {
			return
		}
		b[6] = (b[6] & 15) | 64
		b[8] = (b[8] & 63) | 128
		auditInstance.id = fmt.Sprintf("%x-%x-%x-%x-%x", b[0:4], b[4:6], b[6:8], b[8:10], b[10:16])
	})
	return auditInstance.err
}

type auditTransportKey struct{}
type auditStreamKey struct{}
type auditTransport struct {
	id            string
	client, relay M.Socksaddr
	source        string
	trusted       bool
	streams       atomic.Uint64
}
type auditStream struct {
	transport *auditTransport
	id        string
	user      *string
	access    sync.Once
	failure   sync.Once
	mu        sync.Mutex
	owner     *Inbound
	event     auditEvent
	emitted   bool
}

func newAuditTransport(peer M.Socksaddr) *auditTransport {
	return &auditTransport{id: strconv.FormatUint(auditInstance.transports.Add(1), 10), client: peer, source: "direct"}
}
func (h *Inbound) setAuditUser(name, id string) {
	n, err := strconv.ParseUint(id, 10, 64)
	if err != nil || n == 0 || strconv.FormatUint(n, 10) != id {
		h.auditUsers.Delete(name)
		return
	}
	h.auditUsers.Store(name, id)
}
func (h *Inbound) newAuditStream(ctx context.Context, user string) context.Context {
	tr, _ := ctx.Value(auditTransportKey{}).(*auditTransport)
	if tr == nil {
		return ctx
	}
	st := &auditStream{owner: h, transport: tr, id: strconv.FormatUint(tr.streams.Add(1), 10)}
	if id, ok := h.auditUsers.Load(user); ok {
		s := id.(string)
		st.user = &s
	}
	return context.WithValue(ctx, auditStreamKey{}, st)
}

// Never replace the internal authentication/limiter/counter key. Only use this
// helper at log call sites. Unmapped identities never fall back to a password.
func AuditUserLabel(ctx context.Context, legacy string) string {
	st, _ := ctx.Value(auditStreamKey{}).(*auditStream)
	if st == nil {
		return legacy
	}
	id := "unmapped"
	if st.user != nil {
		id = *st.user
	}
	return "user_id=" + id + " instance_id=" + auditInstance.id + " transport_id=" + st.transport.id + " stream_id=" + st.id
}

type auditEvent struct {
	V              int     `json:"v"`
	Event          string  `json:"event"`
	Authenticated  bool    `json:"authenticated"`
	UserID         *string `json:"user_id"`
	InstanceID     string  `json:"instance_id"`
	TransportID    string  `json:"transport_id"`
	StreamID       string  `json:"stream_id"`
	Network        string  `json:"network"`
	TargetHost     *string `json:"target_host"`
	TargetPort     *uint16 `json:"target_port"`
	ClientIP       *string `json:"client_ip"`
	ClientPort     *uint16 `json:"client_port"`
	RelayIP        *string `json:"relay_ip"`
	RelayPort      *uint16 `json:"relay_port"`
	ClientIPSource string  `json:"client_ip_source"`
	ProxyTrusted   bool    `json:"proxy_trusted"`
}

func auditAddress(a M.Socksaddr) (*string, *uint16) {
	var host *string
	var port *uint16
	if a.Fqdn != "" {
		s := a.Fqdn
		host = &s
	} else if a.Addr.IsValid() && !a.Addr.IsUnspecified() {
		s := a.Addr.Unmap().String()
		host = &s
	}
	if a.Port != 0 {
		n := a.Port
		port = &n
	}
	return host, port
}
func auditSnapshot(st *auditStream, m adapter.InboundContext, network string) auditEvent {
	e := auditEvent{V: 1, Event: "access", Authenticated: true, UserID: st.user, InstanceID: auditInstance.id, TransportID: st.transport.id, StreamID: st.id, Network: network, ClientIPSource: st.transport.source, ProxyTrusted: st.transport.trusted}
	e.TargetHost, e.TargetPort = auditAddress(m.Destination)
	e.ClientIP, e.ClientPort = auditAddress(st.transport.client)
	if st.transport.source == "proxy_protocol" {
		e.RelayIP, e.RelayPort = auditAddress(st.transport.relay)
	}
	return e
}
func (st *auditStream) emitError(ctx context.Context, stage, code string) {
	st.failure.Do(func() {
		st.mu.Lock()
		e := struct {
			auditEvent
			Stage         string `json:"stage"`
			Code          string `json:"code"`
			AccessEmitted bool   `json:"access_emitted"`
		}{st.event, stage, code, st.emitted}
		st.mu.Unlock()
		e.Event = "error"
		if data, err := json.Marshal(e); err == nil {
			st.owner.logger.ErrorContext(ctx, "AUDIT_ERROR ", string(data))
		}
	})
}

// Called only from existing denial branches; it neither rejects nor closes a
// connection. With no authenticated audit context it is a no-op.
func AuditPolicyDenied(ctx context.Context, code string) {
	switch code {
	case "policy_domain_denied", "policy_protocol_denied", "policy_limit_denied":
	default:
		return
	}
	if st, _ := ctx.Value(auditStreamKey{}).(*auditStream); st != nil {
		st.emitError(ctx, "policy", code)
	}
}
func (h *Inbound) auditUoTError(ctx context.Context, _ adapter.InboundContext, readBytes int, err error) {
	if readBytes == 0 && !auditStreamFailure(err) {
		return
	}
	st, _ := ctx.Value(auditStreamKey{}).(*auditStream)
	if st == nil {
		return
	}
	st.mu.Lock()
	if !st.emitted {
		st.event = auditSnapshot(st, adapter.InboundContext{}, "udp")
	}
	st.mu.Unlock()
	st.emitError(ctx, "protocol", "uot_request_invalid")
}
func (h *Inbound) auditAccess(ctx context.Context, m adapter.InboundContext, network string, onClose N.CloseHandlerFunc) N.CloseHandlerFunc {
	st, _ := ctx.Value(auditStreamKey{}).(*auditStream)
	if st == nil {
		return onClose
	}
	st.access.Do(func() {
		st.mu.Lock()
		defer st.mu.Unlock()
		st.event = auditSnapshot(st, m, network)
		if data, err := json.Marshal(st.event); err == nil {
			h.logger.InfoContext(ctx, "AUDIT_ACCESS ", string(data))
			st.emitted = true
		}
	})
	return N.AppendClose(onClose, func(err error) {
		if auditStreamFailure(err) {
			st.emitError(ctx, "stream", "stream_closed_error")
		}
	})
}

// UoT decoding precedes this wrapper, so TCP tunnel markers never become UDP
// target domains. An unknown legacy/association target is null, not guessed.
type auditRouter struct {
	adapter.Router
	inbound *Inbound
}

func (r *auditRouter) RouteConnectionEx(ctx context.Context, c net.Conn, m adapter.InboundContext, close N.CloseHandlerFunc) {
	close = r.inbound.auditAccess(ctx, m, "tcp", close)
	r.Router.RouteConnectionEx(ctx, c, m, close)
}
func (r *auditRouter) RoutePacketConnectionEx(ctx context.Context, c N.PacketConn, m adapter.InboundContext, close N.CloseHandlerFunc) {
	close = r.inbound.auditAccess(ctx, m, "udp", close)
	r.Router.RoutePacketConnectionEx(ctx, c, m, close)
}

// Only this audited AnyTLS UoT adapter suppresses its legacy access lines.
// Errors and all other logger methods remain unchanged.
type auditUOTLogger struct{ log.ContextLogger }

func (auditUOTLogger) InfoContext(context.Context, ...any) {}

// Normal EOF/explicit close/cancellation is not a failed access. Do not use
// IsClosedOrCanceled: that helper also suppresses timeouts and connection resets.
func auditStreamFailure(err error) bool {
	if err == nil {
		return false
	}
	if multi, ok := err.(interface{ Unwrap() []error }); ok {
		for _, cause := range multi.Unwrap() {
			if auditStreamFailure(cause) {
				return true
			}
		}
		return false
	}
	if cause := errors.Unwrap(err); cause != nil {
		return auditStreamFailure(cause)
	}
	return !errors.Is(err, io.EOF) && !errors.Is(err, net.ErrClosed) && !errors.Is(err, io.ErrClosedPipe) && !errors.Is(err, os.ErrClosed) && !errors.Is(err, context.Canceled)
}
