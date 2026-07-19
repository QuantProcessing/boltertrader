package factoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

type openAPIRoundTripFunc func(*http.Request) (*http.Response, error)

func (function openAPIRoundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func openAPIJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

type binanceOpenAPIRouter struct {
	mu          sync.Mutex
	seen        map[string]int
	placeShapes []url.Values
}

func newBinanceOpenAPIRouter() *binanceOpenAPIRouter {
	return &binanceOpenAPIRouter{seen: make(map[string]int)}
}

func (router *binanceOpenAPIRouter) RoundTrip(request *http.Request) (*http.Response, error) {
	router.mu.Lock()
	router.seen[request.Method+" "+request.URL.Path]++
	query := request.URL.Query()
	router.mu.Unlock()

	if request.Method == http.MethodPost && (request.URL.Path == "/api/v3/order" || request.URL.Path == "/fapi/v1/order") {
		router.mu.Lock()
		router.placeShapes = append(router.placeShapes, query)
		router.mu.Unlock()
		status, price, filled, quote := "NEW", query.Get("price"), "0", "0"
		if query.Get("type") == "MARKET" {
			status, price, filled, quote = "FILLED", "0", query.Get("quantity"), "100"
		}
		if request.URL.Path == "/api/v3/order" {
			return openAPIJSONResponse(`{"symbol":"BTCUSDT","orderId":11,"clientOrderId":"` + query.Get("newClientOrderId") + `","transactTime":1720000000000,"price":"` + price + `","origQty":"` + query.Get("quantity") + `","executedQty":"` + filled + `","cummulativeQuoteQty":"` + quote + `","status":"` + status + `","timeInForce":"` + query.Get("timeInForce") + `","type":"` + query.Get("type") + `","side":"BUY"}`), nil
		}
		return openAPIJSONResponse(`{"symbol":"BTCUSDT","orderId":21,"clientOrderId":"` + query.Get("newClientOrderId") + `","price":"` + price + `","origQty":"` + query.Get("quantity") + `","executedQty":"` + filled + `","cumQty":"` + filled + `","cumQuote":"` + quote + `","avgPrice":"100","status":"` + status + `","timeInForce":"` + query.Get("timeInForce") + `","type":"` + query.Get("type") + `","side":"BUY","positionSide":"BOTH","reduceOnly":` + query.Get("reduceOnly") + `,"updateTime":1720000000000}`), nil
	}

	switch request.URL.Path {
	case "/api/v3/exchangeInfo":
		return openAPIJSONResponse(`{"symbols":[{"symbol":"BTCUSDT","status":"TRADING","baseAsset":"BTC","quoteAsset":"USDT","filters":[{"filterType":"PRICE_FILTER","tickSize":"0.1"},{"filterType":"LOT_SIZE","stepSize":"0.001","minQty":"0.001"},{"filterType":"MIN_NOTIONAL","minNotional":"5"}]}]}`), nil
	case "/api/v3/depth":
		return openAPIJSONResponse(`{"lastUpdateId":7,"bids":[["99","1"]],"asks":[["101","2"]]}`), nil
	case "/api/v3/klines":
		return openAPIJSONResponse(`[[1720000000000,"100","101","99","100.5","3",1720000059999]]`), nil
	case "/api/v3/trades":
		return openAPIJSONResponse(`[{"id":1,"price":"100","qty":"0.1","quoteQty":"10","time":1720000000000,"isBuyerMaker":false,"isBestMatch":true}]`), nil
	case "/api/v3/order":
		if request.Method == http.MethodDelete {
			return openAPIJSONResponse(`{"symbol":"BTCUSDT","origClientOrderId":"101","orderId":11,"clientOrderId":"cancel","status":"CANCELED"}`), nil
		}
	case "/api/v3/openOrders":
		return openAPIJSONResponse(`[{"symbol":"BTCUSDT","orderId":11,"clientOrderId":"101","price":"99","origQty":"1","executedQty":"0","cummulativeQuoteQty":"0","status":"NEW","timeInForce":"GTC","type":"LIMIT","side":"BUY"}]`), nil
	case "/api/v3/allOrders":
		return openAPIJSONResponse(`[{"symbol":"BTCUSDT","orderId":10,"clientOrderId":"100","price":"99","origQty":"1","executedQty":"1","cummulativeQuoteQty":"99","status":"FILLED","timeInForce":"GTC","type":"LIMIT","side":"BUY"}]`), nil
	case "/api/v3/myTrades":
		return openAPIJSONResponse(`[{"symbol":"BTCUSDT","id":1,"orderId":10,"price":"99","qty":"1","quoteQty":"99","commission":"0.001","commissionAsset":"BTC","time":1720000000000,"isBuyer":true,"isMaker":false}]`), nil
	case "/api/v3/account":
		return openAPIJSONResponse(`{"balances":[{"asset":"USDT","free":"100","locked":"2"}]}`), nil
	case "/fapi/v1/exchangeInfo":
		return openAPIJSONResponse(`{"symbols":[{"symbol":"BTCUSDT","contractType":"PERPETUAL","status":"TRADING","baseAsset":"BTC","quoteAsset":"USDT","marginAsset":"USDT","filters":[{"filterType":"PRICE_FILTER","tickSize":"0.1"},{"filterType":"LOT_SIZE","stepSize":"0.001","minQty":"0.001"},{"filterType":"MIN_NOTIONAL","notional":"5"}]}]}`), nil
	case "/fapi/v1/depth":
		return openAPIJSONResponse(`{"lastUpdateId":8,"T":1720000000000,"bids":[["99","1"]],"asks":[["101","2"]]}`), nil
	case "/fapi/v1/klines":
		return openAPIJSONResponse(`[[1720000000000,"100","101","99","100.5","3",1720000059999]]`), nil
	case "/fapi/v1/aggTrades":
		return openAPIJSONResponse(`[{"a":2,"p":"100","q":"0.2","T":1720000000000,"m":true}]`), nil
	case "/fapi/v1/order":
		if request.Method == http.MethodDelete {
			return openAPIJSONResponse(`{"symbol":"BTCUSDT","orderId":21,"clientOrderId":"101","price":"99","origQty":"1","executedQty":"0","avgPrice":"0","status":"CANCELED","timeInForce":"GTC","type":"LIMIT","side":"BUY","positionSide":"BOTH","reduceOnly":false,"updateTime":1720000000000}`), nil
		}
	case "/fapi/v1/openOrders":
		return openAPIJSONResponse(`[{"symbol":"BTCUSDT","orderId":21,"clientOrderId":"101","price":"99","origQty":"1","executedQty":"0","avgPrice":"0","status":"NEW","timeInForce":"GTC","type":"LIMIT","side":"BUY","positionSide":"BOTH","reduceOnly":false,"updateTime":1720000000000}]`), nil
	case "/fapi/v1/allOrders":
		return openAPIJSONResponse(`[{"symbol":"BTCUSDT","orderId":20,"clientOrderId":"100","price":"99","origQty":"1","executedQty":"1","avgPrice":"99","status":"FILLED","timeInForce":"GTC","type":"LIMIT","side":"BUY","positionSide":"BOTH","reduceOnly":true,"updateTime":1720000000000}]`), nil
	case "/fapi/v1/userTrades":
		return openAPIJSONResponse(`[{"symbol":"BTCUSDT","id":2,"orderId":20,"price":"99","qty":"1","quoteQty":"99","commission":"0.01","commissionAsset":"USDT","time":1720000000000,"side":"BUY","maker":false,"positionSide":"BOTH"}]`), nil
	case "/fapi/v2/balance":
		return openAPIJSONResponse(`[{"asset":"USDT","balance":"100","availableBalance":"90"}]`), nil
	case "/fapi/v2/account":
		return openAPIJSONResponse(`{"totalMarginBalance":"100","maxWithdrawAmount":"90","totalInitialMargin":"10","totalUnrealizedProfit":"1","assets":[{"asset":"USDT","walletBalance":"100","availableBalance":"90"}]}`), nil
	case "/fapi/v2/positionRisk":
		return openAPIJSONResponse(`[{"symbol":"BTCUSDT","positionAmt":"1","entryPrice":"99","markPrice":"100","unRealizedProfit":"1","liquidationPrice":"50","leverage":"5","isolatedMargin":"0","positionSide":"BOTH"}]`), nil
	case "/fapi/v1/premiumIndex":
		return openAPIJSONResponse(`{"symbol":"BTCUSDT","markPrice":"100","lastFundingRate":"0.0001","nextFundingTime":1720003600000,"time":1720000000000}`), nil
	case "/fapi/v1/fundingRate":
		return openAPIJSONResponse(`[{"symbol":"BTCUSDT","fundingRate":"0.0001","fundingTime":1720000000000,"markPrice":"100"}]`), nil
	case "/fapi/v1/leverage":
		return openAPIJSONResponse(`{"symbol":"BTCUSDT","leverage":5,"maxNotionalValue":"1000000"}`), nil
	}
	return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{"code":-1,"msg":"unexpected OpenAPI route"}`)), Header: make(http.Header)}, nil
}

func TestOpenAPIBinanceRESTExecutionMatrix(t *testing.T) {
	router := newBinanceOpenAPIRouter()
	httpClient := &http.Client{Transport: router}
	settings := Settings{Endpoint: "https://openapi.invalid", Environment: "demo", HTTPClient: httpClient}
	ctx := context.Background()

	spot := NewBinanceSpot("key", "secret", settings)
	assertSpotReadMatrix(t, ctx, spot, "BTC-USDT")
	exerciseOpenAPIOrderBranches(t, ctx, exchange.ProductSpot, spot, "BTC-USDT")
	if _, err := spot.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: "BTC-USDT", OrderID: "11"}); err != nil {
		t.Fatalf("spot CancelOrder: %v", err)
	}
	assertSpotLifecycleMatrix(t, ctx, spot, "BTC-USDT")

	perp := NewBinanceUSDPerp("key", "secret", settings)
	assertPerpReadMatrix(t, ctx, perp, "BTC-USDT")
	exerciseOpenAPIOrderBranches(t, ctx, exchange.ProductPerp, perp, "BTC-USDT")
	if _, err := perp.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: "BTC-USDT", OrderID: "21"}); err != nil {
		t.Fatalf("perp CancelOrder: %v", err)
	}
	assertPerpLifecycleMatrix(t, ctx, perp, "BTC-USDT")

	assertBinanceOpenAPIPlaceShapes(t, router.placeShapes)
}

func exerciseOpenAPIOrderBranches(t *testing.T, ctx context.Context, product exchange.Product, client any, instrument string) {
	t.Helper()
	cases := []exchange.PlaceOrderRequest{
		{Instrument: instrument, ClientOrderID: "101", Side: exchange.SideBuy, Type: exchange.OrderTypeMarket, Quantity: decimal.NewFromInt(1)},
		{Instrument: instrument, ClientOrderID: "102", Side: exchange.SideBuy, Type: exchange.OrderTypeLimit, Quantity: decimal.NewFromInt(1), LimitPrice: decimal.NewFromInt(99), LimitPolicy: exchange.LimitPolicyResting},
		{Instrument: instrument, ClientOrderID: "103", Side: exchange.SideBuy, Type: exchange.OrderTypeLimit, Quantity: decimal.NewFromInt(1), LimitPrice: decimal.NewFromInt(99), LimitPolicy: exchange.LimitPolicyIOC},
		{Instrument: instrument, ClientOrderID: "104", Side: exchange.SideBuy, Type: exchange.OrderTypeLimit, Quantity: decimal.NewFromInt(1), LimitPrice: decimal.NewFromInt(99), LimitPolicy: exchange.LimitPolicyPostOnly},
	}
	for index := range cases {
		if product == exchange.ProductPerp {
			cases[index].ReduceOnly = true
			ack, err := client.(exchange.PerpClient).PlaceOrder(ctx, cases[index])
			if err != nil {
				t.Fatalf("perp PlaceOrder %s/%s: %v", cases[index].Type, cases[index].LimitPolicy, err)
			}
			if ack.OrderType != cases[index].Type {
				t.Errorf("perp ack order type = %q, want %q", ack.OrderType, cases[index].Type)
			}
		} else {
			ack, err := client.(exchange.SpotClient).PlaceOrder(ctx, cases[index])
			if err != nil {
				t.Fatalf("spot PlaceOrder %s/%s: %v", cases[index].Type, cases[index].LimitPolicy, err)
			}
			if ack.OrderType != cases[index].Type {
				t.Errorf("spot ack order type = %q, want %q", ack.OrderType, cases[index].Type)
			}
		}
	}
}

func assertSpotReadMatrix(t *testing.T, ctx context.Context, client exchange.SpotClient, instrument string) {
	t.Helper()
	start := time.UnixMilli(1720000000000)
	end := start.Add(time.Minute)
	if rows, err := client.Instruments(ctx); err != nil || len(rows) != 1 {
		t.Fatalf("Instruments: rows=%d err=%v", len(rows), err)
	}
	if row, err := client.OrderBook(ctx, exchange.OrderBookRequest{Instrument: instrument, Limit: 5}); err != nil || len(row.Bids) != 1 {
		t.Fatalf("OrderBook: bids=%d err=%v", len(row.Bids), err)
	}
	if rows, err := client.Candles(ctx, exchange.CandlesRequest{Instrument: instrument, Interval: "1m", Start: start, End: end, Limit: 1}); err != nil || len(rows.Candles) != 1 {
		t.Fatalf("Candles: rows=%d err=%v", len(rows.Candles), err)
	}
	if rows, err := client.PublicTrades(ctx, exchange.PublicTradesRequest{Instrument: instrument, Limit: 1}); err != nil || len(rows.Trades) != 1 {
		t.Fatalf("PublicTrades: rows=%d err=%v", len(rows.Trades), err)
	}
}

func assertSpotLifecycleMatrix(t *testing.T, ctx context.Context, client exchange.SpotClient, instrument string) {
	t.Helper()
	if rows, err := client.OpenOrders(ctx, exchange.OpenOrdersRequest{Instrument: instrument, Limit: 1}); err != nil || len(rows.Orders) != 1 || rows.Page.Limit != 1 {
		t.Fatalf("OpenOrders: rows=%d err=%v", len(rows.Orders), err)
	}
	if rows, err := client.OrderHistory(ctx, exchange.OrderHistoryRequest{Instrument: instrument, Limit: 10}); err != nil || len(rows.Orders) != 1 {
		t.Fatalf("OrderHistory: rows=%d err=%v", len(rows.Orders), err)
	}
	if rows, err := client.Fills(ctx, exchange.FillsRequest{Instrument: instrument, Limit: 10}); err != nil || len(rows.Fills) != 1 {
		t.Fatalf("Fills: rows=%d err=%v", len(rows.Fills), err)
	}
	if rows, err := client.Balances(ctx); err != nil || len(rows) != 1 {
		t.Fatalf("Balances: rows=%d err=%v", len(rows), err)
	}
	if row, err := client.SpotAccount(ctx); err != nil || len(row.Balances) != 1 {
		t.Fatalf("SpotAccount: balances=%d err=%v", len(row.Balances), err)
	}
}

func assertPerpReadMatrix(t *testing.T, ctx context.Context, client exchange.PerpClient, instrument string) {
	t.Helper()
	assertSpotLikePerpReadMatrix(t, ctx, client, instrument)
}

func assertSpotLikePerpReadMatrix(t *testing.T, ctx context.Context, client exchange.PerpClient, instrument string) {
	t.Helper()
	start := time.UnixMilli(1720000000000)
	end := start.Add(time.Minute)
	if rows, err := client.Instruments(ctx); err != nil || len(rows) != 1 {
		t.Fatalf("Instruments: rows=%d err=%v", len(rows), err)
	}
	if row, err := client.OrderBook(ctx, exchange.OrderBookRequest{Instrument: instrument, Limit: 5}); err != nil || len(row.Bids) != 1 {
		t.Fatalf("OrderBook: bids=%d err=%v", len(row.Bids), err)
	}
	if rows, err := client.Candles(ctx, exchange.CandlesRequest{Instrument: instrument, Interval: "1m", Start: start, End: end, Limit: 1}); err != nil || len(rows.Candles) != 1 {
		t.Fatalf("Candles: rows=%d err=%v", len(rows.Candles), err)
	}
	if rows, err := client.PublicTrades(ctx, exchange.PublicTradesRequest{Instrument: instrument, Limit: 1}); err != nil || len(rows.Trades) != 1 {
		t.Fatalf("PublicTrades: rows=%d err=%v", len(rows.Trades), err)
	}
}

func assertPerpLifecycleMatrix(t *testing.T, ctx context.Context, client exchange.PerpClient, instrument string) {
	t.Helper()
	if rows, err := client.OpenOrders(ctx, exchange.OpenOrdersRequest{Instrument: instrument, Limit: 1}); err != nil || len(rows.Orders) != 1 || rows.Page.Limit != 1 {
		t.Fatalf("OpenOrders: rows=%d err=%v", len(rows.Orders), err)
	}
	if rows, err := client.OrderHistory(ctx, exchange.OrderHistoryRequest{Instrument: instrument, Limit: 10}); err != nil || len(rows.Orders) != 1 {
		t.Fatalf("OrderHistory: rows=%d err=%v", len(rows.Orders), err)
	}
	if rows, err := client.Fills(ctx, exchange.FillsRequest{Instrument: instrument, Limit: 10}); err != nil || len(rows.Fills) != 1 {
		t.Fatalf("Fills: rows=%d err=%v", len(rows.Fills), err)
	}
	if rows, err := client.Balances(ctx); err != nil || len(rows) != 1 {
		t.Fatalf("Balances: rows=%d err=%v", len(rows), err)
	}
	if row, err := client.PerpAccount(ctx); err != nil {
		t.Fatalf("PerpAccount: %v", err)
	} else if !row.MarginUsed.Valid || !row.MarginUsed.Value.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("PerpAccount MarginUsed = %#v, want 10", row.MarginUsed)
	}
	if rows, err := client.Positions(ctx, exchange.PositionsRequest{Instrument: instrument}); err != nil || len(rows) != 1 {
		t.Fatalf("Positions: rows=%d err=%v", len(rows), err)
	}
	if row, err := client.FundingRate(ctx, exchange.FundingRateRequest{Instrument: instrument}); err != nil || !row.Rate.Equal(decimal.RequireFromString("0.0001")) {
		t.Fatalf("FundingRate: rate=%s err=%v", row.Rate, err)
	}
	start := time.UnixMilli(1719990000000)
	if rows, err := client.FundingRateHistory(ctx, exchange.FundingRateHistoryRequest{Instrument: instrument, Start: start, End: start.Add(4 * time.Hour), Limit: 10}); err != nil || len(rows.Rates) != 1 {
		t.Fatalf("FundingRateHistory: rows=%d err=%v", len(rows.Rates), err)
	}
	if row, err := client.SetLeverage(ctx, exchange.SetLeverageRequest{Instrument: instrument, Leverage: 5}); err != nil || row.Effective != 5 {
		t.Fatalf("SetLeverage: effective=%d err=%v", row.Effective, err)
	}
}

func assertBinanceOpenAPIPlaceShapes(t *testing.T, shapes []url.Values) {
	t.Helper()
	if len(shapes) != 8 {
		t.Fatalf("captured place requests = %d, want 8", len(shapes))
	}
	want := []struct {
		orderType string
		tif       string
		reduce    string
	}{
		{"MARKET", "", ""},
		{"LIMIT", "GTC", ""},
		{"LIMIT", "IOC", ""},
		{"LIMIT_MAKER", "", ""},
		{"MARKET", "", "true"},
		{"LIMIT", "GTC", "true"},
		{"LIMIT", "IOC", "true"},
		{"LIMIT", "GTX", "true"},
	}
	for i, row := range want {
		if shapes[i].Get("type") != row.orderType || shapes[i].Get("timeInForce") != row.tif || shapes[i].Get("reduceOnly") != row.reduce {
			t.Errorf("place[%d] = type %q tif %q reduce %q", i, shapes[i].Get("type"), shapes[i].Get("timeInForce"), shapes[i].Get("reduceOnly"))
		}
	}
}

func TestOpenAPIBinancePerpOrderHistoryRejectsMismatchedInstrument(t *testing.T) {
	router := newBinanceOpenAPIRouter()
	transport := openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/fapi/v1/allOrders" {
			return openAPIJSONResponse(`[{"symbol":"ETHUSDT","orderId":20,"clientOrderId":"100","price":"99","origQty":"1","executedQty":"1","avgPrice":"99","status":"FILLED","timeInForce":"GTC","type":"LIMIT","side":"BUY","positionSide":"BOTH","reduceOnly":true,"updateTime":1720000000000}]`), nil
		}
		return router.RoundTrip(request)
	})
	client := NewBinanceUSDPerp("key", "secret", Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "demo",
		HTTPClient:  &http.Client{Transport: transport},
	})

	_, err := client.OrderHistory(context.Background(), exchange.OrderHistoryRequest{Instrument: "BTC-USDT", Limit: 10})
	if !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("OrderHistory error = %v, want ErrMalformedResponse", err)
	}
}

type okxOpenAPIRouter struct {
	mu             sync.Mutex
	placeBodies    []map[string]any
	leverageBodies []map[string]any
}

func (router *okxOpenAPIRouter) RoundTrip(request *http.Request) (*http.Response, error) {
	if request.Method == http.MethodPost {
		body, _ := io.ReadAll(request.Body)
		if request.URL.Path == "/api/v5/trade/order" {
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			router.mu.Lock()
			router.placeBodies = append(router.placeBodies, payload)
			router.mu.Unlock()
			return okxOpenAPIData(`[{"ordId":"31","clOrdId":"` + stringValue(payload["clOrdId"]) + `","sCode":"0","sMsg":""}]`), nil
		}
		switch request.URL.Path {
		case "/api/v5/trade/cancel-order":
			return okxOpenAPIData(`[{"ordId":"31","clOrdId":"101","sCode":"0","sMsg":""}]`), nil
		case "/api/v5/account/set-leverage":
			var payload map[string]any
			_ = json.Unmarshal(body, &payload)
			router.mu.Lock()
			router.leverageBodies = append(router.leverageBodies, payload)
			router.mu.Unlock()
			return okxOpenAPIData(`[{"instId":"BTC-USDT-SWAP","lever":"5","mgnMode":"` + stringValue(payload["mgnMode"]) + `","posSide":"` + stringValue(payload["posSide"]) + `"}]`), nil
		}
	}

	switch request.URL.Path {
	case "/api/v5/public/instruments":
		if request.URL.Query().Get("instType") == "SPOT" {
			return okxOpenAPIData(`[{"instType":"SPOT","instId":"BTC-USDT","baseCcy":"BTC","quoteCcy":"USDT","tickSz":"0.1","lotSz":"0.001","minSz":"0.001","state":"live"}]`), nil
		}
		return okxOpenAPIData(`[{"instType":"SWAP","instId":"BTC-USDT-SWAP","settleCcy":"USDT","ctVal":"0.01","ctValCcy":"BTC","tickSz":"0.1","lotSz":"1","minSz":"1","state":"live"}]`), nil
	case "/api/v5/market/books":
		return okxOpenAPIData(`[{"bids":[["99","1","0","1"]],"asks":[["101","2","0","1"]],"ts":"1720000000000"}]`), nil
	case "/api/v5/market/candles":
		return okxOpenAPIData(`[["1720000000000","100","101","99","100.5","3","3","300","1"]]`), nil
	case "/api/v5/market/trades":
		return okxOpenAPIData(`[{"instId":"` + request.URL.Query().Get("instId") + `","tradeId":"1","px":"100","sz":"1","side":"buy","ts":"1720000000000"}]`), nil
	case "/api/v5/account/config":
		return okxOpenAPIData(`[{"acctLv":"1","posMode":"net_mode"}]`), nil
	case "/api/v5/trade/orders-pending":
		return okxOpenAPIData(`[{"instType":"` + request.URL.Query().Get("instType") + `","instId":"` + request.URL.Query().Get("instId") + `","ordId":"31","clOrdId":"101","side":"buy","ordType":"limit","sz":"1","px":"99","accFillSz":"0","state":"live","cTime":"1720000000000","uTime":"1720000000000","reduceOnly":"false"}]`), nil
	case "/api/v5/trade/orders-history":
		return okxOpenAPIData(`[{"instType":"` + request.URL.Query().Get("instType") + `","instId":"` + request.URL.Query().Get("instId") + `","ordId":"30","clOrdId":"100","side":"buy","ordType":"market","sz":"1","px":"0","accFillSz":"1","avgPx":"100","state":"filled","cTime":"1720000000000","uTime":"1720000001000","reduceOnly":"false"}]`), nil
	case "/api/v5/trade/fills":
		return okxOpenAPIData(`[{"instType":"` + request.URL.Query().Get("instType") + `","instId":"` + request.URL.Query().Get("instId") + `","tradeId":"1","ordId":"30","clOrdId":"100","side":"buy","fillPx":"100","fillSz":"1","fee":"-0.01","feeCcy":"USDT","execType":"T","ts":"1720000000000"}]`), nil
	case "/api/v5/account/balance":
		return okxOpenAPIData(`[{"totalEq":"100","availEq":"90","imr":"10","upl":"1","details":[{"ccy":"USDT","eq":"100","availBal":"90","frozenBal":"10"}]}]`), nil
	case "/api/v5/account/positions":
		return okxOpenAPIData(`[{"instType":"SWAP","instId":"BTC-USDT-SWAP","pos":"1","posSide":"net","avgPx":"99","markPx":"100","upl":"1","liqPx":"50","lever":"5","margin":"10","mgnMode":"isolated"}]`), nil
	case "/api/v5/public/funding-rate":
		return okxOpenAPIData(`[{"instType":"SWAP","instId":"BTC-USDT-SWAP","fundingRate":"0.0001","fundingTime":"1720000000000","nextFundingTime":"1720003600000","ts":"1720000000000"}]`), nil
	case "/api/v5/public/funding-rate-history":
		return okxOpenAPIData(`[{"instType":"SWAP","instId":"BTC-USDT-SWAP","fundingRate":"0.0001","fundingTime":"1720000000000"}]`), nil
	}
	return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{"code":"1","msg":"unexpected OpenAPI route","data":[]}`)), Header: make(http.Header)}, nil
}

