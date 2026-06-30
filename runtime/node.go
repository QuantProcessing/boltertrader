// Package runtime wires the venue-neutral building blocks — the event bus,
// authoritative cache, portfolio, and execution engine — into a single
// TradingNode. The node depends ONLY on core/contract + core/clock, so the same
// node drives a live adapter or a simulated backtest venue unchanged.
package runtime

import (
	"context"
	"sync/atomic"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/bus"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/data"
	"github.com/QuantProcessing/boltertrader/runtime/exec"
	"github.com/QuantProcessing/boltertrader/runtime/observ"
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

	clients Clients
	clk     clock.Clock
	bus     *bus.Bus

	// channels are retained for synchronous backtest stepping (ProcessAvailable).
	marketCh  <-chan contract.MarketEvent
	execCh    <-chan contract.ExecEvent
	accountCh <-chan contract.AccountEvent

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

// NewNode builds a TradingNode over the given clients and clock.
func NewNode(clients Clients, clk clock.Clock, idPrefix string, opts ...Option) *TradingNode {
	if clk == nil {
		clk = clock.NewRealClock()
	}
	c := cache.New()
	pf := portfolio.New()

	var marketCh <-chan contract.MarketEvent
	var execCh <-chan contract.ExecEvent
	var accountCh <-chan contract.AccountEvent
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
		bus:         bus.New(marketCh, execCh, accountCh),
		marketCh:    marketCh,
		execCh:      execCh,
		accountCh:   accountCh,
		aggregators: make(map[string]*data.BarAggregator),
	}
	if clients.Execution != nil {
		n.Exec = exec.New(clients.Execution, c, clk, idPrefix)
	}
	// Reconcile whatever authoritative sources exist: balances/positions from the
	// account client, open orders from the execution client. Either may be nil.
	if clients.Account != nil || clients.Execution != nil {
		n.reconciler = reconcile.New(clients.Account, clients.Execution, c)
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

// Metrics returns a point-in-time snapshot of trading state and event counters.
// Safe to call from any goroutine (reads go through the cache/portfolio locks).
func (n *TradingNode) Metrics() observ.Metrics {
	return observ.Metrics{
		OpenOrders:     len(n.Cache.OpenOrders()),
		Positions:      len(n.Cache.Positions()),
		RealizedPnL:    n.Portfolio.RealizedPnL(),
		RealizedPnLNet: n.Portfolio.RealizedPnLNetFees(),
		Fees:           n.Portfolio.Fees(),
		OrdersSeen:     atomic.LoadInt64(&n.counters.Orders),
		FillsSeen:      atomic.LoadInt64(&n.counters.Fills),
		RejectsSeen:    atomic.LoadInt64(&n.counters.Rejects),
	}
}

// Resync reconciles the cache against venue REST snapshots. Call it at startup
// and after a reconnect. It is a no-op (nil report, nil error) if the node has
// no account client.
func (n *TradingNode) Resync(ctx context.Context) (reconcile.Report, error) {
	if n.reconciler == nil {
		return reconcile.Report{}, nil
	}
	return n.reconciler.Run(ctx)
}

// Reconnect forces a reconnect on any client that supports it
// (contract.Reconnectable), then reconciles. Clients that auto-reconnect
// internally are skipped. Returns the reconciliation report.
func (n *TradingNode) Reconnect(ctx context.Context) (reconcile.Report, error) {
	for _, cl := range []any{n.clients.Market, n.clients.Execution, n.clients.Account} {
		if rc, ok := cl.(contract.Reconnectable); ok {
			if err := rc.Reconnect(ctx); err != nil {
				return reconcile.Report{}, err
			}
		}
	}
	return n.Resync(ctx)
}

// Run consumes events until ctx is cancelled or all client streams close. It
// calls the strategy's OnStart before the loop and OnStop after. It blocks; run
// with `go node.Run(ctx)` or from a dedicated goroutine.
func (n *TradingNode) Run(ctx context.Context) {
	n.Start(ctx)
	defer n.Stop()
	n.bus.Run(ctx, bus.Handlers{
		OnExec:    n.onExec,
		OnAccount: n.onAccount,
		OnMarket:  n.onMarket,
	})
}

// Start builds the strategy context and fires OnStart. It is called by Run, and
// directly by the backtest driver when stepping synchronously.
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

// ProcessAvailable drains all currently-ready events to quiescence on the
// CALLING goroutine. It processes in a fixed priority — market, then exec, then
// account — looping until no channel has a ready event, so the order is
// deterministic (unlike a multi-way select). This is the single-threaded
// stepping used by the backtest driver: feed one input, then ProcessAvailable,
// then feed the next. Because the runtime's Submit is synchronous, any orders a
// strategy places while handling an event are already enqueued before this
// returns, so the loop reaches a true fixpoint.
//
// A closed channel is retired (set to nil) so it is never selected again — a
// closed channel is perpetually ready and would otherwise spin forever yielding
// zero-value events.
//
// Do NOT call this concurrently with Run; pick one execution model.
func (n *TradingNode) ProcessAvailable() {
	market, exec, account := n.marketCh, n.execCh, n.accountCh
	for {
		progressed := false
		if market != nil {
			select {
			case ev, ok := <-market:
				if !ok {
					market = nil
				} else {
					n.onMarket(ev)
					progressed = true
				}
			default:
			}
		}
		if exec != nil {
			select {
			case ev, ok := <-exec:
				if !ok {
					exec = nil
				} else {
					n.onExec(ev)
					progressed = true
				}
			default:
			}
		}
		if account != nil {
			select {
			case ev, ok := <-account:
				if !ok {
					account = nil
				} else {
					n.onAccount(ev)
					progressed = true
				}
			default:
			}
		}
		if !progressed {
			return
		}
	}
}

// onExec applies an execution event to cache and portfolio. Runs on the bus
// goroutine.
func (n *TradingNode) onExec(ev contract.ExecEvent) {
	switch e := ev.(type) {
	case contract.OrderEvent:
		n.Cache.UpsertOrder(e.Order)
		atomic.AddInt64(&n.counters.Orders, 1)
		if n.obs != nil {
			n.obs.OnOrder(e.Order)
		}
	case contract.FillEvent:
		// Determine the position side the fill belongs to. For one-way
		// accounts this is PosNet; hedge accounts carry it on the order.
		posSide := enums.PosNet
		if o, ok := n.Cache.Order(e.Fill.ClientID); ok {
			posSide = o.Request.PositionSide
		}
		n.Portfolio.OnFill(e.Fill, posSide)
		atomic.AddInt64(&n.counters.Fills, 1)
		if n.onFill != nil {
			n.onFill(e.Fill)
		}
		if n.strat != nil {
			n.strat.OnFill(n.stratCtx, e.Fill)
		}
		if n.obs != nil {
			n.obs.OnFill(e.Fill)
		}
	case contract.RejectEvent:
		atomic.AddInt64(&n.counters.Rejects, 1)
		if o, ok := n.Cache.Order(e.ClientID); ok {
			o.Status = enums.StatusRejected
			o.RejectReason = e.Reason
			o.UpdatedAt = n.clk.Now()
			n.Cache.UpsertOrder(o)
		}
		if n.obs != nil {
			n.obs.OnReject(e.ClientID, e.Reason)
		}
	}
}

// onAccount applies a balance/position event to the cache. Runs on the bus
// goroutine.
func (n *TradingNode) onAccount(ev contract.AccountEvent) {
	switch e := ev.(type) {
	case contract.BalanceEvent:
		n.Cache.UpsertBalance(e.Balance)
	case contract.PositionEvent:
		n.Cache.UpsertPosition(e.Position)
	}
}

// onMarket is the DataEngine: it writes the latest market snapshot to the cache
// and dispatches to the strategy. Runs on the bus goroutine.
func (n *TradingNode) onMarket(ev contract.MarketEvent) {
	switch e := ev.(type) {
	case contract.QuoteEvent:
		n.Cache.UpsertQuote(e.Quote)
		if n.strat != nil {
			n.strat.OnQuote(n.stratCtx, e.Quote)
		}
	case contract.BookEvent:
		n.Cache.UpsertBook(e.Book)
	case contract.TradeEvent:
		n.Cache.UpsertTrade(e.Trade)
		if n.strat != nil {
			n.strat.OnTrade(n.stratCtx, e.Trade)
		}
		if agg := n.aggregators[e.Trade.InstrumentID.String()]; agg != nil {
			if bar, ok := agg.OnTrade(e.Trade); ok && n.strat != nil {
				n.strat.OnBar(n.stratCtx, bar)
			}
		}
	}
}
