package sdk

import (
	"context"
	"io"
	"net/http"
	"os"
	"strings"
	"testing"
)

func TestClient_PlaceOrder(t *testing.T) {
	client := requireBybitLiveWrite(t, "BYBIT_TEST_ORDER_QTY", "BYBIT_TEST_ORDER_PRICE")
	symbol := bybitEnvOrDefault("BYBIT_TEST_SYMBOL", bybitLinearSymbol)

	got, err := client.PlaceOrder(context.Background(), PlaceOrderRequest{
		Category:    "linear",
		Symbol:      symbol,
		Side:        bybitEnvOrDefault("BYBIT_TEST_ORDER_SIDE", "Buy"),
		OrderType:   "Limit",
		Qty:         os.Getenv("BYBIT_TEST_ORDER_QTY"),
		Price:       os.Getenv("BYBIT_TEST_ORDER_PRICE"),
		TimeInForce: "GTC",
		OrderLinkID: bybitEnvOrDefault("BYBIT_TEST_ORDER_LINK_ID", ""),
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if got == nil {
		t.Fatal("expected order response")
	}
}

func TestClient_PlaceOrderBuildsNativeOrderParams(t *testing.T) {
	t.Parallel()

	var seenPath string
	var seenBody string
	client := NewClient().
		WithCredentials("test-key", "test-secret").
		WithBaseURL("https://example.test").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenPath = req.URL.Path
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			seenBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(
					`{"retCode":0,"retMsg":"OK","result":{"orderId":"100","orderLinkId":"client-native"}}`,
				)),
				Header: make(http.Header),
			}, nil
		})})

	_, err := client.PlaceOrder(context.Background(), PlaceOrderRequest{
		Category:       "linear",
		Symbol:         "BTCUSDT",
		Side:           "Buy",
		OrderType:      "Limit",
		Qty:            "1",
		Price:          "10",
		TimeInForce:    "GTC",
		OrderLinkID:    "client-native",
		TakeProfit:     "12",
		StopLoss:       "9",
		TPTriggerBy:    "MarkPrice",
		SLTriggerBy:    "IndexPrice",
		TPOrderType:    "Limit",
		SLOrderType:    "Market",
		TPLimitPrice:   "12.1",
		CloseOnTrigger: true,
		IsLeverage:     "1",
		PositionIdx:    1,
		BBOSideType:    "Queue",
		BBOLevel:       "3",
	})
	if err != nil {
		t.Fatalf("PlaceOrder returned error: %v", err)
	}
	if seenPath != "/v5/order/create" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	for _, want := range []string{
		`"takeProfit":"12"`,
		`"stopLoss":"9"`,
		`"tpTriggerBy":"MarkPrice"`,
		`"slTriggerBy":"IndexPrice"`,
		`"tpOrderType":"Limit"`,
		`"slOrderType":"Market"`,
		`"tpLimitPrice":"12.1"`,
		`"closeOnTrigger":true`,
		`"isLeverage":"1"`,
		`"positionIdx":1`,
		`"bboSideType":"Queue"`,
		`"bboLevel":"3"`,
	} {
		if !strings.Contains(seenBody, want) {
			t.Fatalf("expected body to contain %s, got %s", want, seenBody)
		}
	}
}

