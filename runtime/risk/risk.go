// Package risk provides pre-trade risk checks — the safety net that sits in
// front of order submission. The engine is venue-neutral and reads only the
// Cache + Instrument metadata, so it behaves consistently across adapters.
package risk

import (
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/accounting"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/shopspring/decimal"
)

// ErrRiskRejected is the sentinel wrapped by every risk rejection so callers can
// errors.Is against it.
var ErrRiskRejected = errors.New("risk: order rejected")

// Limits configures the pre-trade checks. A zero value disables that check.
type Limits struct {
	// MaxOrderQty caps the quantity of any single order.
	MaxOrderQty decimal.Decimal
	// MaxOrderNotional caps price*qty of any single order (uses order price; for
	// market orders the caller should pass a reference price).
	MaxOrderNotional decimal.Decimal
	// MaxPositionQty caps the absolute resulting net position quantity per
	// instrument/side after the order would fully fill.
	MaxPositionQty decimal.Decimal
}

// Engine performs pre-trade checks against configured limits and a kill switch.
type Engine struct {
	mu                  sync.RWMutex
	limits              Limits
	cache               *cache.Cache
	tripped             bool // kill switch
	seen                map[string]struct{}
	requireAccountState bool
	allowLegacyBalance  bool
	now                 func() time.Time
}

// New builds a risk Engine reading positions from c.
func New(limits Limits, c *cache.Cache) *Engine {
	return &Engine{limits: limits, cache: c, seen: make(map[string]struct{}), now: time.Now}
}

// RequireAccountState makes pre-trade checks fail closed when no fresh account
// state can be selected for the order's venue/product.
func (e *Engine) RequireAccountState() *Engine {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.requireAccountState = true
	return e
}

// AllowLegacyBalanceFallback explicitly enables the pre-account-state spot
// balance path. It exists for adapters/tests that have not migrated to
// AccountStateReporter yet; RequireAccountState still takes precedence.
func (e *Engine) AllowLegacyBalanceFallback() *Engine {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.allowLegacyBalance = true
	return e
}

// WithClock injects a clock for deterministic freshness tests.
func (e *Engine) WithClock(now func() time.Time) *Engine {
	if now == nil {
		return e
	}
	e.mu.Lock()
	defer e.mu.Unlock()
	e.now = now
	return e
}

// Trip activates the kill switch: all subsequent orders are rejected.
func (e *Engine) Trip() {
	e.mu.Lock()
	e.tripped = true
	e.mu.Unlock()
}

// Reset clears the kill switch.
func (e *Engine) Reset() {
	e.mu.Lock()
	e.tripped = false
	e.mu.Unlock()
}

// Tripped reports whether the kill switch is active.
func (e *Engine) Tripped() bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	return e.tripped
}

