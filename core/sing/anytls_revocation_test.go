package sing

import (
	"context"
	"github.com/sagernet/sing-box/log"
	"github.com/sagernet/sing-box/option"
	M "github.com/sagernet/sing/common/metadata"
	"io"
	"testing"
	"time"
)

func TestAnyTLSRevocationClosesTransport(t *testing.T) {
	for _, proxy := range []bool{false, true} {
		for _, audit := range []bool{false, true} {
			f := newReviewFixtureAudit(t, proxy, []option.AnyTLSUser{{Name: "synthetic-a", Password: "secret-a", AuditID: "123"}, {Name: "synthetic-b", Password: "secret-b", AuditID: "124"}}, log.NewNOPFactory().Logger(), audit)
			a := f.clientPassword(t, "secret-a")
			b := f.clientPassword(t, "secret-b")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			sa, err := a.CreateProxy(ctx, M.ParseSocksaddr("echo.invalid:80"))
			if err != nil {
				t.Fatal(err)
			}
			sb, err := b.CreateProxy(ctx, M.ParseSocksaddr("echo.invalid:80"))
			if err != nil {
				t.Fatal(err)
			}
			reviewEcho(t, sa, []byte("before-a"))
			reviewEcho(t, sb, []byte("before-b"))
			// Closing one stream must not remove the transport from the revocation index.
			another, err := a.CreateProxy(ctx, M.ParseSocksaddr("echo.invalid:80"))
			if err != nil {
				t.Fatal(err)
			}
			reviewEcho(t, another, []byte("short-stream"))
			another.Close()
			if err = f.inbound.DelUsers([]string{"synthetic-a"}); err != nil {
				t.Fatal(err)
			}
			sa.SetDeadline(time.Now().Add(2 * time.Second))
			_, werr := sa.Write([]byte("after-a"))
			buf := make([]byte, 7)
			_, rerr := io.ReadFull(sa, buf)
			if werr == nil && rerr == nil {
				t.Fatal("revoked stream still transfers")
			}
			reviewEcho(t, sb, []byte("unaffected-b"))
			if err = f.inbound.AddUsers([]option.AnyTLSUser{{Name: "synthetic-a", Password: "secret-a", AuditID: "123"}}); err != nil {
				t.Fatal(err)
			}
			fresh := f.clientPassword(t, "secret-a")
			restored, err := fresh.CreateProxy(ctx, M.ParseSocksaddr("echo.invalid:80"))
			if err != nil {
				t.Fatal(err)
			}
			reviewEcho(t, restored, []byte("restored"))
			restored.Close()
			fresh.Close()
			sa.Close()
			sb.Close()
			a.Close()
			b.Close()
			cancel()
		}
	}
}
