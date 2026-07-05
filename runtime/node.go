// Package runtime wires the venue-neutral building blocks — the event bus,
// authoritative cache, portfolio, and execution engine — into a single
// TradingNode. The node depends only on core/contract + core/clock and consumes
// live-style client streams through one bus goroutine.
package runtime

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/accounting"
	"github.com/QuantProcessing/boltertrader/runtime/bus"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/data"
	"github.com/QuantProcessing/boltertrader/runtime/exec"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
	"github.com/QuantProcessing/boltertrader/runtime/latency"
	"github.com/QuantProcessing/boltertrader/runtime/lifecycle"
	"github.com/QuantProcessing/boltertrader/runtime/observ"
	"github.com/QuantProcessing/boltertrader/runtime/orderstate"
	"github.com/QuantProcessing/boltertrader/runtime/portfolio"
	"github.com/QuantProcessing/boltertrader/runtime/reconcile"
	"github.com/QuantProcessing/boltertrader/runtime/strategy"
)

// Clients bundles the venue-neutral clients a node drives. Any may be nil for a
// partial node (e.g. market-data only).
type Clients struct {
	Market    contract.MarketDataClient
	Execution contract.ExecutionClient
	Account   contract.AccountClient
}

// TradingNode hosts the runtime state machine. Cache, Portfolio, and Exec are
// exported for strategy/reporting access; all event-driven mutation happens on
// the single bus goroutine.
type TradingNode struct {
	Cache     *cache.Cache
	Portfolio *portfolio.Portfolio
	Exec      *exec.Engine

	clients   Clients
	clk       clock.Clock
	bus       *bus.Bus
	accountID string

	// onFill is an optional raw hook invoked (on the bus goroutine) after each
	// fill is applied to cache and portfolio. Used by tests and simple wiring.
	onFill func(model.Fill)

	// strat is the optional strategy and its prebuilt callback context.
	strat    strategy.Strategy
	stratCtx *strategy.Context

	// aggregators build bars from trades, keyed by InstrumentID.String().
	aggregators map[string]*data.BarAggregator

	// reconciler corrects the cache from venue REST snapshots; nil if no
	// account client.
	reconciler *reconcile.Reconciler

	// obs receives observability callbacks; nil if none registered.
	obs      observ.Observer
	counters observ.Counters
	latency  latency.Recorder
	life     *lifecycle.Machine
	fills    *exec.FillBuffer
}

// Option configures a TradingNode.
type Option func(*TradingNode)

// WithOnFill registers a raw callback fired after each fill is applied.
func WithOnFill(fn func(model.Fill)) Option {
	return func(n *TradingNode) { n.onFill = fn }
}

// WithStrategy registers a callback-style strategy. Its callbacks run on the
// bus goroutine with a Context wired to this node's cache, portfolio, clock,
// and execution engine.
func WithStrategy(s strategy.Strategy) Option {
	return func(n *TradingNode) { n.strat = s }
}

// WithBars registers a bar aggregator for an instrument: trades for it are
// folded into OHLCV bars of the given interval and delivered to the strategy's
// OnBar. label is the interval string carried on the bar (e.g. "1m").
func WithBars(id model.InstrumentID, interval time.Duration, label string) Option {
	return func(n *TradingNode) {
		n.aggregators[id.String()] = data.NewBarAggregator(id, interval, label)
	}
}

func WithLatencyRecorder(rec latency.Recorder) Option {
	return func(n *TradingNode) {
		if rec != nil {
			n.latency = rec
			if n.Exec != nil {
				n.Exec.WithLatencyRecorder(rec)
			}
			if n.reconciler != nil {
				n.reconciler.WithLatencyRecorder(rec)
			}
		}
	}
}

func WithJournal(store journal.Store) Option {
	return func(n *TradingNode) {
		if n.Exec != nil {
			n.Exec.WithJournal(store)
		}
		if n.reconciler != nil {
			n.reconciler.WithStateStore(reconcile.NewJournalStateStore(store))
		}
	}
}

