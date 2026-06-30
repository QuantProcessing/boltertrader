// Package backtest provides an in-process matching-engine venue that implements
// the venue-neutral core/contract interfaces. Driven by a clock.SimulatedClock
// and a replayed stream of SimEvents (trades, funding settlements, mark prices),
// it lets the IDENTICAL strategy and TradingNode code that runs live also run a
// deterministic backtest — the payoff of the backtest/live parity constraint
// held since P1.
//
// Matching is bar/trade level: market orders fill immediately at the last seen
// price (adjusted by an optional slippage model); resting limit orders fill when
// a replayed trade's price crosses the limit. It is not an order-book-level
// simulator.
//
// On top of matching the venue models a linear, cross-margin perpetual account:
// maker/taker fees, average-cost positions with realized/unrealized PnL, leverage
// and initial-margin gating (orders that exceed free margin are rejected),
// funding settlements, and maintenance-margin liquidation. All of this lives in
// the simulated venue — the runtime sees only the same balance/position/fill
// events a live adapter would push, preserving parity. Capital effects engage
// only when StartBalance funds the account; an unfunded backtest is purely a
// matching/PnL harness.
//
// Because the three contract interfaces each declare Events() with a different
// element type, the venue exposes three thin clients (Execution/Market/Account)
// that share one matching engine.
package backtest