func TestClient_BatchOrderEndpointsUseOfficialPaths(t *testing.T) {
	t.Parallel()

	var seenPaths []string
	var seenBodies []string
	client := NewClient().
		WithCredentials("test-key", "test-secret").
		WithBaseURL("https://example.test").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			seenPaths = append(seenPaths, req.URL.Path)
			seenBodies = append(seenBodies, string(body))
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(
					`{"retCode":0,"retMsg":"OK","result":{"list":[{"orderId":"100","orderLinkId":"client-batch"}]}}`,
				)),
				Header: make(http.Header),
			}, nil
		})})

	_, err := client.BatchPlaceOrders(context.Background(), BatchPlaceOrdersRequest{
		Category: "linear",
		Request: []BatchPlaceOrderItem{{
			Symbol:      "BTCUSDT",
			Side:        "Buy",
			OrderType:   "Limit",
			Qty:         "1",
			Price:       "10",
			TimeInForce: "GTC",
			OrderLinkID: "client-batch-place",
			TakeProfit:  "12",
		}},
	})
	if err != nil {
		t.Fatalf("BatchPlaceOrders returned error: %v", err)
	}
	_, err = client.BatchAmendOrders(context.Background(), BatchAmendOrdersRequest{
		Category: "option",
		Request: []BatchAmendOrderItem{{
			Symbol:      "BTC-27MAR26-70000-P-USDT",
			OrderID:     "100",
			OrderLinkID: "client-batch-amend",
			OrderIV:     "0.56",
		}},
	})
	if err != nil {
		t.Fatalf("BatchAmendOrders returned error: %v", err)
	}
	_, err = client.BatchCancelOrders(context.Background(), BatchCancelOrdersRequest{
		Category: "linear",
		Request: []BatchCancelOrderItem{{
			Symbol:      "BTCUSDT",
			OrderID:     "100",
			OrderLinkID: "client-batch-cancel",
		}},
	})
	if err != nil {
		t.Fatalf("BatchCancelOrders returned error: %v", err)
	}

	wantPaths := []string{"/v5/order/create-batch", "/v5/order/amend-batch", "/v5/order/cancel-batch"}
	if strings.Join(seenPaths, ",") != strings.Join(wantPaths, ",") {
		t.Fatalf("unexpected paths: got %v want %v", seenPaths, wantPaths)
	}
	for _, body := range seenBodies {
		if !strings.Contains(body, `"request":[`) {
			t.Fatalf("expected request array in body, got %s", body)
		}
	}
	if !strings.Contains(seenBodies[0], `"takeProfit":"12"`) {
		t.Fatalf("expected takeProfit in batch place body, got %s", seenBodies[0])
	}
	if !strings.Contains(seenBodies[1], `"orderIv":"0.56"`) {
		t.Fatalf("expected orderIv in batch amend body, got %s", seenBodies[1])
	}
	if !strings.Contains(seenBodies[2], `"orderId":"100"`) {
		t.Fatalf("expected orderId in batch cancel body, got %s", seenBodies[2])
	}
}

func TestClient_AmendOrderBuildsOrderIV(t *testing.T) {
	t.Parallel()

	var seenPath string
	var seenBody string
	client := NewClient().
		WithCredentials("test-key", "test-secret").
		WithBaseURL("https://example.test").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenPath = req.URL.Path
			body, err := io.ReadAll(req.Body)
			if err != nil {
				t.Fatalf("read body: %v", err)
			}
			seenBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(
					`{"retCode":0,"retMsg":"OK","result":{"orderId":"100","orderLinkId":"client-option-modify"}}`,
				)),
				Header: make(http.Header),
			}, nil
		})})

	_, err := client.AmendOrder(context.Background(), AmendOrderRequest{
		Category:    "option",
		Symbol:      "BTC-27MAR26-70000-P-USDT",
		OrderID:     "100",
		OrderLinkID: "client-option-modify",
		OrderIV:     "0.56",
	})
	if err != nil {
		t.Fatalf("AmendOrder returned error: %v", err)
	}
	if seenPath != "/v5/order/amend" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	if !strings.Contains(seenBody, `"orderIv":"0.56"`) {
		t.Fatalf("expected orderIv in body, got %s", seenBody)
	}
}

func TestClient_CancelOrder(t *testing.T) {
	client := requireBybitLiveWrite(t, "BYBIT_TEST_ORDER_ID")
	symbol := bybitEnvOrDefault("BYBIT_TEST_SYMBOL", bybitLinearSymbol)

	got, err := client.CancelOrder(context.Background(), CancelOrderRequest{
		Category: "linear",
		Symbol:   symbol,
		OrderID:  os.Getenv("BYBIT_TEST_ORDER_ID"),
	})
	if err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if got == nil {
		t.Fatal("expected cancel response")
	}
}

func TestClient_CancelAllOrders(t *testing.T) {
	client := requireBybitLiveWrite(t)
	symbol := bybitEnvOrDefault("BYBIT_TEST_SYMBOL", bybitLinearSymbol)

	err := client.CancelAllOrders(context.Background(), CancelAllOrdersRequest{
		Category: "linear",
		Symbol:   symbol,
	})
	if err != nil {
		t.Fatalf("CancelAllOrders: %v", err)
	}
}