func WithAccountID(accountID string) Option {
	return func(n *TradingNode) {
		if accountID == "" {
			return
		}
		n.accountID = accountID
		if n.Exec != nil {
			n.Exec.WithAccountID(accountID)
		}
		if n.reconciler != nil {
			n.reconciler.WithAccountID(accountID)
		}
	}
}

func WithAccountStaleAfter(staleAfter time.Duration) Option {
	return func(n *TradingNode) {
		if staleAfter <= 0 {
			return
		}
		if n.Cache != nil {
			n.Cache.SetAccountStaleAfter(staleAfter)
		}
	}
}

// NewNode builds a TradingNode over the given clients and clock.
func NewNode(clients Clients, clk clock.Clock, idPrefix string, opts ...Option) *TradingNode {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	accountID := idPrefix
	if accountID == "" {
		accountID = "bt"
	}
	c := cache.New()
	pf := portfolio.New().WithAccountSource(c)

	var marketCh <-chan contract.MarketEnvelope
	var execCh <-chan contract.ExecEnvelope
	var accountCh <-chan contract.AccountEnvelope
	if clients.Market != nil {
		marketCh = clients.Market.Events()
	}
	if clients.Execution != nil {
		execCh = clients.Execution.Events()
	}
	if clients.Account != nil {
		accountCh = clients.Account.Events()
	}

	n := &TradingNode{
		Cache:       c,
		Portfolio:   pf,
		clients:     clients,
		clk:         clk,
		accountID:   accountID,
		bus:         bus.New(marketCh, execCh, accountCh),
		aggregators: make(map[string]*data.BarAggregator),
		latency:     latency.NewRecorder(4096),
		life:        lifecycle.New(),
		fills:       exec.NewFillBuffer(),
	}
	if clients.Execution != nil {
		n.Exec = exec.New(clients.Execution, c, clk, idPrefix)
		n.Exec.WithAccountID(n.accountID)
		n.Exec.WithLatencyRecorder(n.latency)
		n.Exec.WithCommandGate(n.life)
		n.Exec.WithRecoverabilityHandler(func(err error) {
			n.Halt("exec journal recoverability breach: " + err.Error())
		})
	}
	// Reconcile whatever authoritative sources exist: balances/positions from the
	// account client, open orders from the execution client. Either may be nil.
	if clients.Account != nil || clients.Execution != nil {
		n.reconciler = reconcile.New(clients.Account, clients.Execution, c)
		n.reconciler.WithAccountID(n.accountID)
		n.reconciler.WithLatencyRecorder(n.latency)
		if n.Exec != nil {
			n.reconciler.WithInFlightResolver(n.Exec)
		}
	}
	for _, o := range opts {
		o(n)
	}
	return n
}

// WithRisk attaches a pre-trade risk gate to the execution engine. The provider
// (typically the market client's InstrumentProvider) supplies instrument
// minimums; it may be nil. No-op if the node has no execution client.
func WithRisk(r exec.RiskChecker, provider model.InstrumentProvider) Option {
	return func(n *TradingNode) {
		if n.Exec != nil {
			n.Exec.WithRisk(r, provider)
		}
	}
}

// WithObserver registers an observability sink that receives lifecycle, order,
// fill, and reject callbacks on the runtime goroutine.
func WithObserver(o observ.Observer) Option {
	return func(n *TradingNode) { n.obs = o }
}

func (n *TradingNode) State() lifecycle.Snapshot { return n.life.Snapshot() }

func (n *TradingNode) Health() observ.Health {
	m := n.Metrics()
	s := m.Lifecycle
	return observ.Health{
		Component:               "runtime",
		Status:                  string(s.Node),
		Detail:                  s.Reason,
		Lifecycle:               s,
		Clients:                 n.clientNames(),
		Streams:                 n.streamNames(),
		LastReconciliationError: s.LastReconciliationError,
		LatencyDrops:            m.Latency.DroppedTotal,
		ObserverDrops:           m.ObserverDrops,
		EventQueueDepth:         m.EventQueueDepth,
		InFlight:                m.InFlight,
		PendingFills:            m.PendingFills,
		Accounts:                m.Accounts,
		AccountStateAgeNs:       m.AccountStateAgeNs,
	}
}

