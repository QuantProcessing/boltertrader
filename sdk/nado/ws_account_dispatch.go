package nado

import (
	"sync"

	"github.com/coder/websocket"
)

const (
	nadoAccountCallbackQueueLimit   = 1024
	nadoAccountCallbackControlSlots = 2
)

type accountCallbackKind uint8

const (
	accountCallbackData accountCallbackKind = iota + 1
	accountCallbackStarted
	accountCallbackRecovered
)

type accountCallback struct {
	kind       accountCallbackKind
	generation uint64
	conn       *websocket.Conn
	run        func()
}

// accountCallbackDispatcher is the single ordering boundary for private data
// and reconnect lifecycle callbacks. Candidate-socket data remains buffered
// until replay is complete and the matching recovered callback has returned.
type accountCallbackDispatcher struct {
	mu sync.Mutex

	queue       []accountCallback
	replacement []accountCallback
	pendingData int
	wake        chan struct{}
	limit       int
	stopped     bool

	currentConn     *websocket.Conn
	gapOpen         bool
	generation      uint64
	replacementConn *websocket.Conn
	inFlightKind    accountCallbackKind
}

func newAccountCallbackDispatcher() *accountCallbackDispatcher {
	d := &accountCallbackDispatcher{
		wake:  make(chan struct{}, 1),
		limit: nadoAccountCallbackQueueLimit,
	}
	go d.runLoop()
	return d
}

func (d *accountCallbackDispatcher) activateConnection(generation uint64, conn *websocket.Conn, recovering bool) {
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

func (d *accountCallbackDispatcher) enqueueData(conn *websocket.Conn, callback accountCallback) bool {
	if d == nil || callback.run == nil {
		return true
	}
	d.mu.Lock()
	if d.stopped || d.currentConn != conn {
		d.mu.Unlock()
		return true
	}
	dataLimit := d.limit - nadoAccountCallbackControlSlots
	if dataLimit < 1 {
		dataLimit = 1
	}
	if d.pendingData+1 > dataLimit {
		d.mu.Unlock()
		return false
	}
	callback.kind = accountCallbackData
	callback.conn = conn
	if d.gapOpen {
		if d.replacementConn == nil {
			d.replacementConn = conn
		}
		if d.replacementConn != conn {
			d.mu.Unlock()
			return true
		}
		d.replacement = append(d.replacement, callback)
		d.pendingData++
		d.mu.Unlock()
		return true
	}
	d.queue = append(d.queue, callback)
	d.pendingData++
	d.mu.Unlock()
	d.signal()
	return true
}

func (d *accountCallbackDispatcher) beginGap(generation uint64, started func()) {
	if d == nil {
		return
	}
	d.mu.Lock()
	if d.stopped {
		d.mu.Unlock()
		return
	}
	wasOpen := d.gapOpen
	recoveredInFlight := d.inFlightKind == accountCallbackRecovered
	d.currentConn = nil
	d.gapOpen = true
	d.generation = generation
	d.dropReplacementLocked()
	d.removeQueuedRecoveredLocked()
	if !wasOpen || recoveredInFlight {
		if !d.hasQueuedKindLocked(accountCallbackStarted) {
			d.queue = append(d.queue, accountCallback{
				kind:       accountCallbackStarted,
				generation: generation,
				run:        started,
			})
		}
	}
	d.mu.Unlock()
	d.signal()
}

func (d *accountCallbackDispatcher) discardReplacement(generation uint64, conn *websocket.Conn) {
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

func (d *accountCallbackDispatcher) enqueueRecovered(generation uint64, conn *websocket.Conn, recovered func()) bool {
	if d == nil || conn == nil {
		return false
	}
	d.mu.Lock()
	if d.stopped || !d.gapOpen || d.generation != generation || d.currentConn != conn || d.replacementConn != conn {
		d.mu.Unlock()
		return false
	}
	d.removeQueuedRecoveredLocked()
	d.queue = append(d.queue, accountCallback{
		kind:       accountCallbackRecovered,
		generation: generation,
		conn:       conn,
		run:        recovered,
	})
	d.mu.Unlock()
	d.signal()
	return true
}

func (d *accountCallbackDispatcher) stop() {
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

func (d *accountCallbackDispatcher) runLoop() {
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
			d.queue[0] = accountCallback{}
			d.queue = d.queue[1:]
			if callback.kind == accountCallbackData {
				d.pendingData--
			}
			if callback.kind == accountCallbackRecovered && !d.recoveredIsCurrentLocked(callback) {
				d.mu.Unlock()
				continue
			}
			d.inFlightKind = callback.kind
			d.mu.Unlock()

			if callback.run != nil {
				callback.run()
			}

			d.mu.Lock()
			if !d.stopped && callback.kind == accountCallbackRecovered && d.recoveredIsCurrentLocked(callback) {
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

func (d *accountCallbackDispatcher) recoveredIsCurrentLocked(callback accountCallback) bool {
	return d.gapOpen &&
		d.generation == callback.generation &&
		d.currentConn == callback.conn &&
		d.replacementConn == callback.conn
}

func (d *accountCallbackDispatcher) dropReplacementLocked() {
	d.pendingData -= len(d.replacement)
	if d.pendingData < 0 {
		d.pendingData = 0
	}
	d.replacement = nil
	d.replacementConn = nil
}

func (d *accountCallbackDispatcher) removeQueuedRecoveredLocked() {
	kept := d.queue[:0]
	for _, callback := range d.queue {
		if callback.kind == accountCallbackRecovered {
			continue
		}
		kept = append(kept, callback)
	}
	for i := len(kept); i < len(d.queue); i++ {
		d.queue[i] = accountCallback{}
	}
	d.queue = kept
}

func (d *accountCallbackDispatcher) hasQueuedKindLocked(kind accountCallbackKind) bool {
	for _, callback := range d.queue {
		if callback.kind == kind {
			return true
		}
	}
	return false
}

func (d *accountCallbackDispatcher) signal() {
	select {
	case d.wake <- struct{}{}:
	default:
	}
}
