package counter

import (
	"io"
	"net"
	"testing"
)

func TestPlainReadWriteAccumulates(t *testing.T) {
	a, b := net.Pipe()
	defer a.Close()
	defer b.Close()
	storage := &TrafficStorage{}
	wrapped := NewConnCounter(a, storage)
	go func() { b.Write([]byte("123")); b.Write([]byte("45678")); io.Copy(io.Discard, b) }()
	if _, e := io.ReadFull(wrapped, make([]byte, 3)); e != nil {
		t.Fatal(e)
	}
	if _, e := io.ReadFull(wrapped, make([]byte, 5)); e != nil {
		t.Fatal(e)
	}
	wrapped.Write([]byte("1234"))
	wrapped.Write([]byte("567890"))
	if storage.UpCounter.Load() != 8 || storage.DownCounter.Load() != 10 {
		t.Fatal("ordinary I/O overwrote accumulated counters")
	}
}