func (n *TradingNode) Halt(reason string) {
	n.life.Halt(reason)
	n.emitHealth("halted", reason)
}

func (n *TradingNode) ReduceOnly(reason string) {
	n.life.ReduceOnly(reason)
	n.emitHealth("reducing", reason)
}

func (n *TradingNode) clientNames() []string {
	var out []string
	if n.clients.Market != nil {
		out = append(out, "market")
	}
	if n.clients.Execution != nil {
		out = append(out, "execution")
	}
	if n.clients.Account != nil {
		out = append(out, "account")
	}
	return out
}

func (n *TradingNode) streamNames() []string {
	var out []string
	if n.clients.Market != nil && n.clients.Market.Events() != nil {
		out = append(out, "market")
	}
	if n.clients.Execution != nil && n.clients.Execution.Events() != nil {
		out = append(out, "execution")
	}
	if n.clients.Account != nil && n.clients.Account.Events() != nil {
		out = append(out, "account")
	}
	return out
}

// Metrics returns a point-in-time snapshot of trading state and event counters.
// Safe to call from any goroutine (reads go through the cache/portfolio locks).
func (n *TradingNode) Metrics() observ.Metrics {
	var latencySnapshot latency.Snapshot
	if n.latency != nil {
		latencySnapshot = n.latency.Snapshot()
	}
	var observerDrops uint64
	var queueDepth int
	if dropping, ok := n.obs.(observ.DroppingObserver); ok {
		observerDrops = dropping.Drops()
		queueDepth = dropping.QueueDepth()
	}
	accounts := n.Cache.Accounts()
	return observ.Metrics{
		OpenOrders:        len(n.Cache.OpenOrders()),
		Positions:         len(n.Cache.Positions()),
		RealizedPnL:       n.Portfolio.RealizedPnL(),
		RealizedPnLNet:    n.Portfolio.RealizedPnLNetFees(),
		Fees:              n.Portfolio.Fees(),
		FeesByCurrency:    n.Portfolio.FeesByCurrency(),
		OrdersSeen:        atomic.LoadInt64(&n.counters.Orders),
		FillsSeen:         atomic.LoadInt64(&n.counters.Fills),
		RejectsSeen:       atomic.LoadInt64(&n.counters.Rejects),
		Latency:           latencySnapshot,
		ObserverDrops:     observerDrops,
		EventQueueDepth:   queueDepth,
		Lifecycle:         n.life.Snapshot(),
		InFlight:          inFlightCount(n.Exec),
		PendingFills:      n.fills.Count(),
		Accounts:          len(accounts),
		AccountStateAgeNs: maxAccountStateAge(accounts, n.clk.Now()),
	}
}

func inFlightCount(e *exec.Engine) int {
	if e == nil {
		return 0
	}
	return e.InFlightCount()
}

func maxAccountStateAge(accounts []accounting.Account, now time.Time) int64 {
	var max time.Duration
	for _, acct := range accounts {
		age := acct.Freshness().Age(now)
		if age > max {
			max = age
		}
	}
	return max.Nanoseconds()
}

// Resync reconciles the cache against venue REST snapshots. Call it at startup
// and after a reconnect. It is a no-op (nil report, nil error) if the node has
// no account client.
func (n *TradingNode) Resync(ctx context.Context) (reconcile.Report, error) {
	return n.resync(ctx, "resync", true)
}

