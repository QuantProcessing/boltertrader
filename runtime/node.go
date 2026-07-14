// Package runtime wires the venue-neutral building blocks — the event bus,
// authoritative cache, portfolio, and execution engine — into a single
// TradingNode. The node depends only on core/contract + core/clock and consumes
// live-style client streams through one bus goroutine while serializing direct
// reconciliation work against that event path.
package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
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
	"github.com/shopspring/decimal"
)

// Clients bundles the venue-neutral clients a node drives. Any may be nil for a
// partial node (e.g. market-data only).
type Clients struct {
	Market    contract.MarketDataClient
	Execution contract.ExecutionClient
	Account   contract.AccountClient
}

type pendingStrategyFill struct {
	fill model.Fill
	meta contract.EventMeta
}

// TradingNode hosts the runtime state machine. Cache, Portfolio, and Exec are
// exported for strategy/reporting access; event-driven and recovered mutation
// is serialized through eventMu.
type TradingNode struct {
	Cache     *cache.Cache
	Portfolio *portfolio.Portfolio
	Exec      *exec.Engine

	clients   Clients
	clk       clock.Clock
	bus       *bus.Bus
	accountID string

	expectedAccountID       string
	accountIDConfigErr      error
	adapterBackedAccountID  bool
	legacyAccountIDFallback string

	// onFill is an optional raw hook invoked on the serialized runtime event path
	// after each fill is applied to cache and portfolio.
	onFill         func(model.Fill)
	onExecEnvelope func(contract.ExecEnvelope)

	// strat is the optional strategy and its prebuilt callback context.
	strat                strategy.Strategy
	stratCtx             *strategy.Context
	pendingStrategyFills []pendingStrategyFill
	pendingObserverFills []model.Fill
	observerStarted      bool

	// aggregators build bars from trades, keyed by InstrumentID.String().
	aggregators map[string]*data.BarAggregator

	// reconciler corrects the cache from venue REST snapshots; nil if no
	// account client.
	reconciler *reconcile.Reconciler

	// obs receives observability callbacks; nil if none registered. observerMu
	// serializes every callback and is acquired after eventMu when both apply.
	obs        observ.Observer
	observerMu sync.Mutex
	counters   observ.Counters
	latency    latency.Recorder
	life       *lifecycle.Machine
	fills      *exec.FillBuffer

	// eventMu preserves the single-callback guarantee when a caller-triggered
	// reconciliation applies recovered fills concurrently with the event bus.
	eventMu sync.Mutex
	// reconcileMu serializes startup/manual/automatic reconciliation and guards
	// automatic stream-gap generation state.
	reconcileMu         sync.Mutex
	activeStreamGaps    map[string]uint64
	lastStreamGaps      map[string]uint64
	gapRestore          lifecycle.Snapshot
	fillRecoveryBlocked bool
}

// Option configures a TradingNode.
type Option func(*TradingNode)

// WithOnFill registers a raw callback fired after each fill is applied.
func WithOnFill(fn func(model.Fill)) Option {
	return func(n *TradingNode) { n.onFill = fn }
}

// WithOnExecEnvelope observes the original execution envelope before the
// runtime mutates cache or portfolio state. It is useful for proving adapter
// source/flag provenance without weakening the model-only Observer API.
func WithOnExecEnvelope(fn func(contract.ExecEnvelope)) Option {
	return func(n *TradingNode) { n.onExecEnvelope = fn }
}

