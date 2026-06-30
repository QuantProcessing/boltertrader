package contract

import (
	"context"

	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

// MarketDataClient is the public market-data surface. Request/response methods
// return synchronously; streaming subscriptions deliver typed MarketEvents on
// Events().
type MarketDataClient interface {
	// InstrumentProvider exposes the venue's resolved instrument registry.
	InstrumentProvider() model.InstrumentProvider

	// OrderBook fetches a depth snapshot.
	OrderBook(ctx context.Context, id model.InstrumentID, depth int) (*model.OrderBook, error)
	// Bars fetches historical candles.
	Bars(ctx context.Context, id model.InstrumentID, interval string, limit int) ([]model.Bar, error)

	// SubscribeBook/Quotes/Trades begin streaming; updates arrive on Events().
	SubscribeBook(ctx context.Context, id model.InstrumentID) error
	SubscribeQuotes(ctx context.Context, id model.InstrumentID) error
	SubscribeTrades(ctx context.Context, id model.InstrumentID) error

	// Events is the demultiplexed stream of market push events.
	Events() <-chan MarketEvent
	// Close releases connections and closes Events().
	Close() error
}

// ExecutionClient is the order I/O surface. Submit is SYNCHRONOUS regardless of
// whether the underlying venue is blocking (Binance/OKX) or async (Hyperliquid's
// chan PostResult) — the adapter awaits the venue ack internally (deadline via
// the injected Clock) and returns the acknowledged order. Subsequent fills and
// state transitions arrive on Events().
type ExecutionClient interface {
	Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error)
	Cancel(ctx context.Context, id model.InstrumentID, venueOrderID string) error
	CancelAll(ctx context.Context, id model.InstrumentID) error
	Modify(ctx context.Context, id model.InstrumentID, venueOrderID string, newPrice, newQty decimal.Decimal) (*model.Order, error)
	// OpenOrders returns the open orders for a single instrument.
	OpenOrders(ctx context.Context, id model.InstrumentID) ([]model.Order, error)
	// OrderReports returns OPEN-order snapshots across ALL instruments in one
	// venue-wide query (no instrument filter). It is the reconciliation feed —
	// the equivalent of NautilusTrader's ExecutionMassStatus order reports — used
	// to rebuild local order state after a websocket gap or process restart,
	// including orders placed out-of-band that the cache has never seen.
	OrderReports(ctx context.Context) ([]model.Order, error)

	Events() <-chan ExecEvent
	Close() error
}

// AccountClient is the balance/position/account-state surface. Venues that
// return balance and positions in a single call (Hyperliquid's
// clearinghouseState) fan that one call into both getters inside the adapter.
type AccountClient interface {
	Balances(ctx context.Context) ([]model.AccountBalance, error)
	Positions(ctx context.Context) ([]model.Position, error)
	// SetLeverage sets leverage for an instrument (best-effort per venue).
	SetLeverage(ctx context.Context, id model.InstrumentID, leverage decimal.Decimal) error
	// SetMarginMode sets cross/isolated where supported; returns
	// errs.ErrNotSupported where inapplicable. mode is "cross" or "isolated".
	SetMarginMode(ctx context.Context, id model.InstrumentID, mode string) error

	Events() <-chan AccountEvent
	Close() error
}
