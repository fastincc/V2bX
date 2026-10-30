package shadowsocks

import (
	"context"
	"net"
	"os"
	"sync"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/adapter/inbound"
	"github.com/sagernet/sing-box/common/listener"
	"github.com/sagernet/sing-box/common/mux"
	"github.com/sagernet/sing-box/common/uot"
	C "github.com/sagernet/sing-box/constant"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	"github.com/sagernet/sing-shadowsocks"
	"github.com/sagernet/sing-shadowsocks/shadowaead"
	"github.com/sagernet/sing-shadowsocks/shadowaead_2022"
	"github.com/sagernet/sing/common"
	"github.com/sagernet/sing/common/auth"
	"github.com/sagernet/sing/common/buf"
	E "github.com/sagernet/sing/common/exceptions"
	F "github.com/sagernet/sing/common/format"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/ntp"
)

var (
	_ adapter.TCPInjectableInbound = (*MultiInbound)(nil)
	_ adapter.ManagedSSMServer     = (*MultiInbound)(nil)
)

type MultiInbound struct {
	inbound.Adapter
	ctx        context.Context
	router     adapter.ConnectionRouterEx
	logger     logger.ContextLogger
	listener   *listener.Listener
	service    shadowsocks.MultiService[userIdentity]
	users      []option.ShadowsocksUser
	tracker    adapter.SSMTracker
	usersMu    sync.RWMutex
	identities map[userIdentity]struct{}
	sequence   uint64
}

func newMultiInbound(ctx context.Context, router adapter.Router, logger log.ContextLogger, tag string, options option.ShadowsocksInboundOptions) (*MultiInbound, error) {
	inbound := &MultiInbound{
		Adapter: inbound.NewAdapter(C.TypeShadowsocks, tag),
		ctx:     ctx,
		router:  uot.NewRouter(router, logger),
		logger:  logger,
	}
	var err error
	inbound.router, err = mux.NewRouterWithOptions(inbound.router, logger, common.PtrValueOrDefault(options.Multiplex))
	if err != nil {
		return nil, err
	}
	var udpTimeout time.Duration
	if options.UDPTimeout != 0 {
		udpTimeout = time.Duration(options.UDPTimeout)
	} else {
		udpTimeout = C.UDPTimeout
	}
	var service shadowsocks.MultiService[userIdentity]
	if common.Contains(shadowaead_2022.List, options.Method) {
		service, err = shadowaead_2022.NewMultiServiceWithPassword[userIdentity](
			options.Method,
			options.Password,
			int64(udpTimeout.Seconds()),
			adapter.NewUpstreamHandler(adapter.InboundContext{}, inbound.newConnection, inbound.newPacketConnection, inbound),
			ntp.TimeFuncFromContext(ctx),
		)
	} else if common.Contains(shadowaead.List, options.Method) {
		service, err = shadowaead.NewMultiService[userIdentity](
			options.Method,
			int64(udpTimeout.Seconds()),
			adapter.NewUpstreamHandler(adapter.InboundContext{}, inbound.newConnection, inbound.newPacketConnection, inbound),
		)
	} else {
		return nil, E.New("unsupported method: " + options.Method)
	}
	if err != nil {
		return nil, err
	}
	inbound.service = service
	if err := inbound.replaceUsers(options.Users); err != nil {
		return nil, err
	}
	inbound.listener = listener.New(listener.Options{
		Context:                  ctx,
		Logger:                   logger,
		Network:                  options.Network.Build(),
		Listen:                   options.ListenOptions,
		ConnectionHandler:        inbound,
		PacketHandler:            inbound,
		ThreadUnsafePacketWriter: true,
	})
	return inbound, err
}

func (h *MultiInbound) Start(stage adapter.StartStage) error {
	if stage != adapter.StartStateStart {
		return nil
	}
	return h.listener.Start()
}

func (h *MultiInbound) Close() error {
	return h.listener.Close()
}

func (h *MultiInbound) SetTracker(tracker adapter.SSMTracker) {
	h.tracker = tracker
}

func (h *MultiInbound) UpdateUsers(users []string, uPSKs []string) error {
	if len(users) != len(uPSKs) {
		return os.ErrInvalid
	}
	candidate := make([]option.ShadowsocksUser, len(users))
	for i := range users {
		candidate[i] = option.ShadowsocksUser{Name: users[i], Password: uPSKs[i]}
	}
	h.usersMu.Lock()
	defer h.usersMu.Unlock()
	return h.replaceUsers(candidate)
}

//nolint:staticcheck
func (h *MultiInbound) NewConnectionEx(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	err := h.service.NewConnection(ctx, conn, adapter.UpstreamMetadata(metadata))
	N.CloseOnHandshakeFailure(conn, onClose, err)
	if err != nil {
		if E.IsClosedOrCanceled(err) {
			h.logger.DebugContext(ctx, "connection closed: ", err)
		} else {
			h.logger.ErrorContext(ctx, E.Cause(err, "process connection from ", metadata.Source))
		}
	}
}

//nolint:staticcheck
func (h *MultiInbound) NewPacketEx(buffer *buf.Buffer, source M.Socksaddr) {
	err := h.service.NewPacket(h.ctx, &stubPacketConn{h.listener.PacketWriter()}, buffer, M.Metadata{Source: source})
	if err != nil {
		h.logger.Error(E.Cause(err, "process packet from ", source))
	}
}

func (h *MultiInbound) newConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext) error {
	identity, loaded := auth.UserFromContext[userIdentity](ctx)
	if !loaded || !h.authorized(identity) {
		return os.ErrPermission
	}
	ctx = h.authenticatedContext(ctx, identity)
	user := identity.Name
	if user == "" {
		user = F.ToString(identity.Index)
	} else {
		metadata.User = user
	}
	h.logger.InfoContext(ctx, "[", user, "] inbound connection to ", metadata.Destination)
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	//nolint:staticcheck
	metadata.InboundDetour = h.listener.ListenOptions().Detour
	//nolint:staticcheck
	metadata.InboundOptions = h.listener.ListenOptions().InboundOptions
	if h.tracker != nil {
		conn = h.tracker.TrackConnection(conn, metadata)
	}
	return h.router.RouteConnection(ctx, conn, metadata)
}

func (h *MultiInbound) newPacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext) error {
	identity, loaded := auth.UserFromContext[userIdentity](ctx)
	if !loaded || !h.authorized(identity) {
		return os.ErrPermission
	}
	ctx = h.authenticatedContext(ctx, identity)
	user := identity.Name
	if user == "" {
		user = F.ToString(identity.Index)
	} else {
		metadata.User = user
	}
	ctx = log.ContextWithNewID(ctx)
	h.logger.InfoContext(ctx, "[", user, "] inbound packet connection from ", metadata.Source)
	h.logger.InfoContext(ctx, "[", user, "] inbound packet connection to ", metadata.Destination)
	metadata.Inbound = h.Tag()
	metadata.InboundType = h.Type()
	//nolint:staticcheck
	metadata.InboundDetour = h.listener.ListenOptions().Detour
	//nolint:staticcheck
	metadata.InboundOptions = h.listener.ListenOptions().InboundOptions
	if h.tracker != nil {
		conn = h.tracker.TrackPacketConnection(conn, metadata)
	}
	return h.router.RoutePacketConnection(ctx, conn, metadata)
}

//nolint:staticcheck
func (h *MultiInbound) NewError(ctx context.Context, err error) {
	NewError(h.logger, ctx, err)
}