import (
	"context"
	"sync"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

// Config configures the backtest venue.
type Config struct {
	// FeeRate is the legacy single fee rate applied to a fill's notional
	// (e.g. 0.0005 = 5 bps). Deprecated: prefer MakerFeeRate/TakerFeeRate. When
	// the side-specific rate is zero, FeeRate is used as the fallback.
	FeeRate decimal.Decimal
	// MakerFeeRate and TakerFeeRate are the fee rates applied to a fill's
	// notional by liquidity side. Resting limit orders that are filled passively
	// pay the maker rate; market orders and any order that removes liquidity pay
	// the taker rate.
	MakerFeeRate decimal.Decimal
	TakerFeeRate decimal.Decimal
	// Slippage models adverse price movement on marketable (taker) fills. Nil
	// means no slippage; passive limit fills are never slipped.
	Slippage SlippageModel
	// DefaultLeverage is the leverage applied to instruments for which
	// SetLeverage was never called. Zero is treated as 1x.
	DefaultLeverage decimal.Decimal
	// MaintMarginRate is the maintenance-margin rate (e.g. 0.005 = 0.5%) applied
	// to position notional. A positive value ENABLES liquidation: when account
	// equity falls to or below the summed maintenance margin, all positions are
	// force-closed. Zero disables liquidation.
	MaintMarginRate decimal.Decimal
	// MaintMarginRates optionally overrides MaintMarginRate per instrument,
	// keyed by InstrumentID.String().
	MaintMarginRates map[string]decimal.Decimal
	// OnLiquidation, if set, is invoked (off the venue lock) when a liquidation
	// occurs. It is the venue's observable liquidation signal; the forced-close
	// fills also flow through the normal execution/account event streams.
	OnLiquidation func(Liquidation)
	// StartBalance seeds the account balance reported by Balances.
	StartBalance model.AccountBalance
	// Instruments registers the venue's tradable markets so the matching engine
	// can resolve each contract's multiplier (and, in later steps, its margin
	// parameters). Unregistered instruments default to a multiplier of 1.
	Instruments []*model.Instrument
}

// restingOrder is a limit order waiting to be matched.
type restingOrder struct {
	req       model.OrderRequest
	venueID   string
	remaining decimal.Decimal
}

// Venue is the matching engine. It is not itself a contract client; use
// Execution(), Market(), and Account() to obtain the three clients.
type Venue struct {
	clk clock.Clock
	cfg Config

	mu          sync.Mutex
	lastPrice   map[string]decimal.Decimal   // by InstrumentID, last replayed trade price (the mark)
	instruments map[string]*model.Instrument // by InstrumentID.String() (immutable after New)
	instrList   []*model.Instrument          // registration order, for deterministic All()
	resting     []*restingOrder
	wallet      map[string]decimal.Decimal // settlement balance by currency
	positions   map[string]*simPosition    // open positions by instrument|side
	leverages   map[string]decimal.Decimal // per-instrument leverage by InstrumentID.String()
	marginOn    bool                       // gate cross-margin checks on a funded account
	seq         int64

	exec    *execClient
	market  *marketClient
	account *acctClient
}

// NewVenue builds a backtest venue on the given clock.
func NewVenue(clk clock.Clock, cfg Config) *Venue {
	v := &Venue{
		clk:         clk,
		cfg:         cfg,
		lastPrice:   make(map[string]decimal.Decimal),
		instruments: make(map[string]*model.Instrument, len(cfg.Instruments)),
		wallet:      make(map[string]decimal.Decimal),
		positions:   make(map[string]*simPosition),
		leverages:   make(map[string]decimal.Decimal),
	}
	for _, inst := range cfg.Instruments {
		if inst != nil {
			v.instruments[inst.ID.String()] = inst
			v.instrList = append(v.instrList, inst)
		}
	}
	if cfg.StartBalance.Currency != "" {
		v.wallet[cfg.StartBalance.Currency] = cfg.StartBalance.Total
	}
	// Cross-margin gating applies only to a funded account: a backtest that
	// models no capital (no start balance) is not margin-constrained.
	v.marginOn = cfg.StartBalance.Currency != "" && cfg.StartBalance.Total.IsPositive()
	v.exec = &execClient{v: v, events: make(chan contract.ExecEvent, 4096)}
	v.market = &marketClient{v: v, events: make(chan contract.MarketEvent, 4096)}
	v.account = &acctClient{v: v, events: make(chan contract.AccountEvent, 4096)}
	return v
}

// Execution returns the venue's ExecutionClient.
func (v *Venue) Execution() contract.ExecutionClient { return v.exec }

// Market returns the venue's MarketDataClient.
func (v *Venue) Market() contract.MarketDataClient { return v.market }

// Account returns the venue's AccountClient.
func (v *Venue) Account() contract.AccountClient { return v.account }

func (v *Venue) nextVenueID() string {
	v.seq++
	return "bt-" + decimal.NewFromInt(v.seq).String()
}

// Feed replays one trade tick: it advances the clock to the tick time, emits a
// TradeEvent, records the last price, and matches resting limit orders against
// it. Called by the Runner; safe to call directly in tests.
func (v *Venue) Feed(tick model.TradeTick) {
	v.advanceTo(tick.Timestamp)

	v.market.events <- contract.TradeEvent{Trade: tick}

	v.mu.Lock()
	v.lastPrice[tick.InstrumentID.String()] = tick.Price
	execEvs, acctEvs := v.matchLocked(tick)
	// Mark the instrument's open positions to the new price (mark-to-market),
	// after any fills this tick produced.
	acctEvs = append(acctEvs, v.markPositionLocked(tick.InstrumentID)...)
	// Then check whether the new mark leaves the account underwater.
	liqExec, liqAcct, liq := v.liquidateIfNeededLocked()
	execEvs = append(execEvs, liqExec...)
	acctEvs = append(acctEvs, liqAcct...)
	v.mu.Unlock()

	for _, e := range execEvs {
		v.exec.events <- e
	}
	for _, a := range acctEvs {
		v.account.events <- a
	}
	if liq != nil && v.cfg.OnLiquidation != nil {
		v.cfg.OnLiquidation(*liq)
	}
}

// matchLocked fills resting orders the tick price crosses. Caller holds v.mu.
// Returns the exec events (OrderEvent + FillEvent pairs) and the account events
// (balance changes) to emit after unlock.
func (v *Venue) matchLocked(tick model.TradeTick) ([]contract.ExecEvent, []contract.AccountEvent) {
	var execEvs []contract.ExecEvent
	var acctEvs []contract.AccountEvent
	type matchedOrder struct {
		req       model.OrderRequest
		venueID   string
		remaining decimal.Decimal
		fillPx    decimal.Decimal
	}
	var matched []matchedOrder
	kept := v.resting[:0]
	for _, ro := range v.resting {
		if ro.req.InstrumentID != tick.InstrumentID {
			kept = append(kept, ro)
			continue
		}
		// A buy limit fills when the market trades at or below its price; a sell
		// limit fills when the market trades at or above its price.
		crosses := (ro.req.Side == enums.SideBuy && tick.Price.LessThanOrEqual(ro.req.Price)) ||
			(ro.req.Side == enums.SideSell && tick.Price.GreaterThanOrEqual(ro.req.Price))
		if !crosses {
			kept = append(kept, ro)
			continue
		}
		// Fill fully at the limit price (deterministic; price improvement is
		// out of scope for bar/trade-level matching). A resting limit order that
		// the market crosses is filled passively, so it pays the maker fee.
		fillPx := ro.req.Price
		matched = append(matched, matchedOrder{
			req:       ro.req,
			venueID:   ro.venueID,
			remaining: ro.remaining,
			fillPx:    fillPx,
		})
	}
	v.resting = kept
	for _, m := range matched {
		filled := model.Order{
			Request:      m.req,
			VenueOrderID: m.venueID,
			Status:       enums.StatusFilled,
			FilledQty:    m.remaining,
			AvgFillPrice: m.fillPx,
			UpdatedAt:    v.clk.Now(),
		}
		fill := v.fillEvent(m.req, m.venueID, m.fillPx, m.remaining, enums.LiqMaker)
		execEvs = append(execEvs, contract.OrderEvent{Order: filled}, fill)
		acctEvs = append(acctEvs, v.applyFillLocked(fill.Fill, m.req.PositionSide)...)
	}
	return execEvs, acctEvs
}

func (v *Venue) fillEvent(req model.OrderRequest, venueID string, px, qty decimal.Decimal, liq enums.LiquiditySide) contract.FillEvent {
	rate := v.feeRate(liq)
	fee := decimal.Zero
	if !rate.IsZero() {
		fee = v.notional(req.InstrumentID, px, qty).Mul(rate)
	}
	return contract.FillEvent{Fill: model.Fill{
		InstrumentID: req.InstrumentID,
		VenueOrderID: venueID,
		ClientID:     req.ClientID,
		Side:         req.Side,
		Liquidity:    liq,
		Price:        px,
		Quantity:     qty,
		Fee:          fee,
		FeeCurrency:  v.settleCcy(req.InstrumentID),
		Timestamp:    v.clk.Now(),
	}}
}

// Close closes all three event channels.
func (v *Venue) Close() {
	close(v.exec.events)
	close(v.market.events)
	close(v.account.events)
}

// --- execClient -------------------------------------------------------------

type execClient struct {
	v      *Venue
	events chan contract.ExecEvent
}

func (c *execClient) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	v := c.v
	v.mu.Lock()
	venueID := v.nextVenueID()
	now := v.clk.Now()
	order := model.Order{Request: req, VenueOrderID: venueID, CreatedAt: now, UpdatedAt: now}

	if req.Type == enums.TypeMarket {
		px, ok := v.lastPrice[req.InstrumentID.String()]
		if !ok {
			order.Status = enums.StatusRejected
			order.RejectReason = "no market price available"
			v.mu.Unlock()
			c.events <- contract.OrderEvent{Order: order}
			return &order, nil
		}
		// A market order removes liquidity at the last price, adjusted by the
		// configured slippage model, and pays the taker fee.
		px = v.applySlippage(req.Side, px)
		if reason, rejected := v.marginRejectLocked(req, px); rejected {
			order.Status = enums.StatusRejected
			order.RejectReason = reason
			v.mu.Unlock()
			c.events <- contract.OrderEvent{Order: order}
			c.events <- contract.RejectEvent{ClientID: req.ClientID, Reason: reason}
			return &order, nil
		}
		order.Status = enums.StatusFilled
		order.FilledQty = req.Quantity
		order.AvgFillPrice = px
		fill := v.fillEvent(req, venueID, px, req.Quantity, enums.LiqTaker)
		acctEvs := v.applyFillLocked(fill.Fill, req.PositionSide)
		acctEvs = append(acctEvs, v.markPositionLocked(req.InstrumentID)...)
		v.mu.Unlock()
		c.events <- contract.OrderEvent{Order: order}
		c.events <- fill
		for _, a := range acctEvs {
			v.account.events <- a
		}
		return &order, nil
	}

	if reason, rejected := v.marginRejectLocked(req, req.Price); rejected {
		order.Status = enums.StatusRejected
		order.RejectReason = reason
		v.mu.Unlock()
		c.events <- contract.OrderEvent{Order: order}
		c.events <- contract.RejectEvent{ClientID: req.ClientID, Reason: reason}
		return &order, nil
	}
	order.Status = enums.StatusNew
	v.resting = append(v.resting, &restingOrder{req: req, venueID: venueID, remaining: req.Quantity})
	v.mu.Unlock()
	c.events <- contract.OrderEvent{Order: order}
	return &order, nil
}

