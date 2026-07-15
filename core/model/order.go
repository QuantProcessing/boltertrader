package model

import (
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/shopspring/decimal"
)

// OrderRequest is a venue-neutral order submission. Venue-only conversion and
// request details remain private to the owning adapter.
type OrderRequest struct {
	AccountID    string
	InstrumentID InstrumentID
	ClientID     string // idempotency key (Binance newClientOrderId / OKX clOrdId / HL cloid)
	Side         enums.OrderSide
	Type         enums.OrderType
	TIF          enums.TimeInForce
	Quantity     decimal.Decimal
	Price        decimal.Decimal // zero for market orders
	TriggerPrice decimal.Decimal // stop/take-profit trigger; zero otherwise
	// ActivationPrice is the optional activation price for trailing stop orders.
	ActivationPrice decimal.Decimal
	// TrailingOffsetBps is the trailing callback offset in basis points
	// (25 == 0.25%). Adapters convert this to each venue's native unit.
	TrailingOffsetBps decimal.Decimal
	PositionSide      enums.PositionSide
	ReduceOnly        bool
}

// Order is the lifecycle state of a submitted order as tracked by the runtime.
type Order struct {
	Request      OrderRequest
	VenueOrderID string // Binance OrderID / OKX ordId / HL oid (as string)
	Status       enums.OrderStatus
	FilledQty    decimal.Decimal
	AvgFillPrice decimal.Decimal
	CreatedAt    time.Time // clock.Now() at submit
	UpdatedAt    time.Time
	RejectReason string
}

// Fill is a single execution against an order.
type Fill struct {
	AccountID    string
	InstrumentID InstrumentID
	VenueOrderID string
	ClientID     string
	TradeID      string
	Side         enums.OrderSide
	Liquidity    enums.LiquiditySide
	Price        decimal.Decimal
	Quantity     decimal.Decimal
	Fee          decimal.Decimal
	FeeCurrency  string
	Timestamp    time.Time
}
