package counter

import (
	"sync"
	"sync/atomic"
)

type TrafficCounter struct {
	Counters sync.Map
}

type TrafficStorage struct {
	Owners      atomic.Int64
	membersMu   sync.Mutex
	members     map[*lease]func() error
	sampleMu    sync.Mutex
	UpCounter   atomic.Int64
	DownCounter atomic.Int64
}

func NewTrafficCounter() *TrafficCounter {
	return &TrafficCounter{}
}

func (c *TrafficCounter) GetCounter(uuid string) *TrafficStorage {
	if cts, ok := c.Counters.Load(uuid); ok {
		return cts.(*TrafficStorage)
	}
	newStorage := &TrafficStorage{}
	if cts, loaded := c.Counters.LoadOrStore(uuid, newStorage); loaded {
		return cts.(*TrafficStorage)
	}
	return newStorage
}

func (c *TrafficCounter) GetUpCount(uuid string) int64 {
	if cts, ok := c.Counters.Load(uuid); ok {
		return cts.(*TrafficStorage).UpCounter.Load()
	}
	return 0
}

func (c *TrafficCounter) GetDownCount(uuid string) int64 {
	if cts, ok := c.Counters.Load(uuid); ok {
		return cts.(*TrafficStorage).DownCounter.Load()
	}
	return 0
}

func (c *TrafficCounter) Len() int {
	length := 0
	c.Counters.Range(func(_, _ interface{}) bool {
		length++
		return true
	})
	return length
}

func (c *TrafficCounter) Reset(uuid string) {
	if cts, ok := c.Counters.Load(uuid); ok {
		cts.(*TrafficStorage).UpCounter.Store(0)
		cts.(*TrafficStorage).DownCounter.Store(0)
	}
}

func (c *TrafficCounter) Delete(uuid string) {
	c.Counters.Delete(uuid)
}

func (c *TrafficCounter) Rx(uuid string, n int) {
	cts := c.GetCounter(uuid)
	cts.DownCounter.Add(int64(n))
}

func (c *TrafficCounter) Tx(uuid string, n int) {
	cts := c.GetCounter(uuid)
	cts.UpCounter.Add(int64(n))
}

// Sample serializes samplers; atomic swaps retain data-plane increments that
// arrive during draining. Each direction has its own boundary, as on the wire.
func (s *TrafficStorage) Sample(minimum int64, reset bool) (up, down int64, ready bool) {
	s.sampleMu.Lock()
	defer s.sampleMu.Unlock()
	up, down = s.UpCounter.Load(), s.DownCounter.Load()
	if up+down <= minimum {
		return 0, 0, false
	}
	if reset {
		up, down = s.UpCounter.Swap(0), s.DownCounter.Swap(0)
	}
	return up, down, true
}

// LoadOrCreate prevents competing first streams from replacing a node counter.
func LoadOrCreate(counters *sync.Map, tag string) *TrafficCounter {
	if v, ok := counters.Load(tag); ok {
		return v.(*TrafficCounter)
	}
	value, _ := counters.LoadOrStore(tag, NewTrafficCounter())
	return value.(*TrafficCounter)
}

func (s *TrafficStorage) register(l *lease, close func() error) {
	s.membersMu.Lock()
	defer s.membersMu.Unlock()
	if s.members == nil {
		s.members = make(map[*lease]func() error)
	}
	s.members[l] = close
}
func (s *TrafficStorage) unregister(l *lease) {
	s.membersMu.Lock()
	defer s.membersMu.Unlock()
	delete(s.members, l)
}
func (s *TrafficStorage) CloseWriters() {
	s.membersMu.Lock()
	closers := make([]func() error, 0, len(s.members))
	for _, close := range s.members {
		closers = append(closers, close)
	}
	s.membersMu.Unlock()
	for _, close := range closers {
		_ = close()
	}
}
