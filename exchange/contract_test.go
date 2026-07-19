package exchange

import (
	"context"
	"reflect"
	"sort"
	"testing"
)

type expectedSpotContract interface {
	Instruments(context.Context) ([]Instrument, error)
	OrderBook(context.Context, OrderBookRequest) (OrderBook, error)
	Candles(context.Context, CandlesRequest) (CandlePage, error)
	PublicTrades(context.Context, PublicTradesRequest) (PublicTradePage, error)
	PlaceOrder(context.Context, PlaceOrderRequest) (OrderAcknowledgement, error)
	CancelOrder(context.Context, CancelOrderRequest) (OrderAcknowledgement, error)
	OpenOrders(context.Context, OpenOrdersRequest) (OrderPage, error)
	OrderHistory(context.Context, OrderHistoryRequest) (OrderPage, error)
	Fills(context.Context, FillsRequest) (FillPage, error)
	Balances(context.Context) ([]Balance, error)
	SpotAccount(context.Context) (SpotAccount, error)
	WebSocket() SpotWebSocket
	Close() error
}

type expectedPerpContract interface {
	Instruments(context.Context) ([]Instrument, error)
	OrderBook(context.Context, OrderBookRequest) (OrderBook, error)
	Candles(context.Context, CandlesRequest) (CandlePage, error)
	PublicTrades(context.Context, PublicTradesRequest) (PublicTradePage, error)
	PlaceOrder(context.Context, PlaceOrderRequest) (OrderAcknowledgement, error)
	CancelOrder(context.Context, CancelOrderRequest) (OrderAcknowledgement, error)
	OpenOrders(context.Context, OpenOrdersRequest) (OrderPage, error)
	OrderHistory(context.Context, OrderHistoryRequest) (OrderPage, error)
	Fills(context.Context, FillsRequest) (FillPage, error)
	Balances(context.Context) ([]Balance, error)
	PerpAccount(context.Context) (PerpAccount, error)
	Positions(context.Context, PositionsRequest) ([]Position, error)
	FundingRate(context.Context, FundingRateRequest) (FundingRate, error)
	FundingRateHistory(context.Context, FundingRateHistoryRequest) (FundingRatePage, error)
	SetLeverage(context.Context, SetLeverageRequest) (Leverage, error)
	WebSocket() PerpWebSocket
	Close() error
}

type expectedSpotAccountContract interface {
	Balances(context.Context) ([]Balance, error)
	SpotAccount(context.Context) (SpotAccount, error)
}

type expectedPerpAccountContract interface {
	Balances(context.Context) ([]Balance, error)
	PerpAccount(context.Context) (PerpAccount, error)
	Positions(context.Context, PositionsRequest) ([]Position, error)
}

type expectedPerpReferenceContract interface {
	FundingRate(context.Context, FundingRateRequest) (FundingRate, error)
	FundingRateHistory(context.Context, FundingRateHistoryRequest) (FundingRatePage, error)
	SetLeverage(context.Context, SetLeverageRequest) (Leverage, error)
}

var (
	_ expectedSpotContract          = (SpotClient)(nil)
	_ expectedPerpContract          = (PerpClient)(nil)
	_ expectedSpotAccountContract   = (SpotAccountREST)(nil)
	_ expectedPerpAccountContract   = (PerpAccountREST)(nil)
	_ expectedPerpReferenceContract = (PerpREST)(nil)
	_ MarketREST                    = (SpotClient)(nil)
	_ OrderREST                     = (SpotClient)(nil)
	_ SpotAccountREST               = (SpotClient)(nil)
	_ MarketREST                    = (PerpClient)(nil)
	_ OrderREST                     = (PerpClient)(nil)
	_ PerpAccountREST               = (PerpClient)(nil)
	_ PerpREST                      = (PerpClient)(nil)
)