// WithStrategy registers a callback-style strategy. Its callbacks run on the
// serialized runtime event path with a Context wired to this node's cache,
// portfolio, clock, and execution engine.
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
		accountID = strings.TrimSpace(accountID)
		if accountID == "" {
			return
		}
		n.expectedAccountID = accountID
		n.applyResolvedAccountID()
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
	legacyAccountIDFallback := strings.TrimSpace(idPrefix)
	if legacyAccountIDFallback == "" {
		legacyAccountIDFallback = "bt"
	}
	c := cache.New()
	pf := portfolio.New().WithAccountSource(c)

	var marketCh <-chan contract.MarketEnvelope
	var execCh <-chan contract.ExecEnvelope
	var accountCh <-chan contract.AccountEnvelope
	if clients.Market != nil {
		marketCh = clients.Market.Events()
		pf.WithInstrumentProvider(clients.Market.InstrumentProvider())
	}
	if clients.Execution != nil {
		execCh = clients.Execution.Events()
	}
	if clients.Account != nil {
		accountCh = clients.Account.Events()
	}

	n := &TradingNode{
		Cache:                   c,
		Portfolio:               pf,
		clients:                 clients,
		clk:                     clk,
		accountID:               legacyAccountIDFallback,
		legacyAccountIDFallback: legacyAccountIDFallback,
		bus:                     bus.New(marketCh, execCh, accountCh),
		aggregators:             make(map[string]*data.BarAggregator),
		latency:                 latency.NewRecorder(4096),
		life:                    lifecycle.New(),
		fills:                   exec.NewFillBuffer(),
		activeStreamGaps:        make(map[string]uint64),
		lastStreamGaps:          make(map[string]uint64),
	}
	if clients.Execution != nil {
		n.Exec = exec.New(clients.Execution, c, clk, idPrefix)
		n.Exec.WithLatencyRecorder(n.latency)
		n.Exec.WithCommandGate(n.life)
		n.Exec.WithRecoverabilityHandler(func(err error) {
			n.Halt("exec journal recoverability breach: " + err.Error())
		})
		n.Exec.WithTerminalOrderHandler(func(order model.Order) {
			if err := n.fills.MarkOrderTerminal(order); err != nil {
				n.Halt("fill order identity conflict: " + err.Error())
			}
		})
	}
	// Reconcile whatever authoritative sources exist: balances/positions from the
	// account client, open orders from the execution client. Either may be nil.
	if clients.Account != nil || clients.Execution != nil {
		n.reconciler = reconcile.New(clients.Account, clients.Execution, c)
		n.reconciler.WithLatencyRecorder(n.latency)
		n.reconciler.WithFillApplier(n.applyRecoveredFill)
		n.reconciler.WithFillSeeder(func(fill model.Fill) { n.fills.MarkApplied(fill) })
		if n.Exec != nil {
			n.reconciler.WithInFlightResolver(n.Exec)
		}
	}
	for _, o := range opts {
		o(n)
	}
	n.applyResolvedAccountID()
	return n
}

type resolvedAccountID struct {
	accountID string
	provided  bool
}

func accountIDFromProvider(name string, client any) (resolvedAccountID, error) {
	if client == nil {
		return resolvedAccountID{}, nil
	}
	provider, ok := client.(contract.AccountIDProvider)
	if !ok {
		return resolvedAccountID{}, nil
	}
	accountID := strings.TrimSpace(provider.AccountID())
	if accountID == "" {
		return resolvedAccountID{provided: true}, fmt.Errorf("runtime account id: %s client exposed empty account id", name)
	}
	return resolvedAccountID{accountID: accountID, provided: true}, nil
}

func clientMissingAccountIDProvider(name string, client interface{ Capabilities() contract.Capabilities }) (string, bool) {
	if client == nil {
		return "", false
	}
	if _, ok := client.(contract.AccountIDProvider); ok {
		return "", false
	}
	venue := strings.TrimSpace(client.Capabilities().Venue)
	if isTestRuntimeVenue(venue) {
		return "", false
	}
	if venue == "" {
		return fmt.Sprintf("%s client with empty venue", name), true
	}
	return fmt.Sprintf("%s client for venue %q", name, venue), true
}

func venueClientMissingAccountIDProvider(clients Clients) (string, bool) {
	if desc, ok := clientMissingAccountIDProvider("execution", clients.Execution); ok {
		return desc, true
	}
	if desc, ok := clientMissingAccountIDProvider("account", clients.Account); ok {
		return desc, true
	}
	return "", false
}

func isTestRuntimeVenue(venue string) bool {
	switch strings.ToUpper(strings.TrimSpace(venue)) {
	case "FAKE", "TEST", "T":
		return true
	default:
		return false
	}
}

func resolveRuntimeAccountID(clients Clients, expectedAccountID, fallbackAccountID string) (string, bool, error) {
	expectedAccountID = strings.TrimSpace(expectedAccountID)
	fallbackAccountID = strings.TrimSpace(fallbackAccountID)
	if fallbackAccountID == "" {
		fallbackAccountID = "bt"
	}

	execution, err := accountIDFromProvider("execution", clients.Execution)
	if err != nil {
		return "", false, err
	}
	account, err := accountIDFromProvider("account", clients.Account)
	if err != nil {
		return "", false, err
	}

	var adapterAccountID string
	if execution.provided {
		adapterAccountID = execution.accountID
	}
	if account.provided {
		if adapterAccountID != "" && adapterAccountID != account.accountID {
			return "", false, fmt.Errorf("runtime account id: execution client account id %q does not match account client account id %q", execution.accountID, account.accountID)
		}
		adapterAccountID = account.accountID
	}
	if missing, ok := venueClientMissingAccountIDProvider(clients); ok {
		return "", false, fmt.Errorf("runtime account id: %s must expose AccountIDProvider", missing)
	}

	if adapterAccountID != "" {
		if expectedAccountID != "" && expectedAccountID != adapterAccountID {
			return "", false, fmt.Errorf("runtime account id: expected account id %q does not match adapter account id %q", expectedAccountID, adapterAccountID)
		}
		return adapterAccountID, true, nil
	}
	if expectedAccountID != "" {
		return expectedAccountID, false, nil
	}
	return fallbackAccountID, false, nil
}

