package anytls

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"strings"
	"sync"
	"testing"

	"github.com/sagernet/sing-box/adapter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
)

func TestAuditSharedFailureDedup(t *testing.T) {
	for _, code := range []string{"policy_domain_denied", "policy_protocol_denied", "policy_limit_denied"} {
		t.Run(code, func(t *testing.T) {
			initAuditInstance()
			var b bytes.Buffer
			f, e := log.New(log.Options{Context: context.Background(), DefaultWriter: &b, Options: option.LogOptions{Level: "info", DisableColor: true}})
			if e != nil {
				t.Fatal(e)
			}
			defer f.Close()
			h := &Inbound{logger: f.Logger()}
			h.setAuditUser("credential", "1001")
			ctx := h.newAuditStream(context.WithValue(context.Background(), auditTransportKey{}, newAuditTransport(M.ParseSocksaddr("127.0.0.1:1234"))), "credential")
			close1 := h.auditAccess(ctx, adapter.InboundContext{Destination: M.ParseSocksaddr("example.test:443")}, "tcp", nil)
			close2 := h.auditAccess(ctx, adapter.InboundContext{}, "tcp", nil)
			AuditPolicyDenied(ctx, code)
			var wg sync.WaitGroup
			for i := 0; i < 32; i++ {
				wg.Add(1)
				go func() {
					defer wg.Done()
					AuditPolicyDenied(ctx, code)
					close1(errors.New("later failure"))
					close2(errors.New("another callback"))
				}()
			}
			wg.Wait()
			if strings.Count(b.String(), "AUDIT_ACCESS ") != 1 || strings.Count(b.String(), "AUDIT_ERROR ") != 1 {
				t.Fatal("duplicate stream logs")
			}
			for _, line := range strings.Split(b.String(), "\n") {
				if at := strings.Index(line, "AUDIT_ERROR "); at >= 0 {
					var v map[string]any
					if e = json.Unmarshal([]byte(line[at+12:]), &v); e != nil {
						t.Fatal(e)
					}
					if v["user_id"] != "1001" || v["code"] != code || v["stage"] != "policy" || v["access_emitted"] != true || v["target_host"] != "example.test" {
						t.Fatal("bad policy context")
					}
				}
			}
		})
	}
	AuditPolicyDenied(context.Background(), "policy_domain_denied") // audit off must be a no-op
}
func TestAuditUoTNoFalseAccess(t *testing.T) {
	initAuditInstance()
	var b bytes.Buffer
	f, _ := log.New(log.Options{Context: context.Background(), DefaultWriter: &b, Options: option.LogOptions{Level: "info", DisableColor: true}})
	defer f.Close()
	h := &Inbound{logger: f.Logger()}
	h.setAuditUser("credential", "1001")
	ctx := h.newAuditStream(context.WithValue(context.Background(), auditTransportKey{}, newAuditTransport(M.ParseSocksaddr("127.0.0.1:1234"))), "credential")
	h.auditUoTError(ctx, adapter.InboundContext{}, 0, io.EOF)
	if b.Len() != 0 {
		t.Fatal("empty close misreported")
	}
	h.auditUoTError(ctx, adapter.InboundContext{}, 1, io.EOF)
	h.auditUoTError(ctx, adapter.InboundContext{}, 1, io.ErrUnexpectedEOF)
	if strings.Contains(b.String(), "AUDIT_ACCESS ") || strings.Count(b.String(), "AUDIT_ERROR ") != 1 || !strings.Contains(b.String(), `"access_emitted":false`) || !strings.Contains(b.String(), `"target_host":null`) {
		t.Fatal("bad protocol event")
	}
}
