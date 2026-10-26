package sing

import (
	"bytes"
	"context"
	"encoding/json"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/common/counter"
	"github.com/InazumaV/V2bX/conf"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/common/uot"
)

type auditCapture struct {
	mu sync.Mutex
	b  bytes.Buffer
}

func (c *auditCapture) Write(b []byte) (int, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.b.Write(b)
}
func (c *auditCapture) text() string { c.mu.Lock(); defer c.mu.Unlock(); return c.b.String() }
func (c *auditCapture) events(t *testing.T) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, l := range strings.Split(c.text(), "\n") {
		i := strings.Index(l, "AUDIT_ACCESS ")
		if i < 0 {
			continue
		}
		var e map[string]any
		if err := json.Unmarshal([]byte(l[i+13:]), &e); err != nil {
			t.Fatalf("invalid audit JSON: %v", err)
		}
		out = append(out, e)
	}
	return out
}
func auditLogger(t *testing.T, c *auditCapture) log.ContextLogger {
	t.Helper()
	f, e := log.New(log.Options{Context: context.Background(), DefaultWriter: c, Options: option.LogOptions{Level: "info", DisableColor: true}})
	if e != nil {
		t.Fatal(e)
	}
	t.Cleanup(func() { f.Close() })
	return f.Logger()
}

