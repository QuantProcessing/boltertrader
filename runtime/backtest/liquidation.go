package backtest

import (
	"sort"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

// Liquidation describes a forced close of the whole account triggered when
// cross-margin equity fell to or below the maintenance-margin requirement. It is
// delivered via Config.OnLiquidation. The Closed fills also flow through the
// normal execution/account streams, so the cache and portfolio stay consistent.
type Liquidation struct {
	Time         time.Time
	Currency     string
	EquityBefore decimal.Decimal // equity at the moment liquidation tripped
	MaintMargin  decimal.Decimal // the maintenance requirement that was breached
	WalletAfter  decimal.Decimal // settlement balance after closes (floored at 0)
	Closed       []model.Fill    // the forced closing fills
}

// mmr resolves the maintenance-margin rate for an instrument: a per-instrument
// override when present, else the global rate. Caller holds v.mu.
func (v *Venue) mmr(id model.InstrumentID) decimal.Decimal {
	if v.cfg.MaintMarginRates != nil {
		if r, ok := v.cfg.MaintMarginRates[id.String()]; ok {
			return r
		}
	}
	return v.cfg.MaintMarginRate
}

// maintMarginLocked is the summed maintenance margin across open positions at
// mark. Caller holds v.mu.
func (v *Venue) maintMarginLocked() decimal.Decimal {
	mm := decimal.Zero
	for _, p := range v.positions {
		if p.qty.IsZero() {
			continue
		}
		mark := v.lastPrice[p.id.String()]
		notional := mark.Mul(p.qty.Abs()).Mul(v.multiplier(p.id))
		mm = mm.Add(notional.Mul(v.mmr(p.id)))
	}
	return mm
}

// liquidateIfNeededLocked force-closes every open position at its mark when the
// account is underwater (equity <= maintenance margin). It returns the exec and
// account events to emit after unlocking and, on a liquidation, a *Liquidation
// for the OnLiquidation callback (nil otherwise). Liquidation is disabled unless
// a positive maintenance-margin rate is configured. Caller holds v.mu.
func (v *Venue) liquidateIfNeededLocked() ([]contract.ExecEvent, []contract.AccountEvent, *Liquidation) {
	if !v.marginOn {
		return nil, nil, nil
	}
	mm := v.maintMarginLocked()
	if !mm.IsPositive() {
		return nil, nil, nil // no maintenance requirement -> liquidation off
	}
	eq := v.equityLocked()
	if eq.GreaterThan(mm) {
		return nil, nil, nil // healthy
	}

	// Snapshot the open legs in a deterministic order before mutating the book.
	keys := make([]string, 0, len(v.positions))
	for k := range v.positions {
		if !v.positions[k].qty.IsZero() {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys)

	now := v.clk.Now()
	var execEvs []contract.ExecEvent
	var acctEvs []contract.AccountEvent
	var closed []model.Fill
	instruments := make([]model.InstrumentID, 0, len(keys))
	seen := make(map[string]bool, len(keys))

	for _, ro := range v.resting {
		execEvs = append(execEvs, contract.OrderEvent{Order: model.Order{
			Request:      ro.req,
			VenueOrderID: ro.venueID,
			Status:       enums.StatusCanceled,
			UpdatedAt:    now,
		}})
	}
	v.resting = nil

	for _, k := range keys {
		p := v.positions[k]
		mark := v.lastPrice[p.id.String()]
		side := enums.SideSell
		if p.qty.IsNegative() {
			side = enums.SideBuy
		}
		qty := p.qty.Abs()
		venueID := v.nextVenueID()
		req := model.OrderRequest{
			InstrumentID: p.id,
			Side:         side,
			Type:         enums.TypeMarket,
			Quantity:     qty,
			PositionSide: p.side,
			ReduceOnly:   true,
		}
		order := model.Order{
			Request:      req,
			VenueOrderID: venueID,
			Status:       enums.StatusFilled,
			FilledQty:    qty,
			AvgFillPrice: mark,
			CreatedAt:    now,
			UpdatedAt:    now,
		}
		fill := v.fillEvent(req, venueID, mark, qty, enums.LiqTaker)
		execEvs = append(execEvs, contract.OrderEvent{Order: order}, fill)
		acctEvs = append(acctEvs, v.applyFillLocked(fill.Fill, p.side)...)
		closed = append(closed, fill.Fill)
		if !seen[p.id.String()] {
			seen[p.id.String()] = true
			instruments = append(instruments, p.id)
		}
	}

	// Emit flat position events for every liquidated instrument.
	for _, id := range instruments {
		acctEvs = append(acctEvs, v.markPositionLocked(id)...)
	}

	// Floor the wallet at zero: a bankruptcy loss beyond the balance is absorbed
	// by the (idealized) liquidation engine rather than going negative.
	ccy := v.cfg.StartBalance.Currency
	if v.wallet[ccy].IsNegative() {
		v.wallet[ccy] = decimal.Zero
		acctEvs = append(acctEvs, v.balanceEventLocked(ccy))
	}

	return execEvs, acctEvs, &Liquidation{
		Time:         now,
		Currency:     ccy,
		EquityBefore: eq,
		MaintMargin:  mm,
		WalletAfter:  v.wallet[ccy],
		Closed:       closed,
	}
}
