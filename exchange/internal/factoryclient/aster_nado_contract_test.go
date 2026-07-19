package factoryclient

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

const (
	testAsterUserAddress = "0xf39fd6e51aad88f6f4ce6ab8827279cfffb92266"
	testAsterPrivateKey  = "0xac0974bec39a17e36ba4a6b4d238ff944bacb478cbed5efcae784d7bf4f2ff80"
)

func TestAsterAndNadoBuildersSatisfyPublicContracts(t *testing.T) {
	settings := Settings{Environment: "testnet"}

	var _ exchange.SpotClient = NewAsterSpot(testAsterUserAddress, testAsterPrivateKey, testAsterUserAddress, settings)
	var _ exchange.PerpClient = NewAsterUSDTPerp(testAsterUserAddress, testAsterPrivateKey, testAsterUserAddress, settings)
	var _ exchange.SpotClient = NewNadoSpot(testAsterPrivateKey, "default", settings)
	var _ exchange.PerpClient = NewNadoUSDT0Perp(testAsterPrivateKey, "default", settings)
}

func TestOpenAPIAsterRESTExecutionMatrix(t *testing.T) {
	router := newAsterOpenAPIRouter()
	settings := Settings{
		Environment: "testnet",
		Endpoint:    "https://aster-fixture.invalid",
		HTTPClient:  &http.Client{Transport: router},
	}
	ctx := context.Background()

	spot := NewAsterSpot(testAsterUserAddress, testAsterPrivateKey, testAsterUserAddress, settings)
	assertAsterSpotReadMatrix(t, ctx, spot, "BTC-USDT")
	exerciseAsterOrderBranches(t, ctx, exchange.ProductSpot, spot, "BTC-USDT")
	if _, err := spot.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: "BTC-USDT", OrderID: "11"}); err != nil {
		t.Fatalf("aster spot CancelOrder: %v", err)
	}
	assertAsterSpotLifecycleMatrix(t, ctx, spot, "BTC-USDT")

	perp := NewAsterUSDTPerp(testAsterUserAddress, testAsterPrivateKey, testAsterUserAddress, settings)
	assertAsterPerpReadMatrix(t, ctx, perp, "BTC-USDT")
	exerciseAsterOrderBranches(t, ctx, exchange.ProductPerp, perp, "BTC-USDT")
	if _, err := perp.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: "BTC-USDT", OrderID: "21"}); err != nil {
		t.Fatalf("aster perp CancelOrder: %v", err)
	}
	assertAsterPerpLifecycleMatrix(t, ctx, perp, "BTC-USDT")
	if row, err := perp.SetLeverage(ctx, exchange.SetLeverageRequest{Instrument: "BTC-USDT", Leverage: 5}); err != nil || row.Effective != 5 {
		t.Fatalf("SetLeverage: effective=%d err=%v", row.Effective, err)
	}

	router.mu.Lock()
	defer router.mu.Unlock()
	if router.seen["GET /api/v3/exchangeInfo"] == 0 {
		t.Fatal("spot did not use injected REST endpoint")
	}
	if router.seen["GET /fapi/v3/exchangeInfo"] == 0 {
		t.Fatal("perp did not use injected REST endpoint")
	}
}

type asterOpenAPIRouter struct {
	mu   sync.Mutex
	seen map[string]int
}

func newAsterOpenAPIRouter() *asterOpenAPIRouter {
	return &asterOpenAPIRouter{seen: make(map[string]int)}
}

func asterOpenAPIJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func (router *asterOpenAPIRouter) RoundTrip(request *http.Request) (*http.Response, error) {
	router.mu.Lock()
	router.seen[request.Method+" "+request.URL.Path]++
	query := request.URL.Query()
	router.mu.Unlock()

	if request.Method == http.MethodPost && (request.URL.Path == "/api/v3/order" || request.URL.Path == "/fapi/v3/order") {
		status, price, filled, quote := "NEW", query.Get("price"), "0", "0"
		if query.Get("type") == "MARKET" {
			status, price, filled, quote = "FILLED", "0", query.Get("quantity"), "100"
		}
		if request.URL.Path == "/api/v3/order" {
			return asterOpenAPIJSONResponse(`{"symbol":"BTCUSDT","orderId":11,"clientOrderId":"` + query.Get("newClientOrderId") + `","time":1720000000000,"price":"` + price + `","origQty":"` + query.Get("quantity") + `","executedQty":"` + filled + `","cumQuote":"` + quote + `","status":"` + status + `","timeInForce":"` + query.Get("timeInForce") + `","type":"` + query.Get("type") + `","side":"BUY"}`), nil
		}
		return asterOpenAPIJSONResponse(`{"symbol":"BTCUSDT","orderId":21,"clientOrderId":"` + query.Get("newClientOrderId") + `","price":"` + price + `","origQty":"` + query.Get("quantity") + `","executedQty":"` + filled + `","cumQty":"` + filled + `","cumQuote":"` + quote + `","avgPrice":"100","status":"` + status + `","timeInForce":"` + query.Get("timeInForce") + `","type":"` + query.Get("type") + `","side":"BUY","positionSide":"BOTH","reduceOnly":` + boolQuery(query.Get("reduceOnly")) + `,"updateTime":1720000000000}`), nil
	}

	switch request.URL.Path {
	case "/api/v3/exchangeInfo":
		return asterOpenAPIJSONResponse(`{"symbols":[{"symbol":"BTCUSDT","status":"TRADING","baseAsset":"BTC","quoteAsset":"USDT","filters":[{"filterType":"PRICE_FILTER","tickSize":"0.1"},{"filterType":"LOT_SIZE","stepSize":"0.001","minQty":"0.001"},{"filterType":"MIN_NOTIONAL","minNotional":"5"}]}]}`), nil
	case "/api/v3/depth":
		return asterOpenAPIJSONResponse(`{"lastUpdateId":7,"bids":[["99","1"]],"asks":[["101","2"]]}`), nil
	case "/api/v3/klines":
		return asterOpenAPIJSONResponse(`[[1720000000000,"100","101","99","100.5","3",1720000059999]]`), nil
	case "/api/v3/trades":
		return asterOpenAPIJSONResponse(`[{"id":1,"price":"100","qty":"0.1","time":1720000000000,"isBuyerMaker":false}]`), nil
	case "/api/v3/order":
		if request.Method == http.MethodDelete {
			return asterOpenAPIJSONResponse(`{"symbol":"BTCUSDT","orderId":11,"clientOrderId":"cancel","status":"CANCELED"}`), nil
		}
	case "/api/v3/openOrders":
		return asterOpenAPIJSONResponse(`[{"symbol":"BTCUSDT","orderId":11,"clientOrderId":"101","price":"99","origQty":"1","executedQty":"0","status":"NEW","timeInForce":"GTC","type":"LIMIT","side":"BUY"}]`), nil
	case "/api/v3/allOrders":
		return asterOpenAPIJSONResponse(`[{"symbol":"BTCUSDT","orderId":10,"clientOrderId":"100","price":"99","origQty":"1","executedQty":"1","cumQuote":"99","status":"FILLED","timeInForce":"GTC","type":"LIMIT","side":"BUY"}]`), nil
	case "/api/v3/userTrades":
		return asterOpenAPIJSONResponse(`[{"symbol":"BTCUSDT","id":1,"orderId":10,"price":"99","qty":"1","commission":"0.001","commissionAsset":"BTC","time":1720000000000,"buyer":true,"maker":false}]`), nil
	case "/api/v3/account":
		return asterOpenAPIJSONResponse(`{"balances":[{"asset":"USDT","free":"100","locked":"2"}]}`), nil
	case "/fapi/v3/exchangeInfo":
		return asterOpenAPIJSONResponse(`{"symbols":[{"symbol":"BTCUSDT","contractType":"PERPETUAL","status":"TRADING","baseAsset":"BTC","quoteAsset":"USDT","marginAsset":"USDT","filters":[{"filterType":"PRICE_FILTER","tickSize":"0.1"},{"filterType":"LOT_SIZE","stepSize":"0.001","minQty":"0.001"},{"filterType":"MIN_NOTIONAL","notional":"5"}]}]}`), nil
	case "/fapi/v3/depth":
		return asterOpenAPIJSONResponse(`{"lastUpdateId":8,"T":1720000000000,"bids":[["99","1"]],"asks":[["101","2"]]}`), nil
	case "/fapi/v3/klines":
		return asterOpenAPIJSONResponse(`[[1720000000000,"100","101","99","100.5","3",1720000059999]]`), nil
	case "/fapi/v3/aggTrades":
		return asterOpenAPIJSONResponse(`[{"a":2,"p":"100","q":"0.2","T":1720000000000,"m":true}]`), nil
	case "/fapi/v3/order":
		if request.Method == http.MethodDelete {
			return asterOpenAPIJSONResponse(`{"symbol":"BTCUSDT","orderId":21,"clientOrderId":"101","price":"99","origQty":"1","executedQty":"0","avgPrice":"0","status":"CANCELED","timeInForce":"GTC","type":"LIMIT","side":"BUY","positionSide":"BOTH","reduceOnly":false,"updateTime":1720000000000}`), nil
		}
	case "/fapi/v3/openOrders":
		return asterOpenAPIJSONResponse(`[{"symbol":"BTCUSDT","orderId":21,"clientOrderId":"101","price":"99","origQty":"1","executedQty":"0","avgPrice":"0","status":"NEW","timeInForce":"GTC","type":"LIMIT","side":"BUY","positionSide":"BOTH","reduceOnly":false,"updateTime":1720000000000}]`), nil
	case "/fapi/v3/allOrders":
		return asterOpenAPIJSONResponse(`[{"symbol":"BTCUSDT","orderId":20,"clientOrderId":"100","price":"99","origQty":"1","executedQty":"1","avgPrice":"99","status":"FILLED","timeInForce":"GTC","type":"LIMIT","side":"BUY","positionSide":"BOTH","reduceOnly":true,"updateTime":1720000000000}]`), nil
	case "/fapi/v3/userTrades":
		return asterOpenAPIJSONResponse(`[{"symbol":"BTCUSDT","id":2,"orderId":20,"price":"99","qty":"1","quoteQty":"99","commission":"0.01","commissionAsset":"USDT","time":1720000000000,"side":"BUY","maker":false,"positionSide":"BOTH"}]`), nil
	case "/fapi/v3/balance":
		return asterOpenAPIJSONResponse(`[{"asset":"USDT","balance":"100","availableBalance":"90"}]`), nil
	case "/fapi/v3/accountWithJoinMargin":
		return asterOpenAPIJSONResponse(`{"totalMarginBalance":"100","availableBalance":"90","totalInitialMargin":"10","totalUnrealizedProfit":"1","assets":[{"asset":"USDT","walletBalance":"100","availableBalance":"90"}]}`), nil
	case "/fapi/v3/positionRisk":
		return asterOpenAPIJSONResponse(`[{"symbol":"BTCUSDT","positionAmt":"1","entryPrice":"99","markPrice":"100","unRealizedProfit":"1","liquidationPrice":"50","leverage":"5","isolatedMargin":"0","positionSide":"BOTH"}]`), nil
	case "/fapi/v3/premiumIndex":
		return asterOpenAPIJSONResponse(`{"symbol":"BTCUSDT","markPrice":"100","lastFundingRate":"0.0001","nextFundingTime":1720003600000,"time":1720000000000}`), nil
	case "/fapi/v3/fundingRate":
		return asterOpenAPIJSONResponse(`[{"symbol":"BTCUSDT","fundingRate":"0.0001","fundingTime":1720000000000,"markPrice":"100"}]`), nil
	case "/fapi/v3/leverage":
		return asterOpenAPIJSONResponse(`{"symbol":"BTCUSDT","leverage":5,"maxNotionalValue":"1000000"}`), nil
	}
	return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{"code":-1,"msg":"unexpected Aster route"}`)), Header: make(http.Header)}, nil
}

func boolQuery(value string) string {
	if value == "" {
		return "false"
	}
	return value
}

func assertAsterSpotReadMatrix(t *testing.T, ctx context.Context, client exchange.SpotClient, instrument string) {
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

func assertAsterPerpReadMatrix(t *testing.T, ctx context.Context, client exchange.PerpClient, instrument string) {
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

func assertAsterSpotLifecycleMatrix(t *testing.T, ctx context.Context, client exchange.SpotClient, instrument string) {
	t.Helper()
	if rows, err := client.OpenOrders(ctx, exchange.OpenOrdersRequest{Instrument: instrument, Limit: 1}); err != nil || len(rows.Orders) != 1 {
		t.Fatalf("OpenOrders: rows=%d err=%v", len(rows.Orders), err)
	}
	if rows, err := client.OrderHistory(ctx, exchange.OrderHistoryRequest{Instrument: instrument, Limit: 10}); err != nil || len(rows.Orders) != 1 {
		t.Fatalf("OrderHistory: rows=%d err=%v", len(rows.Orders), err)
	}
	if rows, err := client.Fills(ctx, exchange.FillsRequest{Instrument: instrument, Limit: 10}); err != nil || len(rows.Fills) != 1 {
		t.Fatalf("Fills: rows=%d err=%v", len(rows.Fills), err)
	}
	if rows, err := client.Balances(ctx); err != nil || len(rows) < 1 || !balancesAddUp(rows) {
		t.Fatalf("Balances: rows=%d err=%v", len(rows), err)
	}
	if row, err := client.SpotAccount(ctx); err != nil || len(row.Balances) < 1 || !balancesAddUp(row.Balances) {
		t.Fatalf("SpotAccount: balances=%d err=%v", len(row.Balances), err)
	}
}

func assertAsterPerpLifecycleMatrix(t *testing.T, ctx context.Context, client exchange.PerpClient, instrument string) {
	t.Helper()
	if rows, err := client.OpenOrders(ctx, exchange.OpenOrdersRequest{Instrument: instrument, Limit: 1}); err != nil || len(rows.Orders) != 1 {
		t.Fatalf("OpenOrders: rows=%d err=%v", len(rows.Orders), err)
	}
	if rows, err := client.OrderHistory(ctx, exchange.OrderHistoryRequest{Instrument: instrument, Limit: 10}); err != nil || len(rows.Orders) != 1 {
		t.Fatalf("OrderHistory: rows=%d err=%v", len(rows.Orders), err)
	}
	if rows, err := client.Fills(ctx, exchange.FillsRequest{Instrument: instrument, Limit: 10}); err != nil || len(rows.Fills) != 1 {
		t.Fatalf("Fills: rows=%d err=%v", len(rows.Fills), err)
	}
	if rows, err := client.Balances(ctx); err != nil || len(rows) < 1 || !balancesAddUp(rows) {
		t.Fatalf("Balances: rows=%d err=%v", len(rows), err)
	}
	if row, err := client.PerpAccount(ctx); err != nil || len(row.Balances) < 1 || !balancesAddUp(row.Balances) {
		t.Fatalf("PerpAccount: %#v err=%v", row, err)
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
}

func balancesAddUp(rows []exchange.Balance) bool {
	for _, row := range rows {
		if !row.Available.Add(row.Locked).Equal(row.Total) {
			return false
		}
	}
	return true
}

func exerciseAsterOrderBranches(t *testing.T, ctx context.Context, product exchange.Product, client any, instrument string) {
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
			if _, err := client.(exchange.PerpClient).PlaceOrder(ctx, cases[index]); err != nil {
				t.Fatalf("perp PlaceOrder %s/%s: %v", cases[index].Type, cases[index].LimitPolicy, err)
			}
		} else if _, err := client.(exchange.SpotClient).PlaceOrder(ctx, cases[index]); err != nil {
			t.Fatalf("spot PlaceOrder %s/%s: %v", cases[index].Type, cases[index].LimitPolicy, err)
		}
	}
}

type nadoOpenAPIRouter struct {
	t *testing.T
}

func newNadoOpenAPIRouter(t *testing.T) *nadoOpenAPIRouter {
	t.Helper()
	return &nadoOpenAPIRouter{t: t}
}

func (router *nadoOpenAPIRouter) RoundTrip(request *http.Request) (*http.Response, error) {
	path := request.URL.Path
	if path == "/v1/query" {
		return router.nadoQuery(request)
	}
	if path == "/v1/execute" {
		body, _ := io.ReadAll(request.Body)
		if bytes.Contains(body, []byte(`cancel_orders`)) {
			return asterOpenAPIJSONResponse(`{"status":"success","data":{"cancelled_orders":[{"product_id":1,"sender":"sender","price_x18":"99000000000000000000","amount":"1000000000000000000","expiration":"4000000000","nonce":"1","unfilled_amount":"1000000000000000000","digest":"0x1111111111111111111111111111111111111111111111111111111111111111","placed_at":1720000000000,"appendix":"0","order_type":"limit"}]},"request_type":"cancel_orders"}`), nil
		}
		return asterOpenAPIJSONResponse(`{"status":"success","data":{"digest":"0x1111111111111111111111111111111111111111111111111111111111111111"},"request_type":"execute"}`), nil
	}
	if path == "/v2/orderbook" {
		return asterOpenAPIJSONResponse(`{"product_id":` + productIDForTicker(request.URL.Query().Get("ticker_id")) + `,"ticker_id":"` + request.URL.Query().Get("ticker_id") + `","bids":[[99,1]],"asks":[[101,2]],"timestamp":1720000000000}`), nil
	}
	if path == "/v2/pairs" {
		return asterOpenAPIJSONResponse(`[{"product_id":1,"ticker_id":"ETH_USDT0","base":"ETH","quote":"USDT0"},{"product_id":2,"ticker_id":"ETH-PERP_USDT0","base":"ETH-PERP","quote":"USDT0"}]`), nil
	}
	if path == "/v2/trades" {
		return asterOpenAPIJSONResponse(`[{"product_id":` + productIDForTicker(request.URL.Query().Get("ticker_id")) + `,"ticker_id":"` + request.URL.Query().Get("ticker_id") + `","trade_id":1,"price":100,"base_filled":0.1,"quote_filled":10,"timestamp":1720000000000,"trade_type":"buy"}]`), nil
	}
	if path == "/v2/tickers" {
		return asterOpenAPIJSONResponse(`{"ETH_USDT0":{"product_id":1,"ticker_id":"ETH_USDT0","base_currency":"ETH","quote_currency":"USDT0","last_price":100,"base_volume":1,"quote_volume":100,"price_change_percent_24h":0}}`), nil
	}
	if path == "/v2/contracts" {
		next := int64(1720003600000)
		_ = next
		return asterOpenAPIJSONResponse(`{"ETH-PERP_USDT0":{"product_id":2,"ticker_id":"ETH-PERP_USDT0","base_currency":"ETH","quote_currency":"USDT0","last_price":100,"base_volume":1,"quote_volume":100,"product_type":"perp","contract_price":100,"contract_price_currency":"USDT0","open_interest":1,"open_interest_usd":100,"index_price":100,"mark_price":100,"funding_rate":0.0001,"next_funding_rate_timestamp":1720003600000,"price_change_percent_24h":0}}`), nil
	}
	if path == "/v1" {
		return router.nadoArchive(request)
	}
	return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{"status":"error","error":"unexpected Nado route"}`)), Header: make(http.Header)}, nil
}

