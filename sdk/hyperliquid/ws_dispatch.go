package hyperliquid

import (
	"sync"

	"github.com/gorilla/websocket"
)

const (
	hyperliquidWSCallbackQueueLimit   = 1024
	hyperliquidWSCallbackControlSlots = 2
)

type websocketCallbackKind uint8

const (
	websocketCallbackData websocketCallbackKind = iota + 1
	websocketCallbackStarted
	websocketCallbackRecovered
)

type websocketCallback struct {
	kind       websocketCallbackKind
	generation uint64
	conn       *websocket.Conn
	run        func()
}

// websocketCallbackDispatcher is the single ordering boundary for every
// callback exposed by a WebsocketClient. The socket reader only enqueues work;
// it never waits for user code. During a reconnect gap, replacement-socket data
// stays buffered until the matching recovered callback returns.
type websocketCallbackDispatcher struct {
	mu sync.Mutex

	queue       []websocketCallback
	replacement []websocketCallback
	pendingData int
	wake        chan struct{}
	limit       int
	stopped     bool

	currentConn     *websocket.Conn
	gapOpen         bool
	generation      uint64
	replacementConn *websocket.Conn
	inFlightKind    websocketCallbackKind
}

func newWebsocketCallbackDispatcher() *websocketCallbackDispatcher {
	d := &websocketCallbackDispatcher{
		wake:  make(chan struct{}, 1),
		limit: hyperliquidWSCallbackQueueLimit,
	}
	go d.runLoop()
	return d
}

func (d *websocketCallbackDispatcher) activateConnection(generation uint64, conn *websocket.Conn, recovering bool) {
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

func (d *websocketCallbackDispatcher) enqueueData(conn *websocket.Conn, callbacks []websocketCallback) bool {
	if d == nil || len(callbacks) == 0 {
		return true
	}
	d.mu.Lock()
	if d.stopped || d.currentConn != conn {
		d.mu.Unlock()
		return true
	}
	dataLimit := d.limit - hyperliquidWSCallbackControlSlots
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

func (d *websocketCallbackDispatcher) beginGap(generation uint64, started func()) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	wasOpen := d.gapOpen
	recoveredInFlight := d.inFlightKind == websocketCallbackRecovered
	d.currentConn = nil
	d.gapOpen = true
	d.generation = generation
	d.dropReplacementLocked()
	d.removeQueuedRecoveredLocked()
	if !wasOpen || recoveredInFlight {
		if !d.hasQueuedKindLocked(websocketCallbackStarted) {
			d.queue = append(d.queue, websocketCallback{
				kind:       websocketCallbackStarted,
				generation: generation,
				run:        started,
			})
		}
	}
	d.mu.Unlock()
	d.signal()
}

func (d *websocketCallbackDispatcher) discardReplacement(generation uint64, conn *websocket.Conn) {
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

func (d *websocketCallbackDispatcher) enqueueRecovered(generation uint64, conn *websocket.Conn, recovered func()) bool {
	if d == nil || conn == nil {
		return false
	}
	d.mu.Lock()
	if d.stopped || !d.gapOpen || d.generation != generation || d.currentConn != conn || d.replacementConn != conn {
		d.mu.Unlock()
		return false
	}
	d.removeQueuedRecoveredLocked()
	d.queue = append(d.queue, websocketCallback{
		kind:       websocketCallbackRecovered,
		generation: generation,
		conn:       conn,
		run:        recovered,
	})
	d.mu.Unlock()
	d.signal()
	return true
}

func (d *websocketCallbackDispatcher) reset() {
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

func (d *websocketCallbackDispatcher) stop() {
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

func (d *websocketCallbackDispatcher) runLoop() {
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
			d.queue[0] = websocketCallback{}
			d.queue = d.queue[1:]
			if callback.kind == websocketCallbackData {
				d.pendingData--
			}
			if callback.kind == websocketCallbackRecovered && !d.recoveredIsCurrentLocked(callback) {
				d.mu.Unlock()
				continue
			}
			d.inFlightKind = callback.kind
			d.mu.Unlock()

			if callback.run != nil {
				callback.run()
			}

			d.mu.Lock()
			if !d.stopped && callback.kind == websocketCallbackRecovered && d.recoveredIsCurrentLocked(callback) {
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

func (d *websocketCallbackDispatcher) recoveredIsCurrentLocked(callback websocketCallback) bool {
	return d.gapOpen &&
		d.generation == callback.generation &&
		d.currentConn == callback.conn &&
		d.replacementConn == callback.conn
}

func (d *websocketCallbackDispatcher) dropReplacementLocked() {
	d.pendingData -= len(d.replacement)
	if d.pendingData < 0 {
		d.pendingData = 0
	}
	d.replacement = nil
	d.replacementConn = nil
}

func (d *websocketCallbackDispatcher) removeQueuedRecoveredLocked() {
	kept := d.queue[:0]
	for _, callback := range d.queue {
		if callback.kind == websocketCallbackRecovered {
			continue
		}
		kept = append(kept, callback)
	}
	for i := len(kept); i < len(d.queue); i++ {
		d.queue[i] = websocketCallback{}
	}
	d.queue = kept
}

func (d *websocketCallbackDispatcher) hasQueuedKindLocked(kind websocketCallbackKind) bool {
	for _, callback := range d.queue {
		if callback.kind == kind {
			return true
		}
	}
	return false
}

func (d *websocketCallbackDispatcher) signal() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}
