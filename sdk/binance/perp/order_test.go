package perp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"testing"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

func TestClient_PlaceOrder(t *testing.T) {
	client := requireBinancePerpLiveWrite(t)
	testenv.RequireEnv(t, "BINANCE_PERP_TEST_ORDER_QTY", "BINANCE_PERP_TEST_ORDER_PRICE")
	got, err := client.PlaceOrder(context.Background(), PlaceOrderParams{
		Symbol:           envOrDefault("BINANCE_PERP_TEST_SYMBOL", binancePerpTestSymbol),
		Side:             envOrDefault("BINANCE_PERP_TEST_ORDER_SIDE", "BUY"),
		Type:             "LIMIT",
		TimeInForce:      "GTC",
		Quantity:         os.Getenv("BINANCE_PERP_TEST_ORDER_QTY"),
		Price:            os.Getenv("BINANCE_PERP_TEST_ORDER_PRICE"),
		NewClientOrderID: envOrDefault("BINANCE_PERP_TEST_CLIENT_ORDER_ID", "sdk-live-write-test"),
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if got.OrderID == 0 {
		t.Fatalf("unexpected place order response: %+v", got)
	}
}

func TestClient_CancelOrder(t *testing.T) {
	client := requireBinancePerpLiveWrite(t)
	testenv.RequireEnv(t, "BINANCE_PERP_TEST_CANCEL_ORDER_ID")
	got, err := client.CancelOrder(context.Background(), CancelOrderParams{
		Symbol:  envOrDefault("BINANCE_PERP_TEST_SYMBOL", binancePerpTestSymbol),
		OrderID: os.Getenv("BINANCE_PERP_TEST_CANCEL_ORDER_ID"),
	})
	if err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if got.OrderID == 0 {
		t.Fatalf("unexpected cancel response: %+v", got)
	}
}

func TestClient_ModifyOrder(t *testing.T) {
	client := requireBinancePerpLiveWrite(t)
	testenv.RequireEnv(t, "BINANCE_PERP_TEST_MODIFY_ORDER_ID", "BINANCE_PERP_TEST_ORDER_QTY", "BINANCE_PERP_TEST_ORDER_PRICE")
	orderID, err := strconv.ParseInt(os.Getenv("BINANCE_PERP_TEST_MODIFY_ORDER_ID"), 10, 64)
	if err != nil {
		t.Fatalf("parse BINANCE_PERP_TEST_MODIFY_ORDER_ID: %v", err)
	}
	got, err := client.ModifyOrder(context.Background(), ModifyOrderParams{
		Symbol:   envOrDefault("BINANCE_PERP_TEST_SYMBOL", binancePerpTestSymbol),
		Side:     envOrDefault("BINANCE_PERP_TEST_ORDER_SIDE", "BUY"),
		OrderID:  orderID,
		Quantity: os.Getenv("BINANCE_PERP_TEST_ORDER_QTY"),
		Price:    os.Getenv("BINANCE_PERP_TEST_ORDER_PRICE"),
	})
	if err != nil {
		t.Fatalf("ModifyOrder: %v", err)
	}
	if got.OrderID == 0 {
		t.Fatalf("unexpected modify response: %+v", got)
	}
}

func TestClient_CancelAllOpenOrders(t *testing.T) {
	err := requireBinancePerpLiveWrite(t).CancelAllOpenOrders(context.Background(), CancelAllOrdersParams{
		Symbol: envOrDefault("BINANCE_PERP_TEST_SYMBOL", binancePerpTestSymbol),
	})
	if err != nil {
		t.Fatalf("CancelAllOpenOrders: %v", err)
	}
}

func TestClient_AlgoOrderRESTMethods(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/fapi/v1/algoOrder", func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		switch r.Method {
		case http.MethodPost:
			assertQueryValue(t, q.Get("symbol"), "BTCUSDT", "symbol")
			assertQueryValue(t, q.Get("side"), "BUY", "side")
			assertQueryValue(t, q.Get("type"), "STOP_MARKET", "type")
			assertQueryValue(t, q.Get("algoType"), "CONDITIONAL", "algoType")
			assertQueryValue(t, q.Get("positionSide"), "LONG", "positionSide")
			assertQueryValue(t, q.Get("quantity"), "1.5", "quantity")
			assertQueryValue(t, q.Get("price"), "61000", "price")
			assertQueryValue(t, q.Get("triggerPrice"), "60000", "triggerPrice")
			assertQueryValue(t, q.Get("timeInForce"), "GTC", "timeInForce")
			assertQueryValue(t, q.Get("workingType"), "MARK_PRICE", "workingType")
			assertQueryValue(t, q.Get("priceMatch"), "OPPONENT", "priceMatch")
			assertQueryValue(t, q.Get("closePosition"), "true", "closePosition")
			assertQueryValue(t, q.Get("priceProtect"), "true", "priceProtect")
			assertQueryValue(t, q.Get("reduceOnly"), "true", "reduceOnly")
			assertQueryValue(t, q.Get("activatePrice"), "59000", "activatePrice")
			assertQueryValue(t, q.Get("callbackRate"), "1.2", "callbackRate")
			assertQueryValue(t, q.Get("clientAlgoId"), "algo-client", "clientAlgoId")
			assertQueryValue(t, q.Get("goodTillDate"), "1710000000000", "goodTillDate")
			assertQueryValue(t, q.Get("recvWindow"), "5000", "recvWindow")
			_, _ = w.Write([]byte(`{"algoId":123,"clientAlgoId":"algo-client","algoType":"CONDITIONAL","orderType":"STOP_MARKET","symbol":"BTCUSDT","side":"BUY","positionSide":"LONG","quantity":"1.5","algoStatus":"NEW","triggerPrice":"60000","price":"61000","workingType":"MARK_PRICE","closePosition":true,"priceProtect":true,"reduceOnly":true,"activatePrice":"59000","callbackRate":"1.2","createTime":1700000000000,"updateTime":1700000001000}`))
		case http.MethodGet:
			assertQueryValue(t, q.Get("clientAlgoId"), "algo-client", "clientAlgoId")
			assertQueryValue(t, q.Get("recvWindow"), "5000", "recvWindow")
			_, _ = w.Write([]byte(`{"algoId":123,"clientAlgoId":"algo-client","algoType":"CONDITIONAL","orderType":"STOP_MARKET","symbol":"BTCUSDT","side":"BUY","positionSide":"LONG","quantity":"1.5","algoStatus":"TRIGGERED","actualOrderId":"456","createTime":1700000000000}`))
		case http.MethodDelete:
			assertQueryValue(t, q.Get("algoId"), "123", "algoId")
			assertQueryValue(t, q.Get("recvWindow"), "5000", "recvWindow")
			_, _ = w.Write([]byte(`{"algoId":123,"clientAlgoId":"algo-client","code":"200","msg":"success"}`))
		default:
			t.Fatalf("unexpected algoOrder method: %s", r.Method)
		}
	})
	mux.HandleFunc("/fapi/v1/openAlgoOrders", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected openAlgoOrders method: %s", r.Method)
		}
		q := r.URL.Query()
		assertQueryValue(t, q.Get("symbol"), "BTCUSDT", "symbol")
		assertQueryValue(t, q.Get("algoType"), "CONDITIONAL", "algoType")
		assertQueryValue(t, q.Get("algoId"), "123", "algoId")
		assertQueryValue(t, q.Get("recvWindow"), "5000", "recvWindow")
		_, _ = w.Write([]byte(`[{"algoId":123,"clientAlgoId":"algo-client","algoType":"CONDITIONAL","orderType":"STOP_MARKET","symbol":"BTCUSDT","side":"BUY","quantity":"1.5","algoStatus":"NEW"}]`))
	})
	mux.HandleFunc("/fapi/v1/allAlgoOrders", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			t.Fatalf("unexpected allAlgoOrders method: %s", r.Method)
		}
		q := r.URL.Query()
		assertQueryValue(t, q.Get("symbol"), "BTCUSDT", "symbol")
		assertQueryValue(t, q.Get("algoId"), "123", "algoId")
		assertQueryValue(t, q.Get("startTime"), "1700000000000", "startTime")
		assertQueryValue(t, q.Get("endTime"), "1700003600000", "endTime")
		assertQueryValue(t, q.Get("page"), "2", "page")
		assertQueryValue(t, q.Get("limit"), "100", "limit")
		assertQueryValue(t, q.Get("recvWindow"), "5000", "recvWindow")
		_, _ = w.Write([]byte(`[{"algoId":123,"clientAlgoId":"algo-client","algoType":"CONDITIONAL","orderType":"STOP_MARKET","symbol":"BTCUSDT","side":"BUY","quantity":"1.5","algoStatus":"CANCELED"}]`))
	})
	mux.HandleFunc("/fapi/v1/algoOpenOrders", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodDelete {
			t.Fatalf("unexpected algoOpenOrders method: %s", r.Method)
		}
		q := r.URL.Query()
		assertQueryValue(t, q.Get("symbol"), "BTCUSDT", "symbol")
		assertQueryValue(t, q.Get("recvWindow"), "5000", "recvWindow")
		_, _ = w.Write([]byte(`{"code":200,"msg":"success"}`))
	})
	server := httptest.NewServer(mux)
	defer server.Close()

	client := NewClient().
		WithBaseURL(server.URL).
		WithCredentials("key", "secret").
		WithRateLimiter(nil)

	created, err := client.NewAlgoOrder(context.Background(), NewAlgoOrderParams{
		Symbol:        "BTCUSDT",
		Side:          "BUY",
		PositionSide:  "LONG",
		Type:          "STOP_MARKET",
		TimeInForce:   "GTC",
		Quantity:      "1.5",
		Price:         "61000",
		TriggerPrice:  "60000",
		WorkingType:   "MARK_PRICE",
		PriceMatch:    "OPPONENT",
		ClosePosition: true,
		PriceProtect:  true,
		ReduceOnly:    true,
		ActivatePrice: "59000",
		CallbackRate:  "1.2",
		ClientAlgoID:  "algo-client",
		GoodTillDate:  1710000000000,
		RecvWindow:    5000,
	})
	if err != nil {
		t.Fatalf("NewAlgoOrder: %v", err)
	}
	if created.AlgoID != 123 || created.ClientAlgoID != "algo-client" || !created.ClosePosition || !created.PriceProtect || !created.ReduceOnly {
		t.Fatalf("unexpected created algo response: %+v", created)
	}

	queried, err := client.QueryAlgoOrder(context.Background(), AlgoOrderLookupParams{
		ClientAlgoID: "algo-client",
		RecvWindow:   5000,
	})
	if err != nil {
		t.Fatalf("QueryAlgoOrder: %v", err)
	}
	if queried.ActualOrderID != "456" || queried.AlgoStatus != "TRIGGERED" {
		t.Fatalf("unexpected queried algo response: %+v", queried)
	}

	cancelled, err := client.CancelAlgoOrder(context.Background(), AlgoOrderLookupParams{
		AlgoID:     123,
		RecvWindow: 5000,
	})
	if err != nil {
		t.Fatalf("CancelAlgoOrder: %v", err)
	}
	if cancelled.AlgoID != 123 || cancelled.Code != "200" {
		t.Fatalf("unexpected cancel algo response: %+v", cancelled)
	}

	openOrders, err := client.QueryOpenAlgoOrders(context.Background(), QueryOpenAlgoOrdersParams{
		Symbol:     "BTCUSDT",
		AlgoType:   "CONDITIONAL",
		AlgoID:     123,
		RecvWindow: 5000,
	})
	if err != nil {
		t.Fatalf("QueryOpenAlgoOrders: %v", err)
	}
	if len(openOrders) != 1 || openOrders[0].AlgoID != 123 {
		t.Fatalf("unexpected open algo orders: %+v", openOrders)
	}

	allOrders, err := client.QueryAllAlgoOrders(context.Background(), QueryAllAlgoOrdersParams{
		Symbol:     "BTCUSDT",
		AlgoID:     123,
		StartTime:  1700000000000,
		EndTime:    1700003600000,
		Page:       2,
		Limit:      100,
		RecvWindow: 5000,
	})
	if err != nil {
		t.Fatalf("QueryAllAlgoOrders: %v", err)
	}
	if len(allOrders) != 1 || allOrders[0].AlgoStatus != "CANCELED" {
		t.Fatalf("unexpected all algo orders: %+v", allOrders)
	}

	ok, err := client.CancelAllOpenAlgoOrders(context.Background(), CancelAllOpenAlgoOrdersParams{
		Symbol:     "BTCUSDT",
		RecvWindow: 5000,
	})
	if err != nil {
		t.Fatalf("CancelAllOpenAlgoOrders: %v", err)
	}
	if !ok {
		t.Fatal("expected cancel all open algo orders to return true")
	}
}

