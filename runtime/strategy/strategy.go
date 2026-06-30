// Package strategy defines the callback-style interface a trading strategy
// implements. The runtime invokes these methods on the bus goroutine, so a
// strategy sees a single-threaded, deterministic event order — identical in
// backtest and live. Strategies act through the Context, never by holding an
// adapter or SDK reference, which is what preserves backtest/live parity.
package strategy

import (
	"context"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/portfolio"
	"github.com/shopspring/decimal"
)

// Submitter is the narrow order-submission surface the runtime grants a
// strategy. It is satisfied by the execution engine.
type Submitter interface {
	Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error)
	Cancel(ctx context.Context, clientID string) error
}

// Context is handed to a strategy on every callback. It exposes read-only state
// (cache, portfolio, clock) and the order-submission surface. It carries no
// venue or adapter reference, so a strategy is identical in backtest and live.
type Context struct {
	Ctx       context.Context
	Clock     clock.Clock
	Cache     *cache.Cache
	Portfolio *portfolio.Portfolio
	Orders    Submitter
}

// Buy submits a market or limit buy. A zero price means market.
func (c *Context) Buy(id model.InstrumentID, qty, price decimal.Decimal) (*model.Order, error) {
	return c.Orders.Submit(c.Ctx, order(id, qty, price, true))
}

// Sell submits a market or limit sell. A zero price means market.
func (c *Context) Sell(id model.InstrumentID, qty, price decimal.Decimal) (*model.Order, error) {
	return c.Orders.Submit(c.Ctx, order(id, qty, price, false))
}

func order(id model.InstrumentID, qty, price decimal.Decimal, buy bool) model.OrderRequest {
	req := model.OrderRequest{InstrumentID: id, Quantity: qty}
	if buy {
		req.Side = enums.SideBuy
	} else {
		req.Side = enums.SideSell
	}
	if price.IsZero() {
		req.Type = enums.TypeMarket
	} else {
		req.Type = enums.TypeLimit
		req.TIF = enums.TifGTC
		req.Price = price
	}
	return req
}

// Strategy is the callback interface a strategy implements. All methods are
// optional via the embedded Base; override only what you need. Every callback
// runs on the runtime's single bus goroutine.
type Strategy interface {
	// OnStart is called once before any event is delivered.
	OnStart(c *Context)
	// OnBar is called for each completed bar the strategy is subscribed to.
	OnBar(c *Context, bar model.Bar)
	// OnQuote is called for each top-of-book update.
	OnQuote(c *Context, q model.QuoteTick)
	// OnTrade is called for each public trade print.
	OnTrade(c *Context, t model.TradeTick)
	// OnFill is called after each of our fills is applied to cache/portfolio.
	OnFill(c *Context, f model.Fill)
	// OnStop is called once during shutdown.
	OnStop(c *Context)
}

// Base is a no-op Strategy that concrete strategies embed so they only override
// the callbacks they care about.
type Base struct{}

func (Base) OnStart(*Context)              {}
func (Base) OnBar(*Context, model.Bar)     {}
func (Base) OnQuote(*Context, model.QuoteTick) {}
func (Base) OnTrade(*Context, model.TradeTick) {}
func (Base) OnFill(*Context, model.Fill)   {}
func (Base) OnStop(*Context)               {}
