// Package strategies holds example, ready-to-run strategies built on the
// runtime/strategy callback interface. They are venue-neutral: each acts only
// through the strategy Context, so the identical strategy runs in backtest and
// live.
package strategies

import (
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/strategy"
	"github.com/shopspring/decimal"
)

// PrintTrades is a trivial example strategy: it logs each bar it receives via
// the supplied logf and, optionally, places a single test order on the first
// bar. It demonstrates the minimal shape of a strategy.
type PrintTrades struct {
	strategy.Base

	Instrument model.InstrumentID
	// BuyOnceQty, if positive, submits a single limit buy of this size at the
	// first bar's close price. Zero disables trading (observe-only).
	BuyOnceQty decimal.Decimal
	// Logf receives human-readable progress lines; may be nil.
	Logf func(format string, args ...any)

	bought bool
}

func (s *PrintTrades) log(format string, args ...any) {
	if s.Logf != nil {
		s.Logf(format, args...)
	}
}

func (s *PrintTrades) OnStart(c *strategy.Context) {
	s.log("strategy started at %s", c.Clock.Now())
}

func (s *PrintTrades) OnBar(c *strategy.Context, bar model.Bar) {
	s.log("bar %s O=%s H=%s L=%s C=%s V=%s", bar.Interval, bar.Open, bar.High, bar.Low, bar.Close, bar.Volume)
	if !s.bought && s.BuyOnceQty.IsPositive() {
		s.bought = true
		if _, err := c.Buy(s.Instrument, s.BuyOnceQty, bar.Close); err != nil {
			s.log("buy rejected: %v", err)
		} else {
			s.log("submitted buy %s @ %s", s.BuyOnceQty, bar.Close)
		}
	}
}

func (s *PrintTrades) OnFill(c *strategy.Context, f model.Fill) {
	s.log("FILL %s %s @ %s fee %s | realizedPnL=%s", f.Side, f.Quantity, f.Price, f.Fee, c.Portfolio.RealizedPnLNetFees())
}

func (s *PrintTrades) OnStop(c *strategy.Context) {
	s.log("strategy stopped; realized PnL (net fees)=%s", c.Portfolio.RealizedPnLNetFees())
}
