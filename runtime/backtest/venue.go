// Package backtest provides an in-process matching-engine venue that implements
// the venue-neutral core/contract interfaces. Driven by a clock.SimulatedClock
// and a replayed stream of trade ticks, it lets the IDENTICAL strategy and
// TradingNode code that runs live also run a deterministic backtest — the
// payoff of the backtest/live parity constraint held since P1.
//
// Matching is bar/trade level: market orders fill immediately at the last seen
// price; resting limit orders fill when a replayed trade's price crosses the
// limit. This is simple and deterministic — sufficient to validate parity and
// strategy logic, not an exchange-grade simulator.
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
	// FeeRate is applied to every fill's notional (e.g. 0.0005 = 5 bps). Zero
	// means no fees.
	FeeRate decimal.Decimal
	// StartBalance seeds the account balance reported by Balances.
	StartBalance model.AccountBalance
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

	mu        sync.Mutex
	lastPrice map[string]decimal.Decimal // by InstrumentID, last replayed trade price
	resting   []*restingOrder
	seq       int64

	exec    *execClient
	market  *marketClient
	account *acctClient
}

// NewVenue builds a backtest venue on the given clock.
func NewVenue(clk clock.Clock, cfg Config) *Venue {
	v := &Venue{
		clk:       clk,
		cfg:       cfg,
		lastPrice: make(map[string]decimal.Decimal),
	}
	v.exec = &execClient{v: v, events: make(chan contract.ExecEvent, 4096)}
	v.market = &marketClient{v: v, events: make(chan contract.MarketEvent, 4096)}
	v.account = &acctClient{v: v, events: make(chan contract.AccountEvent, 256)}
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
	if sc, ok := v.clk.(*clock.SimulatedClock); ok && !tick.Timestamp.IsZero() {
		sc.AdvanceTo(tick.Timestamp)
	}

	v.market.events <- contract.TradeEvent{Trade: tick}

	v.mu.Lock()
	v.lastPrice[tick.InstrumentID.String()] = tick.Price
	matched := v.matchLocked(tick)
	v.mu.Unlock()

	for _, m := range matched {
		v.exec.events <- m
	}
}

// matchLocked fills resting orders the tick price crosses. Caller holds v.mu.
// Returns the exec events (OrderEvent + FillEvent pairs) to emit after unlock.
func (v *Venue) matchLocked(tick model.TradeTick) []contract.ExecEvent {
	var events []contract.ExecEvent
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
		// out of scope for bar/trade-level matching).
		fillPx := ro.req.Price
		filled := model.Order{
			Request:      ro.req,
			VenueOrderID: ro.venueID,
			Status:       enums.StatusFilled,
			FilledQty:    ro.remaining,
			AvgFillPrice: fillPx,
			UpdatedAt:    v.clk.Now(),
		}
		events = append(events, contract.OrderEvent{Order: filled})
		events = append(events, v.fillEvent(ro.req, ro.venueID, fillPx, ro.remaining))
	}
	v.resting = kept
	return events
}

func (v *Venue) fillEvent(req model.OrderRequest, venueID string, px, qty decimal.Decimal) contract.FillEvent {
	fee := decimal.Zero
	if !v.cfg.FeeRate.IsZero() {
		fee = px.Mul(qty).Mul(v.cfg.FeeRate)
	}
	liq := enums.LiqTaker
	if req.Type == enums.TypeLimit {
		liq = enums.LiqMaker
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
		FeeCurrency:  v.cfg.StartBalance.Currency,
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
		order.Status = enums.StatusFilled
		order.FilledQty = req.Quantity
		order.AvgFillPrice = px
		fill := v.fillEvent(req, venueID, px, req.Quantity)
		v.mu.Unlock()
		c.events <- contract.OrderEvent{Order: order}
		c.events <- fill
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
	defer v.mu.Unlock()
	for _, ro := range v.resting {
		if ro.venueID == venueOrderID {
			ro.req.Price = newPrice
			ro.req.Quantity = newQty
			ro.remaining = newQty
			return &model.Order{Request: ro.req, VenueOrderID: venueOrderID, Status: enums.StatusNew, UpdatedAt: v.clk.Now()}, nil
		}
	}
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

func (c *marketClient) InstrumentProvider() model.InstrumentProvider { return emptyProvider{} }
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
	if c.v.cfg.StartBalance.Currency == "" {
		return nil, nil
	}
	return []model.AccountBalance{c.v.cfg.StartBalance}, nil
}
func (c *acctClient) Positions(ctx context.Context) ([]model.Position, error) { return nil, nil }
func (c *acctClient) SetLeverage(ctx context.Context, id model.InstrumentID, lev decimal.Decimal) error {
	return nil
}
func (c *acctClient) SetMarginMode(ctx context.Context, id model.InstrumentID, mode string) error {
	return nil
}
func (c *acctClient) Events() <-chan contract.AccountEvent { return c.events }
func (c *acctClient) Close() error                         { return nil }

// emptyProvider is a no-op InstrumentProvider; backtest strategies supply
// instrument ids directly.
type emptyProvider struct{}

func (emptyProvider) Instrument(model.InstrumentID) (*model.Instrument, bool) { return nil, false }
func (emptyProvider) All() []*model.Instrument                                { return nil }

// compile-time interface checks
var (
	_ contract.ExecutionClient  = (*execClient)(nil)
	_ contract.MarketDataClient = (*marketClient)(nil)
	_ contract.AccountClient    = (*acctClient)(nil)
)
