package exchange

import "context"

// MarketREST is the REST-only public market-data surface shared by spot and
// perpetual products.
type MarketREST interface {
	ResolveInstrument(context.Context, string) (Instrument, error)
	RefreshInstruments(context.Context) ([]Instrument, error)
	Instruments(context.Context) ([]Instrument, error)
	OrderBook(context.Context, OrderBookRequest) (OrderBook, error)
	Candles(context.Context, CandlesRequest) (CandlePage, error)
	PublicTrades(context.Context, PublicTradesRequest) (PublicTradePage, error)
}

// OrderREST is the normalized REST order and lifecycle surface.
type OrderREST interface {
	PlaceOrder(context.Context, PlaceOrderRequest) (OrderAcknowledgement, error)
	CancelOrder(context.Context, CancelOrderRequest) (OrderAcknowledgement, error)
	OpenOrders(context.Context, OpenOrdersRequest) (OrderPage, error)
	OrderHistory(context.Context, OrderHistoryRequest) (OrderPage, error)
	Fills(context.Context, FillsRequest) (FillPage, error)
}

// SpotAccountREST is the REST account surface available to spot clients.
type SpotAccountREST interface {
	Balances(context.Context) ([]Balance, error)
	SpotAccount(context.Context) (SpotAccount, error)
}

// PerpAccountREST is the REST account surface available to perpetual clients.
type PerpAccountREST interface {
	Balances(context.Context) ([]Balance, error)
	PerpAccount(context.Context) (PerpAccount, error)
	Positions(context.Context, PositionsRequest) ([]Position, error)
}

// PerpREST is the Perp-only reference-data and leverage surface.
type PerpREST interface {
	FundingRate(context.Context, FundingRateRequest) (FundingRate, error)
	FundingRateHistory(context.Context, FundingRateHistoryRequest) (FundingRatePage, error)
	SetLeverage(context.Context, SetLeverageRequest) (Leverage, error)
}

// PerpMarkPriceREST is an optional public mark-price REST facet. It is
// discovered by type assertion and is intentionally not embedded in PerpClient.
type PerpMarkPriceREST interface {
	MarkPrice(context.Context, Instrument) (MarkPriceEvent, error)
}

// FundingPaymentREST is an optional account funding-payment facet. It is
// discovered by type assertion and is intentionally not embedded in PerpClient.
type FundingPaymentREST interface {
	FundingPayments(context.Context, FundingPaymentHistoryRequest) (FundingPaymentPage, error)
}

// PerpFundingPaymentREST names the optional perp account funding-payment facet.
type PerpFundingPaymentREST interface {
	FundingPaymentREST
}

// SpotClient is the compile-time surface for spot products. It intentionally
// excludes derivative-only account and position methods.
type SpotClient interface {
	MarketREST
	OrderREST
	SpotAccountREST
	WebSocket() SpotWebSocket
	Close() error
}

// PerpClient is the compile-time surface for perpetual products.
type PerpClient interface {
	MarketREST
	OrderREST
	PerpAccountREST
	PerpREST
	WebSocket() PerpWebSocket
	Close() error
}
