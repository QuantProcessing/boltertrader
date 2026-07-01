package backtest

import (
	"sort"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

// simPosition is the matching engine's signed average-cost position for one
// instrument/side. Its realized-PnL math mirrors runtime/portfolio so the
// simulated venue and the runtime's own book agree on every closed trade (the
// parity self-check). Unlike the portfolio, it scales PnL by the instrument's
// contract multiplier, so the two coincide exactly for linear (multiplier-1)
// perps — the scope of this milestone.
type simPosition struct {
	id       model.InstrumentID
	side     enums.PositionSide
	qty      decimal.Decimal // signed: + long, - short
	entry    decimal.Decimal // average entry price
	realized decimal.Decimal // cumulative realized PnL (gross of fees)
}

func posKey(id model.InstrumentID, side enums.PositionSide) string {
	return id.String() + "|" + side.String()
}

func sameSign(a, b decimal.Decimal) bool {
	return (a.IsPositive() && b.IsPositive()) || (a.IsNegative() && b.IsNegative())
}

// settleCcy resolves the settlement currency PnL and fees accrue in: the
// instrument's Settle when registered, else the account's start currency.
func (v *Venue) settleCcy(id model.InstrumentID) string {
	if inst, ok := v.instruments[id.String()]; ok && inst.Settle != "" {
		return inst.Settle
	} else if ok && id.Kind == enums.KindSpot && inst.Quote != "" {
		return inst.Quote
	}
	return v.cfg.StartBalance.Currency
}

// unrealizedPnL marks a position to mark and returns its open profit, scaled by
// the contract multiplier.
func (v *Venue) unrealizedPnL(p *simPosition, mark decimal.Decimal) decimal.Decimal {
	if p == nil || p.qty.IsZero() || mark.IsZero() {
		return decimal.Zero
	}
	mult := v.multiplier(p.id)
	if p.qty.IsPositive() {
		return mark.Sub(p.entry).Mul(p.qty.Abs()).Mul(mult)
	}
	return p.entry.Sub(mark).Mul(p.qty.Abs()).Mul(mult)
}

// applyFillLocked folds a fill into the wallet and the position book, returning
// the balance change to emit. The position change is emitted separately by
// markPositionLocked so position events always carry the current mark. Caller
// holds v.mu.
func (v *Venue) applyFillLocked(f model.Fill, side enums.PositionSide) []contract.AccountEvent {
	k := posKey(f.InstrumentID, side)
	p := v.positions[k]
	if p == nil {
		p = &simPosition{id: f.InstrumentID, side: side}
		v.positions[k] = p
	}

	signed := f.Quantity
	if f.Side == enums.SideSell {
		signed = signed.Neg()
	}

	var realizedDelta decimal.Decimal
	switch {
	case p.qty.IsZero() || sameSign(p.qty, signed):
		// Opening or scaling in: weighted-average the entry price.
		newQty := p.qty.Add(signed)
		if !newQty.IsZero() {
			notional := p.entry.Mul(p.qty.Abs()).Add(f.Price.Mul(signed.Abs()))
			p.entry = notional.Div(newQty.Abs())
		}
		p.qty = newQty
	default:
		// Reducing / closing / flipping: realize PnL on the overlapping amount.
		closing := decimal.Min(p.qty.Abs(), signed.Abs())
		if p.qty.IsPositive() {
			realizedDelta = f.Price.Sub(p.entry).Mul(closing)
		} else {
			realizedDelta = p.entry.Sub(f.Price).Mul(closing)
		}
		realizedDelta = realizedDelta.Mul(v.multiplier(f.InstrumentID))
		p.realized = p.realized.Add(realizedDelta)

		newQty := p.qty.Add(signed)
		switch {
		case newQty.IsZero():
			p.entry = decimal.Zero
		case sameSign(newQty, p.qty):
			// Partial reduce: entry unchanged.
		default:
			// Flipped past flat: the remainder opens at the fill price.
			p.entry = f.Price
		}
		p.qty = newQty
	}

	// Realized PnL credits the wallet; fees always debit it. With no settlement
	// currency the venue is an unfunded matching/PnL harness — positions are
	// still tracked, but there is no wallet to move.
	ccy := f.FeeCurrency
	if ccy == "" {
		ccy = v.settleCcy(f.InstrumentID)
	}
	if ccy == "" {
		return nil
	}
	v.wallet[ccy] = v.wallet[ccy].Add(realizedDelta).Sub(f.Fee)

	return []contract.AccountEvent{v.balanceEventLocked(ccy)}
}

// applySpotFillLocked folds a spot cash fill into per-asset balances. Spot
// inventory is balance-sourced; it does not create derivative positions.
func (v *Venue) applySpotFillLocked(f model.Fill) []contract.AccountEvent {
	inst, ok := v.instruments[f.InstrumentID.String()]
	if !ok || inst.Base == "" || inst.Quote == "" {
		return nil
	}

	baseQty := f.Quantity.Mul(v.multiplier(f.InstrumentID))
	notional := f.Price.Mul(f.Quantity).Mul(v.multiplier(f.InstrumentID))
	touched := map[string]struct{}{}
	touch := func(ccy string) {
		if ccy == "" {
			return
		}
		v.cashCcy[ccy] = true
		touched[ccy] = struct{}{}
	}

	switch f.Side {
	case enums.SideBuy:
		v.wallet[inst.Quote] = v.wallet[inst.Quote].Sub(notional)
		v.wallet[inst.Base] = v.wallet[inst.Base].Add(baseQty)
		touch(inst.Quote)
		touch(inst.Base)
	case enums.SideSell:
		v.wallet[inst.Base] = v.wallet[inst.Base].Sub(baseQty)
		v.wallet[inst.Quote] = v.wallet[inst.Quote].Add(notional)
		touch(inst.Base)
		touch(inst.Quote)
	}

	feeCcy := f.FeeCurrency
	if feeCcy == "" {
		feeCcy = inst.Quote
	}
	if !f.Fee.IsZero() {
		v.wallet[feeCcy] = v.wallet[feeCcy].Sub(f.Fee)
		touch(feeCcy)
	}

	ccys := make([]string, 0, len(touched))
	for ccy := range touched {
		ccys = append(ccys, ccy)
	}
	sort.Strings(ccys)
	out := make([]contract.AccountEvent, 0, len(ccys))
	for _, ccy := range ccys {
		out = append(out, v.balanceEventLocked(ccy))
	}
	return out
}

func (v *Venue) cashRejectLocked(req model.OrderRequest, ref decimal.Decimal) (string, bool) {
	inst, ok := v.instruments[req.InstrumentID.String()]
	if !ok || inst.Base == "" || inst.Quote == "" {
		return "unknown spot instrument " + req.InstrumentID.String(), true
	}
	notional := ref.Mul(req.Quantity).Mul(v.multiplier(req.InstrumentID))
	switch req.Side {
	case enums.SideBuy:
		required := notional
		if ccy := v.settleCcy(req.InstrumentID); ccy == inst.Quote {
			required = required.Add(notional.Mul(v.feeRate(enums.LiqTaker)))
		}
		available := v.wallet[inst.Quote].Sub(v.cashLocks[inst.Quote])
		if required.GreaterThan(available) {
			return "insufficient cash: need " + required.String() + " " + inst.Quote + ", available " + available.String(), true
		}
	case enums.SideSell:
		required := req.Quantity.Mul(v.multiplier(req.InstrumentID))
		available := v.wallet[inst.Base].Sub(v.cashLocks[inst.Base])
		if required.GreaterThan(available) {
			return "insufficient inventory: need " + required.String() + " " + inst.Base + ", available " + available.String(), true
		}
	}
	return "", false
}

func (v *Venue) spotReservationLocked(req model.OrderRequest) (string, decimal.Decimal, bool) {
	inst, ok := v.instruments[req.InstrumentID.String()]
	if !ok || inst.Base == "" || inst.Quote == "" {
		return "", decimal.Zero, false
	}
	switch req.Side {
	case enums.SideBuy:
		return inst.Quote, req.Price.Mul(req.Quantity).Mul(v.multiplier(req.InstrumentID)), true
	case enums.SideSell:
		return inst.Base, req.Quantity.Mul(v.multiplier(req.InstrumentID)), true
	default:
		return "", decimal.Zero, false
	}
}

func (v *Venue) reserveSpotOrderLocked(req model.OrderRequest) []contract.AccountEvent {
	ccy, amount, ok := v.spotReservationLocked(req)
	if !ok || amount.IsZero() {
		return nil
	}
	v.cashCcy[ccy] = true
	v.cashLocks[ccy] = v.cashLocks[ccy].Add(amount)
	return []contract.AccountEvent{v.balanceEventLocked(ccy)}
}

func (v *Venue) releaseSpotOrderLocked(req model.OrderRequest) []contract.AccountEvent {
	ccy, amount, ok := v.spotReservationLocked(req)
	if !ok || amount.IsZero() {
		return nil
	}
	v.cashCcy[ccy] = true
	v.cashLocks[ccy] = v.cashLocks[ccy].Sub(amount)
	if v.cashLocks[ccy].IsNegative() {
		v.cashLocks[ccy] = decimal.Zero
	}
	return []contract.AccountEvent{v.balanceEventLocked(ccy)}
}

// markPositionLocked recomputes the open positions on an instrument against the
// current mark (last trade price) and returns their position events. A flattened
// leg is emitted with zero quantity so the cache evicts it, then dropped from the
// book. Caller holds v.mu.
func (v *Venue) markPositionLocked(id model.InstrumentID) []contract.AccountEvent {
	mark := v.lastPrice[id.String()]
	var legs []*simPosition
	for _, p := range v.positions {
		if p.id == id {
			legs = append(legs, p)
		}
	}
	sort.Slice(legs, func(i, j int) bool { return legs[i].side < legs[j].side })

	now := v.clk.Now()
	out := make([]contract.AccountEvent, 0, len(legs))
	for _, p := range legs {
		out = append(out, contract.PositionEvent{Position: model.Position{
			InstrumentID:  p.id,
			Side:          p.side,
			Quantity:      p.qty,
			EntryPrice:    p.entry,
			MarkPrice:     mark,
			UnrealizedPnL: v.unrealizedPnL(p, mark),
			Leverage:      v.leverage(p.id),
			UpdatedAt:     now,
		}})
		if p.qty.IsZero() {
			delete(v.positions, posKey(p.id, p.side))
			delete(v.leverages, p.id.String()) // forget leverage when flat
		}
	}
	return out
}

// balanceEventLocked builds a balance event for a currency at the current
// available margin. Caller holds v.mu.
func (v *Venue) balanceEventLocked(ccy string) contract.AccountEvent {
	return contract.BalanceEvent{Balance: v.balanceSnapshotLocked(ccy)}
}

// availableLocked returns the free balance for a currency. On a funded account
// it is cross-margin free margin (equity minus used initial margin, floored at
// zero); otherwise the raw wallet balance. Caller holds v.mu.
func (v *Venue) availableLocked(ccy string) decimal.Decimal {
	if v.cashCcy[ccy] {
		avail := v.wallet[ccy].Sub(v.cashLocks[ccy])
		if avail.IsNegative() {
			return decimal.Zero
		}
		return avail
	}
	if !v.marginOn {
		return v.wallet[ccy]
	}
	avail := v.equityLocked().Sub(v.usedInitMarginLocked())
	if avail.IsNegative() {
		return decimal.Zero
	}
	return avail
}

func (v *Venue) balanceSnapshotLocked(ccy string) model.AccountBalance {
	total := v.wallet[ccy]
	locked := decimal.Zero
	if v.cashCcy[ccy] {
		locked = v.cashLocks[ccy]
	}
	return model.AccountBalance{
		Currency:  ccy,
		Total:     total,
		Available: v.availableLocked(ccy),
		Locked:    locked,
		UpdatedAt: v.clk.Now(),
	}
}

// snapshotBalances returns every wallet currency, sorted, for the Balances RPC.
func (v *Venue) snapshotBalances() []model.AccountBalance {
	v.mu.Lock()
	defer v.mu.Unlock()
	ccys := make([]string, 0, len(v.wallet))
	for c := range v.wallet {
		ccys = append(ccys, c)
	}
	sort.Strings(ccys)
	out := make([]model.AccountBalance, 0, len(ccys))
	for _, c := range ccys {
		out = append(out, v.balanceSnapshotLocked(c))
	}
	return out
}

// snapshotPositions returns every open position, sorted by key, for the
// Positions RPC, each marked to the latest price.
func (v *Venue) snapshotPositions() []model.Position {
	v.mu.Lock()
	defer v.mu.Unlock()
	keys := make([]string, 0, len(v.positions))
	for k := range v.positions {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	now := v.clk.Now()
	out := make([]model.Position, 0, len(keys))
	for _, k := range keys {
		p := v.positions[k]
		if p.qty.IsZero() {
			continue
		}
		mark := v.lastPrice[p.id.String()]
		out = append(out, model.Position{
			InstrumentID:  p.id,
			Side:          p.side,
			Quantity:      p.qty,
			EntryPrice:    p.entry,
			MarkPrice:     mark,
			UnrealizedPnL: v.unrealizedPnL(p, mark),
			Leverage:      v.leverage(p.id),
			UpdatedAt:     now,
		})
	}
	return out
}

// leverage returns the configured leverage for an instrument: a value set via
// SetLeverage, else the configured default, else 1 (margin math is fleshed out
// in A3; here it only annotates position events).
func (v *Venue) leverage(id model.InstrumentID) decimal.Decimal {
	if lev, ok := v.leverages[id.String()]; ok && lev.IsPositive() {
		return lev
	}
	if v.cfg.DefaultLeverage.IsPositive() {
		return v.cfg.DefaultLeverage
	}
	return one
}