func (n *TradingNode) resync(ctx context.Context, reason string, restoreRunning bool) (reconcile.Report, error) {
	if n.reconciler == nil {
		return reconcile.Report{}, nil
	}
	prev := n.life.Snapshot()
	if err := n.life.Transition(lifecycle.NodeReconciling, lifecycle.TradingReconciling, reason); err != nil {
		n.life.SetLastReconciliationError(err)
		return reconcile.Report{}, err
	}
	n.emitHealth("reconciling", reason)
	start := time.Now()
	rep, err := n.reconciler.Run(ctx)
	n.life.SetLastReconciliationError(err)
	if n.obs != nil {
		rec := observ.Reconciliation{Reason: "resync", DurationNs: time.Since(start).Nanoseconds()}
		if err != nil {
			rec.Error = err.Error()
		}
		n.obs.OnReconciliation(rec)
	}
	if err != nil {
		n.life.ForceFailed(err.Error())
		n.emitHealth("failed", err.Error())
		return rep, err
	}
	if restoreRunning {
		switch prev.Node {
		case lifecycle.NodeRunning:
			if err := n.life.Transition(lifecycle.NodeRunning, lifecycle.TradingActive, reason+" complete"); err != nil {
				return rep, err
			}
			n.emitHealth("running", reason+" complete")
		case lifecycle.NodeCreated:
			if err := n.life.Transition(lifecycle.NodeCreated, lifecycle.TradingDisabled, reason+" complete"); err != nil {
				return rep, err
			}
			n.emitHealth("created", reason+" complete")
		}
	}
	return rep, err
}

// Reconnect forces a reconnect on any client that supports it
// (contract.Reconnectable), then reconciles. Clients that auto-reconnect
// internally are skipped. Returns the reconciliation report.
func (n *TradingNode) Reconnect(ctx context.Context) (reconcile.Report, error) {
	if err := n.life.Transition(lifecycle.NodeReconnecting, lifecycle.TradingReconciling, "reconnect"); err != nil {
		return reconcile.Report{}, err
	}
	n.emitHealth("reconnecting", "reconnect")
	for _, cl := range []any{n.clients.Market, n.clients.Execution, n.clients.Account} {
		if rc, ok := cl.(contract.Reconnectable); ok {
			if err := rc.Reconnect(ctx); err != nil {
				n.life.ForceFailed(err.Error())
				n.emitHealth("failed", err.Error())
				return reconcile.Report{}, err
			}
		}
	}
	rep, err := n.resync(ctx, "reconnect reconciliation", false)
	if err != nil {
		return rep, err
	}
	if err := n.life.Transition(lifecycle.NodeRunning, lifecycle.TradingActive, "reconnect complete"); err != nil {
		return rep, err
	}
	n.emitHealth("running", "reconnect complete")
	return rep, nil
}

// Run consumes events until ctx is cancelled or all client streams close. It
// calls the strategy's OnStart before the loop and OnStop after. It blocks; run
// with `go node.Run(ctx)` or from a dedicated goroutine.
func (n *TradingNode) Run(ctx context.Context) {
	if err := n.life.Transition(lifecycle.NodeStarting, lifecycle.TradingDisabled, "run start"); err != nil {
		n.life.ForceFailed(err.Error())
		n.emitHealth("failed", err.Error())
		return
	}
	n.emitHealth("starting", "run start")
	if n.Exec != nil {
		if err := n.Exec.ReplayOpenIntents(ctx); err != nil {
			n.life.ForceFailed(err.Error())
			n.emitHealth("failed", err.Error())
			return
		}
	}
	if n.reconciler != nil {
		if _, err := n.resync(ctx, "startup reconciliation", false); err != nil {
			return
		}
	}
	if err := n.life.Transition(lifecycle.NodeRunning, lifecycle.TradingActive, "startup complete"); err != nil {
		n.life.ForceFailed(err.Error())
		n.emitHealth("failed", err.Error())
		return
	}
	n.emitHealth("running", "startup complete")
	n.Start(ctx)
	defer func() {
		_ = n.life.Transition(lifecycle.NodeStopping, lifecycle.TradingDisabled, "stopping")
		n.Stop()
		_ = n.life.Transition(lifecycle.NodeStopped, lifecycle.TradingDisabled, "stopped")
		n.emitHealth("stopped", "stopped")
	}()
	n.bus.Run(ctx, bus.Handlers{
		OnExec:    n.onExec,
		OnAccount: n.onAccount,
		OnMarket:  n.onMarket,
	})
}

func (n *TradingNode) emitHealth(status, detail string) {
	if n.obs != nil {
		n.obs.OnHealth(observ.Health{Component: "runtime", Status: status, Detail: detail})
	}
}

