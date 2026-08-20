package factoryclient

import (
	"fmt"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantProcessing/boltertrader/exchange"
)

// orderBookContract is the venue-neutral guard at the Exchange WebSocket
// boundary. Venue backends may decode different wire protocols, but callers of
// exchange.SpotWebSocket must only observe one set of order-book semantics.
//
// A snapshot replaces the complete visible book. A delta mutates only the
// supplied levels; a zero quantity deletes a level. Sequence continuity is
// enforced whenever a backend can publish predecessor tokens. Backends that
// cannot expose a sequence still receive shape and identity validation.
type orderBookContract struct {
	meta       clientMeta
	instrument exchange.Instrument

	mu       sync.Mutex
	started  bool
	sequence string
}

func newOrderBookContract(meta clientMeta, instrument exchange.Instrument) *orderBookContract {
	return &orderBookContract{meta: meta, instrument: instrument}
}

func (contract *orderBookContract) accept(event exchange.BookEvent) (bool, error) {
	if contract == nil {
		return false, fmt.Errorf("order-book contract is unavailable")
	}
	if event.Kind != exchange.EventSnapshot && event.Kind != exchange.EventDelta {
		return false, contract.violation("event kind must be snapshot or delta")
	}
	if !sameOrderBookInstrument(contract.instrument, event.Instrument) {
		return false, contract.violation("event instrument does not match subscription")
	}
	if err := validateOrderBookLevels(event.Kind, event.Bids, "bid"); err != nil {
		return false, contract.violation(err.Error())
	}
	if err := validateOrderBookLevels(event.Kind, event.Asks, "ask"); err != nil {
		return false, contract.violation(err.Error())
	}

	sequence := strings.TrimSpace(event.Sequence)
	previous := strings.TrimSpace(event.Previous)
	if previous != "" && sequence == "" {
		return false, contract.violation("predecessor requires a sequence")
	}

	contract.mu.Lock()
	defer contract.mu.Unlock()

	if event.Kind == exchange.EventSnapshot {
		contract.started = true
		contract.sequence = sequence
		return true, nil
	}
	if contract.started && sequence != "" && contract.sequence != "" {
		if comparison, comparable := compareOrderBookSequence(sequence, contract.sequence); comparable && comparison <= 0 {
			// Duplicate and stale updates are safe to discard. A forward gap is
			// rejected above when the venue exposes a predecessor token.
			return false, nil
		}
	}
	if contract.started && previous != "" && contract.sequence != "" && previous != contract.sequence {
		return false, contract.violation("delta predecessor does not match the last sequence")
	}
	contract.started = true
	contract.sequence = sequence
	return true, nil
}

func (contract *orderBookContract) violation(message string) error {
	return websocketError(contract.meta, "WatchOrderBook", exchange.KindMalformedResponse, message)
}

func sameOrderBookInstrument(expected, actual exchange.Instrument) bool {
	left, right := expected.Identity, actual.Identity
	if left.Venue != right.Venue || left.Product != right.Product {
		return false
	}
	if left.NativeID != "" || right.NativeID != "" {
		return left.NativeID != "" && left.NativeID == right.NativeID
	}
	return left.NativeSymbol != "" && left.NativeSymbol == right.NativeSymbol
}

func validateOrderBookLevels(kind exchange.EventKind, levels []exchange.BookLevel, side string) error {
	for _, level := range levels {
		if !level.Price.IsPositive() {
			return fmt.Errorf("%s price must be positive", side)
		}
		if level.Quantity.IsNegative() {
			return fmt.Errorf("%s quantity must not be negative", side)
		}
		if kind == exchange.EventSnapshot && level.Quantity.IsZero() {
			return fmt.Errorf("%s snapshot quantity must be positive", side)
		}
	}
	return nil
}

func compareOrderBookSequence(left, right string) (int, bool) {
	leftValue, leftErr := strconv.ParseUint(left, 10, 64)
	rightValue, rightErr := strconv.ParseUint(right, 10, 64)
	if leftErr != nil || rightErr != nil {
		return 0, false
	}
	switch {
	case leftValue < rightValue:
		return -1, true
	case leftValue > rightValue:
		return 1, true
	default:
		return 0, true
	}
}
