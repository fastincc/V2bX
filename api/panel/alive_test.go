package panel

import (
	"github.com/go-resty/resty/v2"
	"net/http"
	"net/http/httptest"
	"testing"
)

func TestAliveFailureRetainsSnapshotAndLegacyCompatible(t *testing.T) {
	body := `{"alive":{"1":2},"device_snapshot":{"version":1,"ttl":180,"ips":{"1":{"192.0.2.1":170}}}}`
	code := 200
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("X-Device-IP-Snapshot") != "1" {
			t.Error("missing negotiation")
		}
		w.WriteHeader(code)
		w.Write([]byte(body))
	}))
	defer srv.Close()
	c := &Client{client: resty.New().SetBaseURL(srv.URL)}
	if a, e := c.GetUserAlive(); e != nil || a[1] != 2 {
		t.Fatal(a, e)
	}
	saved := c.AliveMap
	for _, v := range []struct {
		code int
		body string
	}{{503, "unavailable"}, {200, "<html>"}, {200, `{}`}, {200, `{"alive":{},"device_snapshot":{"version":2}}`}} {
		code, body = v.code, v.body
		if _, e := c.GetUserAlive(); e == nil {
			t.Fatal("bad response accepted")
		}
		if c.AliveMap != saved {
			t.Fatal("state lost")
		}
	}
	code = 200
	body = `{"alive":{"1":2}}`
	if _, e := c.GetUserAlive(); e != nil || c.AliveMap.Snapshot != nil {
		t.Fatal("legacy incompatible", e)
	}
}