func okxOpenAPIData(data string) *http.Response {
	return openAPIJSONResponse(`{"code":"0","msg":"","data":` + data + `}`)
}

func stringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func TestOpenAPIOKXRESTExecutionMatrix(t *testing.T) {
	router := &okxOpenAPIRouter{}
	settings := Settings{Endpoint: "https://openapi.invalid", Environment: "demo", HTTPClient: &http.Client{Transport: router}}
	ctx := context.Background()

	spot := NewOKXSpot("key", "secret", "passphrase", settings)
	assertSpotReadMatrix(t, ctx, spot, "BTC-USDT")
	exerciseOpenAPIOrderBranches(t, ctx, exchange.ProductSpot, spot, "BTC-USDT")
	if _, err := spot.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: "BTC-USDT", OrderID: "31"}); err != nil {
		t.Fatalf("spot CancelOrder: %v", err)
	}
	assertSpotLifecycleMatrix(t, ctx, spot, "BTC-USDT")

	perp := NewOKXUSDTPerp("key", "secret", "passphrase", settings)
	assertPerpReadMatrix(t, ctx, perp, "BTC-USDT-SWAP")
	exerciseOpenAPIOrderBranches(t, ctx, exchange.ProductPerp, perp, "BTC-USDT-SWAP")
	if _, err := perp.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: "BTC-USDT-SWAP", OrderID: "31"}); err != nil {
		t.Fatalf("perp CancelOrder: %v", err)
	}
	assertPerpLifecycleMatrix(t, ctx, perp, "BTC-USDT-SWAP")

	assertOKXOpenAPIPlaceShapes(t, router.placeBodies)
	if len(router.leverageBodies) != 1 {
		t.Fatalf("captured OKX leverage requests = %d, want 1", len(router.leverageBodies))
	}
	if got := stringValue(router.leverageBodies[0]["mgnMode"]); got != "isolated" {
		t.Errorf("OKX SetLeverage changed current margin mode: got %q, want isolated", got)
	}
	if got := stringValue(router.leverageBodies[0]["posSide"]); got != "net" {
		t.Errorf("OKX SetLeverage posSide = %q, want net", got)
	}
}

