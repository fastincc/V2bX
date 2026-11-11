package rate

import (
	"errors"
	"io"
	"net"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/common/dynamic"
	"github.com/juju/ratelimit"
	N "github.com/sagernet/sing/common/network"
)

type eofWaitFixture struct {
	net.Conn
	entered chan struct{}
}

func (c *eofWaitFixture) Read(p []byte) (int, error) {
	c.entered <- struct{}{}
	return len(p), nil
}
func (c *eofWaitFixture) Write(p []byte) (int, error) {
	c.entered <- struct{}{}
	return len(p), nil
}
func (c *eofWaitFixture) Close() error { return nil }

// EOF must cancel both pacing waits while preserving bytes already transferred.
func TestDuplexEOFCancelsRateWaits(t *testing.T) {
	for _, mode := range []string{"static", "dynamic", "combined"} {
		t.Run(mode, func(t *testing.T) {
			f := &eofWaitFixture{entered: make(chan struct{}, 2)}
			h := dynamic.New(0)
			defer h.Close()
			var raw net.Conn = f
			if mode != "dynamic" {
				raw = NewConnRateLimiter(raw, ratelimit.NewBucketWithRate(1, 1))
			}
			if mode != "static" {
				h.Apply(&dynamic.Policy{Version: 1, Revision: 1, Enabled: true, WindowSeconds: 60, ThresholdBytes: 1, SpeedMbps: 1, DurationSeconds: 60}, time.Now())
				h.Observe(1)
				h.Tick(time.Now())
			}
			s := new(counter.TrafficStorage)
			c := counter.NewWrappedLeasedConn(raw, s, func(v net.Conn) net.Conn { return NewDynamicConn(v, h) })
			defer c.Close()
			type result struct {
				n    int
				err  error
				read bool
			}
			done := make(chan result, 2)
			const size = 256 * 1024
			go func() { n, e := c.Read(make([]byte, size)); done <- result{n, e, true} }()
			go func() { n, e := c.Write(make([]byte, size)); done <- result{n, e, false} }()
			// Dynamic writes wait before sending; static pacing follows I/O.
			entered, down := 1, int64(0)
			if mode == "static" {
				entered, down = 2, size
			}
			for i := 0; i < entered; i++ {
				select {
				case <-f.entered:
				case <-time.After(time.Second):
					t.Fatal("I/O did not enter")
				}
			}
			select {
			case <-done:
				t.Fatal("expected pacing wait")
			case <-time.After(10 * time.Millisecond):
			}
			if w, ok := c.(N.WriteCloser); ok {
				w.CloseWrite()
			} else {
				c.Close()
			}
			for i := 0; i < 2; i++ {
				select {
				case r := <-done:
					want := int(down)
					if r.read {
						want = size
					}
					if r.n != want || !errors.Is(r.err, net.ErrClosed) {
						t.Fatal("EOF changed partial I/O", r.n, r.err)
					}
				case <-time.After(500 * time.Millisecond):
					t.Fatal("EOF retained pacing wait")
				}
			}
			if s.Owners.Load() != 0 || s.UpCounter.Load() != size || s.DownCounter.Load() != down {
				t.Fatal("EOF changed completed-byte accounting")
			}
		})
	}
}

type duplexRead struct{ net.Conn }

func (duplexRead) CloseRead() error { return nil }

type duplexWrite struct{ net.Conn }

func (duplexWrite) CloseWrite() error { return nil }

type duplexBoth struct{ net.Conn }

func (duplexBoth) CloseRead() error  { return nil }
func (duplexBoth) CloseWrite() error { return nil }

// Match the production wrapper order, including optional individual layers.
func duplexWrap(c net.Conn, mask int, s *counter.TrafficStorage, h *dynamic.Control) net.Conn {
	if mask&1 != 0 {
		c = NewConnRateLimiter(c, ratelimit.NewBucketWithRate(1e9, 1e9))
	}
	if mask&2 != 0 {
		return counter.NewWrappedLeasedConn(c, s, func(v net.Conn) net.Conn {
			if mask&4 != 0 {
				return NewDynamicConn(v, h)
			}
			return v
		})
	}
	if mask&4 != 0 {
		c = NewDynamicConn(c, h)
	}
	return c
}

