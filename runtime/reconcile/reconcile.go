// Package reconcile brings the local Cache back into agreement with the venue's
// authoritative state. The local cache can diverge after a websocket gap or a
// process restart; the reconciler pulls REST snapshots and applies corrections.
// It is venue-neutral (works through the contract interfaces) so the same logic
// runs against any adapter.
package reconcile

import (
	"context"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/latency"
	"github.com/QuantProcessing/boltertrader/runtime/orderstate"
)

// Report summarizes what a reconciliation pass changed.
type Report struct {
	AccountStatesApplied int
	BalancesUpdated      int
	PositionsUpdated     int
	PositionsCleared     int // positions in cache not present in the snapshot
	PositionOverwrites   int

	OrdersUpdated       int // open orders in both cache and venue, refreshed to venue truth
	OrdersExternal      int // open orders the venue reports that the cache had never seen
	OrdersClosedUnknown int // cache-open orders absent from venue open snapshot; close reason unproven
	OrdersCleared       int // deprecated: retained for older callers; ambiguous closes are not marked Canceled
	OrdersMaterialized  int

	FillsApplied   int
	FillsDuplicate int
	FillsInferred  int

	Partial          bool
	CursorsCommitted int
	Warnings         []model.ReportWarning
	Findings         []Finding
}

// Reconciler pulls authoritative snapshots and corrects the cache. The account
// client (balances/positions) and the execution client (open orders) are each
// optional; whichever is supplied is reconciled.
type Reconciler struct {
	account   contract.AccountClient
	orders    contract.ExecutionClient
	cache     *cache.Cache
	latency   latency.Recorder
	state     StateStore
	fills     map[string]struct{}
	accountID string
	resolver  interface {
		ResolveInFlight(clientID, venueOrderID string, at time.Time)
		ResolveFillInFlight(fill model.Fill, at time.Time) (model.Fill, bool)
	}
}

// New builds a Reconciler. account drives balance/position reconciliation and
// orders drives open-order reconciliation; either may be nil to skip that part.
func New(account contract.AccountClient, orders contract.ExecutionClient, c *cache.Cache) *Reconciler {
	return &Reconciler{account: account, orders: orders, cache: c, state: noopStateStore{}, fills: make(map[string]struct{})}
}

func (r *Reconciler) WithLatencyRecorder(rec latency.Recorder) *Reconciler {
	r.latency = rec
	return r
}

func (r *Reconciler) WithStateStore(store StateStore) *Reconciler {
	if store != nil {
		r.state = store
	}
	return r
}

func (r *Reconciler) WithAccountID(accountID string) *Reconciler {
	r.accountID = accountID
	return r
}

func (r *Reconciler) WithInFlightResolver(resolver interface {
	ResolveInFlight(clientID, venueOrderID string, at time.Time)
	ResolveFillInFlight(fill model.Fill, at time.Time) (model.Fill, bool)
}) *Reconciler {
	r.resolver = resolver
	return r
}

// Run performs one reconciliation pass, overwriting the cache with venue truth:
// balances and positions (clearing cached positions the venue considers flat),
// then open orders (adopting orders the venue reports but the cache never saw,
// refreshing known ones, and marking cache-open orders missing from the venue
// open snapshot as closed with unknown reason). Intended to be called at startup
// and after every reconnect.
func (r *Reconciler) Run(ctx context.Context) (Report, error) {
	cmd := latency.CommandLatency{Command: string(latency.ChainReconciliation), StartedAt: time.Now()}
	defer func() {
		cmd.Finish(time.Now())
		if r.latency != nil {
			r.latency.RecordCommandLatency(cmd)
		}
	}()
	var rep Report

	if r.account != nil {
		if err := r.reconcileAccount(ctx, &rep); err != nil {
			cmd.Err = err.Error()
			return rep, err
		}
	}
	if r.orders != nil {
		if err := r.reconcileOrders(ctx, &rep); err != nil {
			cmd.Err = err.Error()
			return rep, err
		}
	}
	return rep, nil
}