// Check validates an order request against the limits, kill switch, instrument
// minimums, and duplicate-client-id protection. It returns a wrapped
// ErrRiskRejected on failure. inst may be nil (instrument minimums skipped).
func (e *Engine) Check(req model.OrderRequest, inst *model.Instrument) error {
	e.mu.Lock()
	defer e.mu.Unlock()

	if e.tripped {
		return fmt.Errorf("%w: kill switch active", ErrRiskRejected)
	}

	if req.Quantity.IsZero() || req.Quantity.IsNegative() {
		return fmt.Errorf("%w: non-positive quantity %s", ErrRiskRejected, req.Quantity)
	}

	// Duplicate client id (idempotency guard) — only when one is provided.
	if req.ClientID != "" {
		if _, dup := e.seen[req.ClientID]; dup {
			return fmt.Errorf("%w: duplicate client id %q", ErrRiskRejected, req.ClientID)
		}
	}

	if lim := e.limits.MaxOrderQty; !lim.IsZero() && req.Quantity.GreaterThan(lim) {
		return fmt.Errorf("%w: order qty %s exceeds max %s", ErrRiskRejected, req.Quantity, lim)
	}

	if lim := e.limits.MaxOrderNotional; !lim.IsZero() && !req.Price.IsZero() {
		notional := req.Price.Mul(req.Quantity)
		if notional.GreaterThan(lim) {
			return fmt.Errorf("%w: order notional %s exceeds max %s", ErrRiskRejected, notional, lim)
		}
	}

	// Instrument minimums.
	if inst != nil {
		if !inst.MinQty.IsZero() && req.Quantity.LessThan(inst.MinQty) {
			return fmt.Errorf("%w: qty %s below instrument min %s", ErrRiskRejected, req.Quantity, inst.MinQty)
		}
		if !inst.MinNotional.IsZero() && !req.Price.IsZero() {
			if n := req.Price.Mul(req.Quantity); n.LessThan(inst.MinNotional) {
				return fmt.Errorf("%w: notional %s below instrument min %s", ErrRiskRejected, n, inst.MinNotional)
			}
		}
	}

	if req.InstrumentID.Kind == enums.KindSpot {
		if inst == nil {
			return fmt.Errorf("%w: spot instrument metadata required for cash risk check", ErrRiskRejected)
		}
		if err := e.checkSpotBalance(req, inst); err != nil {
			return err
		}
	} else if e.requireAccountState && !req.ReduceOnly {
		if err := e.checkMarginAccount(req, inst); err != nil {
			return err
		}
	}

	// Resulting-position cap: current signed qty + this order's signed delta.
	if lim := e.limits.MaxPositionQty; req.InstrumentID.Kind != enums.KindSpot && !lim.IsZero() {
		cur := decimal.Zero
		if p, ok := e.cache.Position(req.InstrumentID, req.PositionSide); ok {
			cur = p.Quantity
		}
		delta := req.Quantity
		if req.Side == enums.SideSell {
			delta = delta.Neg()
		}
		resulting := cur.Add(delta).Abs()
		if resulting.GreaterThan(lim) {
			return fmt.Errorf("%w: resulting position %s exceeds max %s", ErrRiskRejected, resulting, lim)
		}
	}

	// Record the client id only after all checks pass.
	if req.ClientID != "" {
		e.seen[req.ClientID] = struct{}{}
	}
	return nil
}

func (e *Engine) checkSpotBalance(req model.OrderRequest, inst *model.Instrument) error {
	if acct, ok := e.accountForVenue(req.InstrumentID.Venue); ok {
		return e.checkSpotAccountBalance(req, inst, acct)
	}
	if e.requireAccountState || !e.allowLegacyBalance {
		return fmt.Errorf("%w: no account state for venue %s", ErrRiskRejected, req.InstrumentID.Venue)
	}
	return e.checkLegacySpotBalance(req, inst)
}

func (e *Engine) checkLegacySpotBalance(req model.OrderRequest, inst *model.Instrument) error {
	mult := inst.ContractMultiplier
	if !mult.IsPositive() {
		mult = decimal.NewFromInt(1)
	}
	switch req.Side {
	case enums.SideBuy:
		if inst.Quote == "" {
			return fmt.Errorf("%w: spot buy requires quote currency metadata for cash risk check", ErrRiskRejected)
		}
		if req.Price.IsZero() {
			return fmt.Errorf("%w: spot buy requires a reference price for cash risk check", ErrRiskRejected)
		}
		required := req.Price.Mul(req.Quantity).Mul(mult)
		available := decimal.Zero
		if bal, ok := e.cache.Balance(inst.Quote); ok {
			available = bal.FreeOrAvailable()
		}
		if required.GreaterThan(available) {
			return fmt.Errorf("%w: insufficient %s cash: need %s, available %s", ErrRiskRejected, inst.Quote, required, available)
		}
	case enums.SideSell:
		if inst.Base == "" {
			return fmt.Errorf("%w: spot sell requires base currency metadata for cash risk check", ErrRiskRejected)
		}
		required := req.Quantity.Mul(mult)
		available := decimal.Zero
		if bal, ok := e.cache.Balance(inst.Base); ok {
			available = bal.FreeOrAvailable()
		}
		if required.GreaterThan(available) {
			return fmt.Errorf("%w: insufficient %s inventory: need %s, available %s", ErrRiskRejected, inst.Base, required, available)
		}
	}
	return nil
}

