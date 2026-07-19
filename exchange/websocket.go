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
	Instrument string       `json:"instrument,omitempty"`
	Options    WatchOptions `json:"options,omitempty"`
}

type WatchCandlesRequest struct {
	Instrument string       `json:"instrument,omitempty"`
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

type BookEvent struct {
	Kind       EventKind   `json:"kind,omitempty"`
	Instrument string      `json:"instrument,omitempty"`
	Sequence   string      `json:"sequence,omitempty"`
	Previous   string      `json:"previous,omitempty"`
	Resync     bool        `json:"resync,omitempty"`
	Bids       []BookLevel `json:"bids,omitempty"`
	Asks       []BookLevel `json:"asks,omitempty"`
	Time       time.Time   `json:"time,omitempty"`
}

type BBOEvent struct {
	Instrument string    `json:"instrument,omitempty"`
	Bid        BookLevel `json:"bid"`
	Ask        BookLevel `json:"ask"`
	Time       time.Time `json:"time,omitempty"`
}

type PublicTradeEvent struct {
	Instrument string          `json:"instrument,omitempty"`
	TradeID    string          `json:"trade_id,omitempty"`
	Side       Side            `json:"side,omitempty"`
	Price      decimal.Decimal `json:"price"`
	Quantity   decimal.Decimal `json:"quantity"`
	Time       time.Time       `json:"time,omitempty"`
}

type CandleEvent struct {
	Instrument string `json:"instrument,omitempty"`
	Interval   string `json:"interval,omitempty"`
	Candle     Candle `json:"candle"`
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
	Instrument string          `json:"instrument,omitempty"`
	Price      decimal.Decimal `json:"price"`
	Time       time.Time       `json:"time,omitempty"`
}

type FundingRateEvent struct {
	Instrument  string          `json:"instrument,omitempty"`
	Rate        decimal.Decimal `json:"rate"`
	EffectiveAt time.Time       `json:"effective_at,omitempty"`
	NextAt      time.Time       `json:"next_at,omitempty"`
}

type SpotWebSocket interface {
	WatchOrderBook(context.Context, WatchRequest) (Subscription[BookEvent], error)
	WatchBBO(context.Context, WatchRequest) (Subscription[BBOEvent], error)
	WatchPublicTrades(context.Context, WatchRequest) (Subscription[PublicTradeEvent], error)
	WatchCandles(context.Context, WatchCandlesRequest) (Subscription[CandleEvent], error)
	WatchOrders(context.Context, WatchRequest) (Subscription[OrderEvent], error)
	WatchFills(context.Context, WatchRequest) (Subscription[FillEvent], error)
	WatchBalances(context.Context, WatchAccountRequest) (Subscription[BalanceEvent], error)
	PlaceOrder(context.Context, PlaceOrderRequest) (OrderAcknowledgement, error)
	CancelOrder(context.Context, CancelOrderRequest) (OrderAcknowledgement, error)
	Close() error
}

type PerpWebSocket interface {
	SpotWebSocket
	WatchPositions(context.Context, WatchRequest) (Subscription[PositionEvent], error)
	WatchMarkPrice(context.Context, WatchRequest) (Subscription[MarkPriceEvent], error)
	WatchFundingRate(context.Context, WatchRequest) (Subscription[FundingRateEvent], error)
}
