package sing

import (
	"context"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	"net"
	"sync/atomic"
	"testing"
	"time"
)

type udpHookPacket struct{ closed atomic.Bool }

func (p *udpHookPacket) ReadPacket(b *buf.Buffer) (M.Socksaddr, error) {
	if p.closed.Load() {
		return M.Socksaddr{}, net.ErrClosed
	}
	b.Write([]byte("up"))
	return M.ParseSocksaddr("192.0.2.5:53"), nil
}
func (p *udpHookPacket) WritePacket(b *buf.Buffer, _ M.Socksaddr) error {
	defer b.Release()
	if p.closed.Load() {
		return net.ErrClosed
	}
	return nil
}
func (p *udpHookPacket) Close() error                     { p.closed.Store(true); return nil }
func (p *udpHookPacket) LocalAddr() net.Addr              { return &net.UDPAddr{} }
func (p *udpHookPacket) SetDeadline(time.Time) error      { return nil }
func (p *udpHookPacket) SetReadDeadline(time.Time) error  { return nil }
func (p *udpHookPacket) SetWriteDeadline(time.Time) error { return nil }
func TestUDPAdmissionProtocolMatrix(t *testing.T) {
	for _, proto := range []string{"shadowsocks", "vmess", "vless", "trojan", "hysteria", "hysteria2", "tuic", "anytls"} {
		t.Run(proto, func(t *testing.T) {
			limiter.Init()
			l := limiter.AddLimiter(proto, &conf.LimitConfig{}, []panel.UserInfo{{Id: 9, Uuid: "synthetic", DeviceLimit: 1}}, nil)
			defer limiter.DeleteLimiter(proto)
			h := new(HookServer)
			metadata := adapter.InboundContext{Inbound: proto, InboundType: proto, User: "synthetic", Source: M.ParseSocksaddr("[::ffff:192.0.2.1]:1234"), Destination: M.ParseSocksaddr("192.0.2.5:53")}
			raw := new(udpHookPacket)
			allowed := h.RoutedPacketConnection(context.Background(), raw, metadata, nil, nil)
			defer allowed.Close()
			if raw.closed.Load() {
				t.Fatal("first device denied")
			}
			metadata.Source = M.ParseSocksaddr("192.0.2.1:4321")
			sameRaw := new(udpHookPacket)
			same := h.RoutedPacketConnection(context.Background(), sameRaw, metadata, nil, nil)
			defer same.Close()
			if sameRaw.closed.Load() {
				t.Fatal("mapped IPv4/port dedup failed")
			}
			metadata.Source = M.ParseSocksaddr("192.0.2.2:1234")
			bad := new(udpHookPacket)
			h.RoutedPacketConnection(context.Background(), bad, metadata, nil, nil)
			if !bad.closed.Load() {
				t.Fatal("second device bypassed")
			}
			online, e := l.GetOnlineDevice()
			if e != nil || len(*online) != 1 || (*online)[0].IP != "192.0.2.1" {
				t.Fatal("source IP normalized incorrectly")
			}
			allowed.Close()
			allowed.Close()
			same.Close()
		})
	}
}