func (c *execClient) Cancel(ctx context.Context, id model.InstrumentID, venueOrderID string) error {
	v := c.v
	v.mu.Lock()
	var ev *contract.OrderEvent
	for i, ro := range v.resting {
		if ro.venueID == venueOrderID {
			v.resting = append(v.resting[:i], v.resting[i+1:]...)
			ev = &contract.OrderEvent{Order: model.Order{Request: ro.req, VenueOrderID: venueOrderID, Status: enums.StatusCanceled, UpdatedAt: v.clk.Now()}}
			break
		}
	}
	v.mu.Unlock()
	if ev != nil {
		c.events <- *ev
	}
	return nil
}

func (c *execClient) CancelAll(ctx context.Context, id model.InstrumentID) error {
	v := c.v
	v.mu.Lock()
	var evs []contract.ExecEvent
	kept := v.resting[:0]
	for _, ro := range v.resting {
		if ro.req.InstrumentID == id {
			evs = append(evs, contract.OrderEvent{Order: model.Order{Request: ro.req, VenueOrderID: ro.venueID, Status: enums.StatusCanceled, UpdatedAt: v.clk.Now()}})
			continue
		}
		kept = append(kept, ro)
	}
	v.resting = kept
	v.mu.Unlock()
	for _, e := range evs {
		c.events <- e
	}
	return nil
}