func (router *nadoOpenAPIRouter) nadoQuery(request *http.Request) (*http.Response, error) {
	queryType := request.URL.Query().Get("type")
	if request.Method == http.MethodPost {
		body, _ := io.ReadAll(request.Body)
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		if value, _ := payload["type"].(string); value != "" {
			queryType = value
		}
	}
	switch queryType {
	case "status":
		return asterOpenAPIJSONResponse(`{"status":"success","data":"active","request_type":"status"}`), nil
	case "all_products":
		return router.file("sdk/nado/testdata/all_products.json"), nil
	case "symbols":
		return router.file("sdk/nado/testdata/symbols.json"), nil
	case "contracts":
		return asterOpenAPIJSONResponse(`{"status":"success","data":{"chain_id":"763373","endpoint_addr":"0x1111111111111111111111111111111111111111"},"request_type":"contracts"}`), nil
	case "subaccount_info":
		return router.file("sdk/nado/testdata/subaccount_info.json"), nil
	case "subaccount_orders", "orders":
		return asterOpenAPIJSONResponse(`{"status":"success","data":{"sender":"sender","product_orders":[{"product_id":1,"orders":[{"product_id":1,"sender":"sender","price_x18":"99000000000000000000","amount":"1000000000000000000","expiration":"4000000000","nonce":"1","unfilled_amount":"1000000000000000000","digest":"0x1111111111111111111111111111111111111111111111111111111111111111","placed_at":1720000000000,"appendix":"0","order_type":"limit"}]},{"product_id":2,"orders":[{"product_id":2,"sender":"sender","price_x18":"99000000000000000000","amount":"1000000000000000000","expiration":"4000000000","nonce":"1","unfilled_amount":"1000000000000000000","digest":"0x1111111111111111111111111111111111111111111111111111111111111111","placed_at":1720000000000,"appendix":"0","order_type":"limit"}]}]},"request_type":"orders"}`), nil
	case "fee_rates":
		return asterOpenAPIJSONResponse(`{"status":"success","data":{"maker_fee_rates_x18":["0"],"taker_fee_rates_x18":["0"],"fee_tier":0},"request_type":"fee_rates"}`), nil
	}
	return asterOpenAPIJSONResponse(`{"status":"success","data":{},"request_type":"` + queryType + `"}`), nil
}

