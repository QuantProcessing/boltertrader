// Package streamgap bridges SDK reconnect lifecycle callbacks into the
// runtime's serialized execution event stream.
package streamgap

import (
	"sync"

	"github.com/QuantProcessing/boltertrader/core/contract"
)

// Reporter emits one paired started/recovered event per unexpected private
// stream gap. Initial connects and duplicate SDK callbacks are ignored.
type Reporter struct {
	mu         sync.Mutex
	venue      string
	accountID  string
	streamID   string
	generation uint64
	active     bool
	emit       func(contract.ExecEnvelope) bool
}

func New(venue, accountID, streamID string, emit func(contract.ExecEnvelope) bool) *Reporter {
	return &Reporter{venue: venue, accountID: accountID, streamID: streamID, emit: emit}
}

func (r *Reporter) Started(reason string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.active {
		return true
	}
	generation := r.generation + 1
	if !r.emitEvent(generation, contract.StreamGapStarted, reason) {
		return false
	}
	r.generation = generation
	r.active = true
	return true
}

func (r *Reporter) Recovered(reason string) bool {
	if r == nil {
		return false
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if !r.active {
		return true
	}
	if !r.emitEvent(r.generation, contract.StreamGapRecovered, reason) {
		return false
	}
	r.active = false
	return true
}

func (r *Reporter) emitEvent(generation uint64, phase contract.StreamGapPhase, reason string) bool {
	if r.emit == nil {
		return false
	}
	return r.emit(contract.NewExecEnvelope(contract.StreamGapEvent{
		Venue:      r.venue,
		AccountID:  r.accountID,
		StreamID:   r.streamID,
		Generation: generation,
		Phase:      phase,
		Reason:     reason,
	}))
}