func (c *execClient) Modify(ctx context.Context, id model.InstrumentID, venueOrderID string, newPrice, newQty decimal.Decimal) (*model.Order, error) {
	v := c.v
	v.mu.Lock()
	for i, ro := range v.resting {
		if ro.venueID == venueOrderID {
			newReq := ro.req
			newReq.Price = newPrice
			newReq.Quantity = newQty

			resting := v.resting
			v.resting = append(append([]*restingOrder{}, resting[:i]...), resting[i+1:]...)
			reason, rejected := v.marginRejectLocked(newReq, newPrice)
			v.resting = resting

			if rejected {
				order := model.Order{
					Request:      newReq,
					VenueOrderID: venueOrderID,
					Status:       enums.StatusRejected,
					UpdatedAt:    v.clk.Now(),
					RejectReason: reason,
				}
				v.mu.Unlock()
				c.events <- contract.OrderEvent{Order: order}
				c.events <- contract.RejectEvent{ClientID: newReq.ClientID, Reason: reason}
				return &order, nil
			}

			ro.req = newReq
			ro.remaining = newQty
			order := model.Order{Request: ro.req, VenueOrderID: venueOrderID, Status: enums.StatusNew, UpdatedAt: v.clk.Now()}
			v.mu.Unlock()
			return &order, nil
		}
	}
	v.mu.Unlock()
	return nil, nil
}