func assertOKXOpenAPIPlaceShapes(t *testing.T, bodies []map[string]any) {
	t.Helper()
	if len(bodies) != 8 {
		t.Fatalf("captured OKX place requests = %d, want 8", len(bodies))
	}
	wantTypes := []string{"market", "limit", "ioc", "post_only", "market", "limit", "ioc", "post_only"}
	for index, wantType := range wantTypes {
		if got := stringValue(bodies[index]["ordType"]); got != wantType {
			t.Errorf("place[%d] ordType = %q, want %q", index, got, wantType)
		}
		_, hasPrice := bodies[index]["px"]
		if (wantType == "market") == hasPrice {
			t.Errorf("place[%d] price presence = %v for %s", index, hasPrice, wantType)
		}
		if index == 0 && stringValue(bodies[index]["tgtCcy"]) != "base_ccy" {
			t.Errorf("spot market tgtCcy = %q", stringValue(bodies[index]["tgtCcy"]))
		}
		if index >= 4 && bodies[index]["reduceOnly"] != true {
			t.Errorf("perp place[%d] reduceOnly = %#v", index, bodies[index]["reduceOnly"])
		}
	}
}

func TestOpenAPIOKXSetLeverageRejectsMarginScopeMismatch(t *testing.T) {
	router := &okxOpenAPIRouter{}
	transport := openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost && request.URL.Path == "/api/v5/account/set-leverage" {
			return okxOpenAPIData(`[{"instId":"BTC-USDT-SWAP","lever":5,"mgnMode":"cross","posSide":"net"}]`), nil
		}
		return router.RoundTrip(request)
	})
	client := NewOKXUSDTPerp("key", "secret", "passphrase", Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "demo",
		HTTPClient:  &http.Client{Transport: transport},
	})

	_, err := client.SetLeverage(context.Background(), exchange.SetLeverageRequest{Instrument: "BTC-USDT-SWAP", Leverage: 5})
	if !errors.Is(err, exchange.ErrMalformedResponse) {
		t.Fatalf("SetLeverage error = %v, want ErrMalformedResponse", err)
	}
}

