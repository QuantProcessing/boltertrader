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

	// Resulting-position cap: current signed qty + this order's signed delta.
	if lim := e.limits.MaxPositionQty; !lim.IsZero() {
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
