package model

import (
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/shopspring/decimal"
)

// Position is a venue-neutral position. Quantity is SIGNED — positive long,
// negative short — which uniformly captures Binance's signed PositionAmt,
// OKX's Pos+PosSide, and Hyperliquid's signed Szi. Side disambiguates hedge
// mode where two legs of the same instrument coexist.
type Position struct {
	InstrumentID  InstrumentID
	Side          enums.PositionSide // PosLong/PosShort under hedge mode, else PosNet
	Quantity      decimal.Decimal    // signed: + long, - short
	EntryPrice    decimal.Decimal
	MarkPrice     decimal.Decimal
	UnrealizedPnL decimal.Decimal
	Leverage      decimal.Decimal
	UpdatedAt     time.Time
}

// AccountBalance is a per-currency balance. Hyperliquid reports a single USDC
// balance; Binance and OKX report many.
type AccountBalance struct {
	Currency  string
	Total     decimal.Decimal
	Available decimal.Decimal
	Locked    decimal.Decimal
	UpdatedAt time.Time
}

// CashInvariantOK reports whether a cash-account balance satisfies
// total == available + locked. Margin accounts may intentionally not use this
// invariant because Available can represent free margin instead of free cash.
func (b AccountBalance) CashInvariantOK() bool {
	return b.Total.Equal(b.Available.Add(b.Locked))
}