func TestOpenAPIOKXSetLeverageAcceptsBlankResponsePositionSideForNetScope(t *testing.T) {
	router := &okxOpenAPIRouter{}
	transport := openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method == http.MethodPost && request.URL.Path == "/api/v5/account/set-leverage" {
			return okxOpenAPIData(`[{"instId":"BTC-USDT-SWAP","lever":5,"mgnMode":"isolated","posSide":""}]`), nil
		}
		return router.RoundTrip(request)
	})
	client := NewOKXUSDTPerp("key", "secret", "passphrase", Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "demo",
		HTTPClient:  &http.Client{Transport: transport},
	})

	got, err := client.SetLeverage(context.Background(), exchange.SetLeverageRequest{
		Instrument: "BTC-USDT-SWAP",
		Leverage:   5,
	})
	if err != nil {
		t.Fatalf("SetLeverage net-scope response: %v", err)
	}
	if got.Instrument != "BTC-USDT-SWAP" || got.Effective != 5 {
		t.Fatalf("SetLeverage result = %+v", got)
	}
}

type lighterOpenAPIRouter struct {
	product              exchange.Product
	marketID             int
	instrument           string
	spotIncludesPosition bool
	mu                   sync.Mutex
	placeBodies          []map[string]any
}

func (router *lighterOpenAPIRouter) RoundTrip(request *http.Request) (*http.Response, error) {
	switch request.URL.Path {
	case "/api/v1/orderBookDetails":
		return openAPIJSONResponse(`{"code":200,"message":"","order_book_details":[{"symbol":"BTC","market_id":2,"market_type":"perp","min_base_amount":"0.001","min_quote_amount":"5","size_decimals":3,"price_decimals":1,"supported_quote_decimals":6}],"spot_order_book_details":[{"symbol":"BTC-USDC","market_id":1,"market_type":"spot","min_base_amount":"0.001","min_quote_amount":"5","size_decimals":3,"price_decimals":1,"supported_quote_decimals":6}]}`), nil
	case "/api/v1/orderBookOrders":
		return openAPIJSONResponse(`{"code":200,"message":"","bids":[{"order_index":1,"remaining_base_amount":"1","price":"99"}],"asks":[{"order_index":2,"remaining_base_amount":"2","price":"101"}]}`), nil
	case "/api/v1/candles":
		return openAPIJSONResponse(`{"code":200,"message":"","r":"1m","c":[{"t":1720000000,"o":100,"h":101,"l":99,"c":100.5,"v":3}]}`), nil
	case "/api/v1/recentTrades":
		return openAPIJSONResponse(`{"code":200,"message":"","trades":[{"trade_id":1,"market_id":` + intString(router.marketID) + `,"size":"1","price":"100","is_maker_ask":true,"timestamp":1720000000000}]}`), nil
	case "/api/v1/nextNonce":
		return openAPIJSONResponse(`{"code":200,"message":"","nonce":10}`), nil
	case "/api/v1/sendTx":
		_ = request.ParseMultipartForm(1 << 20)
		if request.FormValue("tx_type") == "14" {
			var payload map[string]any
			_ = json.Unmarshal([]byte(request.FormValue("tx_info")), &payload)
			router.mu.Lock()
			router.placeBodies = append(router.placeBodies, payload)
			router.mu.Unlock()
		}
		return openAPIJSONResponse(`{"code":200,"message":"","tx_hash":"0xabc","predicted_execution_time_ms":1}`), nil
	case "/api/v1/accountActiveOrders":
		return openAPIJSONResponse(`{"code":200,"message":"","next_cursor":"","orders":[` + lighterOpenAPIOrderJSON(router.marketID, "active", false) + `]}`), nil
	case "/api/v1/accountInactiveOrders":
		return openAPIJSONResponse(`{"code":200,"message":"","next_cursor":"","orders":[` + lighterOpenAPIOrderJSON(router.marketID, "filled", router.product == exchange.ProductPerp) + `]}`), nil
	case "/api/v1/trades":
		return openAPIJSONResponse(`{"code":200,"message":"","trades":[{"trade_id":2,"market_id":` + intString(router.marketID) + `,"size":"1","price":"100","bid_id":30,"bid_client_id":101,"bid_account_id":7,"ask_id":31,"ask_account_id":8,"is_maker_ask":false,"timestamp":1720000000000,"taker_fee":100,"maker_fee":50}]}`), nil
	case "/api/v1/account":
		position := ""
		if router.product == exchange.ProductPerp || router.spotIncludesPosition {
			position = `{"market_id":2,"symbol":"BTC","sign":1,"position":"1","avg_entry_price":"99","position_value":"100","unrealized_pnl":"1","liquidation_price":"50","margin_mode":0,"allocated_margin":"10"}`
		}
		positions := `[]`
		if position != "" {
			positions = `[` + position + `]`
		}
		return openAPIJSONResponse(`{"code":200,"message":"","accounts":[{"index":7,"account_index":7,"account_trading_mode":0,"available_balance":"90","collateral":"100","cross_initial_margin_requirement":"10","positions":` + positions + `,"assets":[{"symbol":"USDC","balance":"90","locked_balance":"10"}]}]}`), nil
	case "/api/v1/funding-rates":
		return openAPIJSONResponse(`{"code":200,"message":"","funding_rates":[{"market_id":2,"exchange":"lighter","symbol":"BTC","rate":0.0001}]}`), nil
	case "/api/v1/fundings":
		return openAPIJSONResponse(`{"code":200,"message":"","resolution":"1h","fundings":[{"timestamp":1720000000,"rate":"0.0001","value":"0","direction":"long"}]}`), nil
	}
	return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{"code":404,"message":"unexpected OpenAPI route"}`)), Header: make(http.Header)}, nil
}

func lighterOpenAPIOrderJSON(marketID int, status string, reduceOnly bool) string {
	return `{"order_index":30,"client_order_index":101,"market_index":` + intString(marketID) + `,"initial_base_amount":"1","remaining_base_amount":"0","filled_base_amount":"1","price":"99","is_ask":false,"type":"limit","time_in_force":"good-till-time","reduce_only":` + boolString(reduceOnly) + `,"status":"` + status + `","created_at":1720000000000,"updated_at":1720000001000}`
}

func intString(value int) string {
	return decimal.NewFromInt(int64(value)).String()
}

func boolString(value bool) string {
	if value {
		return "true"
	}
	return "false"
}

func TestLighterSpotBalancesIgnoresPerpPositionsFromUnifiedAccountEnvelope(t *testing.T) {
	router := &lighterOpenAPIRouter{
		product:              exchange.ProductSpot,
		marketID:             1,
		instrument:           "BTC-USDC",
		spotIncludesPosition: true,
	}
	client := NewLighterSpot(openAPILighterPrivateKey, 7, 2, Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "testnet",
		HTTPClient:  &http.Client{Transport: router},
	})

	balances, err := client.Balances(context.Background())
	if err != nil {
		t.Fatalf("Balances: %v", err)
	}
	if len(balances) != 1 || balances[0].Asset != "USDC" {
		t.Fatalf("spot balances = %+v, want unified-account spot assets only", balances)
	}
}

func TestOpenAPILighterRESTExecutionMatrix(t *testing.T) {
	ctx := context.Background()
	spotRouter := &lighterOpenAPIRouter{product: exchange.ProductSpot, marketID: 1, instrument: "BTC-USDC"}
	spotSettings := Settings{Endpoint: "https://openapi.invalid", Environment: "testnet", HTTPClient: &http.Client{Transport: spotRouter}}
	spot := NewLighterSpot(openAPILighterPrivateKey, 7, 2, spotSettings)
	assertSpotReadMatrix(t, ctx, spot, spotRouter.instrument)
	exerciseOpenAPIOrderBranches(t, ctx, exchange.ProductSpot, spot, spotRouter.instrument)
	if _, err := spot.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: spotRouter.instrument, OrderID: "30"}); err != nil {
		t.Fatalf("spot CancelOrder: %v", err)
	}
	assertSpotLifecycleMatrix(t, ctx, spot, spotRouter.instrument)

	perpRouter := &lighterOpenAPIRouter{product: exchange.ProductPerp, marketID: 2, instrument: "BTC"}
	perpSettings := Settings{Endpoint: "https://openapi.invalid", Environment: "testnet", HTTPClient: &http.Client{Transport: perpRouter}}
	perp := NewLighterPerp(openAPILighterPrivateKey, 7, 2, perpSettings)
	assertPerpReadMatrix(t, ctx, perp, perpRouter.instrument)
	exerciseOpenAPIOrderBranches(t, ctx, exchange.ProductPerp, perp, perpRouter.instrument)
	if _, err := perp.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: perpRouter.instrument, OrderID: "30"}); err != nil {
		t.Fatalf("perp CancelOrder: %v", err)
	}
	assertPerpLifecycleMatrix(t, ctx, perp, perpRouter.instrument)

	assertLighterOpenAPIPlaceShapes(t, spotRouter.placeBodies, false)
	assertLighterOpenAPIPlaceShapes(t, perpRouter.placeBodies, true)
}

func assertLighterOpenAPIPlaceShapes(t *testing.T, bodies []map[string]any, reduceOnly bool) {
	t.Helper()
	if len(bodies) != 4 {
		t.Fatalf("captured Lighter place requests = %d, want 4", len(bodies))
	}
	wantTypes := []float64{1, 0, 0, 0}
	wantTIF := []float64{0, 1, 0, 2}
	for index := range bodies {
		if bodies[index]["Type"] != wantTypes[index] || bodies[index]["TimeInForce"] != wantTIF[index] {
			t.Errorf("place[%d] Type=%#v TIF=%#v", index, bodies[index]["Type"], bodies[index]["TimeInForce"])
		}
		if (bodies[index]["ReduceOnly"] == float64(1)) != reduceOnly {
			t.Errorf("place[%d] ReduceOnly=%#v", index, bodies[index]["ReduceOnly"])
		}
		if bodies[index]["ClientOrderIndex"] != float64(101+index) {
			t.Errorf("place[%d] ClientOrderIndex=%#v", index, bodies[index]["ClientOrderIndex"])
		}
		if index == 0 && bodies[index]["Price"] != float64(1016) {
			t.Errorf("market protected Price=%#v, want 1016", bodies[index]["Price"])
		}
	}
}

type hyperliquidOpenAPIRouter struct {
	product         exchange.Product
	coin            string
	mu              sync.Mutex
	placeActions    []map[string]any
	cancelActions   []map[string]any
	leverageActions []map[string]any
}

func (router *hyperliquidOpenAPIRouter) RoundTrip(request *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(request.Body)
	var payload map[string]any
	_ = json.Unmarshal(body, &payload)

	switch request.URL.Path {
	case "/info":
		return router.infoResponse(payload), nil
	case "/exchange":
		action, _ := payload["action"].(map[string]any)
		switch stringValue(action["type"]) {
		case "order":
			router.mu.Lock()
			router.placeActions = append(router.placeActions, action)
			router.mu.Unlock()
			return openAPIJSONResponse(`{"status":"ok","response":{"type":"order","data":{"statuses":[{"filled":{"totalSz":"1","avgPx":"100","oid":41}}]}}}`), nil
		case "cancel":
			router.mu.Lock()
			router.cancelActions = append(router.cancelActions, action)
			router.mu.Unlock()
			return openAPIJSONResponse(`{"status":"ok","response":{"type":"default","data":{"statuses":["success"]}}}`), nil
		case "updateLeverage":
			router.mu.Lock()
			router.leverageActions = append(router.leverageActions, action)
			router.mu.Unlock()
			return openAPIJSONResponse(`{"status":"ok","response":{"type":"default","data":{"status":"ok"}}}`), nil
		}
	}
	return &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"code":404,"message":"unexpected Hyperliquid OpenAPI route"}`)),
	}, nil
}

