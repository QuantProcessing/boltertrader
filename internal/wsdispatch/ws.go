package wsdispatch

import "sync"

type MsgDispatcher interface {
	Dispatch(data []byte) error
}

// Dispatcher serializes websocket callback delivery and can temporarily buffer
// ingress while a caller performs recovery work such as resubscribe or snapshot
// rebuilding. Resume runs the optional hook before draining buffered messages,
// matching NautilusTrader's pre-dispatch recovery ordering.
type Dispatcher struct {
	mu        sync.Mutex
	deliverMu sync.Mutex
	paused    bool
	buffer    []func()
}

func NewDispatcher() *Dispatcher {
	return &Dispatcher{}
}

func (d *Dispatcher) Pause() {
	d.mu.Lock()
	d.paused = true
	d.mu.Unlock()
}

func (d *Dispatcher) Resume(beforeDrain func()) {
	d.mu.Lock()
	if !d.paused {
		d.mu.Unlock()
		return
	}
	buffer := append([]func(){}, d.buffer...)
	d.buffer = nil
	d.mu.Unlock()

	if beforeDrain != nil {
		beforeDrain()
	}

	for {
		for _, fn := range buffer {
			d.deliver(fn)
		}

		d.mu.Lock()
		if len(d.buffer) == 0 {
			d.paused = false
			d.mu.Unlock()
			return
		}
		buffer = append([]func(){}, d.buffer...)
		d.buffer = nil
		d.mu.Unlock()
	}
}

func (d *Dispatcher) Dispatch(fn func()) {
	if fn == nil {
		return
	}
	d.mu.Lock()
	if d.paused {
		d.buffer = append(d.buffer, fn)
		d.mu.Unlock()
		return
	}
	d.mu.Unlock()
	d.deliver(fn)
}

func (d *Dispatcher) deliver(fn func()) {
	d.deliverMu.Lock()
	defer d.deliverMu.Unlock()
	fn()
}
