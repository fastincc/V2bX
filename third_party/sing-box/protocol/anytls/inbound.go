package anytls

import (
	"context"
	"net"
	"strings"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/tls"
	"github.com/sagernet/sing-box/common/trustedproxy"
	"github.com/sagernet/sing-box/common/uot"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"

	anytls "github.com/anytls/sing-anytls"
	"github.com/anytls/sing-anytls/padding"
)

func RegisterInbound(registry *inbound.Registry) {
	inbound.Register[option.AnyTLSInboundOptions](registry, C.TypeAnyTLS, NewInbound)
}

type Inbound struct {
	inbound.Adapter
	tlsConfig         tls.ServerConfig
	router            adapter.ConnectionRouterEx
	logger            logger.ContextLogger
	listener          *listener.Listener
	service           *anytls.Service
	usersMu           sync.Mutex
	users             map[string]option.AnyTLSUser
	epochs            map[string]uint64
	nextEpoch         uint64
	sessions          map[net.Conn]*sessionState
	paddingScheme     []byte
	closed            bool
	proxyPolicy       *trustedproxy.Policy
	auditEnabled      bool
	auditProxyTrusted bool
	auditUsers        sync.Map
}

func NewInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.AnyTLSInboundOptions) (adapter.Inbound, error) {
	inbound := &Inbound{
		Adapter: inbound.NewAdapter(C.TypeAnyTLS, tag),
		router:  uot.NewRouter(router, logger),
		logger:  logger,
	}

	inbound.auditEnabled = options.AuditAccess
	if inbound.auditEnabled {
		if err := initAuditInstance(); err != nil {
			return nil, err
		}
		auditUoT := uot.NewRouter(&auditRouter{Router: router, inbound: inbound}, auditUOTLogger{logger})
		auditUoT.SetRequestErrorHandler(inbound.auditUoTError)
		inbound.router = auditUoT
		for _, user := range options.Users {
			inbound.setAuditUser(user.Name, user.AuditID)
		}
	}
	if proxy := options.TrustedProxyProtocol; proxy != nil && proxy.Enabled {
		policy, err := trustedproxy.NewWithTrust(proxy.TrustedProxies, time.Duration(proxy.HeaderTimeout), proxy.Version, proxy.RequireTrustedProxy)
		if err != nil {
			return nil, E.Cause(err, "invalid AnyTLS trusted proxy configuration")
		}
		inbound.proxyPolicy = policy
		inbound.auditProxyTrusted = len(proxy.TrustedProxies) > 0
	}

	if options.TLS != nil && options.TLS.Enabled {
		tlsConfig, err := tls.NewServer(ctx, logger, common.PtrValueOrDefault(options.TLS))
		if err != nil {
			return nil, err
		}
		inbound.tlsConfig = tlsConfig
	}

	paddingScheme := padding.DefaultPaddingScheme
	if len(options.PaddingScheme) > 0 {
		paddingScheme = []byte(strings.Join(options.PaddingScheme, "\n"))
	}

	inbound.paddingScheme = paddingScheme
	inbound.users = make(map[string]option.AnyTLSUser)
	inbound.epochs = make(map[string]uint64)
	inbound.sessions = make(map[net.Conn]*sessionState)
	if err := inbound.AddUsers(options.Users); err != nil {
		return nil, err
	}

	inbound.listener = listener.New(listener.Options{
		Context:           ctx,
		Logger:            logger,
		Network:           []string{N.NetworkTCP},
		Listen:            options.ListenOptions,
		ConnectionHandler: inbound,
	})

	return inbound, nil
}

func (h *Inbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	if h.tlsConfig != nil {
		err := h.tlsConfig.Start()
		if err != nil {
			return err
		}
	}
	return h.listener.Start()
}

func (h *Inbound) Close() error {
	h.usersMu.Lock()
	h.closed = true
	var connections []net.Conn
	for conn := range h.sessions {
		connections = append(connections, conn)
	}
	h.usersMu.Unlock()
	for _, conn := range connections {
		_ = conn.Close()
	}
	return common.Close(h.listener, h.tlsConfig)
}

