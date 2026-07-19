package factoryclient

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	gate "github.com/QuantProcessing/boltertrader/sdk/gate"
	"github.com/shopspring/decimal"
)

var (
	_ exchange.SpotClient = NewGateSpot("", "", Settings{})
	_ exchange.PerpClient = NewGateUSDTPerp("", "", Settings{})
)

type gateOpenAPIRouter struct {
	mu                sync.Mutex
	placeBodies       []map[string]any
	futuresSettleSeen string
	seen              []string
	failLeverage      bool
}

func (router *gateOpenAPIRouter) RoundTrip(request *http.Request) (*http.Response, error) {
	path := strings.TrimPrefix(request.URL.Path, "/api/v4")
	router.mu.Lock()
	router.seen = append(router.seen, request.Method+" "+path+"?"+request.URL.RawQuery)
	router.mu.Unlock()
	if strings.Contains(request.URL.RawQuery, "secret") || request.Header.Get("SIGN") == "secret" {
		return &http.Response{StatusCode: http.StatusInternalServerError, Body: io.NopCloser(strings.NewReader(`{"label":"LEAK","message":"secret"}`)), Header: make(http.Header)}, nil
	}
	if request.Method == http.MethodPost {
		var body []byte
		if request.Body != nil {
			body, _ = io.ReadAll(request.Body)
		}
		var payload map[string]any
		_ = json.Unmarshal(body, &payload)
		switch path {
		case "/spot/orders":
			router.mu.Lock()
			router.placeBodies = append(router.placeBodies, payload)
			router.mu.Unlock()
			return gateOpenAPIJSONResponse(`{"id":"11","text":"` + gateStringValue(payload["text"]) + `","currency_pair":"` + gateStringValue(payload["currency_pair"]) + `","type":"` + gateStringValue(payload["type"]) + `","side":"` + gateStringValue(payload["side"]) + `","amount":"` + gateStringValue(payload["amount"]) + `","price":"` + gateStringValue(payload["price"]) + `","time_in_force":"` + gateStringValue(payload["time_in_force"]) + `","left":"0","filled_amount":"` + gateStringValue(payload["amount"]) + `","avg_deal_price":"100","status":"closed","finish_as":"filled","create_time_ms":"1720000000000"}`), nil
		case "/futures/usdt/orders":
			router.mu.Lock()
			router.placeBodies = append(router.placeBodies, payload)
			router.futuresSettleSeen = "usdt"
			router.mu.Unlock()
			return gateOpenAPIJSONResponse(`{"id":21,"contract":"` + gateStringValue(payload["contract"]) + `","text":"` + gateStringValue(payload["text"]) + `","size":` + gateNumberValue(payload["size"]) + `,"price":"` + gateStringValue(payload["price"]) + `","tif":"` + gateStringValue(payload["tif"]) + `","left":0,"fill_price":"100","status":"finished","finish_as":"filled","is_reduce_only":` + boolJSON(payload["reduce_only"]) + `,"create_time_ms":"1720000000000"}`), nil
		case "/futures/usdt/positions/BTC_USDT/leverage":
			if router.failLeverage {
				return nil, errors.New("write response lost after send")
			}
			if request.URL.Query().Get("leverage") != "5" {
				return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(`{"label":"INVALID_PARAM_VALUE","message":"missing leverage query"}`)), Header: make(http.Header)}, nil
			}
			return gateOpenAPIJSONResponse(`{"leverage":"5"}`), nil
		}
	}
	if request.Method == http.MethodDelete {
		switch path {
		case "/spot/orders/11":
			return gateOpenAPIJSONResponse(`{"id":"11","text":"t-101","currency_pair":"BTC_USDT","type":"limit","side":"buy","amount":"1","price":"99","left":"1","filled_amount":"0","status":"cancelled","finish_as":"cancelled"}`), nil
		case "/futures/usdt/orders/21":
			return gateOpenAPIJSONResponse(`{"id":21,"contract":"BTC_USDT","size":1,"price":"99","left":1,"status":"finished","finish_as":"cancelled"}`), nil
		}
	}

	switch path {
	case "/spot/currency_pairs":
		return gateOpenAPIJSONResponse(`[{"id":"BTC_USDT","base":"BTC","quote":"USDT","min_base_amount":"0.001","min_quote_amount":"5","amount_precision":3,"precision":1,"trade_status":"tradable"}]`), nil
	case "/spot/order_book":
		return gateOpenAPIJSONResponse(`{"id":7,"current":1720000000000,"update":1720000000000,"bids":[["99","1"]],"asks":[["101","2"]]}`), nil
	case "/spot/candlesticks":
		if request.URL.Query().Get("interval") == "5m" {
			if request.URL.Query().Get("from") != "1720000000" || request.URL.Query().Get("to") != "1720001800" {
				return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(`{"label":"INVALID_PARAM_VALUE","message":"missing candle bounds"}`)), Header: make(http.Header)}, nil
			}
		}
		return gateOpenAPIJSONResponse(`[["1720000000","3","100.5","101","99","100"]]`), nil
	case "/spot/trades":
		return gateOpenAPIJSONResponse(`[{"id":"1","create_time_ms":"1720000000000","currency_pair":"BTC_USDT","side":"buy","amount":"0.1","price":"100"}]`), nil
	case "/spot/orders":
		if request.URL.Query().Get("status") == "finished" {
			return gateOpenAPIJSONResponse(`[{"id":"12","text":"t-102","currency_pair":"BTC_USDT","type":"limit","side":"buy","amount":"1","price":"99","time_in_force":"gtc","left":"0","filled_amount":"1","status":"closed","finish_as":"filled"}]`), nil
		}
		return gateOpenAPIJSONResponse(`[{"id":"11","text":"t-101","currency_pair":"BTC_USDT","type":"limit","side":"buy","amount":"1","price":"99","time_in_force":"gtc","left":"1","filled_amount":"0","status":"open"}]`), nil
	case "/spot/open_orders":
		return gateOpenAPIJSONResponse(`[{"currency_pair":"BTC_USDT","total":1,"orders":[{"id":"11","text":"t-101","currency_pair":"BTC_USDT","type":"limit","side":"buy","amount":"1","price":"99","left":"1","filled_amount":"0","status":"open","finish_as":"open"}]}]`), nil
	case "/spot/my_trades":
		return gateOpenAPIJSONResponse(`[{"id":"1","currency_pair":"BTC_USDT","order_id":"11","side":"buy","role":"taker","amount":"1","price":"99","fee":"0.01","fee_currency":"USDT","create_time_ms":"1720000000000","text":"t-101"}]`), nil
	case "/spot/accounts":
		return gateOpenAPIJSONResponse(`[{"currency":"USDT","available":"100","locked":"2"}]`), nil
	case "/unified/unified_mode":
		return gateOpenAPIJSONResponse(`{"mode":"multi_currency"}`), nil
	case "/unified/accounts":
		return gateOpenAPIJSONResponse(`{"balances":{"USDT":{"available":"100","freeze":"2","equity":"102"}}}`), nil
	case "/futures/usdt/contracts":
		return gateOpenAPIJSONResponse(`[{"name":"BTC_USDT","type":"direct","order_price_round":"0.1","order_size_min":1,"quanto_multiplier":"0.001","funding_rate":"0.0001","mark_price":"100","funding_next_apply":1720003600,"status":"trading"}]`), nil
	case "/futures/usdt/contracts/BTC_USDT":
		return gateOpenAPIJSONResponse(`{"name":"BTC_USDT","type":"direct","order_price_round":"0.1","order_size_min":1,"quanto_multiplier":"0.001","funding_rate":"0.0001","mark_price":"100","funding_next_apply":1720003600,"status":"trading"}`), nil
	case "/futures/usdt/order_book":
		return gateOpenAPIJSONResponse(`{"id":8,"current":"1720000000000","update":"1720000000000","bids":[{"p":"99","s":1}],"asks":[{"p":"101","s":2}]}`), nil
	case "/futures/usdt/candlesticks":
		if request.URL.Query().Get("interval") == "5m" {
			if request.URL.Query().Get("from") != "1720000000" || request.URL.Query().Get("to") != "1720001800" {
				return &http.Response{StatusCode: http.StatusBadRequest, Body: io.NopCloser(strings.NewReader(`{"label":"INVALID_PARAM_VALUE","message":"missing futures candle bounds"}`)), Header: make(http.Header)}, nil
			}
		}
		return gateOpenAPIJSONResponse(`[{"t":1720000000,"v":"3","c":"100.5","h":"101","l":"99","o":"100","sum":"300"}]`), nil
	case "/futures/usdt/trades":
		return gateOpenAPIJSONResponse(`[{"id":2,"create_time":1720000000.125,"contract":"BTC_USDT","size":"1","price":"100"}]`), nil
	case "/futures/usdt/orders":
		if request.URL.Query().Get("status") == "finished" {
			return gateOpenAPIJSONResponse(`[{"id":22,"contract":"BTC_USDT","size":1,"price":"99","left":0,"status":"finished","finish_as":"filled"}]`), nil
		}
		return gateOpenAPIJSONResponse(`[{"id":21,"contract":"BTC_USDT","size":1,"price":"99","left":1,"status":"open"}]`), nil
	case "/futures/usdt/my_trades":
		return gateOpenAPIJSONResponse(`[{"id":2,"create_time":"1720000000","contract":"BTC_USDT","order_id":21,"size":1,"price":"99","role":"taker","fee":"0.01","text":"t-101"}]`), nil
	case "/futures/usdt/accounts":
		return gateOpenAPIJSONResponse(`{"total":"100","available":"90","position_margin":"10","unrealised_pnl":"1","currency":"USDT"}`), nil
	case "/futures/usdt/positions":
		return gateOpenAPIJSONResponse(`[{"contract":"BTC_USDT","size":1,"leverage":"5","value":"100","margin":"10","entry_price":"99","liq_price":"50","mark_price":"100","unrealised_pnl":"1"}]`), nil
	case "/futures/usdt/funding_rate":
		return gateOpenAPIJSONResponse(`[{"t":1720000000,"r":"0.0001"}]`), nil
	}
	return &http.Response{StatusCode: http.StatusNotFound, Body: io.NopCloser(strings.NewReader(`{"label":"NOT_FOUND","message":"unexpected Gate route"}`)), Header: make(http.Header)}, nil
}

