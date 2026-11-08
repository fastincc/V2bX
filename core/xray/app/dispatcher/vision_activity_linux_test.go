//go:build linux

package dispatcher

import (
	"context"
	"errors"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/common/counter"
	"github.com/xtls/xray-core/common/buf"
	"github.com/xtls/xray-core/common/session"
	"github.com/xtls/xray-core/common/signal"
	"github.com/xtls/xray-core/proxy"
	"github.com/xtls/xray-core/transport/internet/stat"
)

type visionActivityCallbacks struct{ read, written func(int64) }

type visionActivityProvider struct{ entered chan visionActivityCallbacks }

func (p *visionActivityProvider) WriteMultiBuffer(mb buf.MultiBuffer) error {
	buf.ReleaseMulti(mb)
	return errors.New("test unexpectedly used buffered copy")
}

func (p *visionActivityProvider) CopyRawSpliceIngress(ctx context.Context, _ net.Conn, _ *net.TCPConn, read, written func(int64)) error {
	p.entered <- visionActivityCallbacks{read, written}
	<-ctx.Done()
	return ctx.Err()
}

type visionActivityHarness struct {
	ctx            context.Context
	cancel         context.CancelFunc
	timer, inTimer *signal.ActivityTimer
	callbacks      visionActivityCallbacks
	read, written  atomic.Int64
	done           chan error
}

func newVisionActivityHarness(t *testing.T, idle time.Duration, inbound string) *visionActivityHarness {
	t.Helper()
	h := new(visionActivityHarness)
	h.ctx, h.cancel = context.WithCancel(context.Background())
	h.timer = signal.CancelAfterInactivity(h.ctx, h.cancel, idle)
	switch inbound {
	case "distinct":
		h.inTimer = signal.CancelAfterInactivity(h.ctx, h.cancel, idle)
	case "same":
		h.inTimer = h.timer
	case "nil":
	default:
		t.Fatal("invalid inbound timer fixture")
	}
	listener, err := net.ListenTCP("tcp", &net.TCPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	dst, err := net.DialTCP("tcp", nil, listener.Addr().(*net.TCPAddr))
	if err != nil {
		t.Fatal(err)
	}
	peer, err := listener.AcceptTCP()
	if err != nil {
		dst.Close()
		t.Fatal(err)
	}
	src, srcPeer := net.Pipe()
	t.Cleanup(func() {
		h.cancel()
		h.timer.SetTimeout(0)
		if h.inTimer != nil && h.inTimer != h.timer {
			h.inTimer.SetTimeout(0)
		}
		dst.Close()
		peer.Close()
		src.Close()
		srcPeer.Close()
		if h.done != nil {
			select {
			case <-h.done:
			case <-time.After(time.Second):
				t.Error("cancelled raw provider did not return")
			}
		}
	})
	ctx := session.ContextWithInbound(h.ctx, &session.Inbound{CanSpliceCopy: 1})
	ctx = session.ContextWithOutbounds(ctx, []*session.Outbound{{CanSpliceCopy: 1}})
	p := &visionActivityProvider{entered: make(chan visionActivityCallbacks, 1)}
	h.done = make(chan error, 1)
	go func() {
		h.done <- proxy.CopyRawConnIfExist(ctx,
			&stat.CounterConnection{Connection: src, ReadCounter: &counter.XrayTrafficCounter{V: &h.read}},
			&stat.CounterConnection{Connection: dst, WriteCounter: &counter.XrayTrafficCounter{V: &h.written}},
			p, h.timer, h.inTimer)
	}()
	select {
	case h.callbacks = <-p.entered:
	case <-time.After(time.Second):
		t.Fatal("raw ingress provider was not selected")
	}
	return h
}

func requireVisionActivityExpiry(t *testing.T, h *visionActivityHarness, bound time.Duration) {
	t.Helper()
	select {
	case <-h.ctx.Done():
	case <-time.After(bound):
		t.Fatal("configured timeout was replaced or false activity kept it alive")
	}
}

func TestVisionBasicSpliceRetainsConfiguredIdle(t *testing.T) {
	for _, inbound := range []string{"nil", "same", "distinct"} {
		t.Run(inbound, func(t *testing.T) {
			h := newVisionActivityHarness(t, 80*time.Millisecond, inbound)
			requireVisionActivityExpiry(t, h, 500*time.Millisecond)
			if h.read.Load() != 0 || h.written.Load() != 0 {
				t.Fatal("idle copy invented traffic")
			}
		})
	}
}

func TestVisionBasicSpliceProgressAndHalfClose(t *testing.T) {
	for _, direction := range []string{"read", "written"} {
		for _, inbound := range []string{"nil", "same", "distinct"} {
			t.Run(direction+"/"+inbound, func(t *testing.T) {
				h := newVisionActivityHarness(t, time.Hour, inbound)
				// Freedom switches this timer after the request reader receives
				// EOF. Model that switch after the actual raw branch was entered.
				h.timer.SetTimeout(100 * time.Millisecond)
				if h.inTimer != nil && h.inTimer != h.timer {
					h.inTimer.SetTimeout(100 * time.Millisecond)
				}
				progress := h.callbacks.read
				if direction == "written" {
					progress = h.callbacks.written
				}
				if progress == nil {
					t.Fatal("missing progress callback")
				}
				for i := 0; i < 20; i++ {
					progress(7)
					select {
					case <-h.ctx.Done():
						t.Fatal("active progress did not refresh both activity timers")
					case <-time.After(20 * time.Millisecond):
					}
				}
				wantRead, wantWritten := int64(140), int64(0)
				if direction == "written" {
					wantRead, wantWritten = 0, 140
				}
				if h.read.Load() != wantRead || h.written.Load() != wantWritten {
					t.Fatal("activity changed independent transport counters", h.read.Load(), h.written.Load())
				}
				requireVisionActivityExpiry(t, h, 500*time.Millisecond)
			})
		}
	}
}

func TestVisionBasicSpliceNonpositiveProgressIsNotActivity(t *testing.T) {
	h := newVisionActivityHarness(t, time.Hour, "distinct")
	h.timer.SetTimeout(80 * time.Millisecond)
	h.inTimer.SetTimeout(80 * time.Millisecond)
	for end := time.Now().Add(300 * time.Millisecond); time.Now().Before(end); {
		if h.callbacks.read != nil {
			h.callbacks.read(0)
			h.callbacks.read(-1)
		}
		if h.callbacks.written != nil {
			h.callbacks.written(0)
			h.callbacks.written(-1)
		}
		select {
		case <-h.ctx.Done():
			if h.read.Load() != 0 || h.written.Load() != 0 {
				t.Fatal("nonpositive progress changed counters")
			}
			return
		case <-time.After(10 * time.Millisecond):
		}
	}
	t.Fatal("zero or negative progress kept an idle connection alive")
}