type expectedSpotWebSocketContract interface {
	WatchOrderBook(context.Context, WatchRequest) (Subscription[BookEvent], error)
	WatchBBO(context.Context, WatchRequest) (Subscription[BBOEvent], error)
	WatchPublicTrades(context.Context, WatchRequest) (Subscription[PublicTradeEvent], error)
	WatchCandles(context.Context, WatchCandlesRequest) (Subscription[CandleEvent], error)
	WatchOrders(context.Context, WatchRequest) (Subscription[OrderEvent], error)
	WatchFills(context.Context, WatchRequest) (Subscription[FillEvent], error)
	WatchBalances(context.Context, WatchAccountRequest) (Subscription[BalanceEvent], error)
	PlaceOrder(context.Context, PlaceOrderRequest) (OrderAcknowledgement, error)
	CancelOrder(context.Context, CancelOrderRequest) (OrderAcknowledgement, error)
	Close() error
}

type expectedPerpWebSocketContract interface {
	expectedSpotWebSocketContract
	WatchPositions(context.Context, WatchRequest) (Subscription[PositionEvent], error)
	WatchMarkPrice(context.Context, WatchRequest) (Subscription[MarkPriceEvent], error)
	WatchFundingRate(context.Context, WatchRequest) (Subscription[FundingRateEvent], error)
}

var (
	_ expectedSpotWebSocketContract = (SpotWebSocket)(nil)
	_ expectedPerpWebSocketContract = (PerpWebSocket)(nil)
)

func TestProductClientMethodSets(t *testing.T) {
	spot := reflect.TypeOf((*SpotClient)(nil)).Elem()
	perp := reflect.TypeOf((*PerpClient)(nil)).Elem()

	assertMethodNames(t, spot, []string{
		"Balances", "CancelOrder", "Candles", "Close", "Fills", "Instruments", "OpenOrders",
		"OrderBook", "OrderHistory", "PlaceOrder", "PublicTrades", "SpotAccount",
		"WebSocket",
	})
	assertMethodNames(t, perp, []string{
		"Balances", "CancelOrder", "Candles", "Close", "Fills", "FundingRate",
		"FundingRateHistory", "Instruments", "OpenOrders", "OrderBook",
		"OrderHistory", "PerpAccount", "PlaceOrder", "Positions", "PublicTrades",
		"SetLeverage", "WebSocket",
	})

	for _, forbidden := range []string{
		"Positions", "PerpAccount", "SetLeverage", "SetMarginMode",
		"PositionSide", "SubscribeOrder", "WatchOrders",
	} {
		if _, ok := spot.MethodByName(forbidden); ok {
			t.Fatalf("SpotClient unexpectedly exports %s", forbidden)
		}
	}
}

func TestAccountRESTMethodSets(t *testing.T) {
	spot := reflect.TypeOf((*SpotAccountREST)(nil)).Elem()
	perp := reflect.TypeOf((*PerpAccountREST)(nil)).Elem()

	assertMethodNames(t, spot, []string{"Balances", "SpotAccount"})
	assertMethodNames(t, perp, []string{"Balances", "PerpAccount", "Positions"})
}

func TestWebSocketFacetMethodSets(t *testing.T) {
	spot := reflect.TypeOf((*SpotWebSocket)(nil)).Elem()
	perp := reflect.TypeOf((*PerpWebSocket)(nil)).Elem()

	assertMethodNames(t, spot, []string{
		"CancelOrder", "Close", "PlaceOrder", "WatchBBO", "WatchBalances",
		"WatchCandles", "WatchFills", "WatchOrderBook", "WatchOrders", "WatchPublicTrades",
	})
	assertMethodNames(t, perp, []string{
		"CancelOrder", "Close", "PlaceOrder", "WatchBBO", "WatchBalances",
		"WatchCandles", "WatchFills", "WatchFundingRate", "WatchMarkPrice",
		"WatchOrderBook", "WatchOrders", "WatchPositions", "WatchPublicTrades",
	})
}

func assertMethodNames(t *testing.T, typ reflect.Type, want []string) {
	t.Helper()
	got := make([]string, 0, typ.NumMethod())
	for index := 0; index < typ.NumMethod(); index++ {
		got = append(got, typ.Method(index).Name)
	}
	sort.Strings(got)
	sort.Strings(want)
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("%s methods = %v, want %v", typ, got, want)
	}
}
