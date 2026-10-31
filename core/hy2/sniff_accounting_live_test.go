package hy2

import (
	"bytes"
	"crypto/tls"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/api/panel"
	"github.com/apernet/hysteria/core/v2/server"
	"github.com/apernet/hysteria/extras/v2/sniff"
)

func sniffLiveConfig(cfg *server.Config) { cfg.RequestHook = &sniff.Sniffer{} }
func TestSniffLiveAccounting(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, payload := range [][]byte{
			{1, 2, 3, 4, 5, 6, 7, 8, 9}, []byte("GET / HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n"), {0x16, 0x03, 0x03, 0, 4, 1, 2, 3, 4},
		} {
			t.Run(fmt.Sprintf("sniff=%t/size=%d", enabled, len(payload)), func(t *testing.T) {
				configure := func(cfg *server.Config) {
					if enabled {
						sniffLiveConfig(cfg)
					}
				}
				f := newTailLifecycleFixture(t, 1, nil, configure)
				c, e := f.dial("127.0.0.1")
				if e != nil {
					t.Fatal(e)
				}
				defer c.Close()
				stream, e := c.TCP(f.tcp)
				if e != nil {
					t.Fatal(e)
				}
				defer stream.Close()
				if e = tailLifecycleExchange(stream, payload); e != nil {
					t.Fatal(e)
				}
				rows, e := f.h.GetUserTrafficSlice(f.tag, false)
				if e != nil {
					t.Fatal(e)
				}
				if len(rows) != 1 || rows[0].UID != 7 || rows[0].Upload != int64(len(payload)) || rows[0].Download != int64(len(payload)) {
					t.Fatalf("sent=%d got=%+v", len(payload), rows)
				}
				var saved []panel.UserTraffic
				if e = f.h.PersistTrafficSnapshot(f.tag, func(r []panel.UserTraffic) error { saved = append(saved, r...); return nil }); e != nil {
					t.Fatal(e)
				}
				if len(saved) != 1 || saved[0] != rows[0] {
					t.Fatal("reliable snapshot changed counts")
				}
				next, _ := f.h.GetUserTrafficSlice(f.tag, false)
				if len(next) != 0 {
					t.Fatal("duplicate counts after checkpoint")
				}
			})
		}
	}
}

type sniffMeasuredConn struct {
	net.Conn
	up, down atomic.Int64
}

func (c *sniffMeasuredConn) Write(p []byte) (int, error) {
	n, e := c.Conn.Write(p)
	c.up.Add(int64(n))
	return n, e
}
func (c *sniffMeasuredConn) Read(p []byte) (int, error) {
	n, e := c.Conn.Read(p)
	c.down.Add(int64(n))
	return n, e
}
func TestSniffLiveHTTPS(t *testing.T) {
	target := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(204) }))
	defer target.Close()
	f := newTailLifecycleFixture(t, 1, nil, sniffLiveConfig)
	c, e := f.dial("127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	stream, e := c.TCP(target.Listener.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer stream.Close()
	measured := &sniffMeasuredConn{Conn: stream}
	tlsConn := tls.Client(measured, &tls.Config{InsecureSkipVerify: true}) // local httptest certificate only
	tlsConn.SetDeadline(time.Now().Add(5 * time.Second))
	if e = tlsConn.Handshake(); e != nil {
		t.Fatal(e)
	}
	if _, e = io.WriteString(tlsConn, "GET / HTTP/1.1\r\nHost: 127.0.0.1\r\n\r\n"); e != nil {
		t.Fatal(e)
	}
	p := make([]byte, 4096)
	n, e := tlsConn.Read(p)
	if e != nil {
		t.Fatal(e)
	}
	if !bytes.Contains(p[:n], []byte("204 No Content")) {
		t.Fatal("wrong response")
	}
	rows, e := f.h.GetUserTrafficSlice(f.tag, false)
	if e != nil {
		t.Fatal(e)
	}
	if len(rows) != 1 || rows[0].Upload != measured.up.Load() || rows[0].Download != measured.down.Load() {
		t.Fatalf("wire=%d/%d rows=%+v", measured.up.Load(), measured.down.Load(), rows)
	}
	t.Logf("real HTTPS 204 exact encrypted-byte accounting: up=%d down=%d", rows[0].Upload, rows[0].Download)
}

type sniffBlockedOutbound struct {
	server.Outbound
	entered chan struct{}
	release chan struct{}
}

func (o *sniffBlockedOutbound) TCP(addr string) (net.Conn, error) {
	close(o.entered)
	<-o.release
	return o.Outbound.TCP(addr)
}
func TestSniffRevokedBeforePutback(t *testing.T) {
	entered, release := make(chan struct{}), make(chan struct{})
	var unblock sync.Once
	defer unblock.Do(func() { close(release) })
	f := newTailLifecycleFixture(t, 1, nil, func(cfg *server.Config) {
		sniffLiveConfig(cfg)
		cfg.Outbound = &sniffBlockedOutbound{cfg.Outbound, entered, release}
	})
	target, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	defer target.Close()
	received := make(chan int, 1)
	go func() {
		conn, e := target.Accept()
		if e != nil {
			return
		}
		defer conn.Close()
		conn.SetReadDeadline(time.Now().Add(3 * time.Second))
		p := make([]byte, 9)
		n, _ := conn.Read(p)
		received <- n
	}()
	c, e := f.dial("127.0.0.1")
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	stream, e := c.TCP(target.Addr().String())
	if e != nil {
		t.Fatal(e)
	}
	defer stream.Close()
	if _, e = stream.Write([]byte{1, 2, 3, 4, 5, 6, 7, 8, 9}); e != nil {
		t.Fatal(e)
	}
	select {
	case <-entered:
	case <-time.After(3 * time.Second):
		t.Fatal("dial not reached")
	}
	if e = f.h.DelUsers([]panel.UserInfo{{Id: 7, Uuid: "synthetic-live"}}, f.tag, nil); e != nil {
		t.Fatal(e)
	}
	unblock.Do(func() { close(release) })
	select {
	case n := <-received:
		if n != 0 {
			t.Fatalf("revoked putback forwarded %d bytes", n)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("target not closed")
	}
	rows, _ := f.h.GetUserTrafficSlice(f.tag, false)
	if len(rows) != 0 {
		t.Fatal("revoked putback charged")
	}
}

func BenchmarkSniffNewStream(b *testing.B) {
	f := newTailLifecycleFixture(b, 0, nil, sniffLiveConfig)
	c, e := f.dial("127.0.0.1")
	if e != nil {
		b.Fatal(e)
	}
	defer c.Close()
	payload := bytes.Repeat([]byte{1}, 1024)
	b.SetBytes(int64(len(payload) * 2))
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		s, e := c.TCP(f.tcp)
		if e != nil {
			b.Fatal(e)
		}
		if e = tailLifecycleExchange(s, payload); e != nil {
			b.Fatal(e)
		}
		s.Close()
	}
	b.StopTimer()
	rows, e := f.h.GetUserTrafficSlice(f.tag, false)
	if e != nil || len(rows) != 1 {
		b.Fatal("missing counters", e)
	}
	b.ReportMetric(float64(rows[0].Upload)/float64(b.N), "upload_B/stream")
	b.ReportMetric(float64(rows[0].Download)/float64(b.N), "download_B/stream")
}
