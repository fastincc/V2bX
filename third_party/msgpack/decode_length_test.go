package msgpack_test

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"strconv"
	"testing"

	"github.com/vmihailenco/msgpack/v5"
)

func TestLength32PlatformRange(t *testing.T) {
	for _, code := range []byte{0xdd, 0xdf, 0xdb, 0xc6, 0xc9} {
		for _, n := range []uint32{0, 1, 0x7fffffff, 0x80000000, 0xffffffff} {
			t.Run(fmt.Sprintf("%x/%08x", code, n), func(t *testing.T) {
				raw := []byte{code, 0, 0, 0, 0, 0x7e}
				binary.BigEndian.PutUint32(raw[1:5], n)
				d := msgpack.NewDecoder(bytes.NewReader(raw))
				var got int
				var err error
				switch code {
				case 0xdd:
					got, err = d.DecodeArrayLen()
				case 0xdf:
					got, err = d.DecodeMapLen()
				case 0xdb, 0xc6:
					got, err = d.DecodeBytesLen()
				case 0xc9:
					_, got, err = d.DecodeExtHeader()
				}
				if strconv.IntSize == 32 && n > 0x7fffffff {
					if err == nil {
						t.Fatal("unrepresentable length accepted", got)
					}
				} else if err != nil || uint64(got) != uint64(n) {
					t.Fatal("representable length changed", got, err)
				}
			})
		}
	}
}

func TestExtensionSkipTypeAndRawRecording(t *testing.T) {
	for _, raw := range [][]byte{
		{0xc9, 0, 0, 0, 0, 0x7e},
		{0xc9, 0, 0, 0, 1, 0x7e, 0x42},
		{0xd4, 0x7e, 0x42},
		{0xc7, 1, 0x7e, 0x42},
		{0xc8, 0, 1, 0x7e, 0x42},
	} {
		d := msgpack.NewDecoder(bytes.NewReader(append(append([]byte{}, raw...), 42)))
		got, err := d.DecodeRaw()
		if err != nil || !bytes.Equal(got, raw) {
			t.Fatal("extension raw recording changed", got, err)
		}
		n, err := d.DecodeInt()
		if err != nil || n != 42 {
			t.Fatal("extension skip consumed adjacent value", n, err)
		}
	}
	for _, raw := range [][]byte{{0xc9, 0, 0, 0, 0}, {0xc9, 0, 0, 0, 1, 0x7e}, {0xc9, 0x7f, 0xff, 0xff, 0xff}, {0xc9, 0xff, 0xff, 0xff, 0xff}} {
		d := msgpack.NewDecoder(bytes.NewReader(raw))
		if err := d.Skip(); err == nil {
			t.Fatal("incomplete extension skipped successfully")
		}
	}
}

func TestInternedStringWideLengths(t *testing.T) {
	for _, code := range []byte{0xdb, 0xc6} {
		for _, n := range []uint32{0, 1, 0x7fffffff, 0x80000000, 0xffffffff} {
			t.Run(fmt.Sprintf("%x/%08x", code, n), func(t *testing.T) {
				raw := []byte{code, 0, 0, 0, 0}
				binary.BigEndian.PutUint32(raw[1:], n)
				if n == 1 {
					raw = append(raw, 'v')
				}
				d := msgpack.NewDecoder(bytes.NewReader(raw))
				d.UseInternedStrings(true)
				got, err := d.DecodeString()
				if n > 1 {
					if err == nil {
						t.Fatal("truncated interned string accepted", got)
					}
				} else if err != nil || (n == 0 && got != "") || (n == 1 && got != "v") {
					t.Fatal("valid interned string changed", got, err)
				}
			})
		}
	}
}
