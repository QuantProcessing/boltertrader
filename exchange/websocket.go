package exchange

import (
	"context"
	"time"

	"github.com/shopspring/decimal"
)

type EventKind string

const (
	EventSnapshot EventKind = "snapshot"
	EventDelta    EventKind = "delta"
	// EventFull is a self-contained replacement delivered after the initial
	// snapshot. Some venues (for example partial-depth and l2Book channels)
	// publish a complete visible book on every push rather than level deltas.
	EventFull EventKind = "full"
)

type SubscriptionState string

const (
	SubscriptionConnecting SubscriptionState = "connecting"
	SubscriptionActive     SubscriptionState = "active"
	SubscriptionGap        SubscriptionState = "gap"
	SubscriptionResyncing  SubscriptionState = "resyncing"
	SubscriptionClosed     SubscriptionState = "closed"
)

type GapPhase string

const (
	GapStarted   GapPhase = "started"
	GapRecovered GapPhase = "recovered"
)

type StreamStatusEvent struct {
	State      SubscriptionState `json:"state,omitempty"`
	Phase      GapPhase          `json:"phase,omitempty"`
	Venue      Venue             `json:"venue,omitempty"`
	Product    Product           `json:"product,omitempty"`
	StreamID   string            `json:"stream_id,omitempty"`
	Generation uint64            `json:"generation"`
	Reason     string            `json:"reason,omitempty"`
	Time       time.Time         `json:"time,omitempty"`
}

type WatchOptions struct {
	Buffer int `json:"buffer,omitempty"`
}

type WatchRequest struct {
	Instrument Instrument   `json:"instrument"`
	Options    WatchOptions `json:"options,omitempty"`
}

type WatchCandlesRequest struct {
	Instrument Instrument   `json:"instrument"`
	Interval   string       `json:"interval,omitempty"`
	Options    WatchOptions `json:"options,omitempty"`
}

type WatchAccountRequest struct {
	Options WatchOptions `json:"options,omitempty"`
}

type Subscription[T any] interface {
	ID() string
	Events() <-chan T
	Status() <-chan StreamStatusEvent
	Errors() <-chan error
	Close() error
}

// ReplaySubscription is implemented by shared public topics that can replay a
// current WebSocket-derived value to a late logical subscriber. Order books use
// it to avoid a REST snapshot while still sharing one native venue topic.
type ReplaySubscription[T any] interface {
	Subscription[T]
	Replay() (T, bool)
}

// BookEvent is the venue-neutral order-book contract exposed by Exchange.
// EventSnapshot starts or replaces the complete visible book. EventFull is a
// later self-contained replacement in the same generation. EventDelta mutates
// only the supplied levels, and a delta level whose quantity is zero deletes
// that price. Sequence and Previous are opaque venue tokens; when Previous is
// present it must equal the last accepted Sequence. A snapshot starts a new
// sequence generation and may therefore reset that relationship.
type BookEvent struct {
	Kind       EventKind   `json:"kind,omitempty"`
	Instrument Instrument  `json:"instrument"`
	Sequence   string      `json:"sequence,omitempty"`
	Previous   string      `json:"previous,omitempty"`
	Resync     bool        `json:"resync,omitempty"`
	Bids       []BookLevel `json:"bids,omitempty"`
	Asks       []BookLevel `json:"asks,omitempty"`
	Time       time.Time   `json:"time,omitempty"`
}

type BBOEvent struct {
	Instrument     Instrument `json:"instrument"`
	Bid            BookLevel  `json:"bid"`
	Ask            BookLevel  `json:"ask"`
	SourceSequence string     `json:"source_sequence,omitempty"`
	Time           time.Time  `json:"time,omitempty"`
}

type PublicTradeEvent struct {
	Instrument Instrument      `json:"instrument"`
	TradeID    string          `json:"trade_id,omitempty"`
	Side       Side            `json:"side,omitempty"`
	Price      decimal.Decimal `json:"price"`
	Quantity   decimal.Decimal `json:"quantity"`
	Time       time.Time       `json:"time,omitempty"`
}

type CandleEvent struct {
	Instrument Instrument `json:"instrument"`
	Interval   string     `json:"interval,omitempty"`
	Candle     Candle     `json:"candle"`
}

type OrderEvent struct {
	Kind  EventKind `json:"kind,omitempty"`
	Order Order     `json:"order"`
}

type FillEvent struct {
	Kind EventKind `json:"kind,omitempty"`
	Fill Fill      `json:"fill"`
}

