// Package portfolio tracks realized PnL from fills and aggregates account-level
// metrics. It maintains an average-cost book per instrument/side so realized
// PnL is computed deterministically as positions are reduced, independent of
// any venue-reported value (which is used only for reconciliation).
package portfolio

import (
	"strings"
	"sync"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/accounting"
	"github.com/shopspring/decimal"
)

type lotKey struct {
	accountID  string
	instrument model.InstrumentID
	side       enums.PositionSide
}

// lot is the running average-cost state for one account/instrument/side.
type lot struct {
	qty                decimal.Decimal // signed: + long, - short
	avgPrice           decimal.Decimal
	realized           decimal.Decimal // cumulative realized PnL for this lot
	feesPaidByCurrency map[string]decimal.Decimal
}

// Portfolio accrues realized PnL and fees from fills. It is written from the bus
// goroutine and read (snapshots) under an RWMutex.
type Portfolio struct {
	mu             sync.RWMutex
	lots           map[lotKey]*lot
	realized       decimal.Decimal // total realized PnL across all instruments
	fees           decimal.Decimal // fees in the PnL currency only
	feesByCurrency map[string]decimal.Decimal
	accounts       AccountSource
}

// AccountSource is the account/position read model Portfolio uses for
// account-level views. runtime/cache implements this interface.
type AccountSource interface {
	Account(accountID string) (accounting.Account, bool)
	AccountForVenue(venue string) (accounting.Account, bool)
	Accounts() []accounting.Account
	Positions() []model.Position
}

// New returns an empty Portfolio.
func New() *Portfolio {
	return &Portfolio{
		lots:           make(map[lotKey]*lot),
		feesByCurrency: make(map[string]decimal.Decimal),
	}
}

// WithAccountSource binds an account/position source for account-level reads.
func (pf *Portfolio) WithAccountSource(source AccountSource) *Portfolio {
	pf.mu.Lock()
	defer pf.mu.Unlock()
	pf.accounts = source
	return pf
}

func lotKeyOf(accountID string, id model.InstrumentID, side enums.PositionSide) lotKey {
	return lotKey{accountID: accountID, instrument: id, side: side}
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

	k := lotKeyOf(f.AccountID, f.InstrumentID, posSide)
	l := pf.lots[k]
	if l == nil {
		l = &lot{feesPaidByCurrency: make(map[string]decimal.Decimal)}
		pf.lots[k] = l
	}

	signed := f.Quantity
	if f.Side == enums.SideSell {
		signed = signed.Neg()
	}
	fillPrice := f.Price
	if spotBuyFeeInBase(f) {
		// Spot buy fills report gross filled base; a base-asset fee reduces the
		// net base received. Sell fills are already the base quantity removed, so
		// do not subtract a reported base fee a second time.
		signed = signed.Sub(f.Fee)
		if !signed.IsZero() {
			fillPrice = f.Price.Mul(f.Quantity).Div(signed.Abs())
		}
	}

	pf.applyFee(l, f)

	switch {
	case l.qty.IsZero() || sameSign(l.qty, signed):
		// Opening or increasing: weighted-average the entry price.
		newQty := l.qty.Add(signed)
		if !newQty.IsZero() {
			notional := l.avgPrice.Mul(l.qty.Abs()).Add(fillPrice.Mul(signed.Abs()))
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
			pnl = fillPrice.Sub(l.avgPrice).Mul(closing)
		} else {
			pnl = l.avgPrice.Sub(fillPrice).Mul(closing)
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
			l.avgPrice = fillPrice
		}
		l.qty = newQty
	}
}

func spotBuyFeeInBase(f model.Fill) bool {
	if f.InstrumentID.Kind != enums.KindSpot || f.Side != enums.SideBuy || f.Fee.IsZero() || f.FeeCurrency == "" {
		return false
	}
	base, _, ok := strings.Cut(f.InstrumentID.Symbol, "-")
	return ok && strings.EqualFold(f.FeeCurrency, base)
}

func (pf *Portfolio) applyFee(l *lot, f model.Fill) {
	if f.Fee.IsZero() {
		return
	}
	ccy := feeCurrency(f)
	if ccy == "" {
		return
	}
	if l.feesPaidByCurrency == nil {
		l.feesPaidByCurrency = make(map[string]decimal.Decimal)
	}
	l.feesPaidByCurrency[ccy] = l.feesPaidByCurrency[ccy].Add(f.Fee)
	if pf.feesByCurrency == nil {
		pf.feesByCurrency = make(map[string]decimal.Decimal)
	}
	pf.feesByCurrency[ccy] = pf.feesByCurrency[ccy].Add(f.Fee)
	if ccy == pnlCurrency(f.InstrumentID) {
		pf.fees = pf.fees.Add(f.Fee)
	}
}

