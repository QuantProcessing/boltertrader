package model

import (
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/shopspring/decimal"
)

// OrderRequest is a venue-neutral order submission. The runtime fills the
// portable fields; venue-only knobs go through Venue (the escape hatch), which
// is nil for fully portable orders.
type OrderRequest struct {
	InstrumentID InstrumentID
	ClientID     string // idempotency key (Binance newClientOrderId / OKX clOrdId / HL cloid)
	Side         enums.OrderSide
	Type         enums.OrderType
	TIF          enums.TimeInForce
	Quantity     decimal.Decimal
	Price        decimal.Decimal // zero for market orders
	TriggerPrice decimal.Decimal // stop/take-profit trigger; zero otherwise
	PositionSide enums.PositionSide
	ReduceOnly   bool

	// Venue is the per-venue escape hatch for non-portable options. Setting it
	// forfeits backtest/live parity for those fields. Nil for portable orders.
	Venue *VenueOrderOpts
}

// VenueOrderOpts carries venue-specific order options with no portable meaning.
// Exactly one sub-struct is expected to be set, matching the target venue; a
// foreign sub-struct must be rejected by the adapter with errs.ErrNotSupported.
//
// The sub-structs are intentionally left to the adapter packages to define and
// attach; this neutral type holds opaque references so core/model stays free of
// any venue import. Adapters type-assert their own option type out of Native.
type VenueOrderOpts struct {
	// Native is the adapter-defined options value (e.g. *binance.OrderOpts).
	// The owning adapter type-asserts it; others reject it.
	Native any
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
