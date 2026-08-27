package factoryclient

import (
	"fmt"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

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
	observed time.Time
	bids     map[string]exchange.BookLevel
	asks     map[string]exchange.BookLevel
}

func newOrderBookContract(meta clientMeta, instrument exchange.Instrument) *orderBookContract {
	return &orderBookContract{
		meta: meta, instrument: instrument,
		bids: make(map[string]exchange.BookLevel), asks: make(map[string]exchange.BookLevel),
	}
}

func (contract *orderBookContract) accept(event exchange.BookEvent) (bool, error) {
	if contract == nil {
		return false, fmt.Errorf("order-book contract is unavailable")
	}
	if event.Kind != exchange.EventSnapshot && event.Kind != exchange.EventDelta && event.Kind != exchange.EventFull {
		return false, contract.violation("event kind must be snapshot, delta, or full")
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

	if event.Kind == exchange.EventSnapshot || event.Kind == exchange.EventFull {
		contract.started = true
		contract.sequence = sequence
		contract.observed = event.Time
		contract.bids = replaceBookSide(event.Bids)
		contract.asks = replaceBookSide(event.Asks)
		return true, nil
	}
	if !contract.started {
		return false, contract.violation("delta arrived before a snapshot or full book")
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
	contract.observed = event.Time
	patchBookSide(contract.bids, event.Bids)
	patchBookSide(contract.asks, event.Asks)
	return true, nil
}

// snapshot returns the current bounded venue-visible book for a subscriber
// joining an already shared native topic. This is replay state at the Exchange
// boundary, not a Gateway order-book engine; all prices still originate from
// the WebSocket stream and no REST join is involved.
func (contract *orderBookContract) snapshot() (exchange.BookEvent, bool) {
	if contract == nil {
		return exchange.BookEvent{}, false
	}
	contract.mu.Lock()
	defer contract.mu.Unlock()
	if !contract.started {
		return exchange.BookEvent{}, false
	}
	return exchange.BookEvent{
		Kind: exchange.EventSnapshot, Instrument: contract.instrument,
		Sequence: contract.sequence, Bids: sortedBookSide(contract.bids, true),
		Asks: sortedBookSide(contract.asks, false), Time: contract.observed,
	}, true
}

func replaceBookSide(levels []exchange.BookLevel) map[string]exchange.BookLevel {
	result := make(map[string]exchange.BookLevel, len(levels))
	patchBookSide(result, levels)
	return result
}

func patchBookSide(current map[string]exchange.BookLevel, levels []exchange.BookLevel) {
	for _, level := range levels {
		key := level.Price.String()
		if level.Quantity.IsZero() {
			delete(current, key)
			continue
		}
		current[key] = level
	}
}

func sortedBookSide(current map[string]exchange.BookLevel, descending bool) []exchange.BookLevel {
	levels := make([]exchange.BookLevel, 0, len(current))
	for _, level := range current {
		levels = append(levels, level)
	}
	sort.Slice(levels, func(i, j int) bool {
		comparison := levels[i].Price.Cmp(levels[j].Price)
		if descending {
			return comparison > 0
		}
		return comparison < 0
	})
	return levels
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
		if (kind == exchange.EventSnapshot || kind == exchange.EventFull) && level.Quantity.IsZero() {
			return fmt.Errorf("%s complete-book quantity must be positive", side)
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
