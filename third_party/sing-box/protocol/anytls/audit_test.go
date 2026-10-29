package anytls

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/netip"
	"strings"
	"sync"
	"syscall"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
)

func TestAuditIDsAndSafeLabel(t *testing.T) {
	if err := initAuditInstance(); err != nil {
		t.Fatal(err)
	}
	instance := auditInstance.id
	h := new(Inbound)
	h.setAuditUser("credential", "1001")
	tr := newAuditTransport(M.ParseSocksaddr("127.0.0.1:1234"))
	ctx := context.WithValue(context.Background(), auditTransportKey{}, tr)
	var seen sync.Map
	var wg sync.WaitGroup
	for i := 0; i < 100; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			c := h.newAuditStream(ctx, "credential")
			st := c.Value(auditStreamKey{}).(*auditStream)
			if _, loaded := seen.LoadOrStore(st.id, true); loaded {
				t.Error("duplicate stream ID")
			}
			if strings.Contains(AuditUserLabel(c, "credential"), "credential") {
				t.Error("credential leaked")
			}
		}()
	}
	wg.Wait()
	other := newAuditTransport(M.ParseSocksaddr("127.0.0.1:1234"))
	if other.id == tr.id || auditInstance.id != instance {
		t.Fatal("ID scope changed")
	}
	for _, id := range []string{"", "0", "-1", "001", "not-a-number"} {
		h.setAuditUser("credential", id)
		c := h.newAuditStream(ctx, "credential")
		if c.Value(auditStreamKey{}).(*auditStream).user != nil || strings.Contains(AuditUserLabel(c, "credential"), "credential") {
			t.Fatal("invalid ID fallback")
		}
	}
	if AuditUserLabel(context.Background(), "legacy") != "user_id=unmapped" {
		t.Fatal("unmapped identity leaked")
	}
}
func TestAuditJSONAddressAndError(t *testing.T) {
	if err := initAuditInstance(); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	factory, err := log.New(log.Options{Context: context.Background(), DefaultWriter: &out, Options: option.LogOptions{Level: "info", DisableColor: true}})
	if err != nil {
		t.Fatal(err)
	}
	defer factory.Close()
	h := &Inbound{logger: factory.Logger()}
	h.setAuditUser("credential", "1001")
	tr := newAuditTransport(M.Socksaddr{Addr: netip.MustParseAddr("2001:db8::1"), Port: 1234})
	tr.source = "proxy_protocol"
	tr.relay = M.ParseSocksaddr("[2001:db8::2]:443")
	tr.trusted = true
	ctx := h.newAuditStream(context.WithValue(context.Background(), auditTransportKey{}, tr), "credential")
	meta := adapter.InboundContext{Destination: M.Socksaddr{Fqdn: "target\"\n.example", Port: 443}}
	close := h.auditAccess(ctx, meta, "tcp", nil)
	h.auditAccess(ctx, meta, "tcp", nil)
	close(errors.New("secret must not be embedded in audit error"))
	close(errors.New("duplicate"))
	var access, errorCount int
	for _, line := range strings.Split(out.String(), "\n") {
		if at := strings.Index(line, "AUDIT_ACCESS "); at >= 0 {
			access++
			var e auditEvent
			if err := json.Unmarshal([]byte(line[at+13:]), &e); err != nil {
				t.Fatal(err)
			}
			if *e.ClientIP != "2001:db8::1" || *e.RelayIP != "2001:db8::2" || *e.TargetHost != meta.Destination.Fqdn || !e.ProxyTrusted {
				t.Fatal("address or escaping mismatch")
			}
		}
		if at := strings.Index(line, "AUDIT_ERROR "); at >= 0 {
			errorCount++
			var e map[string]any
			if err := json.Unmarshal([]byte(line[at+12:]), &e); err != nil {
				t.Fatal(err)
			}
			if e["transport_id"] != tr.id || e["stream_id"] != "1" {
				t.Fatal("error correlation lost")
			}
		}
	}
	if access != 1 || errorCount != 1 || strings.Contains(out.String(), "credential") || strings.Contains(out.String(), "secret must") {
		t.Fatal("duplicate or sensitive audit log")
	}
}

func TestAuditNormalCloseNotError(t *testing.T) {
	for _, e := range []error{nil, io.EOF, net.ErrClosed, io.ErrClosedPipe, context.Canceled, fmt.Errorf("copy: %w", io.EOF), errors.Join(io.EOF, net.ErrClosed)} {
		if auditStreamFailure(e) {
			t.Fatalf("normal close classified as error: %v", e)
		}
	}
	for _, e := range []error{context.DeadlineExceeded, syscall.ECONNREFUSED, syscall.ECONNRESET, fmt.Errorf("dial: %w", syscall.ECONNREFUSED), errors.Join(io.EOF, syscall.ECONNREFUSED)} {
		if !auditStreamFailure(e) {
			t.Fatalf("real failure suppressed: %v", e)
		}
	}
}

func TestSafeIdentityWithoutAccessAudit(t *testing.T) {
	h := &Inbound{}
	h.setAuditUser("synthetic-password", "42")
	ctx := h.withSafeUserLabel(context.Background(), "synthetic-password")
	if got := AuditUserLabel(ctx, "synthetic-password"); got != "user_id=42" {
		t.Fatal("safe identity missing")
	}
	if strings.Contains(AuditUserLabel(context.Background(), "synthetic-password"), "synthetic-password") {
		t.Fatal("fallback leaked credential")
	}
}
