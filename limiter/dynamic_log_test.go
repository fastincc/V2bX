package limiter

import (
	"bytes"
	"encoding/json"
	"io"
	"strings"
	"sync"
	"testing"
	"time"
	"unsafe"

	log "github.com/sirupsen/logrus"
)

type dynamicDiagnosticSink struct {
	mu      sync.Mutex
	buffer  bytes.Buffer
	entered chan struct{}
	release chan struct{}
	once    sync.Once
}

func (w *dynamicDiagnosticSink) Write(p []byte) (int, error) {
	if w.release != nil {
		w.once.Do(func() { close(w.entered); <-w.release })
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.buffer.Write(p)
}

func (w *dynamicDiagnosticSink) records(t *testing.T) []map[string]any {
	t.Helper()
	w.mu.Lock()
	defer w.mu.Unlock()
	var rows []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(w.buffer.String()), "\n") {
		if line == "" {
			continue
		}
		var row map[string]any
		if err := json.Unmarshal([]byte(line), &row); err != nil {
			t.Fatal(err)
		}
		rows = append(rows, row)
	}
	return rows
}

func diagnosticTestLogger(out io.Writer) *log.Logger {
	l := log.New()
	l.SetFormatter(&log.JSONFormatter{})
	l.SetOutput(out)
	l.SetLevel(log.InfoLevel)
	return l
}

func finishDiagnosticQueue(t testing.TB, q *dynamicDiagnosticLog) {
	t.Helper()
	close(q.events) // all test producers must have finished before this call
	// A filtered-only test intentionally did not start the production worker.
	q.start.Do(func() { go q.run() })
	select {
	case <-q.done:
	case <-time.After(3 * time.Second):
		t.Fatal("released diagnostic worker failed to drain")
	}
}

func TestDynamicDiagnosticQueueBoundedAndRecovery(t *testing.T) {
	w := &dynamicDiagnosticSink{entered: make(chan struct{}), release: make(chan struct{})}
	q := newDynamicDiagnosticLog(diagnosticTestLogger(w))
	closed := false
	defer func() {
		if !closed {
			close(w.release)
			finishDiagnosticQueue(t, q)
		}
	}()
	at := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	e := dynamicDiagnostic{at: at, uid: 17, kind: dynamicChanged, state: "triggered"}
	q.emit(e)
	select {
	case <-w.entered:
	case <-time.After(time.Second):
		t.Fatal("logger did not reach the blocked writer")
	}
	for i := 0; i < dynamicDiagnosticCapacity; i++ {
		q.emit(e)
	}
	const producers, each = 8, 128
	var wg sync.WaitGroup
	for i := 0; i < producers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < each; j++ {
				q.emit(e)
			}
		}()
	}
	wg.Wait()
	if got := q.dropped.Load(); got != producers*each {
		t.Fatalf("dropped=%d want=%d", got, producers*each)
	}
	if len(q.events) != dynamicDiagnosticCapacity || cap(q.events) != dynamicDiagnosticCapacity {
		t.Fatal("diagnostic backlog was not bounded")
	}
	t.Logf("diagnostic_queue_payload_bytes=%d capacity=%d", unsafe.Sizeof(dynamicDiagnostic{})*uintptr(cap(q.events)), cap(q.events))
	// Producers cannot touch the logger lock, allocate a goroutine, or allocate
	// an Entry/Fields map when the queue is full and its Writer owns that lock.
	if n := testing.AllocsPerRun(1000, func() { q.emit(e) }); n != 0 {
		t.Fatalf("full diagnostic queue allocated %.1f objects per event", n)
	}
	wantDrop := q.dropped.Load()
	q.logger.SetLevel(log.ErrorLevel) // atomic, safe even while Writer is blocked
	q.emit(dynamicDiagnostic{at: at, uid: 17, kind: dynamicRejected, reason: "synthetic reason"})
	if q.dropped.Load() != wantDrop {
		t.Fatal("filtered event was counted as an overflow")
	}
	q.logger.SetLevel(log.InfoLevel)
	close(w.release)
	closed = true
	finishDiagnosticQueue(t, q)
	var normal, summaries int
	var reported uint64
	for _, row := range w.records(t) {
		if row["dropped"] != nil {
			summaries++
			reported += uint64(row["dropped"].(float64))
			if uint64(row["dropped_total"].(float64)) != wantDrop {
				t.Fatal("cumulative overflow count is wrong")
			}
			continue
		}
		normal++
		if row["uid"] != float64(17) || row["state"] != "triggered" || row["time"] != at.Format(time.RFC3339) || row["msg"] != "Dynamic speed limit changed" {
			t.Fatal("diagnostic scalar fields or event timestamp changed")
		}
	}
	if normal != dynamicDiagnosticCapacity+1 || summaries != 1 || reported != wantDrop {
		t.Fatalf("recovery output: events=%d summaries=%d drops=%d want=%d", normal, summaries, reported, wantDrop)
	}
}

