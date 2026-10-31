package counter

import "sync"

// NodeGate serializes accounting consumers and destructive lifecycle operations
// for one node. Acquire it BEFORE any core identity/node lock. Data-plane I/O and
// user revocation never acquire it. Entries are retained only while in use.
type NodeGate struct {
	all   sync.RWMutex
	mu    sync.Mutex
	nodes map[string]*nodeGateEntry
}

type nodeGateEntry struct {
	mu   sync.Mutex
	refs int
}

func (g *NodeGate) Lock(tag string) func() {
	g.all.RLock()
	g.mu.Lock()
	if g.nodes == nil {
		g.nodes = make(map[string]*nodeGateEntry)
	}
	e := g.nodes[tag]
	if e == nil {
		e = new(nodeGateEntry)
		g.nodes[tag] = e
	}
	e.refs++
	g.mu.Unlock()
	e.mu.Lock()
	return func() {
		e.mu.Unlock()
		g.mu.Lock()
		e.refs--
		if e.refs == 0 {
			delete(g.nodes, tag)
		}
		g.mu.Unlock()
		g.all.RUnlock()
	}
}

// LockAll is used only when closing the whole core.
func (g *NodeGate) LockAll() func() { g.all.Lock(); return g.all.Unlock }
