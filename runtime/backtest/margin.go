package backtest

import (
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

// This file models a single-currency CROSS-margin perp account: one shared
// margin pool backs every position. Equity is wallet balance plus the
// mark-to-market profit of all open positions; usable (free) margin is equity
// minus the initial margin locked by positions and resting orders. Isolated
// margin and multi-currency conversion are out of scope for this milestone.

// equityLocked is wallet balance plus unrealized PnL across all positions, all
// expressed in the single settlement currency. Caller holds v.mu.
func (v *Venue) equityLocked() decimal.Decimal {
	eq := decimal.Zero
	for _, bal := range v.wallet {
		eq = eq.Add(bal)
	}
	for _, p := range v.positions {
		eq = eq.Add(v.unrealizedPnL(p, v.lastPrice[p.id.String()]))
	}
	return eq
}

// usedInitMarginLocked is the initial margin locked by open positions (at mark)
// plus resting non-reduce-only orders (at their limit price). Reducing resting
// orders flagged ReduceOnly reserve nothing; an unflagged resting order is
// charged its full notional — a conservative simplification. Caller holds v.mu.
func (v *Venue) usedInitMarginLocked() decimal.Decimal {
	m := decimal.Zero
	for _, p := range v.positions {
		if p.qty.IsZero() {
			continue
		}
		mark := v.lastPrice[p.id.String()]
		notional := mark.Mul(p.qty.Abs()).Mul(v.multiplier(p.id))
		m = m.Add(notional.Div(v.leverage(p.id)))
	}
	for _, ro := range v.resting {
		if ro.req.ReduceOnly {
			continue
		}
		notional := ro.req.Price.Mul(ro.remaining).Mul(v.multiplier(ro.req.InstrumentID))
		m = m.Add(notional.Div(v.leverage(ro.req.InstrumentID)))
	}
	return m
}

// requiredInitMarginLocked is the initial margin an order would lock by
// INCREASING position exposure, valued at ref. A pure reduce (including a
// reduce-only order or one that closes toward flat) requires none; an order that
// flips past flat is charged only for the overshoot that opens the new side.
// Caller holds v.mu.
func (v *Venue) requiredInitMarginLocked(req model.OrderRequest, ref decimal.Decimal) decimal.Decimal {
	if req.ReduceOnly {
		return decimal.Zero
	}
	signed := req.Quantity
	if req.Side == enums.SideSell {
		signed = signed.Neg()
	}
	var cur decimal.Decimal
	if p := v.positions[posKey(req.InstrumentID, req.PositionSide)]; p != nil {
		cur = p.qty
	}
	newQty := cur.Add(signed)

	var opening decimal.Decimal
	switch {
	case cur.IsZero() || sameSign(cur, signed):
		opening = signed.Abs() // opening or scaling in
	case !newQty.IsZero() && !sameSign(newQty, cur):
		opening = newQty.Abs() // flipped past flat: only the overshoot opens
	default:
		opening = decimal.Zero // pure reduce / close
	}
	if opening.IsZero() {
		return decimal.Zero
	}
	notional := ref.Mul(opening).Mul(v.multiplier(req.InstrumentID))
	return notional.Div(v.leverage(req.InstrumentID))
}

func (v *Venue) reduceOnlyRejectLocked(req model.OrderRequest) (string, bool) {
	if !req.ReduceOnly {
		return "", false
	}
	signed := req.Quantity
	if req.Side == enums.SideSell {
		signed = signed.Neg()
	}
	var cur decimal.Decimal
	if p := v.positions[posKey(req.InstrumentID, req.PositionSide)]; p != nil {
		cur = p.qty
	}
	switch {
	case cur.IsZero():
		return "reduce-only order would open a position", true
	case sameSign(cur, signed):
		return "reduce-only order would increase position exposure", true
	case signed.Abs().GreaterThan(cur.Abs()):
		return "reduce-only order would flip position", true
	default:
		return "", false
	}
}

// marginRejectLocked reports whether an order must be rejected for invalid
// reduce-only semantics or insufficient free margin, with a human-readable reason.
// Margin insufficiency is ignored when margin gating is off. Caller holds v.mu.
func (v *Venue) marginRejectLocked(req model.OrderRequest, ref decimal.Decimal) (string, bool) {
	if reason, rejected := v.reduceOnlyRejectLocked(req); rejected {
		return reason, true
	}
	if !v.marginOn {
		return "", false
	}
	im := v.requiredInitMarginLocked(req, ref)
	if !im.IsPositive() {
		return "", false
	}
	avail := v.availableLocked(v.settleCcy(req.InstrumentID))
	if im.GreaterThan(avail) {
		return "insufficient margin: need " + im.String() + ", available " + avail.String(), true
	}
	return "", false
}
