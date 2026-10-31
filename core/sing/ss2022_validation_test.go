package sing

import (
	"bytes"
	"encoding/base64"
	"fmt"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	vcore "github.com/InazumaV/V2bX/core"
	"strings"
	"testing"
)

func TestSS2022CredentialValidation(t *testing.T) {
	for _, n := range []int{16, 32} {
		cipher := fmt.Sprintf("2022-blake3-aes-%d-gcm", n*8)
		for _, size := range []int{0, 1, n - 1, n, n + 1, 36} {
			u := panel.UserInfo{Id: 42, Uuid: strings.Repeat("x", size)}
			err := validateSS2022Credentials(cipher, []panel.UserInfo{u})
			if (err != nil) != (size < n) {
				t.Fatalf("cipher=%s len=%d err=%v", cipher, size, err)
			}
		}
		// Validation is in bytes, matching the existing byte-prefix derivation.
		if err := validateSS2022Credentials(cipher, []panel.UserInfo{{Id: 1, Uuid: strings.Repeat("界", n/3+1)}}); err != nil {
			t.Fatal(err)
		}
		c, e := New(&conf.CoreConfig{SingConfig: conf.NewSingConfig()})
		if e != nil {
			t.Fatal(e)
		}
		s := c.(*Sing)
		info := &panel.NodeInfo{Type: "shadowsocks", Common: &panel.CommonNode{ServerPort: 0}, Shadowsocks: &panel.ShadowsocksNode{Cipher: cipher, ServerKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, n))}}
		if e = s.AddNode("ss-validation", info, &conf.Options{ListenIP: "127.0.0.1", SingOptions: &conf.SingOptions{}}); e != nil {
			t.Fatal(e)
		}
		original := panel.UserInfo{Id: 1, Uuid: strings.Repeat("a", 36)}
		if _, e = s.AddUsers(&vcore.AddUsersParams{Tag: "ss-validation", NodeInfo: info, Users: []panel.UserInfo{original}}); e != nil {
			t.Fatal(e)
		}
		valid := panel.UserInfo{Id: 2, Uuid: strings.Repeat("b", 36)}
		bad := panel.UserInfo{Id: 3, Uuid: "bad-secret"}
		for _, batch := range [][]panel.UserInfo{{valid, bad}, {bad, valid}} {
			added, err := s.AddUsers(&vcore.AddUsersParams{Tag: "ss-validation", NodeInfo: info, Users: batch})
			if added != 0 || err == nil {
				t.Fatal("invalid addition succeeded", added, err)
			}
			if strings.Contains(err.Error(), bad.Uuid) || strings.Contains(err.Error(), valid.Uuid) {
				t.Fatal("credential in error")
			}
			if len(s.users.active) != 1 {
				t.Fatal("partial identity mapping", len(s.users.active))
			}
		}
		if _, e = s.AddUsers(&vcore.AddUsersParams{Tag: "ss-validation", NodeInfo: info, Users: []panel.UserInfo{valid}}); e != nil {
			t.Fatal("retry failed", e)
		}
		s.Close()
	}
	if err := validateSS2022Credentials("aes-128-gcm", []panel.UserInfo{{Uuid: "x"}}); err != nil {
		t.Fatal("legacy SS changed")
	}
}
func BenchmarkSS2022CredentialValidation(b *testing.B) {
	users := make([]panel.UserInfo, 10000)
	for i := range users {
		users[i] = panel.UserInfo{Id: i + 1, Uuid: "synthetic-benchmark-credential-0000000"}
	}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if e := validateSS2022Credentials("2022-blake3-aes-256-gcm", users); e != nil {
			b.Fatal(e)
		}
	}
}
