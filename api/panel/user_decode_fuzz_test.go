package panel

import (
	"bytes"
	"testing"
)

func FuzzUserListEnvelope(f *testing.F) {
	for _, seed := range []string{`{"users":[]}`, `{}`, `{"users":null}`, `{"users":[{"id":1,"uuid":"synthetic"}]}`, `{"users":[],"meta":`} {
		f.Add([]byte(seed), false)
	}
	f.Add([]byte{0x81, 0xa5, 'u', 's', 'e', 'r', 's', 0x90}, true)
	f.Add([]byte{0x80}, true)
	f.Fuzz(func(t *testing.T, body []byte, binary bool) {
		if len(body) > 64<<10 {
			t.Skip()
		}
		decode := decodeUserListJSON
		if binary {
			decode = decodeUserListMessagePack
		}
		users, err := decode(bytes.NewReader(body))
		if err != nil {
			if users != nil || err != errUserSnapshot {
				t.Fatal("failed response exposed a partial snapshot or decoder details")
			}
		} else if users == nil {
			t.Fatal("successful response confused with HTTP 304")
		}
	})
}