// Start builds the strategy context and fires OnStart. It is called by Run; it
// remains exported for tests and simple embedders that want explicit lifecycle
// control.
func (n *TradingNode) Start(ctx context.Context) {
	if n.obs != nil {
		n.obs.OnNodeStart()
	}
	if n.strat != nil {
		n.stratCtx = &strategy.Context{
			Ctx:       ctx,
			Clock:     n.clk,
			Cache:     n.Cache,
			Portfolio: n.Portfolio,
			Orders:    n.Exec,
		}
		n.strat.OnStart(n.stratCtx)
	}
}

// Stop fires the strategy's OnStop. Idempotent-safe to call once after Start.
func (n *TradingNode) Stop() {
	if n.strat != nil && n.stratCtx != nil {
		n.strat.OnStop(n.stratCtx)
	}
	if n.obs != nil {
		n.obs.OnNodeStop()
	}
}

// onExec applies an execution event to cache and portfolio. Runs on the bus
// goroutine.
func (n *TradingNode) onExec(env contract.ExecEnvelope) {
	applied := time.Now()
	switch e := env.Payload.(type) {
	case contract.OrderEvent:
		n.Cache.UpsertOrder(e.Order)
		if n.Exec != nil {
			if e.Order.Status == enums.StatusRejected || e.Order.Status == enums.StatusExpired {
				reason := e.Order.RejectReason
				if reason == "" {
					reason = e.Order.Status.String()
				}
				n.Exec.RejectInFlight(e.Order.Request.ClientID, e.Order.VenueOrderID, reason, n.clk.Now())
			} else {
				n.Exec.ResolveInFlight(e.Order.Request.ClientID, e.Order.VenueOrderID, n.clk.Now())
			}
		}
		atomic.AddInt64(&n.counters.Orders, 1)
		if n.obs != nil {
			n.obs.OnOrder(e.Order)
		}
		for _, fill := range n.fills.DrainBuffered(e.Order) {
			fillEnv := contract.NewExecEnvelopeWithMeta(contract.FillEvent{Fill: fill.Fill}, fill.Meta)
			n.applyFill(fill.Fill, fillEnv)
		}
	case contract.FillEvent:
		if !n.applyFill(e.Fill, env) {
			n.fills.BufferEnvelope(e.Fill, env.Meta())
		}
	case contract.RejectEvent:
		atomic.AddInt64(&n.counters.Rejects, 1)
		venueOrderID := ""
		if o, ok := n.Cache.Order(e.ClientID); ok {
			o.Status = enums.StatusRejected
			o.RejectReason = e.Reason
			o.UpdatedAt = n.clk.Now()
			n.Cache.UpsertOrder(o)
			venueOrderID = o.VenueOrderID
		}
		if n.Exec != nil {
			n.Exec.RejectInFlight(e.ClientID, venueOrderID, e.Reason, n.clk.Now())
		}
		if n.obs != nil {
			n.obs.OnReject(e.ClientID, e.Reason)
		}
	}
	n.recordEventLatency(latency.ChainExecution, env.Meta(), applied, time.Now())
}

func (n *TradingNode) applyFill(fill model.Fill, env contract.ExecEnvelope) bool {
	resolvedFromInFlight := false
	if fill.ClientID == "" && n.Exec != nil {
		if resolvedFill, ok := n.Exec.ResolveFillInFlight(fill, fill.Timestamp); ok {
			fill = resolvedFill
			resolvedFromInFlight = true
		}
	}
	o, ok := n.orderForFill(fill)
	if !ok {
		if materialized, materializedOK := n.materializeExternalOrder(fill, resolvedFromInFlight); materializedOK {
			o = materialized
			ok = true
		}
	}
	if !ok {
		return false
	}
	if !n.fills.MarkApplied(fill) {
		return true
	}
	o = orderstate.ApplyFill(o, fill, n.clk.Now())
	n.Cache.UpsertOrder(o)
	if n.Exec != nil {
		resolvedAt := fill.Timestamp
		if resolvedAt.IsZero() {
			resolvedAt = n.clk.Now()
		}
		n.Exec.ResolveInFlight(fill.ClientID, fill.VenueOrderID, resolvedAt)
	}
	posSide := o.Request.PositionSide
	n.Portfolio.OnFill(fill, posSide)
	atomic.AddInt64(&n.counters.Fills, 1)
	if n.onFill != nil {
		n.onFill(fill)
	}
	if n.strat != nil {
		n.stratCtx.SetCurrentEventMeta(env.Meta())
		n.strat.OnFill(n.stratCtx, fill)
	}
	if n.obs != nil {
		n.obs.OnFill(fill)
	}
	return true
}