func (r *Reconciler) reconcileAccount(ctx context.Context, rep *Report) error {
	caps := r.account.Capabilities()
	var accountStateID string
	var accountStateAppliedAt time.Time
	if caps.Reports.AccountStateSnapshots {
		reporter, ok := r.account.(contract.AccountStateReporter)
		if !ok {
			return contract.ErrNotSupported
		}
		state, err := reporter.AccountState(ctx)
		if err != nil {
			return err
		}
		appliedAt := time.Now()
		if err := r.cache.ApplyAccountStateAt(state, appliedAt); err != nil {
			return err
		}
		accountStateID = state.AccountID
		accountStateAppliedAt = appliedAt
		rep.AccountStatesApplied++
		rep.BalancesUpdated += len(state.Balances)
	} else {
		balances, err := r.account.Balances(ctx)
		if err != nil {
			return err
		}
		for _, b := range balances {
			r.cache.UpsertBalance(b)
			rep.BalancesUpdated++
		}
	}

	positions, err := r.account.Positions(ctx)
	if err != nil {
		return err
	}

	// Build the set of instrument/side keys the venue reports, so we can clear
	// stale cached positions the venue considers flat.
	seen := make(map[positionKey]struct{}, len(positions))
	for _, p := range positions {
		if cp, ok := r.cache.Position(p.InstrumentID, p.Side); ok && !cp.Quantity.Equal(p.Quantity) {
			rep.PositionOverwrites++
		}
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
	if accountStateID != "" {
		r.cache.MarkAccountReconciled(accountStateID, accountStateAppliedAt)
	}
	return nil
}

// reconcileOrders rebuilds open-order state from the venue's authoritative mass
// status report: the venue's open set is adopted wholesale (catching orders
// placed out-of-band), and any order the cache still treats as open but the
// venue no longer lists is closed locally with unknown reason. A missing order
// is no longer resting, but this bounded pass must not claim a cancel or a fill
// until trade reconciliation can prove that terminal reason.
func (r *Reconciler) reconcileOrders(ctx context.Context, rep *Report) error {
	mass, err := r.orders.GenerateExecutionMassStatus(ctx, model.MassStatusQuery{AccountID: r.accountID})
	if err != nil {
		return err
	}
	if mass.AccountID == "" {
		mass.AccountID = r.accountID
	}
	if err := mass.Validate(); err != nil {
		return err
	}
	stableEventAt := mass.GeneratedAt
	if stableEventAt.IsZero() {
		stableEventAt = time.Now()
	}
	scope := ScopeKey{Venue: mass.Venue, AccountID: mass.AccountID}
	pass := PassHeader{
		PassID:        PassID(scope, stableEventAt),
		Scope:         scope,
		StartedAt:     time.Now(),
		StableEventAt: stableEventAt,
		QueryFrom:     stableEventAt.Add(-mass.Lookback),
		QueryTo:       stableEventAt,
	}
	openFindings, err := r.state.LoadOpenFindings(ctx, scope)
	if err != nil {
		return err
	}
	rep.Findings = append(rep.Findings, openFindings...)
	if err := r.state.BeginPass(ctx, pass); err != nil {
		return err
	}
	rep.Partial = rep.Partial || mass.Partial
	rep.Warnings = append(rep.Warnings, mass.Warnings...)

	venueKeys := make(map[string]struct{}, len(mass.OrderReports))
	for _, report := range mass.OrderReports {
		o := report.Order
		k := orderKeyOf(o)
		venueKeys[k] = struct{}{}
		if o.VenueOrderID != "" {
			venueKeys[o.VenueOrderID] = struct{}{}
		}
		if _, known := r.cache.Order(k); known {
			rep.OrdersUpdated++
		} else {
			rep.OrdersExternal++
		}
		r.cache.UpsertOrder(o)
		if r.resolver != nil {
			r.resolver.ResolveInFlight(o.Request.ClientID, o.VenueOrderID, stableEventAt)
		}
	}

	if err := r.applyFillReports(ctx, pass, mass, rep); err != nil {
		return err
	}

	for _, co := range r.cache.OpenOrders() {
		if _, ok := venueKeys[orderKeyOf(co)]; ok {
			continue
		}
		if co.VenueOrderID != "" {
			if _, ok := venueKeys[co.VenueOrderID]; ok {
				continue
			}
		}
		if mass.Partial {
			finding := r.finding(pass, StreamOrders, FindingWarning, "PARTIAL_ORDER_REPORT", "partial mass-status report cannot prove missing open order terminal state", false)
			rep.Findings = append(rep.Findings, finding)
			if err := r.state.RecordFinding(ctx, finding); err != nil {
				return err
			}
			continue
		}
		co.Status = enums.StatusUnknown
		r.cache.UpsertOrder(co)
		rep.OrdersClosedUnknown++
	}

	cursor := Cursor{
		Scope:              scope,
		Stream:             StreamOrders,
		LastSuccessfulPass: pass.PassID,
		LastReportID:       mass.ReportID,
		LastVenueTime:      stableEventAt,
		LastLocalApplyTime: time.Now(),
		LookbackFloor:      pass.QueryFrom,
		Partial:            mass.Partial,
	}
	if err := r.state.CommitCursor(ctx, cursor); err != nil {
		return err
	}
	rep.CursorsCommitted++
	return nil
}

func (r *Reconciler) applyFillReports(ctx context.Context, pass PassHeader, mass *model.ExecutionMassStatus, rep *Report) error {
	for _, reports := range mass.FillReports {
		for _, report := range reports {
			accountID := report.AccountID
			if accountID == "" {
				accountID = mass.AccountID
			}
			fill := report.Fill
			if fill.ClientID == "" && r.resolver != nil {
				if resolvedFill, resolved := r.resolver.ResolveFillInFlight(fill, fillResolvedAt(fill, pass)); resolved {
					fill = resolvedFill
				}
			}
			if fill.TradeID == "" {
				fill.TradeID = SyntheticTradeID(accountID, fill, pass.StableEventAt)
				rep.FillsInferred++
			}
			fillKey := accountID + "\x00" + orderstate.FillKey(fill)
			if _, ok := r.fills[fillKey]; ok {
				rep.FillsDuplicate++
				continue
			}
			order, ok := r.orderForFill(fill)
			if !ok {
				if materialized, materializedOK := materializeOrderFromFill(fill, pass.StableEventAt); materializedOK {
					order = materialized
					r.cache.UpsertOrder(order)
					rep.OrdersMaterialized++
					ok = true
				}
			}
			if !ok {
				finding := r.finding(pass, StreamFills, FindingBlocking, "FILL_WITHOUT_ORDER", "fill report could not be matched or materialized", true)
				rep.Findings = append(rep.Findings, finding)
				if err := r.state.RecordFinding(ctx, finding); err != nil {
					return err
				}
				continue
			}
			r.applyFillToOrder(order, fill)
			if r.resolver != nil {
				resolvedAt := fill.Timestamp
				if resolvedAt.IsZero() {
					resolvedAt = pass.StableEventAt
				}
				r.resolver.ResolveInFlight(fill.ClientID, fill.VenueOrderID, resolvedAt)
			}
			r.fills[fillKey] = struct{}{}
			rep.FillsApplied++
		}
	}
	return nil
}

func fillResolvedAt(fill model.Fill, pass PassHeader) time.Time {
	if !fill.Timestamp.IsZero() {
		return fill.Timestamp
	}
	return pass.StableEventAt
}

func (r *Reconciler) orderForFill(fill model.Fill) (model.Order, bool) {
	if fill.ClientID != "" {
		if o, ok := r.cache.Order(fill.ClientID); ok {
			return o, true
		}
	}
	if fill.VenueOrderID != "" {
		if o, ok := r.cache.Order(fill.VenueOrderID); ok {
			return o, true
		}
		for _, o := range r.cache.Orders() {
			if o.VenueOrderID == fill.VenueOrderID {
				return o, true
			}
		}
	}
	return model.Order{}, false
}

func (r *Reconciler) applyFillToOrder(order model.Order, fill model.Fill) {
	r.cache.UpsertOrder(orderstate.ApplyFill(order, fill, time.Now()))
}

func materializeOrderFromFill(fill model.Fill, stableEventAt time.Time) (model.Order, bool) {
	if fill.InstrumentID.Symbol == "" || fill.VenueOrderID == "" || fill.Quantity.IsZero() {
		return model.Order{}, false
	}
	clientID := fill.ClientID
	if clientID == "" {
		clientID = "external-" + fill.VenueOrderID
	}
	if fill.ClientID == "" && fill.TradeID != "" {
		clientID += "-" + fill.TradeID
	}
	ts := fill.Timestamp
	if ts.IsZero() {
		ts = stableEventAt
	}
	return model.Order{
		Request: model.OrderRequest{
			InstrumentID: fill.InstrumentID,
			ClientID:     clientID,
			Side:         fill.Side,
			Type:         enums.TypeMarket,
			Quantity:     fill.Quantity,
			Price:        fill.Price,
			PositionSide: enums.PosNet,
		},
		VenueOrderID: fill.VenueOrderID,
		Status:       enums.StatusNew,
		CreatedAt:    ts,
		UpdatedAt:    ts,
	}, true
}

func (r *Reconciler) finding(pass PassHeader, stream ReportStream, severity FindingSeverity, code, message string, blocking bool) Finding {
	id := string(DeterministicID("finding", string(pass.PassID), string(stream), code, message))
	return Finding{
		ID:        id,
		PassID:    pass.PassID,
		Scope:     pass.Scope,
		Stream:    stream,
		Severity:  severity,
		Code:      code,
		Message:   message,
		Blocking:  blocking,
		CreatedAt: time.Now(),
	}
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
