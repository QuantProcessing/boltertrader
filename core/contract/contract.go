package contract

import (
	"context"

	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

// MarketDataClient is the public market-data surface. Request/response methods
// return synchronously; streaming subscriptions deliver typed event envelopes on
// Events().
type MarketDataClient interface {
	// Capabilities declares the venue/product/report/streaming surface.
	Capabilities() Capabilities

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

	// Events is the demultiplexed stream of market push event envelopes.
	Events() <-chan MarketEnvelope
	// Close releases connections and closes Events().
	Close() error
}

// DerivativeReferenceDataClient is an optional public derivative reference-data
// surface implemented by market clients for perps/futures. Streamed or polled
// updates arrive as ReferenceDataEvent values on the normal market Events()
// channel after SubscribeReference.
type DerivativeReferenceDataClient interface {
	ReferenceSnapshot(ctx context.Context, id model.InstrumentID) (model.DerivativeReferenceSnapshot, error)
	SubscribeReference(ctx context.Context, id model.InstrumentID) error
}

// OpenInterestClient is an optional query-only surface for current open
// interest. Phase one intentionally does not put OI in market events or cache.
type OpenInterestClient interface {
	OpenInterest(ctx context.Context, id model.InstrumentID) (model.OpenInterestSnapshot, error)
}

// FundingHistoryClient is optional. Venues that cannot provide normalized
// funding history must return ErrNotSupported.
type FundingHistoryClient interface {
	FundingHistory(ctx context.Context, id model.InstrumentID, query model.FundingRateHistoryQuery) ([]model.FundingRateHistoryEntry, error)
}

// OpenInterestHistoryClient is optional. Phase one requires current OI only.
type OpenInterestHistoryClient interface {
	OpenInterestHistory(ctx context.Context, id model.InstrumentID, query model.OpenInterestHistoryQuery) ([]model.OpenInterestHistoryEntry, error)
}

// ExecutionClient is the order I/O surface. Submit is SYNCHRONOUS regardless of
// whether the underlying venue is blocking (Binance/OKX) or async (Hyperliquid's
// chan PostResult) — the adapter awaits the venue ack internally (deadline via
// the injected Clock) and returns the acknowledged order. Subsequent fills and
// state transitions arrive on Events().
type ExecutionClient interface {
	// Capabilities declares the venue/product/report/streaming surface.
	Capabilities() Capabilities

	Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error)
	Cancel(ctx context.Context, id model.InstrumentID, venueOrderID string) error
	CancelAll(ctx context.Context, id model.InstrumentID) error
	Modify(ctx context.Context, id model.InstrumentID, venueOrderID string, newPrice, newQty decimal.Decimal) (*model.Order, error)
	// OpenOrders returns the open orders for a single instrument.
	OpenOrders(ctx context.Context, id model.InstrumentID) ([]model.Order, error)
	GenerateOrderStatusReports(ctx context.Context, query model.OrderStatusReportQuery) ([]model.OrderStatusReport, error)
	GenerateOrderStatusReport(ctx context.Context, query model.SingleOrderStatusQuery) (*model.OrderStatusReport, error)
	GenerateFillReports(ctx context.Context, query model.FillReportQuery) ([]model.FillReport, error)
	GeneratePositionReports(ctx context.Context, query model.PositionReportQuery) ([]model.PositionReport, error)
	GenerateExecutionMassStatus(ctx context.Context, query model.MassStatusQuery) (*model.ExecutionMassStatus, error)

	Events() <-chan ExecEnvelope
	Close() error
}

// AccountClient is the balance/position/account-state surface. Venues that
// return balance and positions in a single call (Hyperliquid's
// clearinghouseState) fan that one call into both getters inside the adapter.
type AccountClient interface {
	// Capabilities declares the venue/product/report/streaming surface.
	Capabilities() Capabilities

	Balances(ctx context.Context) ([]model.AccountBalance, error)
	Positions(ctx context.Context) ([]model.Position, error)
	// SetLeverage sets leverage for an instrument (best-effort per venue).
	SetLeverage(ctx context.Context, id model.InstrumentID, leverage decimal.Decimal) error
	// SetMarginMode sets cross/isolated where supported; returns
	// contract.ErrNotSupported where inapplicable. mode is "cross" or "isolated".
	SetMarginMode(ctx context.Context, id model.InstrumentID, mode string) error

	Events() <-chan AccountEnvelope
	Close() error
}

// AccountStateReporter is the migration guard for the NT-style account loop.
// AccountClient keeps legacy Balances/Positions methods while adapters migrate;
// runtime reconciliation can type-assert this optional interface and prefer the
// authoritative account state when available.
type AccountStateReporter interface {
	AccountState(ctx context.Context) (model.AccountState, error)
}

// AccountIDProvider is the runtime identity contract for adapters which have
// resolved their logical account scope. Runtime clients may implement it on the
// execution client, account client, or both; if both expose an id they must
// agree before the node is allowed to start.
type AccountIDProvider interface {
	AccountID() string
}

// PreTradeLease owns adapter-local prepared state created during venue-backed
// pre-trade validation. Release must be safe to call more than once.
type PreTradeLease interface {
	Release()
}

// PreparedExecutionClient consumes adapter-local state produced by a successful
// VenuePreTradeValidator call. Runtime uses this optional surface only when risk
// returned a non-nil lease, so Submit can remain the direct-call fallback.
type PreparedExecutionClient interface {
	SubmitPrepared(ctx context.Context, req model.OrderRequest) (*model.Order, error)
}

// VenuePreTradeValidator performs the venue's authoritative read-only capacity
// and payload validation after runtime-local risk checks have passed.
type VenuePreTradeValidator interface {
	ValidatePreTrade(
		ctx context.Context,
		req model.OrderRequest,
		inst *model.Instrument,
	) (PreTradeLease, error)
}
