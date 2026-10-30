package sing

import (
	"context"
	"errors"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/conf"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing/common/buf"
	"github.com/sagernet/sing/common/bufio"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/udpnat"
)

// Deliberately continues accepting I/O after Close, as a buffered UDP transport
// may do. The rejection boundary must not depend on its close semantics.
type rejectOpenPacket struct{ closes, reads, writes atomic.Int32 }

func (c *rejectOpenPacket) ReadPacket(b *buf.Buffer) (M.Socksaddr, error) {
	c.reads.Add(1)
	_, e := b.Write([]byte("buffered"))
	return M.ParseSocksaddr("192.0.2.9:53"), e
}
func (c *rejectOpenPacket) WritePacket(b *buf.Buffer, _ M.Socksaddr) error {
	c.writes.Add(1)
	b.Release()
	return nil
}
func (c *rejectOpenPacket) Close() error { c.closes.Add(1); return nil }
func (*rejectOpenPacket) LocalAddr() net.Addr {
	return &net.UDPAddr{IP: net.IPv4(192, 0, 2, 2), Port: 12345}
}
func (*rejectOpenPacket) SetDeadline(time.Time) error                 { return nil }
func (*rejectOpenPacket) SetReadDeadline(time.Time) error             { return nil }
func (*rejectOpenPacket) SetWriteDeadline(time.Time) error            { return nil }
func (c *rejectOpenPacket) Upstream() any                             { return c }
func (*rejectOpenPacket) ReaderReplaceable() bool                     { return true }
func (*rejectOpenPacket) WriterReplaceable() bool                     { return true }
func (*rejectOpenPacket) InitializeReadWaiter(N.ReadWaitOptions) bool { return false }
func (c *rejectOpenPacket) WaitReadPacket() (*buf.Buffer, M.Socksaddr, error) {
	c.reads.Add(1)
	return buf.As([]byte("wait-buffered")), M.ParseSocksaddr("192.0.2.9:53"), nil
}

func assertRejectedPacket(t *testing.T, c N.PacketConn) {
	t.Helper()
	if _, ok := c.(interface{ Upstream() any }); ok {
		t.Fatal("rejection exposes upstream")
	}
	if N.UnwrapPacketReader(c) != c || N.UnwrapPacketWriter(c) != c {
		t.Fatal("rejection permits unwrapping")
	}
	if _, ok := bufio.CreatePacketReadWaiter(c); ok {
		t.Fatal("rejection exposes optimized read waiter")
	}
	if _, ok := c.(N.VectorisedPacketReadWaiter); ok {
		t.Fatal("rejection exposes vectorized read waiter")
	}
	b := buf.NewPacket()
	defer b.Release()
	if _, e := c.ReadPacket(b); !errors.Is(e, net.ErrClosed) || b.Len() != 0 {
		t.Fatal("rejected read returned data", e)
	}
	if e := c.WritePacket(buf.As([]byte("reply")), M.ParseSocksaddr("192.0.2.2:12345")); !errors.Is(e, net.ErrClosed) {
		t.Fatal("rejected write accepted", e)
	}
	for _, set := range []func(time.Time) error{c.SetDeadline, c.SetReadDeadline, c.SetWriteDeadline} {
		if !errors.Is(set(time.Time{}), net.ErrClosed) {
			t.Fatal("deadline revived rejection")
		}
	}
}

func TestUDPRejectTerminalConnection(t *testing.T) {
	raw := new(rejectOpenPacket)
	c := rejectPacketConnection(raw)
	if c.LocalAddr().String() != raw.LocalAddr().String() {
		t.Fatal("local address changed")
	}
	var wg sync.WaitGroup
	for i := 0; i < 16; i++ {
		wg.Add(1)
		go func() { defer wg.Done(); assertRejectedPacket(t, c); c.Close() }()
	}
	wg.Wait()
	if raw.closes.Load() != 1 || raw.reads.Load() != 0 || raw.writes.Load() != 0 {
		t.Fatal("rejection delegated I/O or repeated close")
	}
}