func (router *nadoOpenAPIRouter) nadoArchive(request *http.Request) (*http.Response, error) {
	body, _ := io.ReadAll(request.Body)
	if bytes.Contains(body, []byte(`"candlesticks"`)) {
		return asterOpenAPIJSONResponse(`{"candlesticks":[{"product_id":1,"granularity":60,"submission_idx":"1","timestamp":"1720000000","open_x18":"99000000000000000000","high_x18":"101000000000000000000","low_x18":"98000000000000000000","close_x18":"100000000000000000000","volume":"1000000000000000000"}]}`), nil
	}
	if bytes.Contains(body, []byte(`"funding_rate"`)) {
		return asterOpenAPIJSONResponse(`{"product_id":2,"funding_rate_x18":"100000000000000","update_time":"1720000000"}`), nil
	}
	if bytes.Contains(body, []byte(`"price"`)) {
		return asterOpenAPIJSONResponse(`{"product_id":2,"index_price_x18":"100000000000000000000","mark_price_x18":"100000000000000000000","update_time":"1720000000"}`), nil
	}
	if bytes.Contains(body, []byte(`"matches"`)) {
		return asterOpenAPIJSONResponse(`{"matches":[{"digest":"0x1111111111111111111111111111111111111111111111111111111111111111","order":{"sender":"sender","priceX18":"99000000000000000000","amount":"1000000000000000000","expiration":"4000000000","nonce":"1","appendix":"0"},"base_filled":"1000000000000000000","quote_filled":"99000000000000000000","fee":"1000000000000000","sequencer_fee":"0","submission_idx":"1","timestamp":"1720000000","pre_balance":{"base":{"spot":{"product_id":1,"balance":{"amount":"0"}}}},"post_balance":{"base":{"spot":{"product_id":1,"balance":{"amount":"1000000000000000000"}}}}},{"digest":"0x2222222222222222222222222222222222222222222222222222222222222222","order":{"sender":"sender","priceX18":"99000000000000000000","amount":"1000000000000000000","expiration":"4000000000","nonce":"2","appendix":"0"},"base_filled":"1000000000000000000","quote_filled":"99000000000000000000","fee":"1000000000000000","sequencer_fee":"0","submission_idx":"2","timestamp":"1720000000","pre_balance":{"base":{"perp":{"product_id":2,"balance":{"amount":"0","v_quote_balance":"0","last_cumulative_funding_x18":"0"}}}},"post_balance":{"base":{"perp":{"product_id":2,"balance":{"amount":"1000000000000000000","v_quote_balance":"-99000000000000000000","last_cumulative_funding_x18":"0"}}}}}],"txs":[]}`), nil
	}
	if bytes.Contains(body, []byte(`"orders"`)) {
		return asterOpenAPIJSONResponse(`{"orders":[{"digest":"0x1111111111111111111111111111111111111111111111111111111111111111","subaccount":"default","product_id":1,"submission_idx":"1","last_fill_submission_idx":"1","amount":"1000000000000000000","price_x18":"99000000000000000000","base_filled":"1000000000000000000","quote_filled":"99000000000000000000","fee":"1000000000000000","builder_fee":"0","closed_amount":"0","realized_pnl":"0","closed_net_entry":"0","closed_margin":"0","first_fill_timestamp":"1720000000","last_fill_timestamp":"1720000000","expiration":"4000000000","nonce":"1","appendix":"0"},{"digest":"0x2222222222222222222222222222222222222222222222222222222222222222","subaccount":"default","product_id":2,"submission_idx":"2","last_fill_submission_idx":"2","amount":"1000000000000000000","price_x18":"99000000000000000000","base_filled":"1000000000000000000","quote_filled":"99000000000000000000","fee":"1000000000000000","builder_fee":"0","closed_amount":"0","realized_pnl":"0","closed_net_entry":"0","closed_margin":"0","first_fill_timestamp":"1720000000","last_fill_timestamp":"1720000000","expiration":"4000000000","nonce":"2","appendix":"0"}]}`), nil
	}
	return asterOpenAPIJSONResponse(`{}`), nil
}