func (e *Engine) checkSpotAccountBalance(req model.OrderRequest, inst *model.Instrument, acct accounting.Account) error {
	if acct.Type() != model.AccountCash || !accountSupportsKind(acct, enums.KindSpot) {
		return fmt.Errorf("%w: unsupported account mode %s for spot order", ErrRiskRejected, acct.Type())
	}
	if err := e.ensureFreshAccount(acct); err != nil {
		return err
	}
	switch req.Side {
	case enums.SideBuy:
		if inst.Quote == "" {
			return fmt.Errorf("%w: spot buy requires quote currency metadata for cash risk check", ErrRiskRejected)
		}
		if req.Price.IsZero() {
			return fmt.Errorf("%w: spot buy requires a reference price for cash risk check", ErrRiskRejected)
		}
		required := orderNotional(req, inst)
		free, ok := acct.BalanceFree(inst.Quote)
		if !ok {
			return fmt.Errorf("%w: missing free balance for %s on account %s", ErrRiskRejected, inst.Quote, acct.ID())
		}
		if required.GreaterThan(free) {
			return fmt.Errorf("%w: insufficient %s cash: need %s, free %s", ErrRiskRejected, inst.Quote, required, free)
		}
	case enums.SideSell:
		if inst.Base == "" {
			return fmt.Errorf("%w: spot sell requires base currency metadata for cash risk check", ErrRiskRejected)
		}
		required := req.Quantity.Mul(contractMultiplier(inst))
		free, ok := acct.BalanceFree(inst.Base)
		if !ok {
			return fmt.Errorf("%w: missing free balance for %s on account %s", ErrRiskRejected, inst.Base, acct.ID())
		}
		if required.GreaterThan(free) {
			return fmt.Errorf("%w: insufficient %s inventory: need %s, free %s", ErrRiskRejected, inst.Base, required, free)
		}
	}
	return nil
}

func (e *Engine) checkMarginAccount(req model.OrderRequest, inst *model.Instrument) error {
	acct, ok := e.accountForVenue(req.InstrumentID.Venue)
	if !ok {
		return fmt.Errorf("%w: no account state for venue %s", ErrRiskRejected, req.InstrumentID.Venue)
	}
	if acct.Type() != model.AccountMargin || !accountSupportsKind(acct, req.InstrumentID.Kind) {
		return fmt.Errorf("%w: unsupported account mode %s for %s order", ErrRiskRejected, acct.Type(), req.InstrumentID.Kind)
	}
	if err := e.ensureFreshAccount(acct); err != nil {
		return err
	}
	if req.Price.IsZero() {
		return fmt.Errorf("%w: %s order requires a reference price for account risk check", ErrRiskRejected, req.InstrumentID.Kind)
	}
	ccy := marginCurrency(req.InstrumentID, inst)
	if ccy == "" {
		return fmt.Errorf("%w: missing margin currency metadata for %s", ErrRiskRejected, req.InstrumentID)
	}
	free, ok := acct.BalanceFree(ccy)
	if !ok {
		return fmt.Errorf("%w: missing free balance for %s on account %s", ErrRiskRejected, ccy, acct.ID())
	}
	required := orderNotional(req, inst)
	if required.GreaterThan(free) {
		return fmt.Errorf("%w: insufficient %s margin: need %s, free %s", ErrRiskRejected, ccy, required, free)
	}
	return nil
}

func (e *Engine) accountForVenue(venue string) (accounting.Account, bool) {
	if e.cache == nil {
		return nil, false
	}
	return e.cache.AccountForVenue(venue)
}

func (e *Engine) ensureFreshAccount(acct accounting.Account) error {
	now := time.Now
	if e.now != nil {
		now = e.now
	}
	if acct.IsFresh(now()) {
		return nil
	}
	f := acct.Freshness()
	return fmt.Errorf("%w: stale account state for %s age %s exceeds %s", ErrRiskRejected, acct.ID(), f.Age(now()), f.StaleAfter)
}

func accountSupportsKind(acct accounting.Account, kind enums.InstrumentKind) bool {
	scope := acct.LastEvent().ModeInfo.ProductScope
	if len(scope) == 0 {
		return false
	}
	for _, supported := range scope {
		if supported == kind {
			return true
		}
	}
	return false
}

func orderNotional(req model.OrderRequest, inst *model.Instrument) decimal.Decimal {
	return req.Price.Mul(req.Quantity).Mul(contractMultiplier(inst))
}

func contractMultiplier(inst *model.Instrument) decimal.Decimal {
	if inst != nil && inst.ContractMultiplier.IsPositive() {
		return inst.ContractMultiplier
	}
	return decimal.NewFromInt(1)
}

func marginCurrency(id model.InstrumentID, inst *model.Instrument) string {
	if inst != nil {
		if inst.Settle != "" {
			return inst.Settle
		}
		if inst.Quote != "" {
			return inst.Quote
		}
	}
	_, quote, ok := strings.Cut(id.Symbol, "-")
	if !ok {
		return ""
	}
	return strings.ToUpper(quote)
}