func (router *hyperliquidOpenAPIRouter) infoResponse(payload map[string]any) *http.Response {
	switch stringValue(payload["type"]) {
	case "spotMeta":
		return openAPIJSONResponse(`{"tokens":[{"name":"USDC","szDecimals":6,"weiDecimals":6,"index":0,"isCanonical":true},{"name":"PURR","szDecimals":3,"weiDecimals":3,"index":1,"isCanonical":true}],"universe":[{"name":"@1","index":1,"tokens":[1,0],"isCanonical":true}]}`)
	case "metaAndAssetCtxs":
		return openAPIJSONResponse(`[{"universe":[{"name":"BTC"}]},[{"funding":"0.0001","markPx":"100","oraclePx":"100","midPx":"100","premium":"0"}]]`)
	case "meta":
		return openAPIJSONResponse(`{"universe":[{"name":"BTC","szDecimals":3,"maxLeverage":50}]}`)
	case "l2Book":
		return openAPIJSONResponse(`{"coin":"` + router.coin + `","levels":[[{"px":"99","sz":"1","n":1}],[{"px":"101","sz":"2","n":1}]],"time":1720000000000}`)
	case "candleSnapshot":
		return openAPIJSONResponse(`[{"t":1720000000000,"T":1720000059999,"s":"` + router.coin + `","i":"1m","o":"100","c":"100.5","h":"101","l":"99","v":"3","n":1}]`)
	case "recentTrades":
		return openAPIJSONResponse(`[{"coin":"` + router.coin + `","side":"B","px":"100","sz":"1","hash":"0xtrade","time":1720000000000,"tid":1,"users":[]}]`)
	case "allMids":
		return openAPIJSONResponse(`{"` + router.coin + `":"100"}`)
	case "frontendOpenOrders":
		return openAPIJSONResponse(`[` + hyperliquidOpenAPIOrderJSON(router.coin, "1", "41", "101", false) + `]`)
	case "historicalOrders":
		return openAPIJSONResponse(`[{"order":` + hyperliquidOpenAPIOrderJSON(router.coin, "0", "40", "100", router.product == exchange.ProductPerp) + `,"status":"filled","statusTimestamp":1720000001000}]`)
	case "userFills":
		return openAPIJSONResponse(`[{"coin":"` + router.coin + `","px":"100","sz":"1","side":"B","time":1720000000000,"startPosition":"0","dir":"Open Long","closedPnl":"0","hash":"0xfill","oid":40,"crossed":true,"fee":"0.01","feeToken":"USDC","tid":2}]`)
	case "spotClearinghouseState":
		return openAPIJSONResponse(`{"balances":[{"coin":"USDC","token":0,"hold":"10","total":"100","entryNtl":"0"}]}`)
	case "clearinghouseState":
		return openAPIJSONResponse(hyperliquidOpenAPIPerpStateJSON())
	case "fundingHistory":
		return openAPIJSONResponse(`[{"coin":"BTC","fundingRate":"0.0001","premium":"0","time":1720000000000}]`)
	}
	return &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     make(http.Header),
		Body:       io.NopCloser(strings.NewReader(`{"code":404,"message":"unexpected Hyperliquid info type"}`)),
	}
}

