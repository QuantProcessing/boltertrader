package exec

import (
	"strings"
	"sync"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/orderstate"
)

type BufferedFill struct {
	Fill model.Fill
	Meta contract.EventMeta
}

type fillIndexKey struct {
	accountID string
	id        string
}

type FillBuffer struct {
	mu       sync.Mutex
	byClient map[fillIndexKey][]BufferedFill
	byVenue  map[fillIndexKey][]BufferedFill
	seen     map[string]struct{}
}

func NewFillBuffer() *FillBuffer {
	return &FillBuffer{
		byClient: make(map[fillIndexKey][]BufferedFill),
		byVenue:  make(map[fillIndexKey][]BufferedFill),
		seen:     make(map[string]struct{}),
	}
}

func (b *FillBuffer) MarkApplied(fill model.Fill) bool {
	key := fillKey(fill)
	if key == "" {
		return true
	}
	b.mu.Lock()
	defer b.mu.Unlock()
	if _, ok := b.seen[key]; ok {
		return false
	}
	b.seen[key] = struct{}{}
	return true
}

func (b *FillBuffer) Buffer(fill model.Fill) {
	b.BufferEnvelope(fill, contract.EventMeta{})
}

func (b *FillBuffer) BufferEnvelope(fill model.Fill, meta contract.EventMeta) {
	b.mu.Lock()
	defer b.mu.Unlock()
	buffered := BufferedFill{Fill: fill, Meta: meta}
	if fill.ClientID != "" {
		key := fillIndexKey{accountID: fill.AccountID, id: fill.ClientID}
		b.byClient[key] = append(b.byClient[key], buffered)
	}
	if fill.VenueOrderID != "" {
		key := fillIndexKey{accountID: fill.AccountID, id: fill.VenueOrderID}
		b.byVenue[key] = append(b.byVenue[key], buffered)
	}
}

func (b *FillBuffer) Drain(order model.Order) []model.Fill {
	buffered := b.DrainBuffered(order)
	out := make([]model.Fill, 0, len(buffered))
	for _, fill := range buffered {
		out = append(out, fill.Fill)
	}
	return out
}

func (b *FillBuffer) DrainBuffered(order model.Order) []BufferedFill {
	b.mu.Lock()
	defer b.mu.Unlock()
	var out []BufferedFill
	accountID := order.Request.AccountID
	if clientID := order.Request.ClientID; clientID != "" {
		out = append(out, b.drainIndex(b.byClient, accountID, clientID)...)
	}
	if venueID := order.VenueOrderID; venueID != "" {
		out = append(out, b.drainIndex(b.byVenue, accountID, venueID)...)
	}
	return dedupeBufferedFills(out)
}

func (b *FillBuffer) drainIndex(index map[fillIndexKey][]BufferedFill, accountID, id string) []BufferedFill {
	var out []BufferedFill
	if accountID != "" {
		key := fillIndexKey{accountID: accountID, id: id}
		out = append(out, index[key]...)
		delete(index, key)
	}
	unscoped := fillIndexKey{id: id}
	out = append(out, index[unscoped]...)
	delete(index, unscoped)
	return out
}

func (b *FillBuffer) Count() int {
	b.mu.Lock()
	defer b.mu.Unlock()
	seen := make(map[string]struct{})
	for _, fills := range b.byClient {
		for _, fill := range fills {
			seen[pendingFillKey(fill.Fill)] = struct{}{}
		}
	}
	for _, fills := range b.byVenue {
		for _, fill := range fills {
			seen[pendingFillKey(fill.Fill)] = struct{}{}
		}
	}
	return len(seen)
}

func dedupeBufferedFills(fills []BufferedFill) []BufferedFill {
	if len(fills) < 2 {
		return fills
	}
	seen := make(map[string]struct{}, len(fills))
	out := fills[:0]
	for _, fill := range fills {
		key := fillKey(fill.Fill)
		if key == "" {
			key = pendingFillKey(fill.Fill)
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, fill)
	}
	return out
}

func fillKey(fill model.Fill) string {
	return orderstate.FillKey(fill)
}

func pendingFillKey(fill model.Fill) string {
	if key := fillKey(fill); key != "" {
		return key
	}
	return strings.Join([]string{
		fill.AccountID,
		fill.InstrumentID.String(),
		fill.ClientID,
		fill.VenueOrderID,
		fill.Price.String(),
		fill.Quantity.String(),
		fill.Timestamp.UTC().Format("2006-01-02T15:04:05.999999999Z07:00"),
	}, "\x00")
}
