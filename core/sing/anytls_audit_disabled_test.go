package sing

import (
	"context"
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
			if strings.Contains(text, "AUDIT_ACCESS ") || strings.Contains(text, "AUDIT_ERROR ") || !strings.Contains(text, "[synthetic-secret] inbound connection to") {
				t.Fatal("disabled logging changed")
			}
			c, _ := f.router.hook.counter.Load("review")
			v := c.(*counter.TrafficCounter)
			if v.GetUpCount("synthetic-secret") != 7 || v.GetDownCount("synthetic-secret") != 7 || v.GetUpCount("1001") != 0 {
				t.Fatal("disabled accounting changed")
			}
		})
	}
}
