package server

import (
	"errors"
	"io"
	"testing"
)

type putbackLogger struct {
	TrafficLogger
	calls  int
	tx, rx uint64
	allow  bool
}

func (l *putbackLogger) LogTraffic(_ string, tx, rx uint64) bool {
	l.calls++
	l.tx += tx
	l.rx += rx
	return l.allow
}

type putbackWriter struct {
	calls, n int
	err      error
}

func (w *putbackWriter) Write(p []byte) (int, error) { w.calls++; return w.n, w.err }
func TestPutbackIngressAccounting(t *testing.T) {
	failed := errors.New("synthetic target failure")
	for _, tc := range []struct {
		name              string
		n                 int
		writeErr, wantErr error
		allow             bool
	}{
		{"complete", 9, nil, nil, true}, {"short", 4, nil, io.ErrShortWrite, true}, {"failed", 0, failed, failed, true}, {"partial_failed", 4, failed, failed, true}, {"revoked", 9, nil, errDisconnect, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			l := &putbackLogger{allow: tc.allow}
			w := &putbackWriter{n: tc.n, err: tc.writeErr}
			stats := &StreamStats{}
			e := writePutback(w, []byte("synthetic"), "opaque-session", l, stats)
			if e != tc.wantErr || l.calls != 1 || l.tx != 9 || l.rx != 0 {
				t.Fatalf("error=%v calls=%d tx=%d rx=%d", e, l.calls, l.tx, l.rx)
			}
			wantWrites := 1
			wantStats := tc.n
			if !tc.allow {
				wantWrites = 0
				wantStats = 0
			}
			if w.calls != wantWrites || stats.Tx.Load() != uint64(wantStats) {
				t.Fatal("unexpected forwarding")
			}
		})
	}
}
func TestPutbackEmptyAndUnmetered(t *testing.T) {
	l := &putbackLogger{allow: true}
	w := &putbackWriter{n: 3}
	stats := &StreamStats{}
	if e := writePutback(w, nil, "id", l, stats); e != nil || l.calls != 0 || w.calls != 0 {
		t.Fatal("empty putback performed work")
	}
	if e := writePutback(w, []byte("abc"), "id", nil, stats); e != nil || w.calls != 1 || stats.Tx.Load() != 3 {
		t.Fatal("unmetered behavior")
	}
}
