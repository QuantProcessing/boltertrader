// Package contract defines the venue-neutral interfaces the trading runtime
// depends on. The runtime imports ONLY core/{enums,model,clock,contract} and
// never an SDK or adapter, so live adapters and runtime fakes share the same
// event contract.
//
// Push updates are delivered as typed event sums (no interface{}) so the
// runtime can switch over them exhaustively.
package contract

import (
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/core/model"
)

type EventSource string

const (
	SourceUnknown        EventSource = ""
	SourceAdapterStream  EventSource = "adapter_stream"
	SourceAdapterREST    EventSource = "adapter_rest"
	SourceRuntime        EventSource = "runtime"
	SourceReconciliation EventSource = "reconciliation"
	SourceTest           EventSource = "test"
)

type EventFlags uint64

const (
	EventFlagFromSnapshot EventFlags = 1 << iota
	EventFlagFromStream
	EventFlagFromReconciliation
	EventFlagSynthetic
	EventFlagAmbiguous
	EventFlagExternal
	EventFlagDroppedBySink
	EventFlagReplay
)

func (f EventFlags) Has(flag EventFlags) bool { return f&flag != 0 }

type EventMeta struct {
	EventID       model.EventID
	Source        EventSource
	Venue         string
	AccountID     string
	InstrumentID  model.InstrumentID
	CorrelationID string
	ClientID      string
	VenueOrderID  string
	TradeID       string
	Sequence      uint64
	TsVenue       time.Time
	TsAdapterRecv time.Time
	TsAdapterEmit time.Time
	TsBusRecv     time.Time
	TsApplied     time.Time
	Flags         EventFlags
}

type EventEnvelope[T any] struct {
	EventMeta
	Payload T
}

func (e EventEnvelope[T]) Meta() EventMeta { return e.EventMeta }

func (e EventEnvelope[T]) Validate() error {
	if e.EventID == "" {
		return fmt.Errorf("event envelope: event id required")
	}
	if !monotonicTimes(e.TsVenue, e.TsAdapterRecv, e.TsAdapterEmit, e.TsBusRecv, e.TsApplied) {
		return fmt.Errorf("event envelope: timestamps are not monotonic")
	}
	return nil
}

func monotonicTimes(times ...time.Time) bool {
	var prev time.Time
	for _, ts := range times {
		if ts.IsZero() {
			continue
		}
		if !prev.IsZero() && ts.Before(prev) {
			return false
		}
		prev = ts
	}
	return true
}

type MarketEnvelope = EventEnvelope[MarketEvent]
type ExecEnvelope = EventEnvelope[ExecEvent]
type AccountEnvelope = EventEnvelope[AccountEvent]

func NewMarketEnvelope(payload MarketEvent) MarketEnvelope {
	return NewMarketEnvelopeWithMeta(payload, EventMeta{Source: SourceAdapterStream, Flags: EventFlagFromStream})
}

func NewExecEnvelope(payload ExecEvent) ExecEnvelope {
	return NewExecEnvelopeWithMeta(payload, EventMeta{Source: SourceAdapterStream, Flags: EventFlagFromStream})
}

func NewAccountEnvelope(payload AccountEvent) AccountEnvelope {
	return NewAccountEnvelopeWithMeta(payload, EventMeta{Source: SourceAdapterStream, Flags: EventFlagFromStream})
}

func NewMarketEnvelopeWithMeta(payload MarketEvent, meta EventMeta) MarketEnvelope {
	return MarketEnvelope{EventMeta: mergeEventMeta(inferMarketMeta(payload), meta), Payload: payload}
}

func NewExecEnvelopeWithMeta(payload ExecEvent, meta EventMeta) ExecEnvelope {
	return ExecEnvelope{EventMeta: mergeEventMeta(inferExecMeta(payload), meta), Payload: payload}
}

func NewAccountEnvelopeWithMeta(payload AccountEvent, meta EventMeta) AccountEnvelope {
	return AccountEnvelope{EventMeta: mergeEventMeta(inferAccountMeta(payload), meta), Payload: payload}
}

