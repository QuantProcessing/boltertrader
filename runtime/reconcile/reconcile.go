// Package reconcile brings the local Cache back into agreement with the venue's
// authoritative state. The local cache can diverge after a websocket gap or a
// process restart; the reconciler pulls REST snapshots and applies corrections.
// It is venue-neutral (works through the contract interfaces) so the same logic
// runs against any adapter.
package reconcile

import (
	"context"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
)

// Report summarizes what a reconciliation pass changed.
type Report struct {
	BalancesUpdated  int
	PositionsUpdated int
	PositionsCleared int // positions in cache not present in the snapshot

	OrdersUpdated  int // open orders in both cache and venue, refreshed to venue truth
	OrdersExternal int // open orders the venue reports that the cache had never seen
	OrdersCleared  int // cache-open orders the venue no longer lists (marked Canceled)
}

// Reconciler pulls authoritative snapshots and corrects the cache. The account
// client (balances/positions) and the execution client (open orders) are each
// optional; whichever is supplied is reconciled.
type Reconciler struct {
	account contract.AccountClient
	orders  contract.ExecutionClient
	cache   *cache.Cache
}

// New builds a Reconciler. account drives balance/position reconciliation and
// orders drives open-order reconciliation; either may be nil to skip that part.
func New(account contract.AccountClient, orders contract.ExecutionClient, c *cache.Cache) *Reconciler {
	return &Reconciler{account: account, orders: orders, cache: c}
}

// Run performs one reconciliation pass, overwriting the cache with venue truth:
// balances and positions (clearing cached positions the venue considers flat),
// then open orders (adopting orders the venue reports but the cache never saw,
// refreshing known ones, and clearing cache-open orders the venue no longer
// lists). Intended to be called at startup and after every reconnect.
func (r *Reconciler) Run(ctx context.Context) (Report, error) {
	var rep Report

	if r.account != nil {
		if err := r.reconcileAccount(ctx, &rep); err != nil {
			return rep, err
		}
	}
	if r.orders != nil {
		if err := r.reconcileOrders(ctx, &rep); err != nil {
			return rep, err
		}
	}
	return rep, nil
}

func (r *Reconciler) reconcileAccount(ctx context.Context, rep *Report) error {
	balances, err := r.account.Balances(ctx)
	if err != nil {
		return err
	}
	for _, b := range balances {
		r.cache.UpsertBalance(b)
		rep.BalancesUpdated++
	}

	positions, err := r.account.Positions(ctx)
	if err != nil {
		return err
	}

	// Build the set of instrument/side keys the venue reports, so we can clear
	// stale cached positions the venue considers flat.
	seen := make(map[positionKey]struct{}, len(positions))
	for _, p := range positions {
		r.cache.UpsertPosition(p)
		rep.PositionsUpdated++
		seen[positionKey{p.InstrumentID.String(), p.Side}] = struct{}{}
	}

	for _, cp := range r.cache.Positions() {
		k := positionKey{cp.InstrumentID.String(), cp.Side}
		if _, ok := seen[k]; !ok {
			// Venue no longer reports this position: force it flat. A
			// zero-quantity upsert removes it from the cache.
			r.cache.UpsertPosition(model.Position{
				InstrumentID: cp.InstrumentID,
				Side:         cp.Side,
			})
			rep.PositionsCleared++
		}
	}
	return nil
}

// reconcileOrders rebuilds open-order state from the venue's authoritative
// venue-wide snapshot (NautilusTrader's ExecutionMassStatus model): the venue's
// open set is adopted wholesale (catching orders placed out-of-band), and any
// order the cache still treats as open but the venue no longer lists is marked
// Canceled — it is no longer resting. Note: this resolves a missing order as
// Canceled without distinguishing a fill that arrived during the gap; recovering
// such fills is the job of trade reconciliation (a future pass).
func (r *Reconciler) reconcileOrders(ctx context.Context, rep *Report) error {
	reports, err := r.orders.OrderReports(ctx)
	if err != nil {
		return err
	}

	venueKeys := make(map[string]struct{}, len(reports))
	for _, o := range reports {
		k := orderKeyOf(o)
		venueKeys[k] = struct{}{}
		if _, known := r.cache.Order(k); known {
			rep.OrdersUpdated++
		} else {
			rep.OrdersExternal++
		}
		r.cache.UpsertOrder(o)
	}

	for _, co := range r.cache.OpenOrders() {
		if _, ok := venueKeys[orderKeyOf(co)]; ok {
			continue
		}
		co.Status = enums.StatusCanceled
		r.cache.UpsertOrder(co)
		rep.OrdersCleared++
	}
	return nil
}

// orderKeyOf mirrors the cache's keying: ClientID is preferred (stable across
// the submit/ack boundary), VenueOrderID is the fallback for venue-discovered
// orders.
func orderKeyOf(o model.Order) string {
	if o.Request.ClientID != "" {
		return o.Request.ClientID
	}
	return o.VenueOrderID
}

type positionKey struct {
	instrument string
	side       enums.PositionSide
}
