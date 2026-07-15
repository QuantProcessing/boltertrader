// Package model defines the venue-neutral domain types the runtime and
// strategies operate on. All prices and sizes are shopspring/decimal — float64
// is forbidden in this layer to keep PnL and risk math exact.
package model

import (
	"fmt"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/shopspring/decimal"
)

// InstrumentID is the neutral handle the runtime uses to refer to a market. It
// never carries a venue's native symbol form (string symbol vs integer asset
// index) — those live on Instrument and are resolved by the adapter at the I/O
// boundary.
type InstrumentID struct {
	Venue  string               // "BINANCE" | "OKX" | "HYPERLIQUID"
	Symbol string               // canonical neutral symbol, e.g. "BTC-USDT"
	Kind   enums.InstrumentKind // Perp | Spot | Future
}

// String renders a stable, unique key, e.g. "BINANCE:BTC-USDT:PERP".
func (id InstrumentID) String() string {
	return fmt.Sprintf("%s:%s:%s", id.Venue, id.Symbol, id.Kind)
}

// PositionModeCap describes whether an instrument supports hedge-mode
// long/short legs or only one-way net positions.
type PositionModeCap uint8

const (
	NetOnly PositionModeCap = iota
	HedgeCapable
)

// Instrument is the resolved, cached description of a tradable market. It
// carries all three venue identity forms so an adapter can build any venue
// payload, plus precision as decimal tick/step (not integer precision) so the
// runtime can round uniformly across venues.
type Instrument struct {
	ID                  InstrumentID
	Base, Quote, Settle string // Settle is the margin/settlement currency

	// Venue identity forms (carried, not interpreted by the runtime):
	VenueSymbol string // Binance symbol / OKX InstId / Hyperliquid Coin
	AssetIndex  *int   // Hyperliquid AssetID (universe array index); nil if N/A

	// Precision, as exact decimals:
	PriceTick      decimal.Decimal // minimum price increment
	SizeStep       decimal.Decimal // minimum size increment
	MinQty         decimal.Decimal // minimum order quantity
	MinNotional    decimal.Decimal // minimum order notional
	PricePrecision int             // convenience mirror derived from PriceTick

	// ContractMultiplier scales an order's size into base-asset units when
	// computing notional value: notional = price * quantity * multiplier. For
	// linear USDT-margined perps (Binance USD-M, OKX linear) one contract is one
	// base unit, so the multiplier is 1; a zero value is treated as 1. It exists
	// for venues whose contract represents a fixed quantity of the base asset
	// (e.g. an OKX contract face value) — those adapters set it explicitly.
	ContractMultiplier decimal.Decimal

	PositionMode PositionModeCap
}

// InstrumentProvider is the per-venue registry that resolves a neutral
// InstrumentID to its full Instrument. Adapters implement it; the runtime reads
// it. This is the seam that hides symbol-string vs asset-index divergence.
type InstrumentProvider interface {
	// Instrument returns the resolved instrument and true if known.
	Instrument(id InstrumentID) (*Instrument, bool)
	// All returns every instrument the provider knows about.
	All() []*Instrument
}