func mergeEventMeta(inferred, override EventMeta) EventMeta {
	meta := inferred
	if override.EventID != "" {
		meta.EventID = override.EventID
	}
	if override.Source != "" {
		meta.Source = override.Source
	}
	if override.Venue != "" {
		meta.Venue = override.Venue
	}
	if override.AccountID != "" {
		meta.AccountID = override.AccountID
	}
	if override.InstrumentID != (model.InstrumentID{}) {
		meta.InstrumentID = override.InstrumentID
	}
	if override.CorrelationID != "" {
		meta.CorrelationID = override.CorrelationID
	}
	if override.ClientID != "" {
		meta.ClientID = override.ClientID
	}
	if override.VenueOrderID != "" {
		meta.VenueOrderID = override.VenueOrderID
	}
	if override.TradeID != "" {
		meta.TradeID = override.TradeID
	}
	if override.Sequence != 0 {
		meta.Sequence = override.Sequence
	}
	if !override.TsVenue.IsZero() {
		meta.TsVenue = override.TsVenue
	}
	if !override.TsAdapterRecv.IsZero() {
		meta.TsAdapterRecv = override.TsAdapterRecv
	}
	if !override.TsAdapterEmit.IsZero() {
		meta.TsAdapterEmit = override.TsAdapterEmit
	}
	if !override.TsBusRecv.IsZero() {
		meta.TsBusRecv = override.TsBusRecv
	}
	if !override.TsApplied.IsZero() {
		meta.TsApplied = override.TsApplied
	}
	if override.Flags != 0 {
		meta.Flags = override.Flags
	}
	return meta
}

func inferMarketMeta(payload MarketEvent) EventMeta {
	meta := EventMeta{}
	switch p := payload.(type) {
	case BookEvent:
		meta.InstrumentID = p.Book.InstrumentID
		meta.Venue = p.Book.InstrumentID.Venue
		if p.Book.Sequence > 0 {
			meta.Sequence = uint64(p.Book.Sequence)
		}
		meta.TsVenue = p.Book.Timestamp
		meta.EventID = model.EventID(joinEventID("market", "book", p.Book.InstrumentID.String(), fmt.Sprint(p.Book.Sequence), eventTimeKey(p.Book.Timestamp)))
	case QuoteEvent:
		meta.InstrumentID = p.Quote.InstrumentID
		meta.Venue = p.Quote.InstrumentID.Venue
		meta.TsVenue = p.Quote.Timestamp
		meta.EventID = model.EventID(joinEventID(
			"market", "quote", p.Quote.InstrumentID.String(),
			p.Quote.BidPrice.String(), p.Quote.BidSize.String(),
			p.Quote.AskPrice.String(), p.Quote.AskSize.String(),
			eventTimeKey(p.Quote.Timestamp),
		))
	case TradeEvent:
		meta.InstrumentID = p.Trade.InstrumentID
		meta.Venue = p.Trade.InstrumentID.Venue
		meta.TradeID = p.Trade.TradeID
		meta.TsVenue = p.Trade.Timestamp
		meta.EventID = model.EventID(joinEventID(
			"market", "trade", p.Trade.InstrumentID.String(), p.Trade.TradeID,
			p.Trade.Price.String(), p.Trade.Quantity.String(), p.Trade.AggressorSide.String(),
			eventTimeKey(p.Trade.Timestamp),
		))
	case ReferenceDataEvent:
		meta.InstrumentID = p.Snapshot.InstrumentID
		meta.Venue = p.Snapshot.InstrumentID.Venue
		meta.TsVenue = p.Snapshot.Timestamp
		meta.EventID = model.EventID(joinEventID("market", "reference", p.Snapshot.InstrumentID.String(), eventTimeKey(p.Snapshot.Timestamp)))
	}
	if meta.EventID == "" {
		meta.EventID = model.EventID(joinEventID("market", fmt.Sprintf("%T", payload), eventTimeKey(time.Now())))
	}
	return meta
}

func inferExecMeta(payload ExecEvent) EventMeta {
	meta := EventMeta{}
	switch p := payload.(type) {
	case OrderEvent:
		meta.InstrumentID = p.Order.Request.InstrumentID
		meta.Venue = p.Order.Request.InstrumentID.Venue
		meta.AccountID = p.Order.Request.AccountID
		meta.ClientID = p.Order.Request.ClientID
		meta.VenueOrderID = p.Order.VenueOrderID
		meta.TsVenue = p.Order.UpdatedAt
		meta.EventID = model.EventID(joinEventID(
			"exec", "order", meta.Venue, meta.AccountID, meta.ClientID, meta.VenueOrderID,
			p.Order.Status.String(), p.Order.FilledQty.String(), eventTimeKey(p.Order.UpdatedAt),
		))
	case FillEvent:
		meta.InstrumentID = p.Fill.InstrumentID
		meta.Venue = p.Fill.InstrumentID.Venue
		meta.AccountID = p.Fill.AccountID
		meta.ClientID = p.Fill.ClientID
		meta.VenueOrderID = p.Fill.VenueOrderID
		meta.TradeID = p.Fill.TradeID
		meta.TsVenue = p.Fill.Timestamp
		meta.EventID = model.EventID(joinEventID("exec", "fill", meta.Venue, meta.AccountID, meta.ClientID, meta.VenueOrderID, meta.TradeID, eventTimeKey(p.Fill.Timestamp)))
	case RejectEvent:
		meta.ClientID = p.ClientID
		meta.EventID = model.EventID(joinEventID("exec", "reject", p.ClientID, p.Reason))
	}
	if meta.EventID == "" {
		meta.EventID = model.EventID(joinEventID("exec", fmt.Sprintf("%T", payload), eventTimeKey(time.Now())))
	}
	return meta
}

