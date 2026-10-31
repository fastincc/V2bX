package sing

import (
	"bytes"
	"encoding/base64"
	"github.com/InazumaV/V2bX/api/panel"
	"github.com/InazumaV/V2bX/conf"
	vcore "github.com/InazumaV/V2bX/core"
	"testing"
)

func TestSS2022ShortCredentialRejectedWithoutPanic(t *testing.T) {
	for _, n := range []int{16, 32} {
		t.Run(string(rune('A'+n)), func(t *testing.T) {
			c, e := New(&conf.CoreConfig{SingConfig: conf.NewSingConfig()})
			if e != nil {
				t.Fatal(e)
			}
			s := c.(*Sing)
			defer s.Close()
			cipher := "2022-blake3-aes-128-gcm"
			if n == 32 {
				cipher = "2022-blake3-aes-256-gcm"
			}
			info := &panel.NodeInfo{Type: "shadowsocks", Common: &panel.CommonNode{ServerPort: 0}, Shadowsocks: &panel.ShadowsocksNode{Cipher: cipher, ServerKey: base64.StdEncoding.EncodeToString(bytes.Repeat([]byte{42}, n))}}
			if e = s.AddNode("audit-short", info, &conf.Options{ListenIP: "127.0.0.1", SingOptions: &conf.SingOptions{}}); e != nil {
				t.Fatal(e)
			}
			panicked := false
			var addErr error
			func() {
				defer func() {
					if recover() != nil {
						panicked = true
					}
				}()
				_, addErr = s.AddUsers(&vcore.AddUsersParams{Tag: "audit-short", NodeInfo: info, Users: []panel.UserInfo{{Id: 1, Uuid: "short"}}})
			}()
			if panicked || addErr == nil {
				t.Fatal("expected safe validation error", panicked, addErr)
			}
			t.Logf("SS2022 key_bytes=%d short panel credential safely rejected", n)
		})
	}
}