func (c *execClient) OpenOrders(ctx context.Context, id model.InstrumentID) ([]model.Order, error) {
	v := c.v
	v.mu.Lock()
	defer v.mu.Unlock()
	var out []model.Order
	for _, ro := range v.resting {
		if ro.req.InstrumentID == id {
			out = append(out, model.Order{Request: ro.req, VenueOrderID: ro.venueID, Status: enums.StatusNew})
		}
	}
	return out, nil
}

// OrderReports returns every resting order across all instruments (venue-wide).
func (c *execClient) OrderReports(ctx context.Context) ([]model.Order, error) {
	v := c.v
	v.mu.Lock()
	defer v.mu.Unlock()
	out := make([]model.Order, 0, len(v.resting))
	for _, ro := range v.resting {
		out = append(out, model.Order{Request: ro.req, VenueOrderID: ro.venueID, Status: enums.StatusNew})
	}
	return out, nil
}

func (c *execClient) Events() <-chan contract.ExecEvent { return c.events }
func (c *execClient) Close() error                      { return nil }

// --- marketClient -----------------------------------------------------------

type marketClient struct {
	v      *Venue
	events chan contract.MarketEvent
}

func (c *marketClient) InstrumentProvider() model.InstrumentProvider { return venueProvider{c.v} }
func (c *marketClient) OrderBook(ctx context.Context, id model.InstrumentID, depth int) (*model.OrderBook, error) {
	return nil, nil
}
func (c *marketClient) Bars(ctx context.Context, id model.InstrumentID, interval string, limit int) ([]model.Bar, error) {
	return nil, nil
}
func (c *marketClient) SubscribeBook(ctx context.Context, id model.InstrumentID) error   { return nil }
func (c *marketClient) SubscribeQuotes(ctx context.Context, id model.InstrumentID) error { return nil }
func (c *marketClient) SubscribeTrades(ctx context.Context, id model.InstrumentID) error { return nil }
func (c *marketClient) Events() <-chan contract.MarketEvent                              { return c.events }
func (c *marketClient) Close() error                                                     { return nil }

// --- acctClient -------------------------------------------------------------

type acctClient struct {
	v      *Venue
	events chan contract.AccountEvent
}

func (c *acctClient) Balances(ctx context.Context) ([]model.AccountBalance, error) {
	return c.v.snapshotBalances(), nil
}
func (c *acctClient) Positions(ctx context.Context) ([]model.Position, error) {
	return c.v.snapshotPositions(), nil
}
func (c *acctClient) SetLeverage(ctx context.Context, id model.InstrumentID, lev decimal.Decimal) error {
	v := c.v
	v.mu.Lock()
	v.leverages[id.String()] = lev
	v.mu.Unlock()
	return nil
}
func (c *acctClient) SetMarginMode(ctx context.Context, id model.InstrumentID, mode string) error {
	return nil
}
func (c *acctClient) Events() <-chan contract.AccountEvent { return c.events }
func (c *acctClient) Close() error                         { return nil }

// venueProvider resolves instruments from the venue's registry (Config.Instruments).
// The registry is immutable after NewVenue, so reads need no lock.
type venueProvider struct{ v *Venue }

func (p venueProvider) Instrument(id model.InstrumentID) (*model.Instrument, bool) {
	inst, ok := p.v.instruments[id.String()]
	return inst, ok
}

func (p venueProvider) All() []*model.Instrument {
	out := make([]*model.Instrument, len(p.v.instrList))
	copy(out, p.v.instrList)
	return out
}

// compile-time interface checks
var (
	_ contract.ExecutionClient  = (*execClient)(nil)
	_ contract.MarketDataClient = (*marketClient)(nil)
	_ contract.AccountClient    = (*acctClient)(nil)
)