func inferAccountMeta(payload AccountEvent) EventMeta {
	meta := EventMeta{}
	switch p := payload.(type) {
	case BalanceEvent:
		meta.AccountID = p.Balance.AccountID
		meta.EventID = model.EventID(joinEventID(
			"account", "balance", p.Balance.AccountID, p.Balance.Currency,
			p.Balance.Total.String(), p.Balance.Free.String(), p.Balance.Available.String(),
			p.Balance.Locked.String(), p.Balance.Borrowed.String(), p.Balance.Interest.String(),
			eventTimeKey(p.Balance.UpdatedAt),
		))
		meta.TsVenue = p.Balance.UpdatedAt
	case PositionEvent:
		meta.InstrumentID = p.Position.InstrumentID
		meta.Venue = p.Position.InstrumentID.Venue
		meta.AccountID = p.Position.AccountID
		meta.EventID = model.EventID(joinEventID(
			"account", "position", p.Position.AccountID, p.Position.InstrumentID.String(),
			p.Position.Side.String(), p.Position.Quantity.String(), eventTimeKey(p.Position.UpdatedAt),
		))
		meta.TsVenue = p.Position.UpdatedAt
	case AccountStateEvent:
		meta.Venue = p.State.Venue
		meta.AccountID = p.State.AccountID
		meta.TsVenue = p.State.TsEvent
		meta.EventID = p.State.EventID
		if meta.EventID == "" {
			meta.EventID = model.AccountStateEventID(p.State.Venue, p.State.AccountID, p.State.TsEvent)
		}
	}
	if meta.EventID == "" {
		meta.EventID = model.EventID(joinEventID("account", fmt.Sprintf("%T", payload), eventTimeKey(time.Now())))
	}
	return meta
}

func joinEventID(parts ...string) string {
	out := make([]string, 0, len(parts))
	for _, part := range parts {
		if part != "" {
			out = append(out, strings.ReplaceAll(part, "|", "/"))
		}
	}
	return strings.Join(out, "|")
}

func eventTimeKey(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}

// ExecEvent is the sum of execution-stream push events (order lifecycle).
type ExecEvent interface{ isExecEvent() }

// OrderEvent reports an order lifecycle transition (new, partial, filled,
// canceled, ...).
type OrderEvent struct{ Order model.Order }

// FillEvent reports a single execution against one of our orders.
type FillEvent struct{ Fill model.Fill }

// RejectEvent reports that an order was rejected, keyed by client id.
type RejectEvent struct {
	ClientID string
	Reason   string
}

func (OrderEvent) isExecEvent()  {}
func (FillEvent) isExecEvent()   {}
func (RejectEvent) isExecEvent() {}

// AccountEvent is the sum of account-stream push events.
type AccountEvent interface{ isAccountEvent() }

// BalanceEvent reports a balance change for one currency.
type BalanceEvent struct{ Balance model.AccountBalance }

// PositionEvent reports a position change for one instrument/side.
type PositionEvent struct{ Position model.Position }

// AccountStateEvent reports an authoritative account-state snapshot or update.
type AccountStateEvent struct{ State model.AccountState }

func (BalanceEvent) isAccountEvent()      {}
func (PositionEvent) isAccountEvent()     {}
func (AccountStateEvent) isAccountEvent() {}

// MarketEvent is the sum of public market-data push events.
type MarketEvent interface{ isMarketEvent() }

// BookEvent carries an order book update (snapshot or rebuilt from diffs by
// the adapter).
type BookEvent struct{ Book model.OrderBook }

// QuoteEvent carries a top-of-book update.
type QuoteEvent struct{ Quote model.QuoteTick }

// TradeEvent carries a public trade print.
type TradeEvent struct{ Trade model.TradeTick }

// ReferenceDataEvent carries normalized current derivative funding/reference
// data. Current OI is query-only and must not be represented as this event.
type ReferenceDataEvent struct {
	Snapshot model.DerivativeReferenceSnapshot
}

func (BookEvent) isMarketEvent()          {}
func (QuoteEvent) isMarketEvent()         {}
func (TradeEvent) isMarketEvent()         {}
func (ReferenceDataEvent) isMarketEvent() {}
