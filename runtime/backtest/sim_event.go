package backtest

import (
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

// SimEvent is one timestamped input to the backtest venue. The Runner replays a
// time-sorted slice of these. Three kinds exist, built with Trade / Funding /
// Mark:
//
//   - Trade: a public trade print that updates the mark, matches resting orders,
//     and marks positions (the same path as Venue.Feed).
//   - Funding: a perpetual funding settlement — longs pay shorts when the rate
//     is positive, and vice versa.
//   - Mark: a mark-price update with no trade print; it re-marks positions and
//     can trigger liquidation between trades, but does not match orders.
type SimEvent interface {
	// Timestamp is when the event occurs (drives the SimulatedClock).
	Timestamp() time.Time
	// feed applies the event to the venue.
	feed(v *Venue)
}

// Trade wraps a trade tick as a SimEvent.
func Trade(t model.TradeTick) SimEvent { return simTrade{t} }

// Funding builds a funding-settlement event for an instrument at a rate.
func Funding(id model.InstrumentID, rate decimal.Decimal, at time.Time) SimEvent {
	return simFunding{id: id, rate: rate, t: at}
}

// Mark builds a mark-price update for an instrument.
func Mark(id model.InstrumentID, price decimal.Decimal, at time.Time) SimEvent {
	return simMark{id: id, px: price, t: at}
}

type simTrade struct{ t model.TradeTick }

func (e simTrade) Timestamp() time.Time { return e.t.Timestamp }
func (e simTrade) feed(v *Venue)        { v.Feed(e.t) }

type simFunding struct {
	id   model.InstrumentID
	rate decimal.Decimal
	t    time.Time
}

func (e simFunding) Timestamp() time.Time { return e.t }
func (e simFunding) feed(v *Venue)        { v.FeedFunding(e.id, e.rate, e.t) }

type simMark struct {
	id model.InstrumentID
	px decimal.Decimal
	t  time.Time
}

func (e simMark) Timestamp() time.Time { return e.t }
func (e simMark) feed(v *Venue)        { v.FeedMark(e.id, e.px, e.t) }

// advanceTo moves a SimulatedClock to t (no-op for other clocks or a zero time).
func (v *Venue) advanceTo(t time.Time) {
	if sc, ok := v.clk.(*clock.SimulatedClock); ok && !t.IsZero() {
		sc.AdvanceTo(t)
	}
}

// FeedFunding settles funding on the open positions of one instrument. The
// payment per position is signedNotional * rate at the current mark: a long
// (positive notional) pays when the rate is positive; a short receives. It can
// push the account underwater, so a liquidation check follows.
func (v *Venue) FeedFunding(id model.InstrumentID, rate decimal.Decimal, at time.Time) {
	v.advanceTo(at)

	v.mu.Lock()
	mark := v.lastPrice[id.String()]
	var acctEvs []contract.AccountEvent
	if !rate.IsZero() && mark.IsPositive() {
		mult := v.multiplier(id)
		paid := make(map[string]bool)
		for _, p := range v.positions {
			if p.id != id || p.qty.IsZero() {
				continue
			}
			payment := mark.Mul(p.qty).Mul(mult).Mul(rate) // signed: + means the holder pays
			ccy := v.settleCcy(id)
			v.wallet[ccy] = v.wallet[ccy].Sub(payment)
			paid[ccy] = true
		}
		for ccy := range paid {
			acctEvs = append(acctEvs, v.balanceEventLocked(ccy))
		}
	}
	liqExec, liqAcct, liq := v.liquidateIfNeededLocked()
	acctEvs = append(acctEvs, liqAcct...)
	v.mu.Unlock()

	for _, e := range liqExec {
		v.exec.events <- e
	}
	for _, a := range acctEvs {
		v.account.events <- a
	}
	if liq != nil && v.cfg.OnLiquidation != nil {
		v.cfg.OnLiquidation(*liq)
	}
}

// FeedMark updates an instrument's mark price without a trade print: it re-marks
// the instrument's positions and runs the liquidation check, but does not match
// resting orders (matching is trade-driven). Use it to drive liquidation between
// trades from a mark-price series.
func (v *Venue) FeedMark(id model.InstrumentID, price decimal.Decimal, at time.Time) {
	v.advanceTo(at)

	v.mu.Lock()
	v.lastPrice[id.String()] = price
	acctEvs := v.markPositionLocked(id)
	liqExec, liqAcct, liq := v.liquidateIfNeededLocked()
	acctEvs = append(acctEvs, liqAcct...)
	v.mu.Unlock()

	for _, e := range liqExec {
		v.exec.events <- e
	}
	for _, a := range acctEvs {
		v.account.events <- a
	}
	if liq != nil && v.cfg.OnLiquidation != nil {
		v.cfg.OnLiquidation(*liq)
	}
}
