package sing

import (
	"context"
	"encoding/json"
	"github.com/InazumaV/V2bX/conf"
	"strconv"
	"strings"
	"testing"

	"github.com/InazumaV/V2bX/common/counter"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
)

func TestAnyTLSAuditDisabledPreservesAuthAndAccounting(t *testing.T) {
	for _, proxy := range []bool{false, true} {
		t.Run(strconv.FormatBool(proxy), func(t *testing.T) {
			capture := new(auditCapture)
			f := newReviewFixtureAudit(t, proxy, []option.AnyTLSUser{{Name: "synthetic-secret", Password: "synthetic-secret", AuditID: "1001"}}, auditLogger(t, capture), false)
			cl := f.clientPassword(t, "synthetic-secret")
			defer cl.Close()
			s, e := cl.CreateProxy(context.Background(), M.ParseSocksaddr("example.test:443"))
			if e != nil {
				t.Fatal(e)
			}
			reviewEcho(t, s, []byte("payload"))
			s.Close()
			<-f.router.done
			<-f.router.sources
			bad := f.clientPassword(t, "incorrect-secret")
			defer bad.Close()
			s, e = bad.CreateProxy(context.Background(), M.ParseSocksaddr("example.test:443"))
			if e == nil {
				e = soakExchange(s, []byte("x"))
				s.Close()
			}
			if e == nil {
				t.Fatal("incorrect auth accepted")
			}
			text := capture.text()
			if strings.Contains(text, "AUDIT_ACCESS ") || strings.Contains(text, "AUDIT_ERROR ") || strings.Contains(text, "synthetic-secret") || !strings.Contains(text, "user_id=1001") {
				t.Fatal("disabled audit must still redact credential identity")
			}
			c, _ := f.router.hook.counter.Load("review")
			v := c.(*counter.TrafficCounter)
			if v.GetUpCount("synthetic-secret") != 7 || v.GetDownCount("synthetic-secret") != 7 || v.GetUpCount("1001") != 0 {
				t.Fatal("disabled accounting changed")
			}
		})
	}
}

func TestAnyTLSAuditNodeJSONSwitch(t *testing.T) {
	for _, tc := range []struct {
		name, field string
		enabled     bool
	}{
		{"omitted", "", true}, {"enabled", `,"AnyTLSAuditAccess":true`, true},
		{"disabled", `,"AnyTLSAuditAccess":false`, false}, {"null", `,"AnyTLSAuditAccess":null`, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var n conf.NodeConfig
			if err := json.Unmarshal([]byte(`{"Core":"sing","NodeType":"anytls"`+tc.field+`}`), &n); err != nil {
				t.Fatal(err)
			}
			if n.Options.SingOptions.AnyTLSAuditAccess != tc.enabled {
				t.Fatal("unexpected parsed audit setting")
			}
			capture := new(auditCapture)
			f := newReviewFixtureAudit(t, false, []option.AnyTLSUser{{Name: "synthetic-secret", Password: "synthetic-secret", AuditID: "1001"}}, auditLogger(t, capture), n.Options.SingOptions.AnyTLSAuditAccess)
			cl := f.clientPassword(t, "synthetic-secret")
			defer cl.Close()
			s, err := cl.CreateProxy(context.Background(), M.ParseSocksaddr("example.test:443"))
			if err != nil {
				t.Fatal(err)
			}
			reviewEcho(t, s, []byte("payload"))
			s.Close()
			<-f.router.done
			<-f.router.sources
			text := capture.text()
			if strings.Contains(text, "AUDIT_ACCESS ") != tc.enabled {
				t.Fatal("audit switch did not control access output")
			}
			if tc.enabled && (len(capture.events(t)) != 1 || strings.Contains(text, "synthetic-secret")) {
				t.Fatal("expected one credential-free access event")
			}
			if strings.Contains(text, "AUDIT_ERROR ") {
				t.Fatal("normal close misreported")
			}
			c, _ := f.router.hook.counter.Load("review")
			v := c.(*counter.TrafficCounter)
			if v.GetUpCount("synthetic-secret") != 7 || v.GetDownCount("synthetic-secret") != 7 {
				t.Fatal("accounting changed")
			}
		})
	}
}