func (h *Inbound) NewConnectionEx(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	var audit *auditTransport
	if h.auditEnabled {
		audit = newAuditTransport(M.SocksaddrFromNet(conn.RemoteAddr()).Unwrap())
	}
	if h.proxyPolicy != nil {
		relayAddress := M.SocksaddrFromNet(conn.RemoteAddr()).Unwrap()
		proxyConn, err := h.proxyPolicy.Accept(conn)
		if err != nil {
			N.CloseOnHandshakeFailure(conn, onClose, err)
			h.logger.ErrorContext(ctx, E.Cause(err, "process connection from ", metadata.Source, ": PROXY handshake"))
			return
		}
		conn = proxyConn
		metadata.Source = M.SocksaddrFromNet(conn.RemoteAddr()).Unwrap()
		if audit != nil {
			audit.source = "proxy_protocol"
			audit.client = metadata.Source
			audit.relay = relayAddress
			audit.trusted = h.auditProxyTrusted
		}
		// The header declares an address, not an authenticated identity. Log once
		// per physical connection, without credentials or per-packet overhead.
		h.logger.InfoContext(ctx, "AnyTLS PROXY addresses: relay=", relayAddress, " client=", metadata.Source, " (declared; authentication pending)")
	}
	if h.tlsConfig != nil {
		tlsConn, err := tls.ServerHandshake(ctx, conn, h.tlsConfig)
		if err != nil {
			N.CloseOnHandshakeFailure(conn, onClose, err)
			h.logger.ErrorContext(ctx, E.Cause(err, "process connection from ", metadata.Source, ": TLS handshake"))
			return
		}
		conn = tlsConn
	}
	if audit != nil {
		ctx = context.WithValue(ctx, auditTransportKey{}, audit)
	}

	h.usersMu.Lock()
	if h.closed {
		h.usersMu.Unlock()
		N.CloseOnHandshakeFailure(conn, onClose, net.ErrClosed)
		return
	}
	state := &sessionState{conn: conn, epochs: h.epochs}
	h.sessions[conn] = state
	service := h.service
	h.usersMu.Unlock()
	// A logical stream closing must not unregister the multiplexed transport.
	defer func() { h.usersMu.Lock(); delete(h.sessions, conn); h.usersMu.Unlock(); _ = conn.Close() }()
	ctx = context.WithValue(ctx, sessionStateKey{}, state)
	err := service.NewConnection(adapter.WithContext(ctx, &metadata), conn, metadata.Source, onClose)

	if err != nil {
		N.CloseOnHandshakeFailure(conn, onClose, err)
		h.logger.ErrorContext(ctx, E.Cause(err, "process connection from ", metadata.Source))
	}
}

type inboundHandler Inbound

func (h *inboundHandler) NewConnectionEx(ctx context.Context, conn net.Conn, source M.Socksaddr, destination M.Socksaddr, onClose N.CloseHandlerFunc) {
	var metadata adapter.InboundContext
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	//nolint:staticcheck
	metadata.InboundDetour = h.listener.ListenOptions().Detour
	//nolint:staticcheck
	metadata.InboundOptions = h.listener.ListenOptions().InboundOptions
	metadata.Source = source
	metadata.Destination = destination.Unwrap()
	if userName, _ := auth.UserFromContext[string](ctx); userName != "" {
		inbound := (*Inbound)(h)
		state, _ := ctx.Value(sessionStateKey{}).(*sessionState)
		inbound.usersMu.Lock()
		epoch, exists := inbound.epochs[userName]
		allowed := state != nil && !inbound.closed && exists && state.epochs[userName] == epoch
		if allowed {
			state.user = userName
		}
		inbound.usersMu.Unlock()
		if !allowed {
			N.CloseOnHandshakeFailure(conn, onClose, net.ErrClosed)
			if state != nil {
				_ = state.conn.Close()
			}
			return
		}
		metadata.User = userName
		if h.auditEnabled {
			ctx = (*Inbound)(h).newAuditStream(ctx, userName)
		} else {
			h.logger.InfoContext(ctx, "[", userName, "] inbound connection to ", metadata.Destination)
		}
	} else {
		h.logger.InfoContext(ctx, "inbound connection to ", metadata.Destination)
	}
	h.router.RouteConnectionEx(ctx, conn, metadata, onClose)
}
