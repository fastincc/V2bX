package panel

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/InazumaV/V2bX/conf"
	"github.com/vmihailenco/msgpack/v5"
)

func userResponseEncode(t testing.TB, format string, value any) []byte {
	t.Helper()
	var body []byte
	var err error
	if format == "msgpack" {
		body, err = msgpack.Marshal(value)
	} else {
		body, err = json.Marshal(value)
	}
	if err != nil {
		t.Fatal(err)
	}
	return body
}

func userResponseType(format string) string {
	if format == "msgpack" {
		return "application/x-msgpack"
	}
	return "application/json"
}

func TestUserListRejectsMalformedSnapshot(t *testing.T) {
	for _, format := range []string{"json", "msgpack"} {
		t.Run(format, func(t *testing.T) {
			enc := func(v any) []byte { return userResponseEncode(t, format, v) }
			empty := enc(map[string]any{"users": []any{}})
			cases := map[string][]byte{
				"empty-body":       {},
				"missing-users":    enc(map[string]any{}),
				"nil-envelope":     enc(nil),
				"array-envelope":   enc([]any{map[string]any{"users": []any{}}}),
				"error-envelope":   enc(map[string]any{"error": "synthetic-private-marker"}),
				"nested-users":     enc(map[string]any{"result": map[string]any{"users": []any{}}}),
				"nil-users":        enc(map[string]any{"users": nil}),
				"object-users":     enc(map[string]any{"users": map[string]any{}}),
				"null-user":        enc(map[string]any{"users": []any{nil}}),
				"scalar-user":      enc(map[string]any{"users": []any{"synthetic-private-marker"}}),
				"invalid-user":     enc(map[string]any{"users": []any{map[string]any{"id": "synthetic-private-marker"}}}),
				"truncated":        empty[:len(empty)-1],
				"second-object":    append(append([]byte{}, empty...), empty...),
				"trailing-garbage": append(append([]byte{}, empty...), 0xc1),
			}
			if format == "json" {
				cases["duplicate-users"] = []byte(`{"users":[],"users":[]}`)
				cases["missing-envelope-end"] = []byte(`{"users":[]`)
				cases["broken-extension"] = []byte(`{"users":[],"extra":[`)
			} else {
				// A two-entry map with the same users key twice.
				cases["duplicate-users"] = append(append([]byte{0x82}, empty[1:]...), empty[1:]...)
				cases["broken-extension"] = append(append([]byte{0x82}, empty[1:]...), 0xa5, 'e', 'x', 't', 'r', 'a')
				cases["impossible-array-length"] = []byte{0x81, 0xa5, 'u', 's', 'e', 'r', 's', 0xdd, 0xff, 0xff, 0xff, 0xff}
			}
			for name, body := range cases {
				t.Run(name, func(t *testing.T) {
					s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", userResponseType(format))
						w.Header().Set("ETag", "invalid-response")
						w.Write(body)
					}))
					defer s.Close()
					c, err := New(&conf.ApiConfig{APIHost: s.URL, NodeType: "anytls"})
					if err != nil {
						t.Fatal(err)
					}
					users, err := c.GetUserList()
					if err == nil || users != nil {
						t.Fatal("malformed snapshot accepted as an authoritative user list")
					}
					if strings.Contains(err.Error(), "synthetic-private-marker") {
						t.Fatal("decoder error leaked response content")
					}
					c.CommitUserList()
					if c.userEtag != "" || c.userResponsePending {
						t.Fatal("malformed response committed an ETag")
					}
				})
			}
		})
	}
}