func (n *TradingNode) applyResolvedAccountID() {
	accountID, adapterBacked, err := resolveRuntimeAccountID(n.clients, n.expectedAccountID, n.legacyAccountIDFallback)
	n.accountIDConfigErr = err
	if err != nil {
		n.adapterBackedAccountID = false
		return
	}
	n.accountID = accountID
	n.adapterBackedAccountID = adapterBacked
	if n.Exec != nil {
		n.Exec.WithAccountID(accountID)
	}
	if n.reconciler != nil {
		n.reconciler.WithAccountID(accountID)
	}
}

func (n *TradingNode) accountIDReady() error {
	if n.accountIDConfigErr == nil {
		return nil
	}
	n.life.ForceFailed(n.accountIDConfigErr.Error())
	n.emitHealth("failed", n.accountIDConfigErr.Error())
	return n.accountIDConfigErr
}

// WithRisk attaches a pre-trade risk gate to the execution engine. The provider
// (typically the market client's InstrumentProvider) supplies instrument
// minimums; it may be nil. No-op if the node has no execution client.
func WithRisk(r exec.RiskChecker, provider model.InstrumentProvider) Option {
	return func(n *TradingNode) {
		if provider == nil && n.clients.Market != nil {
			provider = n.clients.Market.InstrumentProvider()
		}
		if aware, ok := r.(interface {
			SetRuntimeCapabilities(...contract.Capabilities)
		}); ok {
			aware.SetRuntimeCapabilities(runtimeRiskCapabilities(n.clients)...)
		}
		if registrar, ok := r.(interface {
			SetVenuePreTradeValidator(contract.VenuePreTradeValidator, ...enums.InstrumentKind)
		}); ok {
			if validator, ok := n.clients.Execution.(contract.VenuePreTradeValidator); ok {
				kinds := executionSubmitKinds(n.clients.Execution.Capabilities())
				if len(kinds) > 0 {
					registrar.SetVenuePreTradeValidator(validator, kinds...)
				}
			}
		}
		if n.Exec != nil {
			n.Exec.WithRisk(r, provider)
		}
		if provider != nil {
			n.Portfolio.WithInstrumentProvider(provider)
		}
	}
}

func executionSubmitKinds(caps contract.Capabilities) []enums.InstrumentKind {
	if !caps.Trading.Submit {
		return nil
	}
	seen := make(map[enums.InstrumentKind]struct{}, len(caps.Products))
	kinds := make([]enums.InstrumentKind, 0, len(caps.Products))
	for _, product := range caps.Products {
		if !product.Trading || product.Kind == enums.KindUnknown {
			continue
		}
		if _, ok := seen[product.Kind]; ok {
			continue
		}
		seen[product.Kind] = struct{}{}
		kinds = append(kinds, product.Kind)
	}
	return kinds
}

func runtimeRiskCapabilities(clients Clients) []contract.Capabilities {
	caps := make([]contract.Capabilities, 0, 2)
	if clients.Execution != nil {
		caps = append(caps, clients.Execution.Capabilities())
	}
	if clients.Account != nil {
		caps = append(caps, clients.Account.Capabilities())
	}
	return caps
}

func (n *TradingNode) RuntimeCapabilities() []contract.Capabilities {
	return runtimeRiskCapabilities(n.clients)
}

// WithObserver registers an observability sink that receives lifecycle, order,
// fill, and reject callbacks synchronously on the node's serialized observer
// path.
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
	n.reconcileMu.Lock()
	defer n.reconcileMu.Unlock()
	return n.resync(ctx, "resync", true)
}

