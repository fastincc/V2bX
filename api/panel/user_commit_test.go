package panel

import (
	"fmt"
	"github.com/InazumaV/V2bX/conf"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestUserSyncETagRequiresAppliedCommit(t *testing.T) {
	for _, etag := range []bool{true, false} {
		t.Run(fmt.Sprint(etag), func(t *testing.T) {
			bodies := 0
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if etag && r.Header.Get("If-None-Match") == "applied" {
					w.WriteHeader(304)
					return
				}
				bodies++
				if etag {
					w.Header().Set("ETag", "applied")
				}
				w.Header().Set("Content-Type", "application/json")
				fmt.Fprint(w, `{"users":[]}`)
			}))
			defer s.Close()
			c, e := New(&conf.ApiConfig{APIHost: s.URL, NodeType: "anytls"})
			if e != nil {
				t.Fatal(e)
			}
			for i := 0; i < 2; i++ {
				users, e := c.GetUserList()
				if e != nil || users == nil {
					t.Fatal("unapplied list suppressed", e)
				}
			}
			c.CommitUserList()
			users, e := c.GetUserList()
			if e != nil {
				t.Fatal(e)
			}
			if etag && users != nil {
				t.Fatal("committed ETag ignored")
			}
			if !etag && users == nil {
				t.Fatal("legacy panel broke")
			}
			c.InvalidateUserCache()
			users, e = c.GetUserList()
			if e != nil || users == nil {
				t.Fatal("failed state cannot refetch", e)
			}
			want := 3
			if !etag {
				want = 4
			}
			if bodies != want {
				t.Fatal("wrong response count", bodies)
			}
		})
	}
}