func hyperliquidOpenAPIOrderJSON(coin, remainingSize, orderID, portableClientID string, reduceOnly bool) string {
	return `{"coin":"` + coin + `","side":"B","limitPx":"99","sz":"` + remainingSize + `","oid":` + orderID + `,"cloid":"` + hyperliquidNativeClientID(portableClientID) + `","timestamp":1720000000000,"origSz":"1","reduceOnly":` + boolString(reduceOnly) + `,"orderType":"Limit","tif":"Gtc","isTrigger":false,"triggerPx":"0","triggerCondition":""}`
}

func hyperliquidNativeClientID(portable string) string {
	value, _ := decimal.NewFromString(portable)
	return "0x" + strings.Repeat("0", 32-len(value.BigInt().Text(16))) + value.BigInt().Text(16)
}

func hyperliquidOpenAPIPerpStateJSON() string {
	return `{"assetPositions":[{"position":{"coin":"BTC","cumFunding":{"allTime":"0","sinceOpen":"0","sinceChange":"0"},"entryPx":"99","leverage":{"rawUsd":"0","type":"isolated","value":5},"liquidationPx":"50","marginUsed":"10","maxLeverage":50,"positionValue":"100","returnOnEquity":"0.1","szi":"1","unrealizedPnl":"1"},"type":"oneWay"}],"crossMaintenanceMarginUsed":"1","crossMarginSummary":{"accountValue":"100","totalMarginUsed":"10","totalNtlPos":"100","totalRawUsd":"0"},"marginSummary":{"accountValue":"100","totalMarginUsed":"10","totalNtlPos":"100","totalRawUsd":"0"},"time":1720000000000,"withdrawable":"90"}`
}

