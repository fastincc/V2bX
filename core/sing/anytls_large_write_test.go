//go:build sing

package sing

import (
	"bytes"
	"context"
	"fmt"
	M "github.com/sagernet/sing/common/metadata"
	"io"
	"testing"
	"time"
)

func TestAnyTLSLargeWriteActualTransport(t *testing.T) {
	for _, proxy := range []bool{false, true} {
		for _, size := range []int{65535, 65536, 81920, 4 << 20} {
			t.Run(fmt.Sprintf("proxy=%v/bytes=%d", proxy, size), func(t *testing.T) {
				f := newReviewFixture(t, proxy)
				c := f.client(t)
				defer c.Close()
				s, e := c.CreateProxy(context.Background(), M.ParseSocksaddr("echo.invalid:80"))
				if e != nil {
					t.Fatal(e)
				}
				defer s.Close()
				s.SetDeadline(time.Now().Add(10 * time.Second))
				data := bytes.Repeat([]byte{byte(size % 251)}, size)
				received := make([]byte, size)
				done := make(chan error, 1)
				go func() { _, e := io.ReadFull(s, received); done <- e }()
				n, e := s.Write(data)
				if e != nil || n != size {
					t.Fatalf("write %d: %d %v", size, n, e)
				}
				if e = <-done; e != nil || !bytes.Equal(data, received) {
					t.Fatal("echo mismatch", e)
				}
			})
		}
	}
}