func TestUDPRejectPolicyBranches(t *testing.T) {
	for _, mode := range []string{"device", "domain", "protocol", "retired-user"} {
		t.Run(mode, func(t *testing.T) {
			limiter.Init()
			tag := "reject-policy"
			l := limiter.AddLimiter(tag, &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "synthetic", DeviceLimit: 1}}, nil)
			defer limiter.DeleteLimiter(tag)
			h := new(HookServer)
			switch mode {
			case "device":
				l.CheckLimit(format.UserTag(tag, "synthetic"), "192.0.2.1", false, true)
			case "domain":
				if e := l.UpdateRule(&panel.Rules{Regexp: []string{"192\\.0\\.2\\.9"}}); e != nil {
					t.Fatal(e)
				}
			case "protocol":
				if e := l.UpdateRule(&panel.Rules{Protocol: []string{"socks"}}); e != nil {
					t.Fatal(e)
				}
			case "retired-user":
				h.users = &UserMap{active: map[userKey]string{}}
			}
			raw := new(rejectOpenPacket)
			m := adapter.InboundContext{Inbound: tag, InboundType: "shadowsocks", User: "synthetic", Source: M.ParseSocksaddr("192.0.2.2:12345"), Destination: M.ParseSocksaddr("192.0.2.9:53")}
			c := h.RoutedPacketConnection(context.Background(), raw, m, nil, nil)
			defer c.Close()
			assertRejectedPacket(t, c)
			if raw.closes.Load() != 1 || raw.reads.Load() != 0 || raw.writes.Load() != 0 {
				t.Fatal("policy rejection reached transport")
			}
			found := false
			h.counter.Range(func(_, _ any) bool { found = true; return false })
			if found {
				t.Fatal("rejected data acquired billing counter")
			}
		})
	}
}

type rejectNATProbe struct {
	ready  chan struct{}
	done   chan struct{}
	t      *testing.T
	writes atomic.Int32
}

func (h *rejectNATProbe) WritePacket(b *buf.Buffer, _ M.Socksaddr) error {
	h.writes.Add(1)
	b.Release()
	return nil
}
func (*rejectNATProbe) NewError(context.Context, error) {}
func (h *rejectNATProbe) NewPacketConnection(ctx context.Context, c N.PacketConn, m M.Metadata) error {
	defer close(h.done)
	<-h.ready
	hook := new(HookServer)
	c = hook.RoutedPacketConnection(ctx, c, adapter.InboundContext{Inbound: "reject-buffered", User: "synthetic", Source: m.Source, Destination: m.Destination}, nil, nil)
	defer c.Close()
	assertRejectedPacket(h.t, c)
	return nil
}
func TestUDPRejectBufferedNAT(t *testing.T) {
	limiter.Init()
	tag := "reject-buffered"
	l := limiter.AddLimiter(tag, &conf.LimitConfig{}, []panel.UserInfo{{Id: 1, Uuid: "synthetic", DeviceLimit: 1}}, nil)
	defer limiter.DeleteLimiter(tag)
	if _, r := l.CheckLimit(format.UserTag(tag, "synthetic"), "192.0.2.1", false, true); r {
		t.Fatal("first admission denied")
	}
	for i := 0; i < 100; i++ {
		h := &rejectNATProbe{ready: make(chan struct{}), done: make(chan struct{}), t: t}
		s := udpnat.New[int](60, h)
		m := M.Metadata{Source: M.ParseSocksaddr("192.0.2.2:12345"), Destination: M.ParseSocksaddr("192.0.2.9:53")}
		s.NewPacket(context.Background(), i, buf.As([]byte("queued-before-denial")), m, func(N.PacketConn) N.PacketWriter { return h })
		close(h.ready)
		select {
		case <-h.done:
		case <-time.After(time.Second):
			t.Fatal("rejection blocked")
		}
		if h.writes.Load() != 0 {
			t.Fatal("rejected response reached UDP writer")
		}
	}
}

var rejectedPacketBenchmark N.PacketConn

func BenchmarkUDPRejectTerminal(b *testing.B) {
	raw := new(rejectOpenPacket)
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		rejectedPacketBenchmark = rejectPacketConnection(raw)
	}
}
