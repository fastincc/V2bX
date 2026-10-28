package session

import (
	"bytes"
	"encoding/binary"
	"io"
	"net"
	"strconv"
	"sync"
	"testing"
	"time"
)

type captureConn struct {
	bytes.Buffer
	failAfter int
	writes    int
	closed    bool
}

func (c *captureConn) Read(p []byte) (int, error) { return 0, io.EOF }
func (c *captureConn) Write(p []byte) (int, error) {
	c.writes++
	if c.failAfter > 0 && c.writes == c.failAfter {
		return len(p) / 2, nil
	}
	return c.Buffer.Write(p)
}
func (c *captureConn) Close() error                   { c.closed = true; return nil }
func (*captureConn) LocalAddr() net.Addr              { return nil }
func (*captureConn) RemoteAddr() net.Addr             { return nil }
func (*captureConn) SetDeadline(time.Time) error      { return nil }
func (*captureConn) SetReadDeadline(time.Time) error  { return nil }
func (*captureConn) SetWriteDeadline(time.Time) error { return nil }
func TestLargeWriteFrames(t *testing.T) {
	for _, size := range []int{0, 1, 65535, 65536, 81920, 4 << 20} {
		c := &captureConn{}
		s := NewServerSession(c, nil, nil, nil)
		stream := newStream(7, s)
		payload := bytes.Repeat([]byte{0x93}, size)
		n, e := stream.Write(payload)
		if e != nil || n != size {
			t.Fatal(size, n, e)
		}
		var got []byte
		for c.Len() > 0 {
			h := c.Next(7)
			if len(h) != 7 || h[0] != cmdPSH || binary.BigEndian.Uint32(h[1:5]) != 7 {
				t.Fatal("bad header")
			}
			length := int(binary.BigEndian.Uint16(h[5:7]))
			if length == 0 || length > c.Len() {
				t.Fatal("bad length")
			}
			got = append(got, c.Next(length)...)
		}
		if !bytes.Equal(got, payload) {
			t.Fatal("payload mismatch", size)
		}
	}
}
func TestShortWireWriteClosesSession(t *testing.T) {
	c := &captureConn{failAfter: 2}
	s := NewServerSession(c, nil, nil, nil)
	stream := newStream(1, s)
	n, e := stream.Write(make([]byte, 81920))
	if n != 65535 || e != io.ErrShortWrite || !c.closed || !s.IsClosed() {
		t.Fatal(n, e, c.closed)
	}
	if _, e = stream.Write([]byte("later")); e == nil {
		t.Fatal("damaged session reused")
	}
}
func TestConcurrentWritesDoNotInterleaveLogicalWrites(t *testing.T) {
	c := &captureConn{}
	s := NewServerSession(c, nil, nil, nil)
	stream := newStream(1, s)
	var wg sync.WaitGroup
	for i := byte(1); i <= 8; i++ {
		wg.Add(1)
		go func(v byte) {
			defer wg.Done()
			if _, e := stream.Write(bytes.Repeat([]byte{v}, 100000)); e != nil {
				t.Error(e)
			}
		}(i)
	}
	wg.Wait()
	for i := 0; i < 8; i++ {
		a := c.Next(7)
		x := c.Next(int(binary.BigEndian.Uint16(a[5:7])))
		b := c.Next(7)
		y := c.Next(int(binary.BigEndian.Uint16(b[5:7])))
		if len(x) != 65535 || len(y) != 34465 || x[0] != y[0] {
			t.Fatal("interleaved")
		}
	}
}
func BenchmarkFrameWrite(b *testing.B) {
	for _, size := range []int{10240, 65535, 81920} {
		b.Run(strconv.Itoa(size), func(b *testing.B) {
			c := &captureConn{}
			s := NewServerSession(c, nil, nil, nil)
			stream := newStream(1, s)
			data := make([]byte, size)
			b.SetBytes(int64(size))
			b.ReportAllocs()
			for i := 0; i < b.N; i++ {
				c.Reset()
				if _, e := stream.Write(data); e != nil {
					b.Fatal(e)
				}
			}
		})
	}
}