func TestUserListValidEnvelopeAndRecovery(t *testing.T) {
	for _, format := range []string{"json", "msgpack"} {
		for _, etag := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/etag=%v", format, etag), func(t *testing.T) {
				var stage atomic.Int32
				var sawConditional atomic.Bool
				body := userResponseEncode(t, format, map[string]any{
					"extra": map[string]any{"users": []any{}, "future": true},
					"users": []any{map[string]any{"id": 17, "uuid": "synthetic-user", "speed_limit": 7, "device_limit": 2, "future": []int{1, 2}}},
				})
				empty := userResponseEncode(t, format, map[string]any{"users": []any{}})
				invalid := userResponseEncode(t, format, map[string]any{"error": "temporary"})
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", userResponseType(format))
					if etag {
						w.Header().Set("ETag", "same-etag")
					}
					switch stage.Load() {
					case 0:
						w.Write(body)
					case 1:
						w.Write(invalid)
					case 2:
						if r.Header.Get("If-None-Match") != "" {
							sawConditional.Store(true)
							w.WriteHeader(304)
							return
						}
						w.Write(empty)
					default:
						if etag && r.Header.Get("If-None-Match") == "same-etag" {
							w.WriteHeader(304)
						} else {
							w.Write(empty)
						}
					}
				}))
				defer s.Close()
				c, _ := New(&conf.ApiConfig{APIHost: s.URL, NodeType: "anytls"})
				u, err := c.GetUserList()
				if err != nil || len(u) != 1 || u[0].Id != 17 || u[0].Uuid != "synthetic-user" || u[0].DeviceLimit != 2 || u[0].SpeedLimit != 7 {
					t.Fatal("valid legacy fields or unknown extensions broke", err)
				}
				c.CommitUserList()
				stage.Store(1)
				if u, err = c.GetUserList(); err == nil || u != nil {
					t.Fatal("invalid snapshot accepted")
				}
				c.CommitUserList() // even an accidental commit cannot acknowledge failure
				stage.Store(2)
				if u, err = c.GetUserList(); err != nil || u == nil || len(u) != 0 || sawConditional.Load() {
					t.Fatal("full recovery or explicit empty list lost", err)
				}
				c.CommitUserList()
				stage.Store(3)
				u, err = c.GetUserList()
				if err != nil || (u == nil) != etag {
					t.Fatal("304 and explicit empty snapshot must remain distinct", err)
				}
			})
		}
	}
}

func TestUserListRequiresSuccessStatus(t *testing.T) {
	for _, status := range []int{201, 204, 206, 500} {
		t.Run(fmt.Sprint(status), func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(status)
				w.Write([]byte(`{"users":[]}`))
			}))
			defer s.Close()
			c, _ := New(&conf.ApiConfig{APIHost: s.URL, NodeType: "anytls"})
			if users, err := c.GetUserList(); err == nil || users != nil {
				t.Fatal("non-200 response accepted as snapshot")
			}
		})
	}
}

// The benchmark exercises the public HTTP path so it can also run unchanged on
// the release baseline. It includes loopback HTTP overhead, not just decoding.
func BenchmarkUserListHTTP(b *testing.B) {
	for _, format := range []string{"json", "msgpack"} {
		for _, size := range []int{100, 10000} {
			b.Run(fmt.Sprintf("%s/%d", format, size), func(b *testing.B) {
				users := make([]UserInfo, size)
				for i := range users {
					users[i] = UserInfo{Id: i + 1, Uuid: fmt.Sprintf("synthetic-%d", i), DeviceLimit: 2}
				}
				body := userResponseEncode(b, format, UserListBody{Users: users})
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", userResponseType(format))
					w.Write(body)
				}))
				defer s.Close()
				c, _ := New(&conf.ApiConfig{APIHost: s.URL, NodeType: "anytls"})
				b.ReportAllocs()
				b.SetBytes(int64(len(body)))
				b.ResetTimer()
				for i := 0; i < b.N; i++ {
					got, err := c.GetUserList()
					if err != nil || len(got) != size {
						b.Fatal("snapshot decode failed", err)
					}
				}
			})
		}
	}
}

func TestUserListJSONWhitespaceAccepted(t *testing.T) {
	// Keep whitespace legal without accepting a second document or junk.
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(" \n{\"users\":[]}\n \t"))
	}))
	defer s.Close()
	c, _ := New(&conf.ApiConfig{APIHost: s.URL, NodeType: "anytls"})
	u, err := c.GetUserList()
	if err != nil || u == nil || len(u) != 0 {
		t.Fatal("JSON whitespace rejected", err)
	}
}