func (n *TradingNode) resync(ctx context.Context, reason string, restoreRunning bool) (reconcile.Report, error) {
	if err := n.accountIDReady(); err != nil {
		n.life.SetLastReconciliationError(err)
		return reconcile.Report{}, err
	}
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
	var rep reconcile.Report
	var err error
	func() {
		n.eventMu.Lock()
		defer n.eventMu.Unlock()
		rep, err = n.reconciler.Run(ctx)
		if syncErr := n.syncFillTerminalOrders(); err == nil && syncErr != nil {
			err = syncErr
		}
		n.life.SetLastReconciliationError(err)
		rec := observ.Reconciliation{Reason: "resync", DurationNs: time.Since(start).Nanoseconds()}
		if err != nil {
			rec.Error = err.Error()
		}
		n.notifyObserver(func(observer observ.Observer) { observer.OnReconciliation(rec) })
	}()
	if err != nil {
		n.life.ForceFailed(err.Error())
		n.emitHealth("failed", err.Error())
		return rep, err
	}
	if restoreRunning {
		switch prev.Node {
		case lifecycle.NodeRunning:
			if err := n.finishReconciliation(prev, rep, reason+" complete", false); err != nil {
				return rep, err
			}
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
	n.reconcileMu.Lock()
	defer n.reconcileMu.Unlock()
	if err := n.accountIDReady(); err != nil {
		return reconcile.Report{}, err
	}
	prev := n.life.Snapshot()
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
	var rep reconcile.Report
	var err error
	if n.reconciler != nil {
		rep, err = n.resync(ctx, "reconnect reconciliation", false)
	} else {
		err = n.life.Transition(lifecycle.NodeReconciling, lifecycle.TradingReconciling, "reconnect reconciliation")
	}
	if err != nil {
		return rep, err
	}
	if err := n.finishReconciliation(prev, rep, "reconnect complete", true); err != nil {
		return rep, err
	}
	return rep, nil
}

func (n *TradingNode) finishReconciliation(previous lifecycle.Snapshot, rep reconcile.Report, reason string, requireFillHistory bool) error {
	current := n.life.Snapshot()
	verdict := rep.ActivationVerdict()
	cannotRecoverFills := n.executionStreamCannotRecoverFills()
	if requireFillHistory && cannotRecoverFills {
		n.fillRecoveryBlocked = true
	} else if n.fillRecoveryBlocked && !cannotRecoverFills && verdict.Safe {
		n.fillRecoveryBlocked = false
	}
	trading, restrictionReason, restricted := strongestTradingRestriction(previous, current)
	if !restricted {
		if !verdict.Safe {
			trading = lifecycle.TradingReconciling
			restrictionReason = verdict.Reason
			restricted = true
		}
	}
	if !restricted && n.fillRecoveryBlocked {
		trading = lifecycle.TradingReconciling
		restrictionReason = "private execution stream recovered without authoritative fill history"
		restricted = true
	}
	if !restricted {
		trading = lifecycle.TradingActive
		restrictionReason = reason
	}
	if err := n.life.Transition(lifecycle.NodeRunning, trading, restrictionReason); err != nil {
		return err
	}
	status := "running"
	if trading != lifecycle.TradingActive {
		status = string(trading)
	}
	n.emitHealth(status, restrictionReason)
	return nil
}

func strongestTradingRestriction(snapshots ...lifecycle.Snapshot) (lifecycle.TradingState, string, bool) {
	for _, snapshot := range snapshots {
		if snapshot.Trading == lifecycle.TradingHalted {
			return lifecycle.TradingHalted, snapshot.Reason, true
		}
	}
	for _, snapshot := range snapshots {
		if snapshot.Trading == lifecycle.TradingReducing {
			return lifecycle.TradingReducing, snapshot.Reason, true
		}
	}
	return "", "", false
}

func (n *TradingNode) executionStreamCannotRecoverFills() bool {
	if n.clients.Execution == nil {
		return false
	}
	caps := n.clients.Execution.Capabilities()
	return caps.Streaming.Execution && !caps.Reports.FillHistory
}

// OpenInterest queries current venue open interest through the optional market
// client surface. OI remains query-only and is not written to runtime cache.
func (n *TradingNode) OpenInterest(ctx context.Context, id model.InstrumentID) (model.OpenInterestSnapshot, error) {
	client, ok := n.clients.Market.(contract.OpenInterestClient)
	if !ok || client == nil {
		return model.OpenInterestSnapshot{}, fmt.Errorf("open interest: %w", contract.ErrNotSupported)
	}
	return client.OpenInterest(ctx, id)
}

// Run consumes events until ctx is cancelled or all client streams close. It
// calls the strategy's OnStart before the loop and OnStop after. It blocks; run
// with `go node.Run(ctx)` or from a dedicated goroutine.
func (n *TradingNode) Run(ctx context.Context) {
	if err := n.accountIDReady(); err != nil {
		return
	}
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
	startupState := n.life.Snapshot()
	var startupReport reconcile.Report
	var err error
	n.reconcileMu.Lock()
	if n.reconciler != nil {
		startupReport, err = n.resync(ctx, "startup reconciliation", false)
	}
	if err == nil {
		err = n.finishReconciliation(startupState, startupReport, "startup complete", false)
	}
	n.reconcileMu.Unlock()
	if err != nil {
		n.life.ForceFailed(err.Error())
		n.emitHealth("failed", err.Error())
		return
	}
	n.Start(ctx)
	defer func() {
		_ = n.life.Transition(lifecycle.NodeStopping, lifecycle.TradingDisabled, "stopping")
		n.Stop()
		_ = n.life.Transition(lifecycle.NodeStopped, lifecycle.TradingDisabled, "stopped")
		n.emitHealth("stopped", "stopped")
	}()
	n.bus.Run(ctx, bus.Handlers{
		OnExec:    func(env contract.ExecEnvelope) { n.onExecContext(ctx, env) },
		OnAccount: n.onAccount,
		OnMarket:  n.onMarket,
	})
}

func (n *TradingNode) emitHealth(status, detail string) {
	n.notifyObserver(func(observer observ.Observer) {
		observer.OnHealth(observ.Health{Component: "runtime", Status: status, Detail: detail})
	})
}

func (n *TradingNode) notifyObserver(notify func(observ.Observer)) {
	if n.obs == nil {
		return
	}
	n.observerMu.Lock()
	defer n.observerMu.Unlock()
	notify(n.obs)
}

// Start builds the strategy context and fires OnStart. It is called by Run; it
// remains exported for tests and simple embedders that want explicit lifecycle
// control.
func (n *TradingNode) Start(ctx context.Context) {
	if err := n.accountIDReady(); err != nil {
		return
	}
	n.eventMu.Lock()
	defer n.eventMu.Unlock()
	if n.obs != nil {
		n.notifyObserver(func(observer observ.Observer) { observer.OnNodeStart() })
		n.observerStarted = true
	}
	if n.strat != nil {
		stratCtx := &strategy.Context{
			Ctx:                ctx,
			Clock:              n.clk,
			Cache:              n.Cache,
			Portfolio:          n.Portfolio,
			Orders:             n.Exec,
			OpenInterestClient: openInterestClient(n.clients.Market),
		}
		n.strat.OnStart(stratCtx)

		n.stratCtx = stratCtx
		for _, recovered := range n.pendingStrategyFills {
			stratCtx.SetCurrentEventMeta(recovered.meta)
			n.strat.OnFill(stratCtx, recovered.fill)
		}
		n.pendingStrategyFills = nil
	}
	if n.obs != nil {
		for _, fill := range n.pendingObserverFills {
			notifyFill := fill
			n.notifyObserver(func(observer observ.Observer) { observer.OnFill(notifyFill) })
		}
		n.pendingObserverFills = nil
	}
}

func openInterestClient(market contract.MarketDataClient) contract.OpenInterestClient {
	if client, ok := market.(contract.OpenInterestClient); ok {
		return client
	}
	return nil
}

// Stop fires the strategy's OnStop. Idempotent-safe to call once after Start.
func (n *TradingNode) Stop() {
	if n.strat != nil && n.stratCtx != nil {
		n.strat.OnStop(n.stratCtx)
	}
	n.notifyObserver(func(observer observ.Observer) { observer.OnNodeStop() })
}

// onExec applies an execution event to cache and portfolio. Tests that invoke
// it directly use a background context; the bus supplies its run context.
func (n *TradingNode) onExec(env contract.ExecEnvelope) {
	n.onExecContext(context.Background(), env)
}

func (n *TradingNode) onExecContext(ctx context.Context, env contract.ExecEnvelope) {
	applied := time.Now()
	if n.onExecEnvelope != nil {
		n.onExecEnvelope(env)
	}
	if gap, ok := env.Payload.(contract.StreamGapEvent); ok {
		n.onStreamGap(ctx, gap)
		n.recordEventLatency(latency.ChainExecution, env.Meta(), applied, time.Now())
		return
	}
	n.eventMu.Lock()
	defer n.eventMu.Unlock()
	switch e := env.Payload.(type) {
	case contract.OrderEvent:
		if err := n.Cache.UpsertOrderChecked(e.Order); err != nil {
			n.Halt("order identity conflict: " + err.Error())
			n.recordEventLatency(latency.ChainExecution, env.Meta(), applied, time.Now())
			return
		}
		canonical := n.canonicalOrder(e.Order)
		if err := n.syncFillOrderTerminal(canonical); err != nil {
			n.Halt(err.Error())
		}
		if n.Exec != nil {
			resolvedAt := canonical.UpdatedAt
			if resolvedAt.IsZero() {
				resolvedAt = n.clk.Now()
			}
			n.Exec.ResolveOrderInFlight(canonical, resolvedAt)
		}
		atomic.AddInt64(&n.counters.Orders, 1)
		n.notifyObserver(func(observer observ.Observer) { observer.OnOrder(e.Order) })
		for _, fill := range n.fills.DrainBuffered(e.Order) {
			fillEnv := contract.NewExecEnvelopeWithMeta(contract.FillEvent{Fill: fill.Fill}, fill.Meta)
			if !n.applyFill(fill.Fill, fillEnv) {
				n.fills.BufferEnvelope(fill.Fill, fill.Meta)
			}
		}
	case contract.FillEvent:
		if !n.applyFill(e.Fill, env) {
			n.fills.BufferEnvelope(e.Fill, env.Meta())
		}
	case contract.RejectEvent:
		atomic.AddInt64(&n.counters.Rejects, 1)
		venueOrderID := ""
		if o, ok := n.Cache.OrderByClientIDForAccount(n.accountID, e.ClientID); ok {
			o.Status = enums.StatusRejected
			o.RejectReason = e.Reason
			o.UpdatedAt = n.clk.Now()
			if err := n.Cache.UpsertOrderChecked(o); err != nil {
				n.Halt("order identity conflict: " + err.Error())
				break
			}
			if err := n.fills.MarkOrderTerminal(o); err != nil {
				n.Halt(err.Error())
			}
			venueOrderID = o.VenueOrderID
		}
		if n.Exec != nil {
			n.Exec.RejectInFlight(e.ClientID, venueOrderID, e.Reason, n.clk.Now())
		}
		n.notifyObserver(func(observer observ.Observer) { observer.OnReject(e.ClientID, e.Reason) })
	}
	n.recordEventLatency(latency.ChainExecution, env.Meta(), applied, time.Now())
}

func (n *TradingNode) onStreamGap(ctx context.Context, gap contract.StreamGapEvent) {
	if err := n.validateStreamGap(gap); err != nil {
		n.Halt(err.Error())
		return
	}

	n.reconcileMu.Lock()
	defer n.reconcileMu.Unlock()
	key := strings.TrimSpace(gap.StreamID)
	switch gap.Phase {
	case contract.StreamGapStarted:
		if gap.Generation <= n.lastStreamGaps[key] {
			return
		}
		if len(n.activeStreamGaps) == 0 {
			n.gapRestore = n.life.Snapshot()
		}
		n.activeStreamGaps[key] = gap.Generation
		n.lastStreamGaps[key] = gap.Generation
		if len(n.activeStreamGaps) > 1 {
			return
		}
		reason := "private stream gap: " + key
		if detail := strings.TrimSpace(gap.Reason); detail != "" {
			reason += ": " + detail
		}
		if err := n.life.Transition(lifecycle.NodeReconnecting, lifecycle.TradingReconciling, reason); err != nil {
			n.life.ForceFailed(err.Error())
			n.emitHealth("failed", err.Error())
			return
		}
		n.emitHealth("reconnecting", reason)
	case contract.StreamGapRecovered:
		generation, active := n.activeStreamGaps[key]
		if !active || generation != gap.Generation {
			return
		}
		delete(n.activeStreamGaps, key)
		if len(n.activeStreamGaps) != 0 {
			return
		}
		previous := n.gapRestore
		if trading, reason, restricted := strongestTradingRestriction(previous, n.life.Snapshot()); restricted {
			previous.Trading = trading
			previous.Reason = reason
		}
		rep, err := n.resync(ctx, "private stream recovery", false)
		if err != nil {
			return
		}
		if err := n.finishReconciliation(previous, rep, "private stream recovery complete", true); err != nil {
			n.life.ForceFailed(err.Error())
			n.emitHealth("failed", err.Error())
		}
		n.gapRestore = lifecycle.Snapshot{}
	}
}

func (n *TradingNode) validateStreamGap(gap contract.StreamGapEvent) error {
	if err := gap.Validate(); err != nil {
		return err
	}
	if accountID := strings.TrimSpace(gap.AccountID); accountID != "" && accountID != n.accountID {
		return fmt.Errorf("stream gap account id %q does not match runtime account id %q", accountID, n.accountID)
	}
	if n.clients.Execution != nil {
		wantVenue := strings.TrimSpace(n.clients.Execution.Capabilities().Venue)
		if venue := strings.TrimSpace(gap.Venue); venue != "" && wantVenue != "" && !strings.EqualFold(venue, wantVenue) {
			return fmt.Errorf("stream gap venue %q does not match execution venue %q", venue, wantVenue)
		}
	}
	return nil
}

func (n *TradingNode) applyFill(fill model.Fill, env contract.ExecEnvelope) bool {
	return n.applyFillResult(fill, env) != reconcile.FillApplyUnmatched
}

func (n *TradingNode) applyFillResult(fill model.Fill, env contract.ExecEnvelope) reconcile.FillApplyResult {
	resolvedFromInFlight := false
	if n.Exec != nil {
		if resolvedFill, ok := n.Exec.MatchFillInFlight(fill); ok {
			fill = resolvedFill
			resolvedFromInFlight = true
		}
	}
	o, ok, identityErr := n.orderForFill(fill)
	if identityErr != nil {
		n.Halt("fill order identity conflict: " + identityErr.Error())
		return reconcile.FillApplyConflict
	}
	materialized := false
	if !ok {
		if external, materializedOK := n.materializeExternalOrder(fill, resolvedFromInFlight); materializedOK {
			o = external
			ok = true
			materialized = true
		}
	}
	if !ok {
		return reconcile.FillApplyUnmatched
	}
	if fill.AccountID == "" {
		fill.AccountID = o.Request.AccountID
	}
	if fill.AccountID == "" {
		fill.AccountID = n.accountID
	}
	if o.Request.AccountID == "" {
		o.Request.AccountID = fill.AccountID
	}
	if fill.ClientID == "" {
		fill.ClientID = o.Request.ClientID
	}
	if fill.VenueOrderID == "" {
		fill.VenueOrderID = o.VenueOrderID
	}
	if fill.InstrumentID == (model.InstrumentID{}) {
		fill.InstrumentID = o.Request.InstrumentID
	}
	if fill.Side == enums.SideUnknown {
		fill.Side = o.Request.Side
	}
	applied, _, identityErr := n.fills.AcceptAppliedWithCoverageChecked(fill, func(willApply bool, prior decimal.Decimal) error {
		var materializedOrder *model.Order
		if materialized {
			materializedOrder = &o
		}
		committed, committedOK, commitErr := n.Cache.CommitAcceptedFill(
			n.accountID,
			fill,
			materializedOrder,
			willApply,
			prior,
			n.clk.Now(),
		)
		if commitErr != nil {
			return commitErr
		}
		if !committedOK {
			return cache.ErrFillOrderIdentityConflict
		}
		o = committed
		return nil
	})
	if identityErr != nil {
		n.Halt("fill order identity conflict: " + identityErr.Error())
		return reconcile.FillApplyConflict
	}
	if !applied {
		n.resolveOrderInFlight(o, fill.Timestamp)
		return reconcile.FillApplyDuplicate
	}
	if orderstate.IsTerminal(o.Status) {
		if err := n.fills.MarkOrderTerminal(o); err != nil {
			n.Halt("fill order identity conflict: " + err.Error())
			return reconcile.FillApplyConflict
		}
	}
	n.resolveOrderInFlight(o, fill.Timestamp)
	posSide := o.Request.PositionSide
	n.Portfolio.OnFill(fill, posSide)
	atomic.AddInt64(&n.counters.Fills, 1)
	if n.onFill != nil {
		n.onFill(fill)
	}
	if n.strat != nil {
		if n.stratCtx == nil {
			n.pendingStrategyFills = append(n.pendingStrategyFills, pendingStrategyFill{fill: fill, meta: env.Meta()})
		} else {
			n.stratCtx.SetCurrentEventMeta(env.Meta())
			n.strat.OnFill(n.stratCtx, fill)
		}
	}
	if n.obs != nil {
		if n.observerStarted {
			n.notifyObserver(func(observer observ.Observer) { observer.OnFill(fill) })
		} else {
			n.pendingObserverFills = append(n.pendingObserverFills, fill)
		}
	}
	return reconcile.FillApplyApplied
}

func (n *TradingNode) resolveOrderInFlight(order model.Order, at time.Time) {
	if n.Exec == nil {
		return
	}
	if at.IsZero() {
		at = order.UpdatedAt
	}
	if at.IsZero() {
		at = n.clk.Now()
	}
	n.Exec.ResolveOrderInFlight(order, at)
}

func (n *TradingNode) canonicalOrder(hint model.Order) model.Order {
	accountID := hint.Request.AccountID
	if accountID == "" {
		accountID = n.accountID
	}
	if hint.Request.ClientID != "" {
		if order, ok := n.Cache.OrderByClientIDForAccount(accountID, hint.Request.ClientID); ok {
			return order
		}
	}
	if hint.VenueOrderID != "" {
		if order, ok := n.Cache.OrderByVenueOrderIDForAccount(accountID, hint.VenueOrderID); ok {
			return order
		}
	}
	return hint
}

func (n *TradingNode) applyRecoveredFill(fill model.Fill, meta contract.EventMeta) reconcile.FillApplyResult {
	env := contract.NewExecEnvelopeWithMeta(contract.FillEvent{Fill: fill}, meta)
	return n.applyFillResult(fill, env)
}

func (n *TradingNode) orderForFill(fill model.Fill) (model.Order, bool, error) {
	return n.Cache.ResolveOrderForFill(n.accountID, fill)
}

func (n *TradingNode) syncFillOrderTerminal(hint model.Order) error {
	lookup := model.Fill{AccountID: hint.Request.AccountID}
	if hint.Request.ClientID != "" {
		lookup.ClientID = hint.Request.ClientID
	} else {
		lookup.VenueOrderID = hint.VenueOrderID
	}
	canonical, ok, err := n.Cache.ResolveOrderForFill(n.accountID, lookup)
	if err != nil || !ok || !orderstate.IsTerminal(canonical.Status) {
		return err
	}
	return n.fills.MarkOrderTerminal(canonical)
}

func (n *TradingNode) syncFillTerminalOrders() error {
	for _, order := range n.Cache.Orders() {
		if !orderstate.IsTerminal(order.Status) {
			continue
		}
		if err := n.fills.MarkOrderTerminal(order); err != nil {
			return fmt.Errorf("fill order identity conflict: %w", err)
		}
	}
	return nil
}

func (n *TradingNode) materializeExternalOrder(fill model.Fill, allowKnownClient bool) (model.Order, bool) {
	if fill.VenueOrderID == "" || fill.InstrumentID.Symbol == "" ||
		fill.Quantity.IsZero() {
		return model.Order{}, false
	}
	if fill.ClientID != "" && !allowKnownClient {
		return model.Order{}, false
	}
	accountID := strings.TrimSpace(fill.AccountID)
	if accountID == "" {
		accountID = n.accountID
	}
	clientID := fill.ClientID
	if clientID == "" {
		clientID = "external-" + accountID + "-" + fill.VenueOrderID
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
			AccountID:    accountID,
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
	return order, true
}

// onAccount applies a balance/position event to the cache. Runs on the bus
// goroutine.
func (n *TradingNode) onAccount(env contract.AccountEnvelope) {
	n.eventMu.Lock()
	defer n.eventMu.Unlock()
	applied := time.Now()
	payload, meta := n.normalizedAccountPayloadAndMeta(env)
	switch e := payload.(type) {
	case contract.BalanceEvent:
		if err := n.Cache.ApplyBalance(e.Balance); err != nil {
			n.life.ForceFailed(err.Error())
			n.emitHealth("failed", err.Error())
		}
	case contract.PositionEvent:
		n.Cache.UpsertPosition(e.Position)
	case contract.AccountStateEvent:
		if err := n.Cache.ApplyAccountStateAt(e.State, applied); err != nil {
			n.life.ForceFailed(err.Error())
			n.emitHealth("failed", err.Error())
		}
	}
	n.recordEventLatency(latency.ChainAccount, meta, applied, time.Now())
}

func (n *TradingNode) normalizedAccountPayloadAndMeta(env contract.AccountEnvelope) (contract.AccountEvent, contract.EventMeta) {
	meta := env.Meta()
	switch e := env.Payload.(type) {
	case contract.BalanceEvent:
		if e.Balance.AccountID == "" {
			e.Balance.AccountID = n.accountID
		}
		meta.AccountID = e.Balance.AccountID
		return e, meta
	case contract.PositionEvent:
		if e.Position.AccountID == "" {
			e.Position.AccountID = n.accountID
		}
		meta.AccountID = e.Position.AccountID
		return e, meta
	case contract.AccountStateEvent:
		if e.State.AccountID == "" {
			e.State.AccountID = n.accountID
		}
		if e.State.EventID == "" || meta.AccountID != e.State.AccountID {
			e.State.EventID = model.AccountStateEventID(e.State.Venue, e.State.AccountID, e.State.TsEvent)
		}
		meta.Venue = e.State.Venue
		meta.AccountID = e.State.AccountID
		meta.TsVenue = e.State.TsEvent
		meta.EventID = e.State.EventID
		return e, meta
	default:
		return env.Payload, meta
	}
}

// onMarket is the DataEngine: it writes the latest market snapshot to the cache
// and dispatches to the strategy. Runs on the bus goroutine.
func (n *TradingNode) onMarket(env contract.MarketEnvelope) {
	n.eventMu.Lock()
	defer n.eventMu.Unlock()
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
	case contract.ReferenceDataEvent:
		n.Cache.UpsertDerivativeReference(e.Snapshot)
		if n.strat != nil {
			if handler, ok := n.strat.(strategy.DerivativeReferenceHandler); ok {
				n.stratCtx.SetCurrentEventMeta(env.Meta())
				handler.OnDerivativeReference(n.stratCtx, e.Snapshot)
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
	n.notifyObserver(func(observer observ.Observer) { observer.OnLatency(lat) })
}