func TestOpenAPIGateRESTExecutionMatrix(t *testing.T) {
	ctx := context.Background()
	router := &gateOpenAPIRouter{}
	settings := Settings{Endpoint: "https://openapi.invalid", WebSocketEndpoint: "wss://ws.invalid/v4/ws/spot", Environment: "testnet", HTTPClient: &http.Client{Transport: router}}

	spot := NewGateSpot("key", "secret", settings)
	assertGateSpotReadMatrix(t, ctx, spot, "BTC_USDT")
	exerciseGateOpenAPIOrderBranches(t, ctx, exchange.ProductSpot, spot, "BTC_USDT")
	if _, err := spot.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: "BTC_USDT", OrderID: "11"}); err != nil {
		t.Fatalf("spot CancelOrder: %v", err)
	}
	assertGateSpotLifecycleMatrix(t, ctx, spot, "BTC_USDT")

	perp := NewGateUSDTPerp("key", "secret", settings)
	assertGatePerpReadMatrix(t, ctx, perp, "BTC_USDT")
	exerciseGateOpenAPIOrderBranches(t, ctx, exchange.ProductPerp, perp, "BTC_USDT")
	if _, err := perp.CancelOrder(ctx, exchange.CancelOrderRequest{Instrument: "BTC_USDT", OrderID: "21"}); err != nil {
		t.Fatalf("perp CancelOrder: %v", err)
	}
	assertGatePerpLifecycleMatrix(t, ctx, perp, "BTC_USDT")
	if router.futuresSettleSeen != "usdt" {
		t.Fatalf("Gate perp did not use USDT settlement route")
	}
	assertGateOpenAPIPlaceShapes(t, router.placeBodies)
}