func TestClient_AlgoOrderLookupRequiresIdentifier(t *testing.T) {
	client := NewClient().WithRateLimiter(nil)
	if _, err := client.QueryAlgoOrder(context.Background(), AlgoOrderLookupParams{}); err == nil {
		t.Fatal("expected QueryAlgoOrder without algoId or clientAlgoId to fail")
	}
	if _, err := client.CancelAlgoOrder(context.Background(), AlgoOrderLookupParams{}); err == nil {
		t.Fatal("expected CancelAlgoOrder without algoId or clientAlgoId to fail")
	}
}

func TestClient_GetOrder(t *testing.T) {
	client := newLivePrivateClient(t)
	testenv.RequireEnv(t, "BINANCE_PERP_TEST_ORDER_ID")
	orderID, err := strconv.ParseInt(os.Getenv("BINANCE_PERP_TEST_ORDER_ID"), 10, 64)
	if err != nil {
		t.Fatalf("parse BINANCE_PERP_TEST_ORDER_ID: %v", err)
	}
	got, err := client.GetOrder(context.Background(), envOrDefault("BINANCE_PERP_TEST_SYMBOL", binancePerpTestSymbol), orderID, "")
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if got.OrderID != orderID {
		t.Fatalf("unexpected get order response: %+v", got)
	}
}

func TestClient_GetOpenOrders(t *testing.T) {
	got, err := newLivePrivateClient(t).GetOpenOrders(context.Background(), envOrDefault("BINANCE_PERP_TEST_SYMBOL", binancePerpTestSymbol))
	if err != nil {
		t.Fatalf("GetOpenOrders: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil open orders slice")
	}
}

func TestClient_AllOrders(t *testing.T) {
	got, err := newLivePrivateClient(t).AllOrders(context.Background(), envOrDefault("BINANCE_PERP_TEST_SYMBOL", binancePerpTestSymbol), 5, 0, 0, 0)
	if err != nil {
		t.Fatalf("AllOrders: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil all orders slice")
	}
}

func TestClient_MyTrades(t *testing.T) {
	got, err := newLivePrivateClient(t).MyTrades(context.Background(), envOrDefault("BINANCE_PERP_TEST_SYMBOL", binancePerpTestSymbol), 5, 0, 0, 0)
	if err != nil {
		t.Fatalf("MyTrades: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil trades slice")
	}
}

func assertQueryValue(t *testing.T, got, want, name string) {
	t.Helper()
	if got != want {
		t.Fatalf("unexpected %s query value: got %q want %q", name, got, want)
	}
}
