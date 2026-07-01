// Package runtimetest provides an in-memory fake venue that implements the
// core/contract interfaces. It lets the runtime be exercised end-to-end with no
// network, driven by a clock.Clock — the same shape a backtest matching engine
// will take in P9. It is intentionally simple: Submit immediately acks and the
// test pushes fills explicitly via EmitFill.
package runtimetest

import (
	"context"
	"fmt"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

// FakeExec is an in-memory ExecutionClient. Submit synchronously returns an
// acknowledged order (status New) and the test injects fills/order events via
// the Emit* helpers, which land on Events() exactly like a real venue push.
type FakeExec struct {
	events chan contract.ExecEvent
	seq    int64

	// reports is the canned venue-wide open-order snapshot returned by
	// OrderReports; set it to drive reconciliation tests.
	reports []model.Order
}

// NewFakeExec returns a FakeExec with a buffered event channel.
func NewFakeExec() *FakeExec {
	return &FakeExec{events: make(chan contract.ExecEvent, 256)}
}

func (f *FakeExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	f.seq++
	venueID := "v" + decimal.NewFromInt(f.seq).String()
	return &model.Order{
		Request:      req,
		VenueOrderID: venueID,
		Status:       enums.StatusNew,
	}, nil
}

func (f *FakeExec) Cancel(ctx context.Context, id model.InstrumentID, venueOrderID string) error {
	return nil
}
func (f *FakeExec) CancelAll(ctx context.Context, id model.InstrumentID) error { return nil }
func (f *FakeExec) Modify(ctx context.Context, id model.InstrumentID, venueOrderID string, newPrice, newQty decimal.Decimal) (*model.Order, error) {
	return nil, fmt.Errorf("fake execution amend is not modeled: %w", contract.ErrNotSupported)
}
func (f *FakeExec) OpenOrders(ctx context.Context, id model.InstrumentID) ([]model.Order, error) {
	out := make([]model.Order, 0, len(f.reports))
	for _, o := range f.reports {
		if o.Request.InstrumentID == id {
			out = append(out, o)
		}
	}
	return out, nil
}

// OrderReports returns the canned venue-wide open-order snapshot.
func (f *FakeExec) OrderReports(ctx context.Context) ([]model.Order, error) {
	return append([]model.Order(nil), f.reports...), nil
}

// SetOrderReports installs the venue-wide open-order snapshot returned by
// OrderReports/OpenOrders, simulating the venue's authoritative resting set.
func (f *FakeExec) SetOrderReports(orders ...model.Order) { f.reports = orders }

func (f *FakeExec) Events() <-chan contract.ExecEvent { return f.events }
func (f *FakeExec) Close() error                      { close(f.events); return nil }

// EmitOrder pushes an order lifecycle event.
func (f *FakeExec) EmitOrder(o model.Order) { f.events <- contract.OrderEvent{Order: o} }

// EmitFill pushes a fill event.
func (f *FakeExec) EmitFill(fill model.Fill) { f.events <- contract.FillEvent{Fill: fill} }

// FakeAccount is an in-memory AccountClient driven by Emit* helpers.
type FakeAccount struct {
	events    chan contract.AccountEvent
	balances  []model.AccountBalance
	positions []model.Position
}

// NewFakeAccount returns a FakeAccount with a buffered event channel.
func NewFakeAccount() *FakeAccount {
	return &FakeAccount{events: make(chan contract.AccountEvent, 256)}
}

func (f *FakeAccount) Balances(ctx context.Context) ([]model.AccountBalance, error) {
	return append([]model.AccountBalance(nil), f.balances...), nil
}
func (f *FakeAccount) Positions(ctx context.Context) ([]model.Position, error) {
	return append([]model.Position(nil), f.positions...), nil
}
func (f *FakeAccount) SetLeverage(ctx context.Context, id model.InstrumentID, lev decimal.Decimal) error {
	return nil
}
func (f *FakeAccount) SetMarginMode(ctx context.Context, id model.InstrumentID, mode string) error {
	return nil
}
func (f *FakeAccount) Events() <-chan contract.AccountEvent { return f.events }
func (f *FakeAccount) Close() error                         { close(f.events); return nil }

// SetSnapshots installs the account snapshots returned by Balances/Positions,
// simulating the venue's authoritative REST state for reconciliation.
func (f *FakeAccount) SetSnapshots(balances []model.AccountBalance, positions []model.Position) {
	f.balances = append([]model.AccountBalance(nil), balances...)
	f.positions = append([]model.Position(nil), positions...)
}

// EmitBalance pushes a balance event.
func (f *FakeAccount) EmitBalance(b model.AccountBalance) {
	f.events <- contract.BalanceEvent{Balance: b}
}

// EmitPosition pushes a position event.
func (f *FakeAccount) EmitPosition(p model.Position) {
	f.events <- contract.PositionEvent{Position: p}
}

// FakeMarket is an in-memory MarketDataClient driven by Emit* helpers. The
// Subscribe* methods are no-ops (the test pushes data directly). It also
// implements contract.Reconnectable so node.Reconnect can be exercised: each
// call increments Reconnects and connected flips to true.
type FakeMarket struct {
	events   chan contract.MarketEvent
	provider model.InstrumentProvider

	Reconnects int  // number of Reconnect calls
	connected  bool // reported by Connected; set true after Reconnect
}

// NewFakeMarket returns a FakeMarket with a buffered event channel.
func NewFakeMarket() *FakeMarket {
	return &FakeMarket{events: make(chan contract.MarketEvent, 1024)}
}

func (f *FakeMarket) InstrumentProvider() model.InstrumentProvider { return f.provider }
func (f *FakeMarket) OrderBook(ctx context.Context, id model.InstrumentID, depth int) (*model.OrderBook, error) {
	return nil, fmt.Errorf("fake market order book snapshots are not modeled: %w", contract.ErrNotSupported)
}
func (f *FakeMarket) Bars(ctx context.Context, id model.InstrumentID, interval string, limit int) ([]model.Bar, error) {
	return nil, fmt.Errorf("fake market historical bars are not modeled: %w", contract.ErrNotSupported)
}
func (f *FakeMarket) SubscribeBook(ctx context.Context, id model.InstrumentID) error   { return nil }
func (f *FakeMarket) SubscribeQuotes(ctx context.Context, id model.InstrumentID) error { return nil }
func (f *FakeMarket) SubscribeTrades(ctx context.Context, id model.InstrumentID) error { return nil }
func (f *FakeMarket) Events() <-chan contract.MarketEvent                              { return f.events }
func (f *FakeMarket) Close() error                                                     { close(f.events); return nil }

// Connected reports the simulated link state.
func (f *FakeMarket) Connected() bool { return f.connected }

// Reconnect simulates re-establishing the stream: it records the call and marks
// the link up.
func (f *FakeMarket) Reconnect(ctx context.Context) error {
	f.Reconnects++
	f.connected = true
	return nil
}

// EmitQuote pushes a top-of-book update.
func (f *FakeMarket) EmitQuote(q model.QuoteTick) { f.events <- contract.QuoteEvent{Quote: q} }

// EmitTrade pushes a public trade print.
func (f *FakeMarket) EmitTrade(t model.TradeTick) { f.events <- contract.TradeEvent{Trade: t} }
