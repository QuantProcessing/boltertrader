// Package portfolio tracks realized PnL from fills and aggregates account-level
// metrics. It maintains an average-cost book per instrument/side so realized
// PnL is computed deterministically as positions are reduced, independent of
// any venue-reported value (which is used only for reconciliation).
package portfolio

import (
	"sync"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

// lot is the running average-cost state for one instrument/side.
type lot struct {
	qty       decimal.Decimal // signed: + long, - short
	avgPrice  decimal.Decimal
	realized  decimal.Decimal // cumulative realized PnL for this lot
	feesPaid  decimal.Decimal
}

// Portfolio accrues realized PnL and fees from fills. It is written from the bus
// goroutine and read (snapshots) under an RWMutex.
type Portfolio struct {
	mu       sync.RWMutex
	lots     map[string]*lot // key: instrument|side
	realized decimal.Decimal // total realized PnL across all instruments
	fees     decimal.Decimal // total fees paid
}

// New returns an empty Portfolio.
func New() *Portfolio {
	return &Portfolio{lots: make(map[string]*lot)}
}

func lotKey(id model.InstrumentID, side enums.PositionSide) string {
	return id.String() + "|" + side.String()
}

// OnFill updates the average-cost book and accrues realized PnL when a fill
// reduces an existing position. fillSide is the side of the trade (buy/sell);
// posSide assigns it to the right hedge leg (PosNet for one-way accounts).
//
// Convention: a buy adds +qty, a sell adds -qty to the signed lot. When the
// fill is in the opposite direction of the current signed quantity, the
// overlapping amount realizes PnL at (fill price - avg cost).
func (pf *Portfolio) OnFill(f model.Fill, posSide enums.PositionSide) {
	pf.mu.Lock()
	defer pf.mu.Unlock()

	k := lotKey(f.InstrumentID, posSide)
	l := pf.lots[k]
	if l == nil {
		l = &lot{}
		pf.lots[k] = l
	}

	signed := f.Quantity
	if f.Side == enums.SideSell {
		signed = signed.Neg()
	}

	// Fees always reduce PnL.
	l.feesPaid = l.feesPaid.Add(f.Fee)
	pf.fees = pf.fees.Add(f.Fee)

	switch {
	case l.qty.IsZero() || sameSign(l.qty, signed):
		// Opening or increasing: weighted-average the entry price.
		newQty := l.qty.Add(signed)
		if !newQty.IsZero() {
			notional := l.avgPrice.Mul(l.qty.Abs()).Add(f.Price.Mul(signed.Abs()))
			l.avgPrice = notional.Div(newQty.Abs())
		}
		l.qty = newQty
	default:
		// Reducing / closing / flipping.
		closing := decimal.Min(l.qty.Abs(), signed.Abs())
		// PnL per unit: for a long (qty>0) being reduced by a sell,
		// profit = (fillPrice - avgPrice). For a short, profit =
		// (avgPrice - fillPrice). The sign of l.qty captures this.
		var pnl decimal.Decimal
		if l.qty.IsPositive() {
			pnl = f.Price.Sub(l.avgPrice).Mul(closing)
		} else {
			pnl = l.avgPrice.Sub(f.Price).Mul(closing)
		}
		l.realized = l.realized.Add(pnl)
		pf.realized = pf.realized.Add(pnl)

		newQty := l.qty.Add(signed)
		switch {
		case newQty.IsZero():
			l.avgPrice = decimal.Zero
		case sameSign(newQty, l.qty):
			// Partial reduce: avg price unchanged.
		default:
			// Flipped past flat: remaining opens at the fill price.
			l.avgPrice = f.Price
		}
		l.qty = newQty
	}
}

func sameSign(a, b decimal.Decimal) bool {
	return (a.IsPositive() && b.IsPositive()) || (a.IsNegative() && b.IsNegative())
}

// RealizedPnL returns cumulative realized PnL across all instruments (gross of
// fees; use RealizedPnLNetFees for net).
func (pf *Portfolio) RealizedPnL() decimal.Decimal {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	return pf.realized
}

// Fees returns total fees paid.
func (pf *Portfolio) Fees() decimal.Decimal {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	return pf.fees
}

// RealizedPnLNetFees returns realized PnL minus fees.
func (pf *Portfolio) RealizedPnLNetFees() decimal.Decimal {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	return pf.realized.Sub(pf.fees)
}

// AvgPrice returns the current average entry price for an instrument/side.
func (pf *Portfolio) AvgPrice(id model.InstrumentID, side enums.PositionSide) decimal.Decimal {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	if l := pf.lots[lotKey(id, side)]; l != nil {
		return l.avgPrice
	}
	return decimal.Zero
}

// NetQty returns the current signed quantity for an instrument/side as tracked
// by the fill book.
func (pf *Portfolio) NetQty(id model.InstrumentID, side enums.PositionSide) decimal.Decimal {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	if l := pf.lots[lotKey(id, side)]; l != nil {
		return l.qty
	}
	return decimal.Zero
}

// UnrealizedPnL computes mark-to-market PnL for an instrument/side at markPrice.
func (pf *Portfolio) UnrealizedPnL(id model.InstrumentID, side enums.PositionSide, markPrice decimal.Decimal) decimal.Decimal {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	l := pf.lots[lotKey(id, side)]
	if l == nil || l.qty.IsZero() {
		return decimal.Zero
	}
	if l.qty.IsPositive() {
		return markPrice.Sub(l.avgPrice).Mul(l.qty.Abs())
	}
	return l.avgPrice.Sub(markPrice).Mul(l.qty.Abs())
}