func TestOpenAPIHyperliquidRESTExecutionMatrix(t *testing.T) {
	ctx := context.Background()

	spotRouter := &hyperliquidOpenAPIRouter{product: exchange.ProductSpot, coin: "@1"}
	spotSettings := Settings{Endpoint: "https://openapi.invalid", Environment: "testnet", HTTPClient: &http.Client{Transport: spotRouter}}
	spot := NewHyperliquidSpot(openAPITestPrivateKey, spotSettings)
	assertSpotReadMatrix(t, ctx, spot, "PURR-USDC")
	exerciseOpenAPIOrderBranches(t, ctx, exchange.ProductSpot, spot, "PURR-USDC")
	if _, err := spot.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: "PURR-USDC", OrderID: "41"}); err != nil {
		t.Fatalf("spot CancelOrder: %v", err)
	}
	assertSpotLifecycleMatrix(t, ctx, spot, "PURR-USDC")

	perpRouter := &hyperliquidOpenAPIRouter{product: exchange.ProductPerp, coin: "BTC"}
	perpSettings := Settings{Endpoint: "https://openapi.invalid", Environment: "testnet", HTTPClient: &http.Client{Transport: perpRouter}}
	perp := NewHyperliquidPerp(openAPITestPrivateKey, perpSettings)
	assertPerpReadMatrix(t, ctx, perp, "BTC-USDC")
	exerciseOpenAPIOrderBranches(t, ctx, exchange.ProductPerp, perp, "BTC-USDC")
	if _, err := perp.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: "BTC-USDC", OrderID: "41"}); err != nil {
		t.Fatalf("perp CancelOrder: %v", err)
	}
	assertPerpLifecycleMatrix(t, ctx, perp, "BTC-USDC")

	assertHyperliquidOpenAPIPlaceShapes(t, spotRouter.placeActions, false, 10001)
	assertHyperliquidOpenAPIPlaceShapes(t, perpRouter.placeActions, true, 0)
	if len(spotRouter.cancelActions) != 1 || len(perpRouter.cancelActions) != 1 {
		t.Fatalf("captured Hyperliquid cancels = spot:%d perp:%d, want one each", len(spotRouter.cancelActions), len(perpRouter.cancelActions))
	}
	if len(perpRouter.leverageActions) != 1 {
		t.Fatalf("captured Hyperliquid leverage actions = %d, want 1", len(perpRouter.leverageActions))
	}
	if perpRouter.leverageActions[0]["isCross"] != false {
		t.Errorf("Hyperliquid leverage changed existing isolated margin mode: %#v", perpRouter.leverageActions[0])
	}
}