func (router *nadoOpenAPIRouter) file(path string) *http.Response {
	router.t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		body, err = os.ReadFile("../../../" + path)
	}
	if err != nil {
		router.t.Fatalf("read fixture %s: %v", path, err)
	}
	return asterOpenAPIJSONResponse(string(body))
}

func productIDForTicker(ticker string) string {
	if ticker == "ETH-PERP_USDT0" {
		return "2"
	}
	return "1"
}

func TestAsterWebSocketOrderCommandsUseRESTBridge(t *testing.T) {
	router := newAsterOpenAPIRouter()
	settings := Settings{Environment: "testnet", Endpoint: "https://aster-fixture.invalid", HTTPClient: &http.Client{Transport: router}}
	socket := NewAsterUSDTPerp(testAsterUserAddress, testAsterPrivateKey, testAsterUserAddress, settings).WebSocket()

	ack, err := socket.PlaceOrder(context.Background(), exchange.PlaceOrderRequest{
		Instrument:    "BTC-USDT",
		ClientOrderID: "101",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeLimit,
		Quantity:      decimal.NewFromInt(1),
		LimitPrice:    decimal.NewFromInt(99),
		LimitPolicy:   exchange.LimitPolicyResting,
		ReduceOnly:    true,
	})
	if err != nil {
		t.Fatalf("ws PlaceOrder REST bridge: %v", err)
	}
	if ack.Venue != exchange.VenueAster || ack.Product != exchange.ProductPerp || ack.OrderID == "" {
		t.Fatalf("unexpected ack: %+v", ack)
	}
}

