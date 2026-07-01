// Package risk provides pre-trade risk checks — the safety net that sits in
// front of order submission. The engine is venue-neutral and reads only the
// Cache + Instrument metadata, so it behaves identically in backtest and live.
package risk

import (
	"errors"
	"fmt"
	"sync"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
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
	mu      sync.RWMutex
	limits  Limits
	cache   *cache.Cache
	tripped bool // kill switch
	seen    map[string]struct{}
}

// New builds a risk Engine reading positions from c.
func New(limits Limits, c *cache.Cache) *Engine {
	return &Engine{limits: limits, cache: c, seen: make(map[string]struct{})}
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
			available = bal.Available
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
			available = bal.Available
		}
		if required.GreaterThan(available) {
			return fmt.Errorf("%w: insufficient %s inventory: need %s, available %s", ErrRiskRejected, inst.Base, required, available)
		}
	}
	return nil
}
