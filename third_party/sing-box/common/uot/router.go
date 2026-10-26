package uot

import (
	"context"
	"io"
	"net"
	"net/netip"

	"github.com/sagernet/sing-box/adapter"
	E "github.com/sagernet/sing/common/exceptions"
	"github.com/sagernet/sing/common/logger"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/uot"
)

var _ adapter.ConnectionRouterEx = (*Router)(nil)

type Router struct {
	router       adapter.ConnectionRouterEx
	logger       logger.ContextLogger
	requestError func(context.Context, adapter.InboundContext, int, error)
}

func NewRouter(router adapter.ConnectionRouterEx, logger logger.ContextLogger) *Router {
	return &Router{router: router, logger: logger}
}

// Optional observer; set only while constructing an inbound. It cannot alter
// decoding, routing or the original error. Disabled inbounds keep the old path.
func (r *Router) SetRequestErrorHandler(handler func(context.Context, adapter.InboundContext, int, error)) {
	r.requestError = handler
}

type requestReader struct {
	io.Reader
	n int
}

func (r *requestReader) Read(p []byte) (int, error) { n, e := r.Reader.Read(p); r.n += n; return n, e }
func (r *Router) readRequest(ctx context.Context, c net.Conn, m adapter.InboundContext) (*uot.Request, error) {
	if r.requestError == nil {
		return uot.ReadRequest(c)
	}
	reader := &requestReader{Reader: c}
	req, err := uot.ReadRequest(reader)
	if err != nil {
		r.requestError(ctx, m, reader.n, err)
	}
	return req, err
}

func (r *Router) RouteConnection(ctx context.Context, conn net.Conn, metadata adapter.InboundContext) error {
	switch metadata.Destination.Fqdn {
	case uot.MagicAddress:
		request, err := r.readRequest(ctx, conn, metadata)
		if err != nil {
			return E.Cause(err, "read UoT request")
		}
		if request.IsConnect {
			r.logger.InfoContext(ctx, "inbound UoT connect connection to ", request.Destination)
		} else {
			r.logger.InfoContext(ctx, "inbound UoT connection to ", request.Destination)
		}
		metadata.Domain = metadata.Destination.Fqdn
		metadata.Destination = request.Destination
		return r.router.RoutePacketConnection(ctx, uot.NewConn(conn, *request), metadata)
	case uot.LegacyMagicAddress:
		r.logger.InfoContext(ctx, "inbound legacy UoT connection")
		metadata.Domain = metadata.Destination.Fqdn
		metadata.Destination = M.Socksaddr{Addr: netip.IPv4Unspecified()}
		return r.RoutePacketConnection(ctx, uot.NewConn(conn, uot.Request{}), metadata)
	}
	return r.router.RouteConnection(ctx, conn, metadata)
}

func (r *Router) RoutePacketConnection(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext) error {
	return r.router.RoutePacketConnection(ctx, conn, metadata)
}

func (r *Router) RouteConnectionEx(ctx context.Context, conn net.Conn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	switch metadata.Destination.Fqdn {
	case uot.MagicAddress:
		request, err := r.readRequest(ctx, conn, metadata)
		if err != nil {
			err = E.Cause(err, "UoT read request")
			r.logger.ErrorContext(ctx, "process connection from ", metadata.Source, ": ", err)
			N.CloseOnHandshakeFailure(conn, onClose, err)
			return
		}
		if request.IsConnect {
			r.logger.InfoContext(ctx, "inbound UoT connect connection to ", request.Destination)
		} else {
			r.logger.InfoContext(ctx, "inbound UoT connection to ", request.Destination)
		}
		metadata.Domain = metadata.Destination.Fqdn
		metadata.Destination = request.Destination
		r.router.RoutePacketConnectionEx(ctx, uot.NewConn(conn, *request), metadata, onClose)
		return
	case uot.LegacyMagicAddress:
		r.logger.InfoContext(ctx, "inbound legacy UoT connection")
		metadata.Domain = metadata.Destination.Fqdn
		metadata.Destination = M.Socksaddr{Addr: netip.IPv4Unspecified()}
		r.RoutePacketConnectionEx(ctx, uot.NewConn(conn, uot.Request{}), metadata, onClose)
		return
	}
	r.router.RouteConnectionEx(ctx, conn, metadata, onClose)
}

func (r *Router) RoutePacketConnectionEx(ctx context.Context, conn N.PacketConn, metadata adapter.InboundContext, onClose N.CloseHandlerFunc) {
	r.router.RoutePacketConnectionEx(ctx, conn, metadata, onClose)
}
