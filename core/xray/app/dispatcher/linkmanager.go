package dispatcher

import (
	sync "sync"
	"time"

	"github.com/xtls/xray-core/common"
	"github.com/xtls/xray-core/common/buf"
)

type ManagedWriter struct {
	writer  buf.Writer
	manager *LinkManager
}

func (w *ManagedWriter) WriteMultiBuffer(mb buf.MultiBuffer) error {
	return w.writer.WriteMultiBuffer(mb)
}

func (w *ManagedWriter) Close() error {
	w.manager.RemoveWriter(w)
	return common.Close(w.writer)
}

type ManagedReader struct {
	reader  buf.TimeoutReader
	manager *LinkManager
}

func (r *ManagedReader) ReadMultiBuffer() (buf.MultiBuffer, error) {
	return r.reader.ReadMultiBuffer()
}

func (r *ManagedReader) ReadMultiBufferTimeout(t time.Duration) (buf.MultiBuffer, error) {
	return r.reader.ReadMultiBufferTimeout(t)
}

func (r *ManagedReader) Interrupt() {
	r.manager.RemoveReader(r)
	common.Interrupt(r.reader)
}

type LinkManager struct {
	writers map[*ManagedWriter]struct{}
	readers map[*ManagedReader]struct{}
	mu      sync.Mutex
}

func (m *LinkManager) AddLink(writer *ManagedWriter, reader *ManagedReader) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.writers[writer] = struct{}{}
	m.readers[reader] = struct{}{}
}

func (m *LinkManager) RemoveWriter(writer *ManagedWriter) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.writers, writer)
}

func (m *LinkManager) RemoveReader(reader *ManagedReader) {
	m.mu.Lock()
	defer m.mu.Unlock()
	delete(m.readers, reader)
}

func (m *LinkManager) CloseAll() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for w := range m.writers {
		common.Close(w)
	}
	for r := range m.readers {
		common.Interrupt(r)
	}
}