func (n *TradingNode) orderForFill(fill model.Fill) (model.Order, bool) {
	if fill.ClientID != "" {
		if o, ok := n.Cache.Order(fill.ClientID); ok {
			return o, true
		}
	}
	if fill.VenueOrderID != "" {
		if o, ok := n.Cache.Order(fill.VenueOrderID); ok {
			return o, true
		}
		for _, o := range n.Cache.Orders() {
			if o.VenueOrderID == fill.VenueOrderID {
				return o, true
			}
		}
	}
	return model.Order{}, false
}

func (n *TradingNode) materializeExternalOrder(fill model.Fill, allowKnownClient bool) (model.Order, bool) {
	if fill.VenueOrderID == "" || fill.InstrumentID.Symbol == "" ||
		fill.Quantity.IsZero() || fill.Price.IsZero() {
		return model.Order{}, false
	}
	if fill.ClientID != "" && !allowKnownClient {
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
		ts = n.clk.Now()
	}
	order := model.Order{
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
	}
	n.Cache.UpsertOrder(order)
	return order, true
}

// onAccount applies a balance/position event to the cache. Runs on the bus
// goroutine.
func (n *TradingNode) onAccount(env contract.AccountEnvelope) {
	applied := time.Now()
	switch e := env.Payload.(type) {
	case contract.BalanceEvent:
		n.Cache.UpsertBalance(e.Balance)
	case contract.PositionEvent:
		n.Cache.UpsertPosition(e.Position)
	case contract.AccountStateEvent:
		if err := n.Cache.ApplyAccountStateAt(e.State, applied); err != nil {
			n.life.ForceFailed(err.Error())
			n.emitHealth("failed", err.Error())
		}
	}
	n.recordEventLatency(latency.ChainAccount, env.Meta(), applied, time.Now())
}

// onMarket is the DataEngine: it writes the latest market snapshot to the cache
// and dispatches to the strategy. Runs on the bus goroutine.
func (n *TradingNode) onMarket(env contract.MarketEnvelope) {
	applied := time.Now()
	switch e := env.Payload.(type) {
	case contract.QuoteEvent:
		n.Cache.UpsertQuote(e.Quote)
		if n.strat != nil {
			n.stratCtx.SetCurrentEventMeta(env.Meta())
			n.strat.OnQuote(n.stratCtx, e.Quote)
		}
	case contract.BookEvent:
		n.Cache.UpsertBook(e.Book)
	case contract.TradeEvent:
		n.Cache.UpsertTrade(e.Trade)
		if n.strat != nil {
			n.stratCtx.SetCurrentEventMeta(env.Meta())
			n.strat.OnTrade(n.stratCtx, e.Trade)
		}
		if agg := n.aggregators[e.Trade.InstrumentID.String()]; agg != nil {
			if bar, ok := agg.OnTrade(e.Trade); ok && n.strat != nil {
				n.stratCtx.SetCurrentEventMeta(env.Meta())
				n.strat.OnBar(n.stratCtx, bar)
			}
		}
	}
	n.recordEventLatency(latency.ChainMarket, env.Meta(), applied, time.Now())
}

func (n *TradingNode) recordEventLatency(chain latency.Chain, meta contract.EventMeta, applied, callbackDone time.Time) {
	if n.latency == nil {
		return
	}
	lat := latency.EventFromMeta(chain, meta, applied, callbackDone)
	n.latency.RecordEventLatency(lat)
	if n.obs != nil {
		n.obs.OnLatency(lat)
	}
}