func TestDynamicDiagnosticLevelAndWarningCompatibility(t *testing.T) {
	w := &dynamicDiagnosticSink{}
	l := diagnosticTestLogger(w)
	l.SetLevel(log.WarnLevel)
	q := newDynamicDiagnosticLog(l)
	at := time.Now()
	q.emit(dynamicDiagnostic{at: at, uid: 1, kind: dynamicChanged, state: "expired"})
	q.emit(dynamicDiagnostic{at: at, uid: 2, kind: dynamicRejected, reason: "stale dynamic policy"})
	q.emit(dynamicDiagnostic{at: at, uid: 3, kind: dynamicUnsupported})
	finishDiagnosticQueue(t, q)
	rows := w.records(t)
	if len(rows) != 2 || q.dropped.Load() != 0 {
		t.Fatal("level filtering or overflow accounting changed")
	}
	if rows[0]["uid"] != float64(2) || rows[0]["reason"] != "stale dynamic policy" || rows[0]["msg"] != "Dynamic speed policy rejected" || rows[0]["level"] != "warning" {
		t.Fatal("rejection warning fields changed")
	}
	if rows[1]["uid"] != float64(3) || rows[1]["msg"] != "Dynamic speed policy unsupported on Xray Vision raw copy; fixed limit unchanged" {
		t.Fatal("unsupported Vision warning fields changed")
	}
}

func TestDynamicDiagnosticConcurrentStartAndDelivery(t *testing.T) {
	w := &dynamicDiagnosticSink{}
	q := newDynamicDiagnosticLog(diagnosticTestLogger(w))
	const producers, each = 16, 128
	var wg sync.WaitGroup
	for i := 0; i < producers; i++ {
		wg.Add(1)
		go func(id int) {
			defer wg.Done()
			for j := 0; j < each; j++ {
				q.emit(dynamicDiagnostic{at: time.Now(), uid: id*each + j, kind: dynamicChanged, state: "expired"})
			}
		}(i)
	}
	wg.Wait()
	finishDiagnosticQueue(t, q)
	seen := make(map[int]bool)
	var reported uint64
	for _, row := range w.records(t) {
		if drops, ok := row["dropped"].(float64); ok {
			reported += uint64(drops)
			continue
		}
		uid := int(row["uid"].(float64))
		if seen[uid] {
			t.Fatal("event emitted more than once")
		}
		seen[uid] = true
	}
	if uint64(len(seen))+q.dropped.Load() != producers*each || reported != q.dropped.Load() {
		t.Fatal("accepted and dropped events did not reconcile")
	}
}

func BenchmarkDynamicDiagnosticFiltered(b *testing.B) {
	l := diagnosticTestLogger(io.Discard)
	l.SetLevel(log.ErrorLevel)
	q := newDynamicDiagnosticLog(l)
	e := dynamicDiagnostic{uid: 1, kind: dynamicChanged, state: "triggered"}
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q.emit(e)
	}
}

func BenchmarkDynamicDiagnosticQueueFull(b *testing.B) {
	w := &dynamicDiagnosticSink{entered: make(chan struct{}), release: make(chan struct{})}
	q := newDynamicDiagnosticLog(diagnosticTestLogger(w))
	e := dynamicDiagnostic{uid: 1, kind: dynamicChanged, state: "triggered"}
	q.emit(e)
	<-w.entered
	for i := 0; i < dynamicDiagnosticCapacity; i++ {
		q.emit(e)
	}
	b.Cleanup(func() { close(w.release); finishDiagnosticQueue(b, q) })
	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		q.emit(e)
	}
}