func TestOpenAPIHyperliquidRESTNormalizesEveryLimitPolicyConservatively(t *testing.T) {
	ctx := context.Background()
	tests := []struct {
		name       string
		product    exchange.Product
		coin       string
		instrument string
		newClient  func(*hyperliquidOpenAPIRouter) any
	}{
		{
			name:       "spot",
			product:    exchange.ProductSpot,
			coin:       "@1",
			instrument: "PURR-USDC",
			newClient: func(router *hyperliquidOpenAPIRouter) any {
				return NewHyperliquidSpot(openAPITestPrivateKey, Settings{
					Endpoint:    "https://openapi.invalid",
					Environment: "testnet",
					HTTPClient:  &http.Client{Transport: router},
				})
			},
		},
		{
			name:       "perp",
			product:    exchange.ProductPerp,
			coin:       "BTC",
			instrument: "BTC-USDC",
			newClient: func(router *hyperliquidOpenAPIRouter) any {
				return NewHyperliquidPerp(openAPITestPrivateKey, Settings{
					Endpoint:    "https://openapi.invalid",
					Environment: "testnet",
					HTTPClient:  &http.Client{Transport: router},
				})
			},
		},
	}
	policies := []exchange.LimitPolicy{
		exchange.LimitPolicyResting,
		exchange.LimitPolicyIOC,
		exchange.LimitPolicyPostOnly,
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := &hyperliquidOpenAPIRouter{product: test.product, coin: test.coin}
			client := test.newClient(router)
			for index, policy := range policies {
				for _, side := range []exchange.Side{exchange.SideBuy, exchange.SideSell} {
					sideIndex := 0
					if side == exchange.SideSell {
						sideIndex = 1
					}
					request := exchange.PlaceOrderRequest{
						Instrument:    test.instrument,
						ClientOrderID: intString(200 + index*2 + sideIndex),
						Side:          side,
						Type:          exchange.OrderTypeLimit,
						Quantity:      decimal.NewFromInt(1),
						LimitPrice:    decimal.RequireFromString("101.23456"),
						LimitPolicy:   policy,
						ReduceOnly:    test.product == exchange.ProductPerp,
					}
					var err error
					if test.product == exchange.ProductSpot {
						_, err = client.(exchange.SpotClient).PlaceOrder(ctx, request)
					} else {
						_, err = client.(exchange.PerpClient).PlaceOrder(ctx, request)
					}
					if err != nil {
						t.Fatalf("%s %s PlaceOrder: %v", policy, side, err)
					}
				}
			}
			if len(router.placeActions) != len(policies)*2 {
				t.Fatalf("captured place actions = %d, want %d", len(router.placeActions), len(policies)*2)
			}
			for index, action := range router.placeActions {
				orders, _ := action["orders"].([]any)
				order, _ := orders[0].(map[string]any)
				want := "101.23"
				if index%2 == 1 {
					want = "101.24"
				}
				if got := stringValue(order["p"]); got != want {
					t.Errorf("place[%d] price = %q, want %q", index, got, want)
				}
			}
		})
	}
}

func assertHyperliquidOpenAPIPlaceShapes(t *testing.T, actions []map[string]any, reduceOnly bool, assetID float64) {
	t.Helper()
	if len(actions) != 4 {
		t.Fatalf("captured Hyperliquid place actions = %d, want 4", len(actions))
	}
	wantTIF := []string{"Ioc", "Gtc", "Ioc", "Alo"}
	for index, action := range actions {
		orders, _ := action["orders"].([]any)
		if len(orders) != 1 {
			t.Fatalf("place[%d] orders = %#v", index, action["orders"])
		}
		order, _ := orders[0].(map[string]any)
		orderType, _ := order["t"].(map[string]any)
		limit, _ := orderType["limit"].(map[string]any)
		if got := stringValue(limit["tif"]); got != wantTIF[index] {
			t.Errorf("place[%d] TIF = %q, want %q", index, got, wantTIF[index])
		}
		if order["a"] != assetID {
			t.Errorf("place[%d] asset = %#v, want %#v", index, order["a"], assetID)
		}
		if order["r"] != reduceOnly {
			t.Errorf("place[%d] reduceOnly = %#v, want %v", index, order["r"], reduceOnly)
		}
		if got := stringValue(order["c"]); got != hyperliquidNativeClientID(intString(101+index)) {
			t.Errorf("place[%d] cloid = %q", index, got)
		}
		if index == 0 {
			price, err := decimal.NewFromString(stringValue(order["p"]))
			if err != nil || !price.IsPositive() {
				t.Errorf("market place[%d] did not use a protected positive IOC price: %#v", index, order["p"])
			}
		}
	}
}

func TestOpenAPIHyperliquidSetLeveragePostSendFailureUsesSetLeverageBoundary(t *testing.T) {
	router := &hyperliquidOpenAPIRouter{product: exchange.ProductPerp, coin: "BTC"}
	transport := openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/exchange" {
			return &http.Response{
				StatusCode: http.StatusServiceUnavailable,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"code":503,"message":"unavailable"}`)),
			}, nil
		}
		return router.RoundTrip(request)
	})
	client := NewHyperliquidPerp(openAPITestPrivateKey, Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "testnet",
		HTTPClient:  &http.Client{Transport: transport},
	})

	_, err := client.SetLeverage(context.Background(), exchange.SetLeverageRequest{Instrument: "BTC-USDC", Leverage: 5})
	if !errors.Is(err, exchange.ErrAmbiguousOutcome) {
		t.Fatalf("SetLeverage error = %v, want ErrAmbiguousOutcome", err)
	}
	var normalized *exchange.Error
	if !errors.As(err, &normalized) {
		t.Fatalf("SetLeverage error type = %T, want *exchange.Error", err)
	}
	if got := normalized.Details().Operation; got != "SetLeverage" {
		t.Fatalf("SetLeverage operation = %q, want SetLeverage", got)
	}
}
