// Package observ provides lightweight, dependency-free observability hooks for
// the runtime: an Observer interface the TradingNode calls on lifecycle and
// trading events, and a Metrics snapshot aggregated from runtime state. Concrete
// sinks (zap, prometheus, etc.) implement Observer without the runtime taking a
// dependency on any of them.
package observ

import (
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

// Observer receives runtime events. All methods are called on the bus goroutine
// (or the backtest driver goroutine), so implementations must not block. Any
// method may be a no-op; embed Base to implement only what you need.
type Observer interface {
	// OnNodeStart is called once when the node starts.
	OnNodeStart()
	// OnNodeStop is called once when the node stops.
	OnNodeStop()
	// OnOrder is called for every order lifecycle update applied to the cache.
	OnOrder(o model.Order)
	// OnFill is called for every fill applied to cache and portfolio.
	OnFill(f model.Fill)
	// OnReject is called when an order is rejected (by the venue or risk gate).
	OnReject(clientID, reason string)
}

// Base is a no-op Observer for embedding.
type Base struct{}

func (Base) OnNodeStart()                  {}
func (Base) OnNodeStop()                   {}
func (Base) OnOrder(model.Order)           {}
func (Base) OnFill(model.Fill)             {}
func (Base) OnReject(string, string)       {}

// Metrics is a point-in-time snapshot of runtime trading state.
type Metrics struct {
	OpenOrders        int
	Positions         int
	RealizedPnL       decimal.Decimal
	RealizedPnLNet    decimal.Decimal
	Fees              decimal.Decimal
	OrdersSeen        int64
	FillsSeen         int64
	RejectsSeen       int64
}

// Counters accumulates event counts. Fields are accessed via sync/atomic by the
// runtime (the bus goroutine adds; Metrics readers load), so reads from other
// goroutines are race-free and eventually consistent.
type Counters struct {
	Orders  int64
	Fills   int64
	Rejects int64
}
