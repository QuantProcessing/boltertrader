package startgate

import "sync"

// Gate buffers private-stream callbacks until every startup subscription has
// been acknowledged. Commit flushes them in receive order; Abort discards them.
// This keeps Adapter.Start atomic without losing legitimate initial snapshots
// that arrive between individual subscription acknowledgements.
type Gate struct {
	mu      sync.Mutex
	ready   bool
	aborted bool
	pending []func()
}

func (g *Gate) Handle(event func()) {
	if event == nil {
		return
	}
	g.mu.Lock()
	if g.aborted {
		g.mu.Unlock()
		return
	}
	if !g.ready {
		g.pending = append(g.pending, event)
		g.mu.Unlock()
		return
	}
	g.mu.Unlock()
	event()
}

func (g *Gate) Commit() {
	for {
		g.mu.Lock()
		if g.aborted || g.ready {
			g.mu.Unlock()
			return
		}
		if len(g.pending) == 0 {
			g.ready = true
			g.mu.Unlock()
			return
		}
		event := g.pending[0]
		g.pending = g.pending[1:]
		g.mu.Unlock()
		event()
	}
}

func (g *Gate) Abort() {
	g.mu.Lock()
	g.aborted = true
	g.pending = nil
	g.mu.Unlock()
}
