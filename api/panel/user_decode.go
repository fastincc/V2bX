package panel

import (
	"encoding/json/jsontext"
	"encoding/json/v2"
	"errors"
	"io"

	"github.com/vmihailenco/msgpack/v5"
	"github.com/vmihailenco/msgpack/v5/msgpcode"
)

// Errors must never wrap decoder errors: they can contain credentials from the
// response. A snapshot is authoritative only after the entire envelope passes.
var errUserSnapshot = errors.New("invalid user list response: expected one complete object with a users array")

func decodeUserListJSON(r io.Reader) ([]UserInfo, error) {
	d := jsontext.NewDecoder(r)
	tok, err := d.ReadToken()
	if err != nil || tok.Kind() != '{' {
		return nil, errUserSnapshot
	}
	var users []UserInfo
	seen := false
	for d.PeekKind() != '}' {
		key, err := d.ReadToken()
		if err != nil || key.Kind() != '"' {
			return nil, errUserSnapshot
		}
		if key.String() != "users" {
			if err := d.SkipValue(); err != nil {
				return nil, errUserSnapshot
			}
			continue
		}
		if seen {
			return nil, errUserSnapshot
		}
		seen = true
		tok, err = d.ReadToken()
		if err != nil || tok.Kind() != '[' {
			return nil, errUserSnapshot
		}
		// An explicit empty array must remain non-nil; nil means HTTP 304.
		users = make([]UserInfo, 0)
		for d.PeekKind() != ']' {
			if d.PeekKind() != '{' {
				return nil, errUserSnapshot
			}
			val, err := d.ReadValue()
			if err != nil {
				return nil, errUserSnapshot
			}
			var u UserInfo
			if err := json.Unmarshal(val, &u); err != nil {
				return nil, errUserSnapshot
			}
			users = append(users, u)
		}
		if _, err = d.ReadToken(); err != nil { // close users array
			return nil, errUserSnapshot
		}
	}
	if _, err = d.ReadToken(); err != nil || !seen { // close envelope
		return nil, errUserSnapshot
	}
	if _, err = d.ReadToken(); err != io.EOF {
		return nil, errUserSnapshot
	}
	return users, nil
}

func messagePackMap(code byte) bool {
	return msgpcode.IsFixedMap(code) || code == msgpcode.Map16 || code == msgpcode.Map32
}

func decodeUserListMessagePack(r io.Reader) ([]UserInfo, error) {
	d := msgpack.NewDecoder(r)
	code, err := d.PeekCode()
	if err != nil || !messagePackMap(code) {
		return nil, errUserSnapshot
	}
	fields, err := d.DecodeMapLen()
	if err != nil {
		return nil, errUserSnapshot
	}
	var users []UserInfo
	seen := false
	for i := 0; i < fields; i++ {
		code, err := d.PeekCode()
		if err != nil || !msgpcode.IsString(code) {
			return nil, errUserSnapshot
		}
		key, err := d.DecodeString()
		if err != nil {
			return nil, errUserSnapshot
		}
		if key != "users" {
			if err := d.Skip(); err != nil {
				return nil, errUserSnapshot
			}
			continue
		}
		if seen {
			return nil, errUserSnapshot
		}
		seen = true
		n, err := d.DecodeArrayLen()
		if err != nil || n < 0 {
			return nil, errUserSnapshot
		}
		// Do not allocate an unbounded slice from an untrusted length prefix.
		users = make([]UserInfo, 0, min(n, 1024))
		var chunks [][]UserInfo
		for j := 0; j < n; j++ {
			code, err := d.PeekCode()
			if err != nil || !messagePackMap(code) {
				return nil, errUserSnapshot
			}
			if len(users) == cap(users) {
				chunks = append(chunks, users)
				users = make([]UserInfo, 0, min(n-j, 1024))
			}
			users = append(users, UserInfo{})
			if err := d.Decode(&users[len(users)-1]); err != nil {
				return nil, errUserSnapshot
			}
		}
		if len(chunks) > 0 {
			// Flatten only after all declared rows exist. Chunked decoding
			// avoids both huge prefix allocations and per-row heap objects.
			flat := make([]UserInfo, n)
			offset := 0
			for _, chunk := range chunks {
				offset += copy(flat[offset:], chunk)
			}
			copy(flat[offset:], users)
			users = flat
		}
	}
	if !seen {
		return nil, errUserSnapshot
	}
	if _, err = d.PeekCode(); err != io.EOF {
		return nil, errUserSnapshot
	}
	return users, nil
}