func TestClient_AmendOrder(t *testing.T) {
	client := requireBybitLiveWrite(t, "BYBIT_TEST_ORDER_ID", "BYBIT_TEST_ORDER_QTY", "BYBIT_TEST_ORDER_PRICE")
	symbol := bybitEnvOrDefault("BYBIT_TEST_SYMBOL", bybitLinearSymbol)

	got, err := client.AmendOrder(context.Background(), AmendOrderRequest{
		Category: "linear",
		Symbol:   symbol,
		OrderID:  os.Getenv("BYBIT_TEST_ORDER_ID"),
		Qty:      os.Getenv("BYBIT_TEST_ORDER_QTY"),
		Price:    os.Getenv("BYBIT_TEST_ORDER_PRICE"),
	})
	if err != nil {
		t.Fatalf("AmendOrder: %v", err)
	}
	if got == nil {
		t.Fatal("expected amend response")
	}
}

func TestClient_GetOpenOrders(t *testing.T) {
	got, err := newLivePrivateClient(t).GetOpenOrders(context.Background(), "linear", bybitLinearSymbol)
	if err != nil {
		t.Fatalf("GetOpenOrders: %v", err)
	}
	if got == nil {
		t.Fatal("expected open orders slice")
	}
}

func TestClient_GetOrderHistory(t *testing.T) {
	got, err := newLivePrivateClient(t).GetOrderHistory(context.Background(), "linear", bybitLinearSymbol)
	if err != nil {
		t.Fatalf("GetOrderHistory: %v", err)
	}
	if got == nil {
		t.Fatal("expected order history slice")
	}
}

func TestClient_GetOrderHistoryFiltered(t *testing.T) {
	client := newLivePrivateClient(t)
	orderID := os.Getenv("BYBIT_TEST_ORDER_ID")
	if orderID == "" {
		t.Skip("BYBIT_TEST_ORDER_ID is required for filtered order history live test")
	}

	got, err := client.GetOrderHistoryFiltered(context.Background(), "linear", bybitLinearSymbol, orderID, "")
	if err != nil {
		t.Fatalf("GetOrderHistoryFiltered: %v", err)
	}
	if got == nil {
		t.Fatal("expected filtered order history slice")
	}
}

func TestClient_GetRealtimeOrders(t *testing.T) {
	got, err := newLivePrivateClient(t).GetRealtimeOrders(context.Background(), "linear", bybitLinearSymbol, "", "", "", 0)
	if err != nil {
		t.Fatalf("GetRealtimeOrders: %v", err)
	}
	if got == nil {
		t.Fatal("expected realtime orders slice")
	}
}

func TestClient_GetExecutionsBuildsPrivateQuery(t *testing.T) {
	t.Parallel()

	var seenPath string
	var seenQuery string
	client := NewClient().
		WithCredentials("key", "secret").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenPath = req.URL.Path
			seenQuery = req.URL.RawQuery
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(
					`{"retCode":0,"retMsg":"OK","result":{"nextPageCursor":"","list":[{"execId":"exec-1","orderId":"100","orderLinkId":"client-1","symbol":"BTCUSDT","side":"Buy","execPrice":"10","execQty":"0.5","execFee":"0.01","feeCurrency":"USDT","execTime":"1710000000123"}]}}`,
				)),
				Header: make(http.Header),
			}, nil
		})})

	got, err := client.GetExecutions(context.Background(), "linear", "BTCUSDT", "", "")
	if err != nil {
		t.Fatalf("GetExecutions returned error: %v", err)
	}
	if seenPath != "/v5/execution/list" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	for _, want := range []string{"category=linear", "symbol=BTCUSDT", "limit=50"} {
		if !strings.Contains(seenQuery, want) {
			t.Fatalf("expected query %q in %s", want, seenQuery)
		}
	}
	if len(got) != 1 || got[0].ExecID != "exec-1" || got[0].OrderLinkID != "client-1" {
		t.Fatalf("unexpected executions: %+v", got)
	}
}