func TestOpenAPINadoRESTExecutionMatrix(t *testing.T) {
	router := newNadoOpenAPIRouter(t)
	settings := Settings{Environment: "testnet", Endpoint: "https://nado-fixture.invalid/v1", WebSocketEndpoint: "wss://nado-fixture.invalid/ws", HTTPClient: &http.Client{Transport: router}}
	ctx := context.Background()

	spot := NewNadoSpot(testAsterPrivateKey, "default", settings)
	assertAsterSpotReadMatrix(t, ctx, spot, "ETH-USDT0")
	exerciseAsterOrderBranches(t, ctx, exchange.ProductSpot, spot, "ETH-USDT0")
	if _, err := spot.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: "ETH-USDT0", OrderID: "0x1111111111111111111111111111111111111111111111111111111111111111"}); err != nil {
		t.Fatalf("nado spot CancelOrder: %v", err)
	}
	assertAsterSpotLifecycleMatrix(t, ctx, spot, "ETH-USDT0")

	perp := NewNadoUSDT0Perp(testAsterPrivateKey, "default", settings)
	assertAsterPerpReadMatrix(t, ctx, perp, "ETH-PERP-USDT0")
	exerciseAsterOrderBranches(t, ctx, exchange.ProductPerp, perp, "ETH-PERP-USDT0")
	if _, err := perp.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: "ETH-PERP-USDT0", OrderID: "0x1111111111111111111111111111111111111111111111111111111111111111"}); err != nil {
		t.Fatalf("nado perp CancelOrder: %v", err)
	}
	assertAsterPerpLifecycleMatrix(t, ctx, perp, "ETH-PERP-USDT0")
	leverage, err := perp.SetLeverage(ctx, exchange.SetLeverageRequest{Instrument: "ETH-PERP-USDT0", Leverage: 7})
	if err != nil {
		t.Fatalf("nado SetLeverage: %v", err)
	}
	if leverage.Instrument != "ETH-PERP-USDT0" || leverage.Effective != 0 {
		t.Fatalf("nado SetLeverage result = %+v, want canonical instrument with backend-managed Effective=0", leverage)
	}
}

