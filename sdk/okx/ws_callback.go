package okx

import (
	"sync"

	"github.com/gorilla/websocket"
)

const (
	okxWSCallbackQueueLimit   = 1024
	okxWSCallbackControlSlots = 2
)

type okxWebsocketCallbackKind uint8

const (
	okxWebsocketCallbackData okxWebsocketCallbackKind = iota + 1
	okxWebsocketCallbackStarted
	okxWebsocketCallbackRecovered
)

type okxWebsocketCallback struct {
	kind       okxWebsocketCallbackKind
	generation uint64
	conn       *websocket.Conn
	run        func()
}

// okxWebsocketCallbackDispatcher is the ordering and backpressure boundary for
// socket-originated user callbacks. Replacement-socket data remains buffered
// until the matching recovered callback has returned.
type okxWebsocketCallbackDispatcher struct {
	mu sync.Mutex

	queue       []okxWebsocketCallback
	replacement []okxWebsocketCallback
	pendingData int
	wake        chan struct{}
	limit       int
	stopped     bool

	currentConn     *websocket.Conn
	gapOpen         bool
	generation      uint64
	replacementConn *websocket.Conn
	inFlightKind    okxWebsocketCallbackKind
}

func newOKXWebsocketCallbackDispatcher() *okxWebsocketCallbackDispatcher {
	d := &okxWebsocketCallbackDispatcher{
		wake:  make(chan struct{}, 1),
		limit: okxWSCallbackQueueLimit,
	}
	go d.runLoop()
	return d
}

func (d *okxWebsocketCallbackDispatcher) activateConnection(generation uint64, conn *websocket.Conn, recovering bool) {
	if d == nil || conn == nil {
		return
	}
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.stopped {
		return
	}
	d.currentConn = conn
	if !recovering || !d.gapOpen || d.generation != generation {
		return
	}
	if d.replacementConn != conn {
		d.dropReplacementLocked()
		d.removeQueuedRecoveredLocked()
		d.replacementConn = conn
	}
}

func (d *okxWebsocketCallbackDispatcher) enqueueData(conn *websocket.Conn, callbacks []okxWebsocketCallback) bool {
	if d == nil || len(callbacks) == 0 {
		return true
	}
	d.mu.Lock()
	if d.stopped || d.currentConn != conn {
		d.mu.Unlock()
		return true
	}
	dataLimit := d.limit - okxWSCallbackControlSlots
	if dataLimit < 1 {
		dataLimit = 1
	}
	if d.pendingData+len(callbacks) > dataLimit {
		d.mu.Unlock()
		return false
	}
	if d.gapOpen {
		if d.replacementConn == nil {
			d.replacementConn = conn
		}
		if d.replacementConn != conn {
			d.mu.Unlock()
			return true
		}
		d.replacement = append(d.replacement, callbacks...)
		d.pendingData += len(callbacks)
		d.mu.Unlock()
		return true
	}
	d.queue = append(d.queue, callbacks...)
	d.pendingData += len(callbacks)
	d.mu.Unlock()
	d.signal()
	return true
}

func (d *okxWebsocketCallbackDispatcher) beginGap(generation uint64, started func()) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	wasOpen := d.gapOpen
	recoveredInFlight := d.inFlightKind == okxWebsocketCallbackRecovered
	d.currentConn = nil
	d.gapOpen = true
	d.generation = generation
	d.dropReplacementLocked()
	d.removeQueuedRecoveredLocked()
	if !wasOpen || recoveredInFlight {
		if !d.hasQueuedKindLocked(okxWebsocketCallbackStarted) {
			d.queue = append(d.queue, okxWebsocketCallback{
				kind:       okxWebsocketCallbackStarted,
				generation: generation,
				run:        started,
			})
		}
	}
	d.mu.Unlock()
	d.signal()
}

func (d *okxWebsocketCallbackDispatcher) discardReplacement(generation uint64, conn *websocket.Conn) {
	if d == nil || conn == nil {
		return
	}
	d.mu.Lock()
	if d.currentConn == conn {
		d.currentConn = nil
	}
	if d.gapOpen && d.generation == generation && d.replacementConn == conn {
		d.dropReplacementLocked()
		d.removeQueuedRecoveredLocked()
	}
	d.mu.Unlock()
}