func TestGateSDKFundingHistoryAndSetLeverageMethods(t *testing.T) {
	router := &gateOpenAPIRouter{}
	client := gate.NewClient().WithCredentials("key", "secret").WithBaseURL("https://openapi.invalid").WithHTTPClient(&http.Client{Transport: router})
	rates, err := client.ListFuturesFundingRateHistory(context.Background(), gate.SettleUSDT, "BTC_USDT", time.Unix(1719990000, 0), time.Unix(1720003600, 0), 10)
	if err != nil || len(rates) != 1 {
		t.Fatalf("ListFuturesFundingRateHistory rows=%d err=%v", len(rates), err)
	}
	leverage, err := client.SetFuturesLeverage(context.Background(), gate.SettleUSDT, "BTC_USDT", 5)
	if err != nil || leverage.Leverage != "5" {
		t.Fatalf("SetFuturesLeverage = %+v err=%v", leverage, err)
	}
}

func TestGateOrderHistoryUsesFinishedHistoryEndpoints(t *testing.T) {
	ctx := context.Background()
	router := &gateOpenAPIRouter{}
	settings := Settings{Endpoint: "https://openapi.invalid", Environment: "testnet", HTTPClient: &http.Client{Transport: router}}
	spot := NewGateSpot("key", "secret", settings)
	perp := NewGateUSDTPerp("key", "secret", settings)

	spotRows, err := spot.OrderHistory(ctx, exchange.OrderHistoryRequest{Instrument: "BTC_USDT", Limit: 10})
	if err != nil {
		t.Fatalf("spot OrderHistory: %v", err)
	}
	if len(spotRows.Orders) != 1 || spotRows.Orders[0].OrderID != "12" || spotRows.Orders[0].ClientOrderID != "102" {
		t.Fatalf("spot OrderHistory rows = %+v", spotRows.Orders)
	}
	perpRows, err := perp.OrderHistory(ctx, exchange.OrderHistoryRequest{Instrument: "BTC_USDT", Limit: 10})
	if err != nil {
		t.Fatalf("perp OrderHistory: %v", err)
	}
	if len(perpRows.Orders) != 1 || perpRows.Orders[0].OrderID != "22" {
		t.Fatalf("perp OrderHistory rows = %+v", perpRows.Orders)
	}
	if !router.saw("GET /spot/orders?", "status=finished") {
		t.Fatalf("spot history did not use finished /spot/orders route: %#v", router.seen)
	}
	if router.saw("GET /spot/open_orders?", "") {
		t.Fatalf("spot history used open orders route: %#v", router.seen)
	}
	if !router.saw("GET /futures/usdt/orders?", "status=finished") {
		t.Fatalf("perp history did not use finished futures route: %#v", router.seen)
	}
}