func TestNadoWebSocketSurfaceExposesEveryMethod(t *testing.T) {
	ws := NewNadoUSDT0Perp(testAsterPrivateKey, "default", Settings{Environment: "testnet"}).WebSocket()
	exercisePerpWebSocketSurface(t, ws)
}

func exerciseSpotWebSocketSurface(t *testing.T, ws exchange.SpotWebSocket) {
	t.Helper()
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sub, err := ws.WatchOrderBook(ctx, exchange.WatchRequest{Instrument: "BTC-USDT"}); err == nil {
		_ = sub.Close()
	}
	if sub, err := ws.WatchBBO(ctx, exchange.WatchRequest{Instrument: "BTC-USDT"}); err == nil {
		_ = sub.Close()
	}
	if sub, err := ws.WatchPublicTrades(ctx, exchange.WatchRequest{Instrument: "BTC-USDT"}); err == nil {
		_ = sub.Close()
	}
	if sub, err := ws.WatchCandles(ctx, exchange.WatchCandlesRequest{Instrument: "BTC-USDT", Interval: "1m"}); err == nil {
		_ = sub.Close()
	}
	if sub, err := ws.WatchOrders(ctx, exchange.WatchRequest{Instrument: "BTC-USDT"}); err == nil {
		_ = sub.Close()
	}
	if sub, err := ws.WatchFills(ctx, exchange.WatchRequest{Instrument: "BTC-USDT"}); err == nil {
		_ = sub.Close()
	}
	if sub, err := ws.WatchBalances(ctx, exchange.WatchAccountRequest{}); err == nil {
		_ = sub.Close()
	}
	_ = ws.Close()
}

func exercisePerpWebSocketSurface(t *testing.T, ws exchange.PerpWebSocket) {
	t.Helper()
	exerciseSpotWebSocketSurface(t, ws)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if sub, err := ws.WatchPositions(ctx, exchange.WatchRequest{Instrument: "BTC-USDT"}); err == nil {
		_ = sub.Close()
	}
	if sub, err := ws.WatchMarkPrice(ctx, exchange.WatchRequest{Instrument: "BTC-USDT"}); err == nil {
		_ = sub.Close()
	}
	if sub, err := ws.WatchFundingRate(ctx, exchange.WatchRequest{Instrument: "BTC-USDT"}); err == nil {
		_ = sub.Close()
	}
	_ = ws.Close()
}
