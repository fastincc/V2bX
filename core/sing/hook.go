package sing

import (
	"context"
	"fmt"
	"net"
	"sync"

	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/common/rate"

	"github.com/InazumaV/V2bX/limiter"

	"github.com/InazumaV/V2bX/common/counter"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/protocol/anytls"
	"github.com/sagernet/sing-box/protocol/shadowsocks"
	N "github.com/sagernet/sing/common/network"
)

var _ adapter.ConnectionTracker = (*HookServer)(nil)

type HookServer struct {
	users   *UserMap
	counter sync.Map //map[string]*counter.TrafficCounter
}

func (h *HookServer) ModeList() []string {
	return nil
}

func (h *HookServer) RoutedConnection(ctx context.Context, conn net.Conn, m adapter.InboundContext, _ adapter.Rule, _ adapter.Outbound) net.Conn {
	l, err := limiter.GetLimiter(m.Inbound)
	if err != nil {
		log.Warn("get limiter for ", m.Inbound, " error: ", err)
		return conn
	}
	logUser := anytls.AuditUserLabel(ctx, m.User)
	taguuid := format.UserTag(m.Inbound, m.User)
	ip := m.Source.Addr.Unmap().String()
	if b, r := l.CheckLimit(taguuid, ip, true, true); r {
		anytls.AuditPolicyDenied(ctx, "policy_limit_denied")
		conn.Close()
		log.Error("[", m.Inbound, "] ", "Limited ", logUser, " by ip or conn")
		return conn
	} else if b != nil {
		conn = rate.NewConnRateLimiter(conn, b)
	}
	conn = l.TrackDeviceConn(conn, taguuid, ip)
	if l != nil {
		destStr := m.Destination.AddrString()
		protocol := m.Protocol
		if l.CheckDomainRule(destStr) {
			anytls.AuditPolicyDenied(ctx, "policy_domain_denied")
			log.Error(fmt.Sprintf(
				"User %s access domain %s reject by rule",
				logUser,
				destStr))
			conn.Close()
			return conn
		}
		if len(protocol) != 0 {
			if l.CheckProtocolRule(protocol) {
				anytls.AuditPolicyDenied(ctx, "policy_protocol_denied")
				log.Error(fmt.Sprintf(
					"User %s access protocol %s reject by rule",
					logUser,
					protocol))
				conn.Close()
				return conn
			}
		}
	}
	if h.users != nil {
		h.users.mapLock.RLock()
		defer h.users.mapLock.RUnlock()
	}
	if !shadowsocks.UserAuthorized(ctx) {
		conn.Close()
		return conn
	}
	key := m.User
	if h.users != nil {
		var ok bool
		key, ok = h.users.active[userKey{m.Inbound, m.User}]
		if !ok {
			conn.Close()
			return conn
		}
	}
	t := counter.LoadOrCreate(&h.counter, m.Inbound)
	conn = counter.NewLeasedConn(conn, t.GetCounter(key))
	return conn
}

func (h *HookServer) RoutedPacketConnection(ctx context.Context, conn N.PacketConn, m adapter.InboundContext, _ adapter.Rule, _ adapter.Outbound) N.PacketConn {
	l, err := limiter.GetLimiter(m.Inbound)
	if err != nil {
		log.Warn("get limiter for ", m.Inbound, " error: ", err)
		return conn
	}
	ip := m.Source.Addr.Unmap().String()
	logUser := anytls.AuditUserLabel(ctx, m.User)
	taguuid := format.UserTag(m.Inbound, m.User)
	if b, r := l.CheckLimit(taguuid, ip, false, true); r {
		anytls.AuditPolicyDenied(ctx, "policy_limit_denied")
		conn.Close()
		log.Error("[", m.Inbound, "] ", "Limited ", logUser, " by ip or conn")
		return conn
	} else if b != nil {
		conn = rate.NewPacketConnRateLimiter(conn, b)
	}
	conn = &devicePacketConn{PacketConn: conn, release: l.TrackDevice(taguuid, ip)}
	if l != nil {
		destStr := m.Destination.AddrString()
		protocol := m.Destination.Network()
		if l.CheckDomainRule(destStr) {
			anytls.AuditPolicyDenied(ctx, "policy_domain_denied")
			log.Error(fmt.Sprintf(
				"User %s access domain %s reject by rule",
				logUser,
				destStr))
			conn.Close()
			return conn
		}
		if len(protocol) != 0 {
			if l.CheckProtocolRule(protocol) {
				anytls.AuditPolicyDenied(ctx, "policy_protocol_denied")
				log.Error(fmt.Sprintf(
					"User %s access protocol %s reject by rule",
					logUser,
					protocol))
				conn.Close()
				return conn
			}
		}
	}
	if h.users != nil {
		h.users.mapLock.RLock()
		defer h.users.mapLock.RUnlock()
	}
	if !shadowsocks.UserAuthorized(ctx) {
		conn.Close()
		return conn
	}
	key := m.User
	if h.users != nil {
		var ok bool
		key, ok = h.users.active[userKey{m.Inbound, m.User}]
		if !ok {
			conn.Close()
			return conn
		}
	}
	t := counter.LoadOrCreate(&h.counter, m.Inbound)
	conn = counter.NewLeasedPacketConn(conn, t.GetCounter(key))
	return conn
}

// Keep a device slot for the lifetime of an authenticated UDP logical session.
type devicePacketConn struct {
	N.PacketConn
	release func()
}

func (c *devicePacketConn) Close() error { err := c.PacketConn.Close(); c.release(); return err }

func (c *devicePacketConn) FrontHeadroom() int { return N.CalculateFrontHeadroom(c.PacketConn) }
func (c *devicePacketConn) RearHeadroom() int  { return N.CalculateRearHeadroom(c.PacketConn) }