func TestGateMalformedRESTRowsReturnMalformedResponse(t *testing.T) {
	client := NewGateSpot("key", "secret", Settings{Endpoint: "https://openapi.invalid", HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
		return gateOpenAPIJSONResponse(`[{"id":"1","create_time_ms":"1720000000000","currency_pair":"BTC_USDT","side":"hold","amount":"not-a-number","price":"100"}]`), nil
	})}})
	_, err := client.PublicTrades(context.Background(), exchange.PublicTradesRequest{Instrument: "BTC_USDT", Limit: 1})
	assertGateErrorKind(t, err, exchange.KindMalformedResponse)
}

func TestGateOfficialOpenFinishStatusesAreAccepted(t *testing.T) {
	meta := clientMeta{venue: exchange.VenueGate, product: exchange.ProductSpot}
	for _, finishAs := range []string{"open", "unknown"} {
		if err := gateValidateStatus(meta, "PlaceOrder", "open", finishAs); err != nil {
			t.Fatalf("finish_as %q: %v", finishAs, err)
		}
	}
}

func TestGateSpotBalancesUseUnifiedAccountWhenUnifiedModeIsEnabled(t *testing.T) {
	router := &gateOpenAPIRouter{}
	client := NewGateSpot("key", "secret", Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "testnet",
		HTTPClient:  &http.Client{Transport: router},
	})

	balances, err := client.Balances(context.Background())
	if err != nil {
		t.Fatalf("Balances: %v", err)
	}
	if len(balances) != 1 || balances[0].Asset != "USDT" ||
		!balances[0].Available.Equal(decimal.NewFromInt(100)) ||
		!balances[0].Locked.Equal(decimal.NewFromInt(2)) ||
		!balances[0].Total.Equal(decimal.NewFromInt(102)) {
		t.Fatalf("balances=%+v, want unified account values", balances)
	}
	if !router.saw("GET /unified/unified_mode?", "") || !router.saw("GET /unified/accounts?", "") {
		t.Fatalf("unified account routes not used: %#v", router.seen)
	}
	if router.saw("GET /spot/accounts?", "") {
		t.Fatalf("classic spot balance route used for unified account: %#v", router.seen)
	}
}

func TestGateSetLeverageTransportFailureIsAmbiguous(t *testing.T) {
	router := &gateOpenAPIRouter{failLeverage: true}
	client := NewGateUSDTPerp("key", "secret", Settings{Endpoint: "https://openapi.invalid", HTTPClient: &http.Client{Transport: router}})
	_, err := client.SetLeverage(context.Background(), exchange.SetLeverageRequest{Instrument: "BTC_USDT", Leverage: 5})
	assertGateErrorKind(t, err, exchange.KindAmbiguousOutcome)
}