type BalanceEvent struct {
	Kind     EventKind `json:"kind,omitempty"`
	Balances []Balance `json:"balances,omitempty"`
	Time     time.Time `json:"time,omitempty"`
}

type PositionEvent struct {
	Kind      EventKind  `json:"kind,omitempty"`
	Positions []Position `json:"positions,omitempty"`
	Time      time.Time  `json:"time,omitempty"`
}

type MarkPriceEvent struct {
	Instrument Instrument      `json:"instrument"`
	Price      decimal.Decimal `json:"price"`
	Time       time.Time       `json:"time,omitempty"`
}

type FundingRateEvent struct {
	Instrument  Instrument      `json:"instrument"`
	Rate        decimal.Decimal `json:"rate"`
	EffectiveAt time.Time       `json:"effective_at,omitempty"`
	NextAt      time.Time       `json:"next_at,omitempty"`
}

// DerivativeReferenceEvent is the optional rich reference-price stream used
// by the Gateway Feed Owner. Some venues publish mark, index, funding and open
// interest in one native ticker; preserving that shape prevents the common
// WatchMarkPrice facade from throwing away fields needed by other projections.
// Every value remains optional because other venues split the fields across
// several native topics.
type DerivativeReferenceEvent struct {
	Instrument             Instrument      `json:"instrument"`
	MarkPrice              OptionalDecimal `json:"mark_price"`
	IndexPrice             OptionalDecimal `json:"index_price"`
	FundingRate            OptionalDecimal `json:"funding_rate"`
	FundingIntervalHours   OptionalInt     `json:"funding_interval_hours"`
	NextFundingTime        time.Time       `json:"next_funding_time,omitempty"`
	OpenInterest           OptionalDecimal `json:"open_interest"`
	OpenInterestNotional   OptionalDecimal `json:"open_interest_notional"`
	OpenInterestAsset      string          `json:"open_interest_asset,omitempty"`
	FundingObservedAt      time.Time       `json:"funding_observed_at,omitempty"`
	MarkObservedAt         time.Time       `json:"mark_observed_at,omitempty"`
	IndexObservedAt        time.Time       `json:"index_observed_at,omitempty"`
	OpenInterestObservedAt time.Time       `json:"open_interest_observed_at,omitempty"`
}

// DerivativeReferenceWebSocket is an additive capability. It intentionally is
// not embedded in PerpWebSocket, so existing adapters and third-party callers
// remain source compatible while richer adapters can expose their native
// ticker without opening a second connection.
type DerivativeReferenceWebSocket interface {
	WatchDerivativeReference(context.Context, WatchRequest) (Subscription[DerivativeReferenceEvent], error)
}

// MarketWebSocket contains only public market streams. Public connectors return
// an object with this method set, not an authenticated socket behind a narrower
// interface that callers could recover through a type assertion.
type MarketWebSocket interface {
	WatchOrderBook(context.Context, WatchRequest) (Subscription[BookEvent], error)
	WatchBBO(context.Context, WatchRequest) (Subscription[BBOEvent], error)
	WatchPublicTrades(context.Context, WatchRequest) (Subscription[PublicTradeEvent], error)
	WatchCandles(context.Context, WatchCandlesRequest) (Subscription[CandleEvent], error)
	Close() error
}

// PerpMarketWebSocket adds public derivative streams without account or order
// access. DerivativeReferenceWebSocket remains an optional additional facet.
type PerpMarketWebSocket interface {
	MarketWebSocket
	WatchMarkPrice(context.Context, WatchRequest) (Subscription[MarkPriceEvent], error)
	WatchFundingRate(context.Context, WatchRequest) (Subscription[FundingRateEvent], error)
}

// SpotWebSocket is the complete socket surface of an account client. Market-only
// consumers should depend on MarketWebSocket instead.
type SpotWebSocket interface {
	MarketWebSocket
	WatchOrders(context.Context, WatchRequest) (Subscription[OrderEvent], error)
	WatchFills(context.Context, WatchRequest) (Subscription[FillEvent], error)
	WatchBalances(context.Context, WatchAccountRequest) (Subscription[BalanceEvent], error)
	PlaceOrder(context.Context, PlaceOrderRequest) (OrderAcknowledgement, error)
	CancelOrder(context.Context, CancelOrderRequest) (OrderAcknowledgement, error)
}

type PerpWebSocket interface {
	SpotWebSocket
	PerpMarketWebSocket
	WatchPositions(context.Context, WatchRequest) (Subscription[PositionEvent], error)
}