func TestAnyTLSAuditAccessIdentityAndMultiplex(t *testing.T) {
	const secret = "11111111-1111-4111-8111-111111111111"
	for _, proxy := range []bool{false, true} {
		t.Run(strconv.FormatBool(proxy), func(t *testing.T) {
			capture := new(auditCapture)
			f := newReviewFixtureAudit(t, proxy, []option.AnyTLSUser{{Name: secret, Password: secret, AuditID: "1001"}}, auditLogger(t, capture), true)
			cl := f.clientPassword(t, secret)
			defer cl.Close()
			for i := 0; i < 3; i++ {
				s, e := cl.CreateProxy(context.Background(), M.ParseSocksaddr("example.com:443"))
				if e != nil {
					t.Fatal(e)
				}
				reviewEcho(t, s, []byte("payload"))
				s.Close()
				<-f.router.done
				<-f.router.sources
			}
			events := capture.events(t)
			if len(events) != 3 {
				t.Fatalf("events=%d", len(events))
			}
			for i, e := range events {
				if e["user_id"] != "1001" || e["authenticated"] != true || e["event"] != "access" || e["network"] != "tcp" || e["target_host"] != "example.com" || e["target_port"] != float64(443) || e["stream_id"] != strconv.Itoa(i+1) || e["proxy_trusted"] != false {
					t.Fatalf("invalid event %#v", e)
				}
				if e["instance_id"] != events[0]["instance_id"] || e["transport_id"] != events[0]["transport_id"] {
					t.Fatal("lost shared transport")
				}
				if proxy {
					if e["client_ip_source"] != "proxy_protocol" || e["client_ip"] != "198.51.100.9" || e["relay_ip"] != "127.0.0.1" {
						t.Fatal("proxy address mismatch")
					}
				} else {
					if e["client_ip_source"] != "direct" || e["client_ip"] != "127.0.0.1" || e["relay_ip"] != nil || e["relay_port"] != nil {
						t.Fatal("direct mismatch")
					}
				}
			}
			if strings.Contains(capture.text(), secret) || strings.Contains(capture.text(), "inbound connection to") {
				t.Fatal("credential or duplicate legacy access log")
			}
			c, _ := f.router.hook.counter.Load("review")
			tc := c.(*counter.TrafficCounter)
			if tc.GetUpCount(secret) != 21 || tc.GetDownCount(secret) != 21 || tc.GetUpCount("1001") != 0 {
				t.Fatal("internal accounting identity changed")
			}
			// A new physical connection has a distinct process-scoped transport ID.
			second := f.clientPassword(t, secret)
			defer second.Close()
			s, e := second.CreateProxy(context.Background(), M.ParseSocksaddr("example.net:80"))
			if e != nil {
				t.Fatal(e)
			}
			reviewEcho(t, s, []byte("x"))
			s.Close()
			<-f.router.done
			<-f.router.sources
			last := capture.events(t)[3]
			if last["transport_id"] == events[0]["transport_id"] || last["stream_id"] != "1" || last["instance_id"] != events[0]["instance_id"] {
				t.Fatal("transport uniqueness failed")
			}
		})
	}
}
func TestAnyTLSAuditUnmappedAndRejected(t *testing.T) {
	const secret = "22222222-2222-4222-8222-222222222222"
	c := new(auditCapture)
	f := newReviewFixtureAudit(t, true, []option.AnyTLSUser{{Name: secret, Password: secret}}, auditLogger(t, c), true)
	bad := f.clientPassword(t, "wrong-password")
	s, e := bad.CreateProxy(context.Background(), M.ParseSocksaddr("example.com:443"))
	if e == nil {
		e = soakExchange(s, []byte("x"))
		s.Close()
	}
	bad.Close()
	if e == nil {
		t.Fatal("bad password accepted")
	}
	if len(c.events(t)) != 0 {
		t.Fatal("unauthenticated access logged")
	}
	cl := f.clientPassword(t, secret)
	defer cl.Close()
	s, e = cl.CreateProxy(context.Background(), M.ParseSocksaddr("example.com:443"))
	if e != nil {
		t.Fatal(e)
	}
	reviewEcho(t, s, []byte("x"))
	s.Close()
	<-f.router.done
	<-f.router.sources
	ev := c.events(t)
	if len(ev) != 1 || ev[0]["user_id"] != nil || strings.Contains(c.text(), secret) {
		t.Fatal("unmapped identity leaked")
	}
	// Panel add-users mapping affects only the audit ID; existing password stays.
	if e = f.inbound.AddUsers([]option.AnyTLSUser{{Name: secret, Password: secret, AuditID: "1001"}}); e != nil {
		t.Fatal(e)
	}
	s, e = cl.CreateProxy(context.Background(), M.ParseSocksaddr("example.com:443"))
	if e != nil {
		t.Fatal(e)
	}
	reviewEcho(t, s, []byte("x"))
	s.Close()
	<-f.router.done
	<-f.router.sources
	if c.events(t)[1]["user_id"] != "1001" {
		t.Fatal("updated panel ID not mapped")
	}
}
func TestAnyTLSAuditUDPAndNodeSetting(t *testing.T) {
	var node conf.NodeConfig
	if e := json.Unmarshal([]byte(`{"Core":"sing","NodeType":"anytls","NodeID":93,"AnyTLSAuditAccess":true}`), &node); e != nil || !node.Options.SingOptions.AnyTLSAuditAccess {
		t.Fatal("audit flag mapping failed")
	}
	c := new(auditCapture)
	f := newReviewFixtureAudit(t, true, []option.AnyTLSUser{{Name: "test-user", Password: "test-password", AuditID: "1001"}}, auditLogger(t, c), true)
	cl := f.client(t)
	defer cl.Close()
	s, e := cl.CreateProxy(context.Background(), M.Socksaddr{Fqdn: uot.LegacyMagicAddress, Port: 443})
	if e != nil {
		t.Fatal(e)
	}
	defer s.Close()
	select {
	case <-f.router.done:
	case <-time.After(3 * time.Second):
		t.Fatal("UDP did not reach router")
	}
	<-f.router.sources
	ev := c.events(t)
	if len(ev) != 1 || ev[0]["network"] != "udp" || ev[0]["target_host"] != nil || ev[0]["target_port"] != nil {
		t.Fatalf("legacy UDP target fabricated: %#v", ev)
	}
	connected, e := cl.CreateProxy(context.Background(), M.Socksaddr{Fqdn: uot.MagicAddress, Port: 443})
	if e != nil {
		t.Fatal(e)
	}
	defer connected.Close()
	if e = uot.WriteRequest(connected, uot.Request{IsConnect: true, Destination: M.ParseSocksaddr("dns.example:53")}); e != nil {
		t.Fatal(e)
	}
	select {
	case <-f.router.done:
	case <-time.After(3 * time.Second):
		t.Fatal("connected UDP did not route")
	}
	<-f.router.sources
	ev = c.events(t)
	if len(ev) != 2 || ev[1]["network"] != "udp" || ev[1]["target_host"] != "dns.example" || ev[1]["target_port"] != float64(53) {
		t.Fatalf("actual UDP target not preserved: %#v", ev)
	}

	if strings.Contains(c.text(), "inbound UoT") || strings.Contains(c.text(), "inbound legacy UoT") {
		t.Fatal("duplicate UDP access log")
	}

}
