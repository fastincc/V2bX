package dispatcher

import (
	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
	"net"
	"sync"
)

// One manager belongs to one authorization generation, never a reusable UUID.
// Close blocks new work, while in-flight operations retain their accounting key.
type LinkManager struct {
	LimitKey   string
	links      map[*ManagedWriter]buf.Reader
	mu         sync.Mutex
	closed     bool
	operations int
	nextRead   uint64
	readers    map[uint64]func()
}

func NewLinkManager(key string) *LinkManager {
	return &LinkManager{LimitKey: key, links: make(map[*ManagedWriter]buf.Reader)}
}
func (m *LinkManager) begin() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return false
	}
	m.operations++
	return true
}
func (m *LinkManager) end()         { m.mu.Lock(); m.operations--; m.mu.Unlock() }
func (m *LinkManager) Active() bool { m.mu.Lock(); defer m.mu.Unlock(); return !m.closed }
func (m *LinkManager) Quiescent() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.closed && m.operations == 0
}
func (m *LinkManager) AddLink(writer *ManagedWriter, reader buf.Reader) {
	m.mu.Lock()
	if m.closed {
		m.mu.Unlock()
		common.Interrupt(reader)
		common.Close(writer.writer)
		return
	}
	m.links[writer] = reader
	m.mu.Unlock()
}
func (m *LinkManager) RemoveWriter(writer *ManagedWriter) {
	m.mu.Lock()
	delete(m.links, writer)
	m.mu.Unlock()
}
func (m *LinkManager) CloseAll() {
	m.mu.Lock()
	m.closed = true
	readers := m.readers
	m.readers = make(map[uint64]func())
	links := m.links
	m.links = make(map[*ManagedWriter]buf.Reader)
	m.mu.Unlock()
	for _, closeRead := range readers {
		closeRead()
	}
	for w, r := range links {
		common.Interrupt(r)
		common.Interrupt(w.writer)
		common.Close(w.writer)
	}
}

type ManagedWriter struct {
	writer  buf.Writer
	manager *LinkManager
}

func (w *ManagedWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	if !w.manager.begin() {
		buf.ReleaseMulti(mb)
		return net.ErrClosed
	}
	defer w.manager.end()
	return w.writer.WriteMultiBuffer(mb)
}
func (w *ManagedWriter) Close() error { w.manager.RemoveWriter(w); return common.Close(w.writer) }
func (w *ManagedWriter) Interrupt()   { common.Interrupt(w.writer) }
func (m *LinkManager) wrap(writer buf.Writer, reader buf.Reader) buf.Writer {
	w := &ManagedWriter{writer: writer, manager: m}
	m.AddLink(w, reader)
	return w
}

// Resume is used only when a node reload is rolled back, never for user re-add.
func (m *LinkManager) Resume() { m.mu.Lock(); m.closed = false; m.mu.Unlock() }

// A protocol reader may not implement Interrupt. Keep its transport closer
// while a read is in flight so revocation/draining can wake it reliably.
func (m *LinkManager) beginRead(closeRead func()) (func(), bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.closed {
		return nil, false
	}
	m.operations++
	m.nextRead++
	id := m.nextRead
	if m.readers == nil {
		m.readers = make(map[uint64]func())
	}
	m.readers[id] = closeRead
	return func() { m.mu.Lock(); delete(m.readers, id); m.operations--; m.mu.Unlock() }, true
}