func feeCurrency(f model.Fill) string {
	if f.FeeCurrency != "" {
		return strings.ToUpper(f.FeeCurrency)
	}
	return pnlCurrency(f.InstrumentID)
}

func pnlCurrency(id model.InstrumentID) string {
	_, quote, ok := strings.Cut(id.Symbol, "-")
	if !ok {
		return ""
	}
	return strings.ToUpper(quote)
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

// Fees returns fees in the same currency as realized PnL. Use FeesByCurrency for
// the full multi-currency fee ledger.
func (pf *Portfolio) Fees() decimal.Decimal {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	return pf.fees
}

// FeesByCurrency returns a copy of all observed fees keyed by fee currency.
func (pf *Portfolio) FeesByCurrency() map[string]decimal.Decimal {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	out := make(map[string]decimal.Decimal, len(pf.feesByCurrency))
	for ccy, fee := range pf.feesByCurrency {
		out[ccy] = fee
	}
	return out
}

// RealizedPnLNetFees returns realized PnL minus same-currency fees.
func (pf *Portfolio) RealizedPnLNetFees() decimal.Decimal {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	return pf.realized.Sub(pf.fees)
}

// AvgPrice returns the current average entry price for an instrument/side.
func (pf *Portfolio) AvgPrice(id model.InstrumentID, side enums.PositionSide) decimal.Decimal {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	if l := pf.legacyLot(id, side); l != nil {
		return l.avgPrice
	}
	return decimal.Zero
}

func (pf *Portfolio) AvgPriceForAccount(accountID string, id model.InstrumentID, side enums.PositionSide) decimal.Decimal {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	if l := pf.lots[lotKeyOf(accountID, id, side)]; l != nil {
		return l.avgPrice
	}
	return decimal.Zero
}

// NetQty returns the current signed quantity for an instrument/side as tracked
// by the fill book.
func (pf *Portfolio) NetQty(id model.InstrumentID, side enums.PositionSide) decimal.Decimal {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	if l := pf.legacyLot(id, side); l != nil {
		return l.qty
	}
	return decimal.Zero
}

func (pf *Portfolio) NetQtyForAccount(accountID string, id model.InstrumentID, side enums.PositionSide) decimal.Decimal {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	if l := pf.lots[lotKeyOf(accountID, id, side)]; l != nil {
		return l.qty
	}
	return decimal.Zero
}

// UnrealizedPnL computes mark-to-market PnL for an instrument/side at markPrice.
func (pf *Portfolio) UnrealizedPnL(id model.InstrumentID, side enums.PositionSide, markPrice decimal.Decimal) decimal.Decimal {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	l := pf.legacyLot(id, side)
	return unrealizedForLot(l, markPrice)
}

func (pf *Portfolio) UnrealizedPnLForAccount(accountID string, id model.InstrumentID, side enums.PositionSide, markPrice decimal.Decimal) decimal.Decimal {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	return unrealizedForLot(pf.lots[lotKeyOf(accountID, id, side)], markPrice)
}

func (pf *Portfolio) RealizedPnLForAccount(accountID string) decimal.Decimal {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	var realized decimal.Decimal
	for key, l := range pf.lots {
		if key.accountID == accountID {
			realized = realized.Add(l.realized)
		}
	}
	return realized
}

func (pf *Portfolio) FeesByCurrencyForAccount(accountID string) map[string]decimal.Decimal {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	out := make(map[string]decimal.Decimal)
	for key, l := range pf.lots {
		if key.accountID != accountID {
			continue
		}
		for ccy, fee := range l.feesPaidByCurrency {
			out[ccy] = out[ccy].Add(fee)
		}
	}
	return out
}

func (pf *Portfolio) RealizedPnLNetFeesForAccount(accountID string) decimal.Decimal {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	var realized, fees decimal.Decimal
	for key, l := range pf.lots {
		if key.accountID != accountID {
			continue
		}
		realized = realized.Add(l.realized)
		for ccy, fee := range l.feesPaidByCurrency {
			if ccy == pnlCurrency(key.instrument) {
				fees = fees.Add(fee)
			}
		}
	}
	return realized.Sub(fees)
}

func (pf *Portfolio) legacyLot(id model.InstrumentID, side enums.PositionSide) *lot {
	if l := pf.lots[lotKeyOf("", id, side)]; l != nil {
		return l
	}
	var out *lot
	found := false
	for key, l := range pf.lots {
		if key.instrument != id || key.side != side {
			continue
		}
		if found {
			return nil
		}
		out = l
		found = true
	}
	return out
}

func unrealizedForLot(l *lot, markPrice decimal.Decimal) decimal.Decimal {
	if l == nil || l.qty.IsZero() {
		return decimal.Zero
	}
	if l.qty.IsPositive() {
		return markPrice.Sub(l.avgPrice).Mul(l.qty.Abs())
	}
	return l.avgPrice.Sub(markPrice).Mul(l.qty.Abs())
}

// Account returns an account snapshot from the bound account source.
func (pf *Portfolio) Account(accountID string) (accounting.Account, bool) {
	source := pf.accountSource()
	if source == nil {
		return nil, false
	}
	return source.Account(accountID)
}

// AccountForVenue returns the unambiguous account for a venue.
func (pf *Portfolio) AccountForVenue(venue string) (accounting.Account, bool) {
	source := pf.accountSource()
	if source == nil {
		return nil, false
	}
	return source.AccountForVenue(venue)
}

// AccountSummary returns the venue-reported account summary for an account.
func (pf *Portfolio) AccountSummary(accountID string) (*model.AccountSummary, bool) {
	acct, ok := pf.Account(accountID)
	if !ok {
		return nil, false
	}
	return acct.Summary(), true
}

// AccountSummaryForVenue returns the venue-reported account summary for an
// unambiguous venue account.
func (pf *Portfolio) AccountSummaryForVenue(venue string) (*model.AccountSummary, bool) {
	acct, ok := pf.AccountForVenue(venue)
	if !ok {
		return nil, false
	}
	return acct.Summary(), true
}

// Accounts returns account snapshots from the bound account source.
func (pf *Portfolio) Accounts() []accounting.Account {
	source := pf.accountSource()
	if source == nil {
		return nil
	}
	return source.Accounts()
}

// Equity returns per-currency account equity. It starts from reported total
// balances and adds venue-matching unrealized PnL where the account scope owns
// that instrument kind.
func (pf *Portfolio) Equity(accountID string) (map[string]decimal.Decimal, bool) {
	source := pf.accountSource()
	if source == nil {
		return nil, false
	}
	acct, ok := source.Account(accountID)
	if !ok {
		return nil, false
	}
	out := make(map[string]decimal.Decimal)
	for _, bal := range acct.Balances() {
		out[bal.Currency] = out[bal.Currency].Add(bal.Total)
	}
	for _, pos := range source.Positions() {
		if !accountOwnsPosition(acct, pos) || pos.UnrealizedPnL.IsZero() {
			continue
		}
		if ccy := pnlCurrency(pos.InstrumentID); ccy != "" {
			out[ccy] = out[ccy].Add(pos.UnrealizedPnL)
		}
	}
	return out, true
}

// MarginInitial aggregates initial margin by currency for an account.
func (pf *Portfolio) MarginInitial(accountID string) (map[string]decimal.Decimal, bool) {
	return pf.marginByCurrency(accountID, true)
}

// MarginMaintenance aggregates maintenance margin by currency for an account.
func (pf *Portfolio) MarginMaintenance(accountID string) (map[string]decimal.Decimal, bool) {
	return pf.marginByCurrency(accountID, false)
}

// NetExposure returns signed mark exposure by instrument for positions owned by
// the account. MarkPrice is preferred, EntryPrice is the deterministic fallback.
func (pf *Portfolio) NetExposure(accountID string) (map[model.InstrumentID]decimal.Decimal, bool) {
	source := pf.accountSource()
	if source == nil {
		return nil, false
	}
	acct, ok := source.Account(accountID)
	if !ok {
		return nil, false
	}
	out := make(map[model.InstrumentID]decimal.Decimal)
	for _, pos := range source.Positions() {
		if !accountOwnsPosition(acct, pos) {
			continue
		}
		price := pos.MarkPrice
		if price.IsZero() {
			price = pos.EntryPrice
		}
		exposure := pos.Quantity
		if !price.IsZero() {
			exposure = exposure.Mul(price)
		}
		out[pos.InstrumentID] = out[pos.InstrumentID].Add(exposure)
	}
	return out, true
}

func (pf *Portfolio) marginByCurrency(accountID string, initial bool) (map[string]decimal.Decimal, bool) {
	source := pf.accountSource()
	if source == nil {
		return nil, false
	}
	acct, ok := source.Account(accountID)
	if !ok {
		return nil, false
	}
	out := make(map[string]decimal.Decimal)
	for _, margin := range acct.Margins() {
		value := margin.Maintenance
		if initial {
			value = margin.Initial
		}
		out[margin.Currency] = out[margin.Currency].Add(value)
	}
	return out, true
}

func (pf *Portfolio) accountSource() AccountSource {
	pf.mu.RLock()
	defer pf.mu.RUnlock()
	return pf.accounts
}

func accountOwnsPosition(acct accounting.Account, pos model.Position) bool {
	if acct == nil || acct.Venue() != pos.InstrumentID.Venue {
		return false
	}
	return pos.AccountID == acct.ID()
}
