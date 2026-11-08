package panel

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/InazumaV/V2bX/conf"
	"github.com/vmihailenco/msgpack/v5"
)

// All inputs are small, truncated wire messages. The length boundary is in the
// header, not in an allocated multi-gigabyte test buffer. Keep the public HTTP
// API here so the same test runs against the unmodified dependency as an oracle.
func TestUserListMessagePackLengthBoundaries(t *testing.T) {
	t.Logf("runtime_int_bits=%d", strconv.IntSize)
	codes := []struct {
		name string
		code byte
	}{{"array32", 0xdd}, {"map32", 0xdf}, {"str32", 0xdb}, {"bin32", 0xc6}, {"ext32-missing-type", 0xc9}}
	for _, where := range []string{"top-unknown", "user-unknown", "policy-unknown", "policy-value", "top-key", "user-key", "policy-key", "user-map", "user-uuid"} {
		for _, code := range codes {
			if (where == "top-key" || where == "user-key" || where == "policy-key") && code.code != 0xdb {
				continue
			}
			if where == "user-map" && code.code != 0xdf {
				continue
			}
			if where == "user-uuid" && code.code != 0xdb && code.code != 0xc6 {
				continue
			}
			for _, size := range []uint32{0x7fffffff, 0x80000000, 0xffffffff} {
				t.Run(fmt.Sprintf("%s/%s/%08x", where, code.name, size), func(t *testing.T) {
					tail := []byte{code.code, 0, 0, 0, 0}
					binary.BigEndian.PutUint32(tail[1:], size)
					body := userLengthEnvelope(t, where, tail)
					defer func() {
						if recovered := recover(); recovered != nil {
							t.Fatalf("malformed wire length panicked instead of rejecting the snapshot: %v", recovered)
						}
					}()
					s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
						w.Header().Set("Content-Type", "application/x-msgpack")
						w.Header().Set("ETag", "malformed-length")
						_, _ = w.Write(body)
					}))
					defer s.Close()
					c, err := New(&conf.ApiConfig{APIHost: s.URL, NodeType: "anytls"})
					if err != nil {
						t.Fatal(err)
					}
					users, err := c.GetUserList()
					if err == nil || users != nil {
						t.Fatal("truncated length became an authoritative or partial user list")
					}
					c.CommitUserList()
					if c.userEtag != "" || c.userResponsePending {
						t.Fatal("invalid length response was acknowledged")
					}
				})
			}
		}
	}
}

func userLengthEnvelope(t testing.TB, where string, tail []byte) []byte {
	t.Helper()
	var b bytes.Buffer
	e := msgpack.NewEncoder(&b)
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	if where == "top-unknown" || where == "top-key" {
		must(e.EncodeMapLen(2))
		must(e.EncodeString("users"))
		must(e.EncodeArrayLen(0))
		if where == "top-unknown" {
			must(e.EncodeString("extra"))
		}
	} else {
		must(e.EncodeMapLen(1))
		must(e.EncodeString("users"))
		must(e.EncodeArrayLen(1))
		switch where {
		case "user-map": // tail itself declares the user's incomplete map
		case "user-key":
			must(e.EncodeMapLen(1))
		default:
			if where == "user-uuid" {
				must(e.EncodeMapLen(2))
			} else {
				must(e.EncodeMapLen(3))
			}
			must(e.EncodeString("id"))
			must(e.EncodeInt(1))
			must(e.EncodeString("uuid"))
			if where != "user-uuid" {
				must(e.EncodeString("synthetic-length-user"))
				switch where {
				case "user-unknown":
					must(e.EncodeString("extra"))
				case "policy-value", "policy-unknown", "policy-key":
					must(e.EncodeString("dynamic_speed_limit"))
					if where == "policy-unknown" {
						must(e.EncodeMapLen(2))
						must(e.EncodeString("enabled"))
						must(e.EncodeBool(false))
						must(e.EncodeString("extra"))
					} else if where == "policy-key" {
						must(e.EncodeMapLen(1))
					}
				default:
					t.Fatal("unknown envelope fixture")
				}
			}
		}
	}
	b.Write(tail)
	if where == "top-key" || where == "user-key" || where == "policy-key" {
		// A nil value after the impossible key exposes decoders that turn the
		// negative key length into an empty key, then accept the map as complete.
		b.WriteByte(0xc0)
	}
	return b.Bytes()
}

func TestUserListMessagePackValidWideLengthHeaders(t *testing.T) {
	// Non-minimal but legal wide headers must remain accepted. Include nested
	// unknown fields and a structurally valid invalid policy, whose failure is
	// deliberately isolated from the user's authorization by the old contract.
	for _, tail := range [][]byte{
		{0xdd, 0, 0, 0, 1, 0xc0},
		{0xdf, 0, 0, 0, 1, 0xa1, 'k', 0xc0},
		{0xdb, 0, 0, 0, 1, 'v'},
		{0xc6, 0, 0, 0, 1, 0x7f},
		{0xc9, 0, 0, 0, 0, 0x7e},
		{0xc9, 0, 0, 0, 1, 0x7e, 0x42},
	} {
		for _, where := range []string{"top-unknown", "user-unknown", "policy-unknown"} {
			t.Run(fmt.Sprintf("%s/%x", where, tail), func(t *testing.T) {
				body := userLengthEnvelope(t, where, tail)
				s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					w.Header().Set("Content-Type", "application/x-msgpack")
					_, _ = w.Write(body)
				}))
				defer s.Close()
				c, err := New(&conf.ApiConfig{APIHost: s.URL, NodeType: "anytls"})
				if err != nil {
					t.Fatal(err)
				}
				users, err := c.GetUserList()
				if err != nil || users == nil {
					t.Fatal("valid wide-header extension rejected", err)
				}
				want := 1
				if where == "top-unknown" {
					want = 0
				}
				if len(users) != want || (want == 1 && (users[0].Id != 1 || users[0].Uuid != "synthetic-length-user")) {
					t.Fatal("valid extension changed user identity")
				}
				if where == "policy-unknown" && (users[0].DynamicSpeedLimit == nil || users[0].DynamicSpeedLimit.Validate() == nil) {
					t.Fatal("structurally valid invalid policy was not kept isolated")
				}
			})
		}
	}
}
