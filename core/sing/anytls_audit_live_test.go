package sing

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"io"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	server "github.com/sagernet/sing-box/protocol/anytls"
	M "github.com/sagernet/sing/common/metadata"
	N "github.com/sagernet/sing/common/network"
	"github.com/sagernet/sing/common/uot"
)

// Real loopback destinations, never the existing node service or panel account.
type auditLiveRouter struct {
	adapter.Router
	logger log.ContextLogger
	done   chan error
}

func (r *auditLiveRouter) RouteConnectionEx(ctx context.Context, c net.Conn, m adapter.InboundContext, cb N.CloseHandlerFunc) {
	go func() {
		var result error
		defer func() {
			c.Close()
			if cb != nil {
				cb(result)
			}
			r.done <- result
		}()
		out, err := net.DialTimeout("tcp", m.Destination.String(), time.Second)
		if err != nil {
			result = err
			r.logger.ErrorContext(ctx, "TARGET_CONNECT_FAILED ", server.AuditUserLabel(ctx, ""), " target=", m.Destination, " error=", err)
			return
		}
		defer out.Close()
		out.SetDeadline(time.Now().Add(3 * time.Second))
		c.SetDeadline(time.Now().Add(3 * time.Second))
		b := make([]byte, len("audit-probe"))
		if _, result = io.ReadFull(c, b); result != nil {
			return
		}
		if _, result = out.Write(b); result != nil {
			return
		}
		if _, result = io.ReadFull(out, b); result != nil {
			return
		}
		_, result = c.Write(b)
	}()
}
func (r *auditLiveRouter) RoutePacketConnectionEx(ctx context.Context, c N.PacketConn, m adapter.InboundContext, cb N.CloseHandlerFunc) {
	go func() {
		var result error
		defer func() {
			c.Close()
			if cb != nil {
				cb(result)
			}
			r.done <- result
		}()
		pc, ok := c.(net.PacketConn)
		if !ok {
			panic("UoT must expose packet API")
		}
		pc.SetDeadline(time.Now().Add(3 * time.Second))
		b := make([]byte, 512)
		n, addr, err := pc.ReadFrom(b)
		if err != nil {
			result = err
			return
		}
		out, err := net.DialTimeout("udp", addr.String(), time.Second)
		if err != nil {
			result = err
			return
		}
		defer out.Close()
		out.SetDeadline(time.Now().Add(3 * time.Second))
		if _, result = out.Write(b[:n]); result != nil {
			return
		}
		n, result = out.Read(b)
		if result != nil {
			return
		}
		_, result = pc.WriteTo(b[:n], addr)
	}()
}
func TestAnyTLSAuditLiveEvidence(t *testing.T) {
	if os.Getenv("ANYTLS_AUDIT_EVIDENCE") != "1" {
		t.Skip("explicit isolated evidence run only")
	}
	tcpEcho := func(network, address string) net.Listener {
		l, e := net.Listen(network, address)
		if e != nil {
			t.Fatal(e)
		}
		t.Cleanup(func() { l.Close() })
		go func() {
			for {
				c, e := l.Accept()
				if e != nil {
					return
				}
				go func() { defer c.Close(); c.SetDeadline(time.Now().Add(3 * time.Second)); io.Copy(c, c) }()
			}
		}()
		return l
	}
	ipv4 := tcpEcho("tcp4", "127.0.0.1:0")
	ipv6 := tcpEcho("tcp6", "[::1]:0")
	udp, e := net.ListenPacket("udp6", "[::1]:0")
	if e != nil {
		t.Fatal(e)
	}
	defer udp.Close()
	go func() {
		b := make([]byte, 512)
		for {
			n, a, e := udp.ReadFrom(b)
			if e != nil {
				return
			}
			udp.WriteTo(b[:n], a)
		}
	}()
	dead, e := net.Listen("tcp4", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	deadAddr := dead.Addr().String()
	dead.Close()
	raw := make([]byte, 32)
	if _, e = rand.Read(raw); e != nil {
		t.Fatal(e)
	}
	secret := hex.EncodeToString(raw)
	capture := new(auditCapture)
	factory, e := log.New(log.Options{Context: context.Background(), DefaultWriter: capture, BaseTime: time.Now(), Options: option.LogOptions{Level: "info", DisableColor: true, Timestamp: true}})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { factory.Close() })
	logger := factory.NewLogger("inbound/anytls[audit-isolated]")
	f := newReviewFixtureAudit(t, true, []option.AnyTLSUser{{Name: secret, Password: secret, AuditID: "1001"}}, logger, true)
	r := &auditLiveRouter{logger: logger, done: make(chan error, 16)}
	f.router.forward = r
	cl := f.clientPassword(t, secret)
	defer cl.Close()
	tcp := func(target string, fail bool) {
		c, e := cl.CreateProxy(context.Background(), M.ParseSocksaddr(target))
		if e != nil {
			t.Fatal(e)
		}
		defer c.Close()
		c.SetDeadline(time.Now().Add(3 * time.Second))
		if fail {
			c.Write([]byte("audit-probe"))
			b := make([]byte, 1)
			if _, e = c.Read(b); e == nil {
				t.Fatal("closed target unexpectedly worked")
			}
		} else {
			reviewEcho(t, c, []byte("audit-probe"))
		}
		select {
		case e := <-r.done:
			if (e != nil) != fail {
				t.Fatalf("unexpected target result: %v", e)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("route timeout")
		}
	}
	tcp(ipv4.Addr().String(), false)
	tcp(ipv4.Addr().String(), false)
	tcp(deadAddr, true)
	c, e := cl.CreateProxy(context.Background(), M.Socksaddr{Fqdn: uot.MagicAddress, Port: 443})
	if e != nil {
		t.Fatal(e)
	}
	req := uot.Request{IsConnect: true, Destination: M.SocksaddrFromNet(udp.LocalAddr())}
	if e = uot.WriteRequest(c, req); e != nil {
		t.Fatal(e)
	}
	uc := uot.NewConn(c, req)
	uc.SetDeadline(time.Now().Add(3 * time.Second))
	if _, e = uc.Write([]byte("audit-probe")); e != nil {
		t.Fatal(e)
	}
	b := make([]byte, 64)
	n, e := uc.Read(b)
	if e != nil || string(b[:n]) != "audit-probe" {
		t.Fatal("UDP payload mismatch", e)
	}
	uc.Close()
	if e = <-r.done; e != nil {
		t.Fatal(e)
	}
	tcp(ipv6.Addr().String(), false)
	ev := capture.events(t)
	if len(ev) != 5 {
		t.Fatalf("access count %d", len(ev))
	}
	for _, v := range ev {
		if v["transport_id"] != ev[0]["transport_id"] {
			t.Fatal("not same underlying transport")
		}
	}
	if ev[3]["network"] != "udp" || ev[3]["target_host"] != "::1" || ev[4]["target_host"] != "::1" {
		t.Fatal("IPv6/UDP target mismatch")
	}
	logs := capture.text()
	if strings.Contains(logs, secret) {
		t.Fatal("secret in log")
	}
	if strings.Count(logs, "AUDIT_ERROR ") != 1 {
		t.Fatal("expected one correlated failure")
	}
	// Filter pre-auth diagnostics; retain the consecutive authenticated events.
	for _, line := range strings.Split(logs, "\n") {
		if strings.Contains(line, "AUDIT_ACCESS ") || strings.Contains(line, "AUDIT_ERROR ") || strings.Contains(line, "TARGET_CONNECT_FAILED ") {
			t.Log(line)
		}
	}
	t.Log("PASS: one authenticated transport, 2 TCP IPv4 echoes, refused TCP destination, UDP IPv6 echo, TCP IPv6 echo")
}
