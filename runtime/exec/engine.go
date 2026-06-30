// Package exec provides the ExecutionEngine: the runtime's order-submission
// front door. It assigns a stable ClientID, records the intended order in the
// Cache as PendingNew, submits through the venue-neutral ExecutionClient, and
// records the acknowledged order. Subsequent lifecycle/fill events flow in via
// the bus, not here.
package exec

import (
	"context"
	"fmt"
	"sync/atomic"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
)

// Engine submits orders and keeps the Cache in step with what we sent.
type Engine struct {
	client contract.ExecutionClient
	cache  *cache.Cache
	clk    clock.Clock

	seq    uint64
	prefix string

	// risk, if set, gates every submission. provider resolves instrument
	// metadata for instrument-level checks. Both optional.
	risk     RiskChecker
	provider model.InstrumentProvider
}

// RiskChecker is the pre-trade gate ExecEngine consults before submitting. It is
// satisfied by runtime/risk.Engine. Decoupled via interface so exec doesn't
// import risk (and to keep it swappable/testable).
type RiskChecker interface {
	Check(req model.OrderRequest, inst *model.Instrument) error
}

// New builds an ExecutionEngine. idPrefix namespaces generated client ids
// (e.g. a strategy name) so concurrent strategies don't collide.
func New(client contract.ExecutionClient, c *cache.Cache, clk clock.Clock, idPrefix string) *Engine {
	if idPrefix == "" {
		idPrefix = "bt"
	}
	return &Engine{client: client, cache: c, clk: clk, prefix: idPrefix}
}

// WithRisk attaches a pre-trade risk gate and an instrument provider for
// instrument-level checks (provider may be nil).
func (e *Engine) WithRisk(r RiskChecker, provider model.InstrumentProvider) *Engine {
	e.risk = r
	e.provider = provider
	return e
}

// nextClientID generates a stable, unique idempotency key. It is monotonic
// within a process run and namespaced by prefix + submit time.
func (e *Engine) nextClientID() string {
	n := atomic.AddUint64(&e.seq, 1)
	return fmt.Sprintf("%s-%d-%d", e.prefix, e.clk.Now().UnixMilli(), n)
}

// Submit assigns a ClientID if absent, records the order as PendingNew, submits
// it, and records the acknowledged order. The acknowledged order is returned.
func (e *Engine) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	if req.ClientID == "" {
		req.ClientID = e.nextClientID()
	}

	// Pre-trade risk gate. A rejection never touches the venue or the cache.
	if e.risk != nil {
		var inst *model.Instrument
		if e.provider != nil {
			if got, ok := e.provider.Instrument(req.InstrumentID); ok {
				inst = got
			}
		}
		if err := e.risk.Check(req, inst); err != nil {
			return nil, err
		}
	}

	// Optimistically record intent so the order is visible even if the ack is
	// slow or the process restarts mid-flight.
	now := e.clk.Now()
	e.cache.UpsertOrder(model.Order{
		Request:   req,
		Status:    enums.StatusPendingNew,
		CreatedAt: now,
		UpdatedAt: now,
	})

	order, err := e.client.Submit(ctx, req)
	if err != nil {
		// Mark the intent rejected so the cache doesn't hold a phantom pending.
		e.cache.UpsertOrder(model.Order{
			Request:      req,
			Status:       enums.StatusRejected,
			CreatedAt:    now,
			UpdatedAt:    e.clk.Now(),
			RejectReason: err.Error(),
		})
		return nil, err
	}
	e.cache.UpsertOrder(*order)
	return order, nil
}

// Cancel cancels a known order by client id, resolving its venue id from the
// cache.
func (e *Engine) Cancel(ctx context.Context, clientID string) error {
	o, ok := e.cache.Order(clientID)
	if !ok {
		return fmt.Errorf("exec: unknown order %q", clientID)
	}
	return e.client.Cancel(ctx, o.Request.InstrumentID, o.VenueOrderID)
}
