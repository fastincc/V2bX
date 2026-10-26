package sing

import (
	"context"
	"fmt"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/InazumaV/V2bX/common/format"
	"github.com/InazumaV/V2bX/limiter"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	"github.com/sagernet/sing/common/uot"
)

// Regression coverage for explicit policy and authenticated parse errors.
func TestAnyTLSAuditPolicyDenialCoverage(t *testing.T) {
	capture := new(auditCapture)
	logger := auditLogger(t, capture)
	old := log.StdLogger()
	log.SetStdLogger(logger)
	defer log.SetStdLogger(old)
	f := newReviewFixtureAudit(t, false, []option.AnyTLSUser{{Name: "synthetic-secret", Password: "synthetic-secret", AuditID: "1001"}}, logger, true)
	limit, e := limiter.GetLimiter("review")
	if e != nil {
		t.Fatal(e)
	}
	limit.DomainRules = []*regexp.Regexp{regexp.MustCompile(`^blocked\.test$`)}
	cl := f.clientPassword(t, "synthetic-secret")
	defer cl.Close()
	c, e := cl.CreateProxy(context.Background(), M.ParseSocksaddr("blocked.test:443"))
	if e != nil {
		t.Fatal(e)
	}
	defer c.Close()
	c.SetDeadline(time.Now().Add(time.Second))
	c.Write([]byte("x"))
	if _, e = c.Read(make([]byte, 1)); e == nil {
		t.Fatal("rule did not reject")
	}
	<-f.router.done
	<-f.router.sources
	s := capture.text()
	if len(capture.events(t)) != 1 || !strings.Contains(s, "reject by rule") || !strings.Contains(s, "user_id=1001") || strings.Contains(s, "synthetic-secret") {
		t.Fatal("denial identity/logging changed")
	}
	if strings.Count(s, "AUDIT_ERROR ") != 1 || !strings.Contains(s, `"code":"policy_domain_denied"`) || !strings.Contains(s, `"access_emitted":true`) {
		t.Fatal("missing or duplicate policy error")
	}
	t.Log("policy denial: one access, one correlated error")
}
func TestAnyTLSAuditMalformedUoTCoverage(t *testing.T) {
	capture := new(auditCapture)
	f := newReviewFixtureAudit(t, false, []option.AnyTLSUser{{Name: "synthetic-secret", Password: "synthetic-secret", AuditID: "1001"}}, auditLogger(t, capture), true)
	cl := f.clientPassword(t, "synthetic-secret")
	defer cl.Close()
	c, e := cl.CreateProxy(context.Background(), M.Socksaddr{Fqdn: uot.MagicAddress, Port: 443})
	if e != nil {
		t.Fatal(e)
	}
	c.Write([]byte{1})
	c.Close()
	until := time.Now().Add(time.Second)
	for !strings.Contains(capture.text(), "UoT read request") && time.Now().Before(until) {
		time.Sleep(5 * time.Millisecond)
	}
	s := capture.text()
	if !strings.Contains(s, "UoT read request") {
		t.Fatal("expected original UoT error log")
	}
	if strings.Contains(s, "AUDIT_ACCESS ") || strings.Count(s, "AUDIT_ERROR ") != 1 || !strings.Contains(s, `"code":"uot_request_invalid"`) || !strings.Contains(s, `"access_emitted":false`) || !strings.Contains(s, `"target_host":null`) {
		t.Fatal("invalid UoT error semantics")
	}
	t.Log("malformed UoT: one standalone error, no fabricated access")
}

func TestAnyTLSAuditUoTEmptyCloseAndDisabled(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		for _, partial := range []bool{false, true} {
			t.Run(fmt.Sprintf("enabled=%v/partial=%v", enabled, partial), func(t *testing.T) {
				capture := new(auditCapture)
				f := newReviewFixtureAudit(t, false, []option.AnyTLSUser{{Name: "synthetic-secret", Password: "synthetic-secret", AuditID: "1001"}}, auditLogger(t, capture), enabled)
				cl := f.clientPassword(t, "synthetic-secret")
				defer cl.Close()
				c, e := cl.CreateProxy(context.Background(), M.Socksaddr{Fqdn: uot.MagicAddress, Port: 443})
				if e != nil {
					t.Fatal(e)
				}
				if partial {
					c.Write([]byte{1})
				}
				c.Close()
				until := time.Now().Add(time.Second)
				for !strings.Contains(capture.text(), "UoT read request") && time.Now().Before(until) {
					time.Sleep(5 * time.Millisecond)
				}
				text := capture.text()
				if !strings.Contains(text, "UoT read request") {
					t.Fatal("expected unchanged decode/close path")
				}
				want := 0
				if enabled && partial {
					want = 1
				}
				if strings.Count(text, "AUDIT_ERROR ") != want {
					t.Fatal("empty close false positive or disabled behavior changed")
				}
			})
		}
	}
}

func TestAnyTLSAuditOtherPolicyBranches(t *testing.T) {
	for _, enabled := range []bool{true, false} {
		for _, kind := range []string{"protocol", "limit"} {
			t.Run(fmt.Sprintf("%v/%s", enabled, kind), func(t *testing.T) {
				capture := new(auditCapture)
				logger := auditLogger(t, capture)
				old := log.StdLogger()
				log.SetStdLogger(logger)
				defer log.SetStdLogger(old)
				f := newReviewFixtureAudit(t, false, []option.AnyTLSUser{{Name: "synthetic-secret", Password: "synthetic-secret", AuditID: "1001"}}, logger, enabled)
				l, _ := limiter.GetLimiter("review")
				if kind == "protocol" {
					l.ProtocolRules = []string{"tls"}
					f.router.testProtocol = "tls"
				} else {
					v, _ := l.UserLimitInfo.Load(format.UserTag("review", "synthetic-secret"))
					v.(*limiter.UserLimitInfo).DeviceLimit = 1
					l.AliveList[123] = 1
				}
				cl := f.clientPassword(t, "synthetic-secret")
				defer cl.Close()
				c, e := cl.CreateProxy(context.Background(), M.ParseSocksaddr("example.test:443"))
				if e != nil {
					t.Fatal(e)
				}
				defer c.Close()
				c.SetDeadline(time.Now().Add(time.Second))
				c.Write([]byte("x"))
				if _, e = c.Read(make([]byte, 1)); e == nil {
					t.Fatal("policy did not reject")
				}
				<-f.router.done
				<-f.router.sources
				s := capture.text()
				want := 0
				if enabled {
					want = 1
					if !strings.Contains(s, `"code":"policy_`+kind+`_denied"`) {
						t.Fatal("wrong policy reason")
					}
				}
				if strings.Count(s, "AUDIT_ERROR ") != want {
					t.Fatal("wrong error count")
				}
			})
		}
	}
}
