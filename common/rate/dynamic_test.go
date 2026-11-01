package rate

import (
	"errors"
	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/common/dynamic"
	"github.com/sagernet/sing/common/buf"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"net"
	"os"
	"testing"
	"time"
)

func TestDynamicPacketDeadlineCloseAndFraming(t *testing.T) {
	for _, mode := range []string{"deadline", "close", "disable"} {
		t.Run(mode, func(t *testing.T) {
			h := dynamic.New(0)
			defer h.Close()
			h.Apply(&dynamic.Policy{Version: 1, Revision: 1, Enabled: true, WindowSeconds: 60, ThresholdBytes: 1, SpeedMbps: 1, DurationSeconds: 60}, time.Now())
			h.Observe(1)
			h.Tick(time.Now())
			f := new(packetFixture)
			c := NewDynamicPacketConn(f, h)
			defer c.Close()
			b := buf.NewSize(60000)
			b.Extend(60000)
			done := make(chan error, 1)
			go func() { done <- c.WritePacket(b, M.ParseSocksaddr("example.test:53")) }()
			time.Sleep(15 * time.Millisecond)
			switch mode {
			case "deadline":
				c.SetWriteDeadline(time.Now())
			case "close":
				c.Close()
			case "disable":
				h.Apply(nil, time.Now())
			}
			select {
			case e := <-done:
				if mode == "disable" {
					if e != nil || f.writes.Load() != 60000 {
						t.Fatal(e, f.writes.Load())
					}
				} else if (!errors.Is(e, net.ErrClosed) && !errors.Is(e, os.ErrDeadlineExceeded)) || f.writes.Load() != 0 {
					t.Fatal(e, f.writes.Load())
				}
			case <-time.After(time.Second):
				t.Fatal("packet wait stuck")
			}
		})
	}
}

func TestDynamicPacketCoreDrainCancelsOuterWait(t *testing.T) {
	h := dynamic.New(0)
	defer h.Close()
	h.Apply(&dynamic.Policy{Version: 1, Revision: 1, Enabled: true, WindowSeconds: 60, ThresholdBytes: 1, SpeedMbps: 1, DurationSeconds: 60}, time.Now())
	h.Observe(1)
	h.Tick(time.Now())
	f := new(packetFixture)
	s := new(counter.TrafficStorage)
	c := counter.NewWrappedLeasedPacketConn(f, s, func(p N.PacketConn) N.PacketConn { return NewDynamicPacketConn(p, h) })
	defer c.Close()
	b := buf.NewSize(60000)
	b.Extend(60000)
	done := make(chan error, 1)
	go func() { done <- c.WritePacket(b, M.Socksaddr{}) }()
	time.Sleep(15 * time.Millisecond)
	s.CloseWriters()
	select {
	case e := <-done:
		if !errors.Is(e, net.ErrClosed) {
			t.Fatal(e)
		}
	case <-time.After(300 * time.Millisecond):
		t.Fatal("UDP core drain did not cancel outer pacing")
	}
	if f.writes.Load() != 0 || s.DownCounter.Load() != 0 || s.Owners.Load() != 0 || !b.IsEmpty() {
		t.Fatal("unsent datagram leaked or billed")
	}
}