func TestGatePerpPlaceOrderConvertsNeutralBaseQuantityToContractCount(t *testing.T) {
	router := &gateOpenAPIRouter{}
	client := NewGateUSDTPerp("key", "secret", Settings{Endpoint: "https://openapi.invalid", HTTPClient: &http.Client{Transport: router}})
	ack, err := client.PlaceOrder(context.Background(), exchange.PlaceOrderRequest{
		Instrument:    "BTC_USDT",
		ClientOrderID: "101",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeLimit,
		Quantity:      decimal.RequireFromString("0.05"),
		LimitPrice:    decimal.NewFromInt(100),
		LimitPolicy:   exchange.LimitPolicyResting,
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if len(router.placeBodies) != 1 || gateNumberValue(router.placeBodies[0]["size"]) != "50" {
		t.Fatalf("place bodies = %+v, want 50 Gate contracts", router.placeBodies)
	}
	if !ack.FilledQuantity.Equal(decimal.RequireFromString("0.05")) {
		t.Fatalf("ack filled quantity = %s, want neutral base quantity 0.05", ack.FilledQuantity)
	}
}

func TestGatePerpPlaceOrderRejectsQuantityNotAlignedToContractMultiplierBeforeSend(t *testing.T) {
	router := &gateOpenAPIRouter{}
	client := NewGateUSDTPerp("key", "secret", Settings{Endpoint: "https://openapi.invalid", HTTPClient: &http.Client{Transport: router}})
	_, err := client.PlaceOrder(context.Background(), exchange.PlaceOrderRequest{
		Instrument:    "BTC_USDT",
		ClientOrderID: "101",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeLimit,
		Quantity:      decimal.RequireFromString("0.0005"),
		LimitPrice:    decimal.NewFromInt(100),
		LimitPolicy:   exchange.LimitPolicyResting,
	})
	assertGateErrorKind(t, err, exchange.KindInvalidRequest)
	if len(router.placeBodies) != 0 {
		t.Fatalf("misaligned futures quantity reached transport")
	}
}

func TestGateFundingHistoryRequestSemantics(t *testing.T) {
	client := NewGateUSDTPerp("key", "secret", Settings{Endpoint: "https://openapi.invalid", HTTPClient: &http.Client{Transport: &gateOpenAPIRouter{}}})
	_, err := client.FundingRateHistory(context.Background(), exchange.FundingRateHistoryRequest{Instrument: "BTC_USDT", Cursor: "next"})
	assertGateErrorKind(t, err, exchange.KindInvalidRequest)
	_, err = client.FundingRateHistory(context.Background(), exchange.FundingRateHistoryRequest{Instrument: "BTC_USDT", Start: time.Unix(20, 0), End: time.Unix(10, 0)})
	assertGateErrorKind(t, err, exchange.KindInvalidRequest)
	_, err = client.FundingRateHistory(context.Background(), exchange.FundingRateHistoryRequest{Limit: 1})
	assertGateErrorKind(t, err, exchange.KindInvalidRequest)
}

func TestGateOpenOrdersRejectsUnsupportedCursor(t *testing.T) {
	spot := NewGateSpot("key", "secret", Settings{Endpoint: "https://openapi.invalid", HTTPClient: &http.Client{Transport: &gateOpenAPIRouter{}}})
	_, err := spot.OpenOrders(context.Background(), exchange.OpenOrdersRequest{Instrument: "BTC_USDT", Cursor: "next"})
	assertGateErrorKind(t, err, exchange.KindInvalidRequest)

	perp := NewGateUSDTPerp("key", "secret", Settings{Endpoint: "https://openapi.invalid", HTTPClient: &http.Client{Transport: &gateOpenAPIRouter{}}})
	_, err = perp.OpenOrders(context.Background(), exchange.OpenOrdersRequest{Instrument: "BTC_USDT", Cursor: "next"})
	assertGateErrorKind(t, err, exchange.KindInvalidRequest)
}

func TestGateFillsRejectsUnsupportedCursorAndWindows(t *testing.T) {
	spot := NewGateSpot("key", "secret", Settings{Endpoint: "https://openapi.invalid", HTTPClient: &http.Client{Transport: &gateOpenAPIRouter{}}})
	_, err := spot.Fills(context.Background(), exchange.FillsRequest{Instrument: "BTC_USDT", Cursor: "next"})
	assertGateErrorKind(t, err, exchange.KindInvalidRequest)
	_, err = spot.Fills(context.Background(), exchange.FillsRequest{Instrument: "BTC_USDT", Start: time.Unix(20, 0), End: time.Unix(30, 0)})
	assertGateErrorKind(t, err, exchange.KindInvalidRequest)

	perp := NewGateUSDTPerp("key", "secret", Settings{Endpoint: "https://openapi.invalid", HTTPClient: &http.Client{Transport: &gateOpenAPIRouter{}}})
	_, err = perp.Fills(context.Background(), exchange.FillsRequest{Instrument: "BTC_USDT", Cursor: "next"})
	assertGateErrorKind(t, err, exchange.KindInvalidRequest)
	_, err = perp.Fills(context.Background(), exchange.FillsRequest{Instrument: "BTC_USDT", Start: time.Unix(20, 0), End: time.Unix(30, 0)})
	assertGateErrorKind(t, err, exchange.KindInvalidRequest)
}

func TestGateFillsAppliesSupportedOrderIDFilters(t *testing.T) {
	ctx := context.Background()
	router := &gateOpenAPIRouter{}
	settings := Settings{Endpoint: "https://openapi.invalid", HTTPClient: &http.Client{Transport: router}}

	spot := NewGateSpot("key", "secret", settings)
	if _, err := spot.Fills(ctx, exchange.FillsRequest{Instrument: "BTC_USDT", OrderID: "11", Limit: 10}); err != nil {
		t.Fatalf("spot Fills: %v", err)
	}
	if !router.saw("GET /spot/my_trades?", "order_id=11") {
		t.Fatalf("spot fills did not pass order_id: %#v", router.seen)
	}

	perp := NewGateUSDTPerp("key", "secret", settings)
	if _, err := perp.Fills(ctx, exchange.FillsRequest{Instrument: "BTC_USDT", OrderID: "21", Limit: 10}); err != nil {
		t.Fatalf("perp Fills: %v", err)
	}
	if !router.saw("GET /futures/usdt/my_trades?", "order=21") {
		t.Fatalf("perp fills did not pass order filter: %#v", router.seen)
	}
}

func TestGateCandlesApplyIntervalAndWindowSemantics(t *testing.T) {
	ctx := context.Background()
	start := time.Unix(1720000000, 0)
	end := time.Unix(1720001800, 0)
	client := NewGateSpot("key", "secret", Settings{Endpoint: "https://openapi.invalid", HTTPClient: &http.Client{Transport: &gateOpenAPIRouter{}}})
	page, err := client.Candles(ctx, exchange.CandlesRequest{Instrument: "BTC_USDT", Interval: "5m", Start: start, End: end, Limit: 1})
	if err != nil {
		t.Fatalf("Candles: %v", err)
	}
	if len(page.Candles) != 1 {
		t.Fatalf("candles = %d", len(page.Candles))
	}
	if got, want := page.Candles[0].CloseTime, start.Add(5*time.Minute); !got.Equal(want) {
		t.Fatalf("CloseTime = %s, want %s", got, want)
	}
	_, err = client.Candles(ctx, exchange.CandlesRequest{Instrument: "BTC_USDT", Interval: "5m", Cursor: "next"})
	assertGateErrorKind(t, err, exchange.KindInvalidRequest)
	_, err = client.Candles(ctx, exchange.CandlesRequest{Instrument: "BTC_USDT", Interval: "not-real"})
	assertGateErrorKind(t, err, exchange.KindInvalidRequest)
}

func TestGateConcreteClientsRedactStringAndGoString(t *testing.T) {
	spot := NewGateSpot("GATE-KEY-CANARY", "GATE-SECRET-CANARY", Settings{})
	perp := NewGateUSDTPerp("GATE-KEY-CANARY", "GATE-SECRET-CANARY", Settings{})
	for name, client := range map[string]any{"spot": spot, "perp": perp} {
		for _, rendered := range []string{fmt.Sprintf("%v", client), fmt.Sprintf("%#v", client)} {
			if strings.Contains(rendered, "GATE-KEY-CANARY") || strings.Contains(rendered, "GATE-SECRET-CANARY") {
				t.Fatalf("%s rendered credentials: %s", name, rendered)
			}
			if !strings.Contains(rendered, "credentials:redacted") {
				t.Fatalf("%s redaction marker missing: %s", name, rendered)
			}
		}
	}
}

func assertGateOpenAPIPlaceShapes(t *testing.T, bodies []map[string]any) {
	t.Helper()
	if len(bodies) != 8 {
		t.Fatalf("captured Gate place requests = %d, want 8", len(bodies))
	}
	wantTypes := []string{"market", "limit", "limit", "limit", "", "", "", ""}
	wantTIF := []string{"ioc", "gtc", "ioc", "poc", "ioc", "gtc", "ioc", "poc"}
	for index, body := range bodies {
		if got, want := gateStringValue(body["text"]), fmt.Sprintf("t-%d", 101+index%4); got != want {
			t.Errorf("place[%d] text = %q, want %q", index, got, want)
		}
		if index < 4 && gateStringValue(body["type"]) != wantTypes[index] {
			t.Errorf("spot place[%d] type=%q", index, gateStringValue(body["type"]))
		}
		if got := gateStringValue(body["time_in_force"]) + gateStringValue(body["tif"]); got != wantTIF[index] {
			t.Errorf("place[%d] tif = %q, want %q", index, got, wantTIF[index])
		}
		if index >= 4 && body["reduce_only"] != true {
			t.Errorf("perp place[%d] reduce_only=%#v", index, body["reduce_only"])
		}
	}
	if got, want := gateStringValue(bodies[0]["amount"]), "101"; got != want {
		t.Errorf("spot market buy amount = %q, want quote amount %q", got, want)
	}
}

func boolJSON(value any) string {
	if b, ok := value.(bool); ok && b {
		return "true"
	}
	return "false"
}

func gateOpenAPIJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func gateStringValue(value any) string {
	if text, ok := value.(string); ok {
		return text
	}
	return ""
}

func gateNumberValue(value any) string {
	switch number := value.(type) {
	case float64:
		return fmt.Sprintf("%.0f", number)
	case json.Number:
		return number.String()
	default:
		return fmt.Sprint(value)
	}
}

func (router *gateOpenAPIRouter) saw(prefix, contains string) bool {
	router.mu.Lock()
	defer router.mu.Unlock()
	for _, seen := range router.seen {
		if strings.HasPrefix(seen, prefix) && (contains == "" || strings.Contains(seen, contains)) {
			return true
		}
	}
	return false
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func assertGateErrorKind(t *testing.T, err error, kind exchange.ErrorKind) {
	t.Helper()
	var normalized *exchange.Error
	if !errors.As(err, &normalized) {
		t.Fatalf("error = %v, want exchange error kind %s", err, kind)
	}
	if normalized.Kind() != kind {
		t.Fatalf("error kind = %s, want %s (%v)", normalized.Kind(), kind, err)
	}
}

func exerciseGateOpenAPIOrderBranches(t *testing.T, ctx context.Context, product exchange.Product, client any, instrument string) {
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
			continue
		}
		if _, err := client.(exchange.SpotClient).PlaceOrder(ctx, cases[index]); err != nil {
			t.Fatalf("spot PlaceOrder %s/%s: %v", cases[index].Type, cases[index].LimitPolicy, err)
		}
	}
}

func assertGateSpotReadMatrix(t *testing.T, ctx context.Context, client exchange.SpotClient, instrument string) {
	t.Helper()
	if rows, err := client.Instruments(ctx); err != nil || len(rows) != 1 {
		t.Fatalf("Instruments: rows=%d err=%v", len(rows), err)
	}
	if row, err := client.OrderBook(ctx, exchange.OrderBookRequest{Instrument: instrument, Limit: 5}); err != nil || len(row.Bids) != 1 {
		t.Fatalf("OrderBook: bids=%d err=%v", len(row.Bids), err)
	}
	if rows, err := client.Candles(ctx, exchange.CandlesRequest{Instrument: instrument, Interval: "1m", Limit: 1}); err != nil || len(rows.Candles) != 1 {
		t.Fatalf("Candles: rows=%d err=%v", len(rows.Candles), err)
	}
	if rows, err := client.PublicTrades(ctx, exchange.PublicTradesRequest{Instrument: instrument, Limit: 1}); err != nil || len(rows.Trades) != 1 {
		t.Fatalf("PublicTrades: rows=%d err=%v", len(rows.Trades), err)
	}
}

func assertGatePerpReadMatrix(t *testing.T, ctx context.Context, client exchange.PerpClient, instrument string) {
	t.Helper()
	if rows, err := client.Instruments(ctx); err != nil || len(rows) != 1 {
		t.Fatalf("Instruments: rows=%d err=%v", len(rows), err)
	} else if !rows[0].QuantityIncrement.Equal(decimal.RequireFromString("0.001")) || !rows[0].MinQuantity.Equal(decimal.RequireFromString("0.001")) {
		t.Fatalf("Instrument quantity semantics = increment %s min %s, want neutral base units", rows[0].QuantityIncrement, rows[0].MinQuantity)
	}
	if row, err := client.OrderBook(ctx, exchange.OrderBookRequest{Instrument: instrument, Limit: 5}); err != nil || len(row.Bids) != 1 {
		t.Fatalf("OrderBook: bids=%d err=%v", len(row.Bids), err)
	} else if !row.Bids[0].Quantity.Equal(decimal.RequireFromString("0.001")) {
		t.Fatalf("OrderBook bid quantity = %s, want neutral base quantity 0.001", row.Bids[0].Quantity)
	}
	if rows, err := client.Candles(ctx, exchange.CandlesRequest{Instrument: instrument, Interval: "1m", Limit: 1}); err != nil || len(rows.Candles) != 1 {
		t.Fatalf("Candles: rows=%d err=%v", len(rows.Candles), err)
	}
	if rows, err := client.PublicTrades(ctx, exchange.PublicTradesRequest{Instrument: instrument, Limit: 1}); err != nil || len(rows.Trades) != 1 {
		t.Fatalf("PublicTrades: rows=%d err=%v", len(rows.Trades), err)
	} else if !rows.Trades[0].Quantity.Equal(decimal.RequireFromString("0.001")) {
		t.Fatalf("PublicTrades quantity = %s, want neutral base quantity 0.001", rows.Trades[0].Quantity)
	}
}

func assertGateSpotLifecycleMatrix(t *testing.T, ctx context.Context, client exchange.SpotClient, instrument string) {
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
	if rows, err := client.Balances(ctx); err != nil || len(rows) != 1 {
		t.Fatalf("Balances: rows=%d err=%v", len(rows), err)
	}
	if row, err := client.SpotAccount(ctx); err != nil || len(row.Balances) != 1 {
		t.Fatalf("SpotAccount: balances=%d err=%v", len(row.Balances), err)
	}
}

func assertGatePerpLifecycleMatrix(t *testing.T, ctx context.Context, client exchange.PerpClient, instrument string) {
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
	if rows, err := client.Balances(ctx); err != nil || len(rows) != 1 {
		t.Fatalf("Balances: rows=%d err=%v", len(rows), err)
	}
	if row, err := client.PerpAccount(ctx); err != nil || !row.MarginUsed.Valid || !row.MarginUsed.Value.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("PerpAccount: row=%+v err=%v", row, err)
	}
	if rows, err := client.Positions(ctx, exchange.PositionsRequest{Instrument: instrument}); err != nil || len(rows) != 1 {
		t.Fatalf("Positions: rows=%d err=%v", len(rows), err)
	} else if !rows[0].Quantity.Equal(decimal.RequireFromString("0.001")) {
		t.Fatalf("Positions quantity = %s, want neutral base quantity 0.001", rows[0].Quantity)
	}
	if row, err := client.FundingRate(ctx, exchange.FundingRateRequest{Instrument: instrument}); err != nil || !row.Rate.Equal(decimal.RequireFromString("0.0001")) {
		t.Fatalf("FundingRate: rate=%s err=%v", row.Rate, err)
	}
	if rows, err := client.FundingRateHistory(ctx, exchange.FundingRateHistoryRequest{Instrument: instrument, Limit: 10}); err != nil || len(rows.Rates) != 1 {
		t.Fatalf("FundingRateHistory: rows=%d err=%v", len(rows.Rates), err)
	}
	if row, err := client.SetLeverage(ctx, exchange.SetLeverageRequest{Instrument: instrument, Leverage: 5}); err != nil || row.Effective != 5 {
		t.Fatalf("SetLeverage: effective=%d err=%v", row.Effective, err)
	}
}
