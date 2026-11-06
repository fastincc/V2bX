//go:build linux

package visionsplice

import (
	"bytes"
	"context"
	"errors"
	"io"
	"sync/atomic"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestCopyIngressReportsBeforeLongStreamCloses(t *testing.T) {
	in, src := pair(t)
	dst, out := pair(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var read, written atomic.Int64
	done := make(chan error, 1)
	go func() {
		_, e := CopyIngress(ctx, dst, src, func(n int64) { read.Add(n) }, func(n int64) { written.Add(n) })
		done <- e
	}()
	for i := 1; i <= 3; i++ {
		p := bytes.Repeat([]byte{byte(i)}, 32768)
		go func() { _, _ = in.Write(p) }()
		out.SetReadDeadline(time.Now().Add(time.Second))
		got := make([]byte, len(p))
		if _, e := io.ReadFull(out, got); e != nil || !bytes.Equal(got, p) {
			t.Fatal("payload", e)
		}
		want := int64(i * len(p))
		for deadline := time.Now().Add(time.Second); written.Load() != want && time.Now().Before(deadline); {
			time.Sleep(time.Millisecond)
		}
		if read.Load() != want || written.Load() != want {
			t.Fatal("live progress", read.Load(), written.Load(), want)
		}
		select {
		case e := <-done:
			t.Fatal("long stream ended early", e)
		default:
		}
	}
	in.CloseWrite()
	if e := <-done; e != nil {
		t.Fatal(e)
	}
}

func TestCopyIngressOutputFailureRetainsReadBytes(t *testing.T) {
	in, src := pair(t)
	dst, _ := pair(t)
	p := bytes.Repeat([]byte{7}, 32768)
	go func() { in.Write(p); in.CloseWrite() }()
	var read, written int64
	n, e := CopyIngress(context.Background(), dst, src, func(n int64) {
		read += n
		// Fail after ingress was consumed, before forwarding can succeed.
		dst.Close()
	}, func(n int64) { written += n })
	if e == nil || read <= 0 || read > int64(len(p)) || written != 0 || n != 0 {
		t.Fatal("ingress accounting on output failure", read, written, n, e)
	}
}

func TestCopyIngressBufferedFallbackDrainsPipeWithoutRebilling(t *testing.T) {
	var fds [2]int
	if e := unix.Pipe2(fds[:], unix.O_CLOEXEC|unix.O_NONBLOCK); e != nil {
		t.Fatal(e)
	}
	defer unix.Close(fds[0])
	defer unix.Close(fds[1])
	pending := []byte("already-read-pipe-prefix")
	if n, e := unix.Write(fds[1], pending); e != nil || n != len(pending) {
		t.Fatal(n, e)
	}
	tail := []byte("subsequent-source-tail")
	read := int64(len(pending))
	var written int64
	var out bytes.Buffer
	n, e := copyIngressBuffered(context.Background(), &out, bytes.NewReader(tail), fds[0], int64(len(pending)), func(n int64) { read += n }, func(n int64) { written += n })
	want := append(append([]byte{}, pending...), tail...)
	if e != nil || !bytes.Equal(out.Bytes(), want) || n != int64(len(want)) || read != n || written != n {
		t.Fatal("fallback order/count", out.String(), n, read, written, e)
	}
}

type failIngressWriter struct{ err error }

func (w failIngressWriter) Write(p []byte) (int, error) { return min(3, len(p)), w.err }

func TestCopyIngressBufferedPartialWriteKeepsIngressBoundary(t *testing.T) {
	sentinel := errors.New("synthetic output failure")
	p := []byte("the whole source read is accounted before output")
	var read, written int64
	n, e := copyIngressBuffered(context.Background(), failIngressWriter{sentinel}, bytes.NewReader(p), -1, 0, func(n int64) { read += n }, func(n int64) { written += n })
	if e != sentinel || read != int64(len(p)) || written != 3 || n != 3 {
		t.Fatal("partial failure", read, written, n, e)
	}
}

func TestCopyIngressCancellationReleasesIdleCopy(t *testing.T) {
	_, src := pair(t)
	dst, _ := pair(t)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { CopyIngress(ctx, dst, src, nil, nil); close(done) }()
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("cancelled idle splice did not return")
	}
}
