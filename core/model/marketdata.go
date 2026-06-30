package model

import (
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/shopspring/decimal"
)

// BookLevel is one price level of an order book.
type BookLevel struct {
	Price    decimal.Decimal
	Quantity decimal.Decimal
}

// OrderBook is a depth snapshot. Bids and Asks are ordered best-first
// (Bids descending price, Asks ascending price).
type OrderBook struct {
	InstrumentID InstrumentID
	Bids         []BookLevel
	Asks         []BookLevel
	Sequence     int64
	Timestamp    time.Time
}

// QuoteTick is a top-of-book update (best bid / best ask).
type QuoteTick struct {
	InstrumentID InstrumentID
	BidPrice     decimal.Decimal
	BidSize      decimal.Decimal
	AskPrice     decimal.Decimal
	AskSize      decimal.Decimal
	Timestamp    time.Time
}

// TradeTick is a single public trade print.
type TradeTick struct {
	InstrumentID  InstrumentID
	Price         decimal.Decimal
	Quantity      decimal.Decimal
	AggressorSide enums.OrderSide
	TradeID       string
	Timestamp     time.Time
}

// Bar is an OHLCV candle for a given interval.
type Bar struct {
	InstrumentID InstrumentID
	Interval     string
	Open         decimal.Decimal
	High         decimal.Decimal
	Low          decimal.Decimal
	Close        decimal.Decimal
	Volume       decimal.Decimal
	OpenTime     time.Time
	CloseTime    time.Time
}