func TestDuplexCapabilitiesMatchTransport(t *testing.T) {
	for capability := 0; capability < 4; capability++ {
		for mask := 1; mask < 8; mask++ {
			a, b := net.Pipe()
			var raw net.Conn = a
			switch capability {
			case 1:
				raw = duplexRead{a}
			case 2:
				raw = duplexWrite{a}
			case 3:
				raw = duplexBoth{a}
			}
			h := dynamic.New(0)
			s := new(counter.TrafficStorage)
			c := duplexWrap(raw, mask, s, h)
			_, rd := c.(N.ReadCloser)
			_, wr := c.(N.WriteCloser)
			if rd != (capability&1 != 0) || wr != (capability&2 != 0) {
				t.Errorf("capability=%d layers=%d: read=%v write=%v", capability, mask, rd, wr)
			}
			c.Close()
			b.Close()
			h.Close()
			if s.Owners.Load() != 0 {
				t.Fatal("closed accounting owner retained")
			}
		}
	}
}

func TestDuplexUnsupportedEOFFullyCloses(t *testing.T) {
	for mask := 1; mask < 8; mask++ {
		a, b := net.Pipe()
		defer b.Close()
		h := dynamic.New(0)
		defer h.Close()
		s := new(counter.TrafficStorage)
		c := duplexWrap(a, mask, s, h)
		defer c.Close()
		// This is the production copy completion decision, not a direct Close.
		if w, ok := c.(N.WriteCloser); ok {
			w.CloseWrite()
		} else {
			c.Close()
		}
		b.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		if n, err := b.Read(make([]byte, 1)); n != 0 || err != io.EOF {
			t.Errorf("layers=%d: expected EOF, got n=%d err=%v", mask, n, err)
		}
		if s.Owners.Load() != 0 {
			t.Errorf("layers=%d: EOF retained owner", mask)
		}
	}
}

func TestDuplexTCPHalfCloseKeepsReverseTraffic(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	a, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	defer a.Close()
	b, err := l.Accept()
	if err != nil {
		t.Fatal(err)
	}
	defer b.Close()
	a.SetDeadline(time.Now().Add(2 * time.Second))
	b.SetDeadline(time.Now().Add(2 * time.Second))
	h := dynamic.New(0)
	defer h.Close()
	s := new(counter.TrafficStorage)
	c := duplexWrap(a, 7, s, h)
	defer c.Close()
	if err := c.(N.WriteCloser).CloseWrite(); err != nil {
		t.Fatal(err)
	}
	if n, err := b.Read(make([]byte, 1)); n != 0 || err != io.EOF {
		t.Fatal("TCP half-close did not reach peer", n, err)
	}
	if s.Owners.Load() != 1 {
		t.Fatal("real half-close retired live reverse direction")
	}
	if _, err := b.Write([]byte("tail")); err != nil {
		t.Fatal(err)
	}
	p := make([]byte, 4)
	if _, err := io.ReadFull(c, p); err != nil || string(p) != "tail" {
		t.Fatal("reverse direction lost", err)
	}
	c.Close()
	c.Close()
	if s.UpCounter.Load() != 4 || s.DownCounter.Load() != 0 || s.Owners.Load() != 0 {
		t.Fatal("tail accounting/release mismatch")
	}
}

type duplexSink struct{ net.Conn }

func (duplexSink) Write(p []byte) (int, error) { return len(p), nil }
func (duplexSink) Close() error                { return nil }

func BenchmarkDuplexUnlimited(b *testing.B) {
	h := dynamic.New(0)
	defer h.Close()
	b.Run("tcp-capable-transfer", func(b *testing.B) {
		s := new(counter.TrafficStorage)
		c := duplexWrap(duplexBoth{duplexSink{}}, 6, s, h)
		defer c.Close()
		payload := make([]byte, 32768)
		b.SetBytes(int64(len(payload)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := c.Write(payload); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("transfer", func(b *testing.B) {
		s := new(counter.TrafficStorage)
		c := duplexWrap(duplexSink{}, 6, s, h)
		defer c.Close()
		payload := make([]byte, 32768)
		b.SetBytes(int64(len(payload)))
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			if _, err := c.Write(payload); err != nil {
				b.Fatal(err)
			}
		}
	})
	b.Run("create-close", func(b *testing.B) {
		s := new(counter.TrafficStorage)
		b.ReportAllocs()
		b.ResetTimer()
		for i := 0; i < b.N; i++ {
			c := duplexWrap(duplexSink{}, 6, s, h)
			c.Close()
		}
	})
}