func (d *okxWebsocketCallbackDispatcher) enqueueRecovered(generation uint64, conn *websocket.Conn, recovered func()) bool {
	if d == nil || conn == nil {
		return false
	}
	d.mu.Lock()
	if d.stopped || !d.gapOpen || d.generation != generation || d.currentConn != conn || d.replacementConn != conn {
		d.mu.Unlock()
		return false
	}
	d.removeQueuedRecoveredLocked()
	d.queue = append(d.queue, okxWebsocketCallback{
		kind:       okxWebsocketCallbackRecovered,
		generation: generation,
		conn:       conn,
		run:        recovered,
	})
	d.mu.Unlock()
	d.signal()
	return true
}

func (d *okxWebsocketCallbackDispatcher) reset() {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	d.queue = nil
	d.dropReplacementLocked()
	d.pendingData = 0
	d.currentConn = nil
	d.gapOpen = false
	d.generation = 0
	d.replacementConn = nil
	d.inFlightKind = 0
	d.mu.Unlock()
	d.signal()
}

func (d *okxWebsocketCallbackDispatcher) stop() {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	d.stopped = true
	d.queue = nil
	d.dropReplacementLocked()
	d.pendingData = 0
	d.currentConn = nil
	d.gapOpen = false
	d.generation = 0
	d.replacementConn = nil
	d.inFlightKind = 0
	d.mu.Unlock()
	d.signal()
}

func (d *okxWebsocketCallbackDispatcher) runLoop() {
	for range d.wake {
		for {
			d.mu.Lock()
			if d.stopped {
				d.mu.Unlock()
				return
			}
			if len(d.queue) == 0 {
				d.mu.Unlock()
				break
			}
			callback := d.queue[0]
			d.queue[0] = okxWebsocketCallback{}
			d.queue = d.queue[1:]
			if callback.kind == okxWebsocketCallbackData {
				d.pendingData--
			}
			if callback.kind == okxWebsocketCallbackRecovered && !d.recoveredIsCurrentLocked(callback) {
				d.mu.Unlock()
				continue
			}
			d.inFlightKind = callback.kind
			d.mu.Unlock()

			if callback.run != nil {
				callback.run()
			}

			d.mu.Lock()
			if !d.stopped && callback.kind == okxWebsocketCallbackRecovered && d.recoveredIsCurrentLocked(callback) {
				d.gapOpen = false
				d.generation = 0
				d.replacementConn = nil
				if len(d.replacement) > 0 {
					d.queue = append(d.queue, d.replacement...)
					d.replacement = nil
				}
			}
			d.inFlightKind = 0
			d.mu.Unlock()
		}
	}
}

func (d *okxWebsocketCallbackDispatcher) recoveredIsCurrentLocked(callback okxWebsocketCallback) bool {
	return d.gapOpen &&
		d.generation == callback.generation &&
		d.currentConn == callback.conn &&
		d.replacementConn == callback.conn
}

func (d *okxWebsocketCallbackDispatcher) dropReplacementLocked() {
	d.pendingData -= len(d.replacement)
	if d.pendingData < 0 {
		d.pendingData = 0
	}
	d.replacement = nil
	d.replacementConn = nil
}

func (d *okxWebsocketCallbackDispatcher) removeQueuedRecoveredLocked() {
	kept := d.queue[:0]
	for _, callback := range d.queue {
		if callback.kind == okxWebsocketCallbackRecovered {
			continue
		}
		kept = append(kept, callback)
	}
	for i := len(kept); i < len(d.queue); i++ {
		d.queue[i] = okxWebsocketCallback{}
	}
	d.queue = kept
}

func (d *okxWebsocketCallbackDispatcher) hasQueuedKindLocked(kind okxWebsocketCallbackKind) bool {
	for _, callback := range d.queue {
		if callback.kind == kind {
			return true
		}
	}
	return false
}

func (d *okxWebsocketCallbackDispatcher) signal() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}
