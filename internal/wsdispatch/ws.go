package wsdispatch

import (
	"errors"
	"sync"
)

// ErrBufferFull reports that a paused dispatcher exceeded its configured
// callback capacity and invalidated that delivery generation.
var ErrBufferFull = errors.New("websocket callback buffer full")

// DefaultBufferLimit bounds retained websocket callbacks during recovery.
const DefaultBufferLimit = 1024

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
	revision  uint64
	maxBuffer int
	overflow  bool
}

func NewDispatcher() *Dispatcher {
	return &Dispatcher{}
}

// NewBoundedDispatcher returns a dispatcher whose paused callback generation
// is capped at maxBuffered. Callers must use DispatchChecked at ingress so an
// overflow can be propagated to the owning transport and recovered fail-closed.
func NewBoundedDispatcher(maxBuffered int) *Dispatcher {
	if maxBuffered < 1 {
		panic("wsdispatch: max buffered callbacks must be positive")
	}
	return &Dispatcher{maxBuffer: maxBuffered}
}

func (d *Dispatcher) Pause() {
	d.mu.Lock()
	d.paused = true
	d.mu.Unlock()
}

// Reset drops buffered callbacks and returns the dispatcher to its unpaused
// state. A callback already executing under deliverMu is allowed to finish;
// callbacks that have not started never cross into the next client lifecycle.
func (d *Dispatcher) Reset() {
	d.mu.Lock()
	d.revision++
	d.paused = false
	d.overflow = false
	for i := range d.buffer {
		d.buffer[i] = nil
	}
	d.buffer = nil
	d.mu.Unlock()
}

func (d *Dispatcher) Resume(beforeDrain func()) {
	d.mu.Lock()
	if !d.paused || d.overflow {
		d.mu.Unlock()
		return
	}
	buffer := append([]func(){}, d.buffer...)
	d.buffer = nil
	revision := d.revision
	d.mu.Unlock()

	if beforeDrain != nil && !d.deliverAtRevision(beforeDrain, revision) {
		return
	}

	for {
		for _, fn := range buffer {
			if !d.deliverAtRevision(fn, revision) {
				return
			}
		}

		d.mu.Lock()
		if d.revision != revision {
			d.mu.Unlock()
			return
		}
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
	_ = d.DispatchChecked(fn)
}

// DispatchChecked admits fn for serialized delivery. A bounded dispatcher
// returns ErrBufferFull if a paused generation exceeds its capacity. Overflow
// invalidates and drops that generation; Reset is required before delivery can
// resume, so callers can fail closed instead of continuing after an event gap.
func (d *Dispatcher) DispatchChecked(fn func()) error {
	if fn == nil {
		return nil
	}
	d.mu.Lock()
	if d.paused {
		if d.overflow {
			d.mu.Unlock()
			return ErrBufferFull
		}
		if d.maxBuffer > 0 && len(d.buffer) >= d.maxBuffer {
			d.revision++
			d.overflow = true
			for i := range d.buffer {
				d.buffer[i] = nil
			}
			d.buffer = nil
			d.mu.Unlock()
			return ErrBufferFull
		}
		d.buffer = append(d.buffer, fn)
		d.mu.Unlock()
		return nil
	}
	revision := d.revision
	d.mu.Unlock()
	d.deliverAtRevision(fn, revision)
	return nil
}

func (d *Dispatcher) deliverAtRevision(fn func(), revision uint64) bool {
	d.deliverMu.Lock()
	defer d.deliverMu.Unlock()
	d.mu.Lock()
	current := d.revision == revision
	d.mu.Unlock()
	if !current {
		return false
	}
	fn()
	return true
}
