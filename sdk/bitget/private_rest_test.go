package sdk

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"testing"
)

func TestOrderCommandsReturnTypedResponseError(t *testing.T) {
	client := NewClient().WithCredentials("key", "secret", "passphrase").WithBaseURL("https://example.test").WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(*http.Request) (*http.Response, error) {
		return &http.Response{
			StatusCode: http.StatusOK,
			Body:       io.NopCloser(strings.NewReader(`{"code":"43001","msg":"order does not exist","data":{}}`)),
			Header:     make(http.Header),
		}, nil
	})})
	tests := []struct {
		name string
		run  func() error
	}{
		{name: "submit", run: func() error { _, err := client.PlaceOrder(context.Background(), &PlaceOrderRequest{}); return err }},
		{name: "cancel", run: func() error { _, err := client.CancelOrder(context.Background(), &CancelOrderRequest{}); return err }},
		{name: "modify", run: func() error { _, err := client.ModifyOrder(context.Background(), &ModifyOrderRequest{}); return err }},
		{name: "cancel all", run: func() error { return client.CancelAllOrders(context.Background(), &CancelAllOrdersRequest{}) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := test.run()
			var responseErr *ResponseError
			if !errors.As(err, &responseErr) || responseErr.Code != "43001" || responseErr.Message != "order does not exist" {
				t.Fatalf("error=%v (%T), want typed response error", err, err)
			}
		})
	}
}

func TestOrderCommandsRejectMalformedSuccessResponses(t *testing.T) {
	tests := []struct {
		name string
		body string
		run  func(*Client) error
		want string
	}{
		{
			name: "missing response code",
			body: `{"msg":"success","data":{"orderId":"100","clientOid":"client-1"}}`,
			run: func(client *Client) error {
				_, err := client.PlaceOrder(context.Background(), &PlaceOrderRequest{ClientOID: "client-1"})
				return err
			},
			want: "without code",
		},
		{
			name: "missing response data",
			body: `{"code":"00000","msg":"success"}`,
			run: func(client *Client) error {
				_, err := client.PlaceOrder(context.Background(), &PlaceOrderRequest{ClientOID: "client-1"})
				return err
			},
			want: "without data",
		},
		{
			name: "place missing order id",
			body: `{"code":"00000","msg":"success","data":{"clientOid":"client-1"}}`,
			run: func(client *Client) error {
				_, err := client.PlaceOrder(context.Background(), &PlaceOrderRequest{ClientOID: "client-1"})
				return err
			},
			want: "without order id",
		},
		{
			name: "place client id mismatch",
			body: `{"code":"00000","msg":"success","data":{"orderId":"100","clientOid":"other"}}`,
			run: func(client *Client) error {
				_, err := client.PlaceOrder(context.Background(), &PlaceOrderRequest{ClientOID: "client-1"})
				return err
			},
			want: "mismatched client order id",
		},
		{
			name: "cancel order id mismatch",
			body: `{"code":"00000","msg":"success","data":{"orderId":"101","clientOid":"client-1"}}`,
			run: func(client *Client) error {
				_, err := client.CancelOrder(context.Background(), &CancelOrderRequest{OrderID: "100", ClientOID: "client-1"})
				return err
			},
			want: "mismatched order id",
		},
		{
			name: "modify client id mismatch",
			body: `{"code":"00000","msg":"success","data":{"orderId":"100","clientOid":"other"}}`,
			run: func(client *Client) error {
				_, err := client.ModifyOrder(context.Background(), &ModifyOrderRequest{OrderID: "100", ClientOID: "client-1"})
				return err
			},
			want: "mismatched client order id",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client := NewClient().
				WithCredentials("key", "secret", "passphrase").
				WithBaseURL("https://example.test").
				WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(*http.Request) (*http.Response, error) {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(strings.NewReader(test.body)),
						Header:     make(http.Header),
					}, nil
				})})
			err := test.run(client)
			if err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v, want text %q", err, test.want)
			}
		})
	}
}

func TestBitgetResponseErrorDefinitiveClassification(t *testing.T) {
	tests := []struct {
		name string
		code string
		want bool
	}{
		{name: "order business rejection", code: "43001", want: true},
		{name: "UTA order business rejection", code: "25204", want: true},
		{name: "auth rejection", code: "40006", want: true},
		{name: "rate limit", code: "42900"},
		{name: "timeout", code: "40010"},
		{name: "backend", code: "40725"},
		{name: "release window", code: "40808"},
		{name: "unknown operation result", code: "45001"},
		{name: "unknown future code", code: "99999"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := IsDefinitiveCommandRejection(&ResponseError{Code: test.code}); got != test.want {
				t.Fatalf("code %s definitive=%t, want %t", test.code, got, test.want)
			}
		})
	}
}

// Keep the exported response shape source-compatible for callers that use an
// unkeyed composite literal. Cursor pagination is an SDK implementation detail.
var _ = OrderList{nil, ""}

func TestClient_PlaceOrder(t *testing.T) {
	client := requireBitgetLiveWrite(t, "BITGET_TEST_ORDER_QTY", "BITGET_TEST_ORDER_PRICE")
	symbol := bitgetEnvOrDefault("BITGET_TEST_SYMBOL", bitgetSpotSymbol)

	got, err := client.PlaceOrder(context.Background(), &PlaceOrderRequest{
		Category:    bitgetSpotCategory,
		Symbol:      symbol,
		Qty:         os.Getenv("BITGET_TEST_ORDER_QTY"),
		Price:       os.Getenv("BITGET_TEST_ORDER_PRICE"),
		Side:        bitgetEnvOrDefault("BITGET_TEST_ORDER_SIDE", "buy"),
		OrderType:   "limit",
		TimeInForce: "gtc",
		ClientOID:   bitgetEnvOrDefault("BITGET_TEST_CLIENT_ORDER_ID", ""),
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if got == nil {
		t.Fatal("expected place order response")
	}
}

func TestClient_CancelOrder(t *testing.T) {
	client := requireBitgetLiveWrite(t, "BITGET_TEST_ORDER_ID")
	symbol := bitgetEnvOrDefault("BITGET_TEST_SYMBOL", bitgetSpotSymbol)

	got, err := client.CancelOrder(context.Background(), &CancelOrderRequest{
		Category: bitgetSpotCategory,
		Symbol:   symbol,
		OrderID:  os.Getenv("BITGET_TEST_ORDER_ID"),
	})
	if err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if got == nil {
		t.Fatal("expected cancel order response")
	}
}

func TestClient_CancelAllOrders(t *testing.T) {
	client := requireBitgetLiveWrite(t)
	symbol := bitgetEnvOrDefault("BITGET_TEST_SYMBOL", bitgetSpotSymbol)

	if err := client.CancelAllOrders(context.Background(), &CancelAllOrdersRequest{Category: bitgetSpotCategory, Symbol: symbol}); err != nil {
		t.Fatalf("CancelAllOrders: %v", err)
	}
}

func TestClient_ModifyOrder(t *testing.T) {
	client := requireBitgetLiveWrite(t, "BITGET_TEST_ORDER_ID", "BITGET_TEST_ORDER_PRICE")
	symbol := bitgetEnvOrDefault("BITGET_TEST_SYMBOL", bitgetSpotSymbol)

	got, err := client.ModifyOrder(context.Background(), &ModifyOrderRequest{
		Category: bitgetSpotCategory,
		Symbol:   symbol,
		OrderID:  os.Getenv("BITGET_TEST_ORDER_ID"),
		NewPrice: os.Getenv("BITGET_TEST_ORDER_PRICE"),
		NewQty:   os.Getenv("BITGET_TEST_ORDER_QTY"),
	})
	if err != nil {
		t.Fatalf("ModifyOrder: %v", err)
	}
	if got == nil {
		t.Fatal("expected modify order response")
	}
}

func TestClient_ModifyOrderBuildsUTARequest(t *testing.T) {
	var seenPath string
	var seenBody string
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			seenPath = req.URL.Path
			seenBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"code":"00000","msg":"success","data":{"orderId":"100","clientOid":"client-1"}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	got, err := client.ModifyOrder(context.Background(), &ModifyOrderRequest{
		Category:   bitgetPerpCategory,
		Symbol:     bitgetPerpSymbol,
		OrderID:    "100",
		ClientOID:  "client-1",
		NewQty:     "2",
		NewPrice:   "11",
		AutoCancel: "yes",
	})
	if err != nil {
		t.Fatalf("ModifyOrder: %v", err)
	}
	if got == nil || got.OrderID != "100" {
		t.Fatalf("unexpected modify response: %+v", got)
	}
	if seenPath != "/api/v3/trade/modify-order" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	for _, want := range []string{`"category":"USDT-FUTURES"`, `"symbol":"BTCUSDT"`, `"orderId":"100"`, `"clientOid":"client-1"`, `"qty":"2"`, `"price":"11"`, `"autoCancel":"yes"`} {
		if !strings.Contains(seenBody, want) {
			t.Fatalf("expected modify body to contain %s, got %s", want, seenBody)
		}
	}
	if strings.Contains(seenBody, "newQty") || strings.Contains(seenBody, "newPrice") {
		t.Fatalf("modify request used stale quantity/price field names: %s", seenBody)
	}
}

func TestClient_GetAccountSettingsBuildsUTARequest(t *testing.T) {
	var seenPath string
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenPath = req.URL.Path
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(
					`{"code":"00000","msg":"success","data":{"accountMode":"unified","assetMode":"union","accountLevel":"trader","holdMode":"single_hold","symbolSettings":[{"symbol":"BTCUSDT","category":"USDT-FUTURES","marginMode":"crossed"}]}}`,
				)),
				Header: make(http.Header),
			}, nil
		})})

	got, err := client.GetAccountSettings(context.Background())
	if err != nil {
		t.Fatalf("GetAccountSettings: %v", err)
	}
	if seenPath != "/api/v3/account/settings" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	if got.AccountMode != "unified" || got.HoldMode != "single_hold" {
		t.Fatalf("unexpected account settings: %+v", got)
	}
	if len(got.SymbolSettings) != 1 || got.SymbolSettings[0].Category != ProductTypeUSDTFutures {
		t.Fatalf("unexpected symbol settings: %+v", got.SymbolSettings)
	}
}

func TestClient_GetAccountInfoAcceptsNumericParentID(t *testing.T) {
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(
					`{"code":"00000","msg":"success","data":{"userId":"100","inviterId":"0","parentId":12345,"channelCode":"","channel":"","ips":"","permType":"read_write","permissions":["trade"],"regisTime":"1"}}`,
				)),
				Header: make(http.Header),
			}, nil
		})})

	got, err := client.GetAccountInfo(context.Background())
	if err != nil {
		t.Fatalf("GetAccountInfo: %v", err)
	}
	if got.ParentID != "12345" {
		t.Fatalf("parent id=%q, want numeric value preserved as string", got.ParentID)
	}
}

func TestClient_PlaceOrderBuildsNativeUTAParams(t *testing.T) {
	var seenBody string
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			seenBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"code":"00000","msg":"success","data":{"orderId":"100","clientOid":"client-native"}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	_, err := client.PlaceOrder(context.Background(), &PlaceOrderRequest{
		Category:     bitgetPerpCategory,
		Symbol:       bitgetPerpSymbol,
		Qty:          "1",
		Price:        "10",
		Side:         "sell",
		TradeSide:    "close",
		OrderType:    "limit",
		TimeInForce:  "post_only",
		MarginMode:   "isolated",
		MarginCoin:   "USDT",
		ClientOID:    "client-native",
		ReduceOnly:   "yes",
		PosSide:      "long",
		STPMode:      "cancel_both",
		TPTriggerBy:  "mark",
		SLTriggerBy:  "market",
		TakeProfit:   "12",
		StopLoss:     "8",
		TPOrderType:  "limit",
		SLOrderType:  "market",
		TPLimitPrice: "12.5",
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	for _, want := range []string{`"posSide":"long"`, `"tradeSide":"close"`, `"marginMode":"isolated"`, `"marginCoin":"USDT"`, `"stpMode":"cancel_both"`, `"timeInForce":"post_only"`, `"reduceOnly":"yes"`, `"tpTriggerBy":"mark"`, `"slTriggerBy":"market"`, `"takeProfit":"12"`, `"stopLoss":"8"`, `"tpOrderType":"limit"`, `"slOrderType":"market"`, `"tpLimitPrice":"12.5"`} {
		if !strings.Contains(seenBody, want) {
			t.Fatalf("expected place body to contain %s, got %s", want, seenBody)
		}
	}
}

func TestClient_BatchPlaceOrdersBuildsUTARequest(t *testing.T) {
	var seenPath string
	var seenBody string
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			seenPath = req.URL.Path
			seenBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"code":"00000","msg":"success","data":[{"orderId":"100","clientOid":"client-1"},{"orderId":"101","clientOid":"client-2"}]}`)),
				Header:     make(http.Header),
			}, nil
		})})

	got, err := client.BatchPlaceOrders(context.Background(), []PlaceOrderRequest{{
		Category:    bitgetPerpCategory,
		Symbol:      bitgetPerpSymbol,
		Qty:         "1",
		Price:       "10",
		Side:        "buy",
		OrderType:   "limit",
		TimeInForce: "gtc",
		ClientOID:   "client-1",
	}, {
		Category:    bitgetPerpCategory,
		Symbol:      bitgetPerpSymbol,
		Qty:         "2",
		Price:       "11",
		Side:        "sell",
		OrderType:   "limit",
		TimeInForce: "gtc",
		ClientOID:   "client-2",
	}})
	if err != nil {
		t.Fatalf("BatchPlaceOrders: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected two batch place responses, got %+v", got)
	}
	if seenPath != "/api/v3/trade/place-batch" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	for _, want := range []string{`"category":"USDT-FUTURES"`, `"symbol":"BTCUSDT"`, `"qty":"1"`, `"qty":"2"`, `"clientOid":"client-1"`, `"clientOid":"client-2"`} {
		if !strings.Contains(seenBody, want) {
			t.Fatalf("expected batch place body to contain %s, got %s", want, seenBody)
		}
	}
}

func TestClient_BatchCancelOrdersBuildsUTARequest(t *testing.T) {
	var seenPath string
	var seenBody string
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			seenPath = req.URL.Path
			seenBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"code":"00000","msg":"success","data":[{"orderId":"100","clientOid":"client-1"},{"orderId":"101","clientOid":"client-2"}]}`)),
				Header:     make(http.Header),
			}, nil
		})})

	got, err := client.BatchCancelOrders(context.Background(), []CancelOrderRequest{{
		Category: bitgetPerpCategory,
		Symbol:   bitgetPerpSymbol,
		OrderID:  "100",
	}, {
		Category:  bitgetPerpCategory,
		Symbol:    bitgetPerpSymbol,
		ClientOID: "client-2",
	}})
	if err != nil {
		t.Fatalf("BatchCancelOrders: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected two batch cancel responses, got %+v", got)
	}
	if seenPath != "/api/v3/trade/cancel-batch" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	for _, want := range []string{`"category":"USDT-FUTURES"`, `"symbol":"BTCUSDT"`, `"orderId":"100"`, `"clientOid":"client-2"`} {
		if !strings.Contains(seenBody, want) {
			t.Fatalf("expected batch cancel body to contain %s, got %s", want, seenBody)
		}
	}
}

func TestClient_BatchModifyOrdersBuildsUTARequest(t *testing.T) {
	var seenPath string
	var seenBody string
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			seenPath = req.URL.Path
			seenBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"code":"00000","msg":"success","data":[{"orderId":"100","clientOid":"client-1"},{"orderId":"101","clientOid":"client-2"}]}`)),
				Header:     make(http.Header),
			}, nil
		})})

	got, err := client.BatchModifyOrders(context.Background(), []ModifyOrderRequest{{
		Category:   bitgetPerpCategory,
		Symbol:     bitgetPerpSymbol,
		OrderID:    "100",
		ClientOID:  "client-1",
		NewQty:     "2",
		NewPrice:   "11",
		AutoCancel: "yes",
	}, {
		Category:    bitgetPerpCategory,
		Symbol:      bitgetPerpSymbol,
		OrderID:     "101",
		NewClientID: "client-2-replaced",
		NewQty:      "3",
		NewPrice:    "12",
	}})
	if err != nil {
		t.Fatalf("BatchModifyOrders: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("expected two batch modify responses, got %+v", got)
	}
	if seenPath != "/api/v3/trade/batch-modify-order" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	for _, want := range []string{`"category":"USDT-FUTURES"`, `"symbol":"BTCUSDT"`, `"orderId":"100"`, `"clientOid":"client-1"`, `"orderId":"101"`, `"newClientOid":"client-2-replaced"`, `"qty":"2"`, `"qty":"3"`, `"price":"11"`, `"price":"12"`, `"autoCancel":"yes"`} {
		if !strings.Contains(seenBody, want) {
			t.Fatalf("expected batch modify body to contain %s, got %s", want, seenBody)
		}
	}
	if strings.Contains(seenBody, "newQty") || strings.Contains(seenBody, "newPrice") {
		t.Fatalf("batch modify request used stale quantity/price field names: %s", seenBody)
	}
}

func TestClient_GetFillsBuildsTradeFillsQuery(t *testing.T) {
	var seenPath string
	var seenQuery string
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenPath = req.URL.Path
			seenQuery = req.URL.RawQuery
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"code":"00000","msg":"success","data":{"list":[{"execId":"fill-1","orderId":"100","clientOid":"client-1","category":"USDT-FUTURES","symbol":"BTCUSDT","side":"buy","execPrice":"10","execQty":"0.1","createdTime":"4000","feeDetail":[{"feeCoin":"USDT","fee":"0.01"}]}],"cursor":"next"}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	got, err := client.GetFills(context.Background(), GetFillsRequest{
		Category: bitgetPerpCategory,
		OrderID:  "100",
		Limit:    "100",
	})
	if err != nil {
		t.Fatalf("GetFills: %v", err)
	}
	if len(got) != 1 || got[0].ExecID != "fill-1" || got[0].CreatedTime != "4000" {
		t.Fatalf("unexpected fills: %+v", got)
	}
	if seenPath != "/api/v3/trade/fills" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	for _, want := range []string{"category=USDT-FUTURES", "orderId=100", "limit=100"} {
		if !strings.Contains(seenQuery, want) {
			t.Fatalf("expected query to contain %s, got %s", want, seenQuery)
		}
	}
}

func TestClient_GetFillsBoundedConsumesCursorWithinOverallLimit(t *testing.T) {
	calls := 0
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			body := `{"code":"00000","msg":"success","data":{"list":[{"execId":"fill-1"}],"cursor":"next"}}`
			if req.URL.Query().Get("cursor") == "next" {
				body = `{"code":"00000","msg":"success","data":{"list":[{"execId":"fill-2"}],"cursor":""}}`
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})})

	records, saturated, err := client.GetFillsBounded(context.Background(), GetFillsRequest{Category: bitgetPerpCategory, Limit: "150"})
	if err != nil {
		t.Fatalf("GetFillsBounded: %v", err)
	}
	if saturated || len(records) != 2 || records[0].ExecID != "fill-1" || records[1].ExecID != "fill-2" {
		t.Fatalf("records=%+v saturated=%v, want both complete pages", records, saturated)
	}
	if calls != 2 {
		t.Fatalf("calls=%d, want 2", calls)
	}
}

func TestClient_GetFillsBoundedStopsAtOverallLimit(t *testing.T) {
	calls := 0
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			return &http.Response{
				StatusCode: http.StatusOK,
				Body: io.NopCloser(strings.NewReader(
					`{"code":"00000","msg":"success","data":{"list":[{"execId":"fill-1"},{"execId":"fill-2"}],"cursor":"more"}}`,
				)),
				Header: make(http.Header),
			}, nil
		})})

	records, saturated, err := client.GetFillsBounded(context.Background(), GetFillsRequest{Category: bitgetPerpCategory, Limit: "2"})
	if err != nil {
		t.Fatalf("GetFillsBounded: %v", err)
	}
	if !saturated || len(records) != 2 {
		t.Fatalf("records=%+v saturated=%v, want two records and saturation", records, saturated)
	}
	if calls != 1 {
		t.Fatalf("calls=%d, want 1", calls)
	}
}

func TestClient_GetFillsBoundedTreatsExactFullPageWithoutCursorAsComplete(t *testing.T) {
	rows := strings.TrimSuffix(strings.Repeat(`{"execId":"fill"},`, 100), ",")
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			body := `{"code":"00000","msg":"success","data":{"list":[` + rows + `],"cursor":""}}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})})

	records, saturated, err := client.GetFillsBounded(context.Background(), GetFillsRequest{Category: bitgetPerpCategory, Limit: "100"})
	if err != nil {
		t.Fatalf("GetFillsBounded: %v", err)
	}
	if saturated || len(records) != 100 {
		t.Fatalf("records=%d saturated=%v, want exact complete page", len(records), saturated)
	}
}

func TestClient_GetFillsBoundedRejectsCursorCycleWithoutPartialData(t *testing.T) {
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			cursor := req.URL.Query().Get("cursor")
			next := "a"
			if cursor == "a" {
				next = "b"
			} else if cursor == "b" {
				next = "a"
			}
			body := `{"code":"00000","msg":"success","data":{"list":[{"execId":"fill"}],"cursor":"` + next + `"}}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})})

	records, saturated, err := client.GetFillsBounded(context.Background(), GetFillsRequest{Category: bitgetPerpCategory, Limit: "100"})
	if err == nil || !strings.Contains(err.Error(), "repeated cursor") {
		t.Fatalf("error=%v, want repeated cursor", err)
	}
	if records != nil || saturated {
		t.Fatalf("records=%+v saturated=%v, want fail-closed empty result", records, saturated)
	}
}

func TestClient_GetFillsBoundedRejectsLaterPageErrorWithoutPartialData(t *testing.T) {
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			body := `{"code":"00000","msg":"success","data":{"list":[{"execId":"first"}],"cursor":"next"}}`
			if req.URL.Query().Get("cursor") == "next" {
				body = `{"code":"40000","msg":"later page failed","data":{"list":[],"cursor":""}}`
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})})

	records, saturated, err := client.GetFillsBounded(context.Background(), GetFillsRequest{Category: bitgetPerpCategory, Limit: "100"})
	if err == nil || !strings.Contains(err.Error(), "later page failed") {
		t.Fatalf("error=%v, want later-page venue error", err)
	}
	if records != nil || saturated {
		t.Fatalf("records=%+v saturated=%v, want fail-closed empty result", records, saturated)
	}
}

func TestClient_GetFillsBoundedRejectsEmptyNonTerminalPage(t *testing.T) {
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			body := `{"code":"00000","msg":"success","data":{"list":[],"cursor":"next"}}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})})

	records, saturated, err := client.GetFillsBounded(context.Background(), GetFillsRequest{Category: bitgetPerpCategory, Limit: "100"})
	if err == nil || !strings.Contains(err.Error(), "empty page") {
		t.Fatalf("error=%v, want empty non-terminal page rejection", err)
	}
	if records != nil || saturated {
		t.Fatalf("records=%+v saturated=%v, want fail-closed empty result", records, saturated)
	}
}

func TestClient_GetFillsBoundedRejectsExcessivePageCount(t *testing.T) {
	calls := 0
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			body := fmt.Sprintf(`{"code":"00000","msg":"success","data":{"list":[{"execId":"fill-%d"}],"cursor":"cursor-%d"}}`, calls, calls)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})})

	records, saturated, err := client.GetFillsBounded(context.Background(), GetFillsRequest{
		Category: bitgetPerpCategory,
		Limit:    strconv.Itoa(privatePaginationMaxPages + 1),
	})
	if err == nil || !strings.Contains(err.Error(), "page safety limit") {
		t.Fatalf("error=%v, want page safety limit", err)
	}
	if records != nil || saturated {
		t.Fatalf("records=%+v saturated=%v, want fail-closed empty result", records, saturated)
	}
	if calls != privatePaginationMaxPages {
		t.Fatalf("calls=%d, want exactly %d bounded requests", calls, privatePaginationMaxPages)
	}
}

func TestClient_GetOrderHistoryBoundedConsumesMoreThanTwoHundredRowsWithVenueLimit(t *testing.T) {
	const totalOrders = 217
	calls := 0
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			query := req.URL.Query()
			if req.URL.Path != "/api/v3/trade/history-orders" {
				t.Fatalf("path=%q, want UTA history-orders", req.URL.Path)
			}
			if query.Get("category") != bitgetPerpCategory || query.Get("symbol") != bitgetPerpSymbol {
				t.Fatalf("scope query=%q, want %s/%s", req.URL.RawQuery, bitgetPerpCategory, bitgetPerpSymbol)
			}
			if query.Get("startTime") != "1000" || query.Get("endTime") != "2000" {
				t.Fatalf("time query=%q, want startTime=1000 and endTime=2000", req.URL.RawQuery)
			}
			if _, ok := query["orderId"]; ok {
				t.Fatalf("unsupported orderId filter was sent: %q", req.URL.RawQuery)
			}
			if _, ok := query["clientOid"]; ok {
				t.Fatalf("unsupported clientOid filter was sent: %q", req.URL.RawQuery)
			}
			limit, err := strconv.Atoi(query.Get("limit"))
			if err != nil || limit <= 0 || limit > 100 {
				t.Fatalf("venue limit=%q, want 1..100", query.Get("limit"))
			}
			start := 0
			if query.Get("cursor") != "" {
				start, err = strconv.Atoi(query.Get("cursor"))
				if err != nil {
					t.Fatalf("cursor=%q is not an integer offset", query.Get("cursor"))
				}
			}
			end := start + limit
			if end > totalOrders {
				end = totalOrders
			}
			var rows strings.Builder
			for i := start; i < end; i++ {
				if rows.Len() > 0 {
					rows.WriteByte(',')
				}
				fmt.Fprintf(&rows, `{"orderId":"order-%d"}`, i)
			}
			next := ""
			if end < totalOrders {
				next = strconv.Itoa(end)
			}
			body := fmt.Sprintf(`{"code":"00000","msg":"success","data":{"list":[%s],"cursor":"%s"}}`, rows.String(), next)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})})

	records, saturated, err := client.GetOrderHistoryBounded(context.Background(), GetOrderHistoryRequest{
		Category:  bitgetPerpCategory,
		Symbol:    bitgetPerpSymbol,
		StartTime: "1000",
		EndTime:   "2000",
		Limit:     "1000",
	})
	if err != nil {
		t.Fatalf("GetOrderHistoryBounded: %v", err)
	}
	if saturated || calls != 3 || len(records) != totalOrders || records[0].OrderID != "order-0" || records[totalOrders-1].OrderID != "order-216" {
		t.Fatalf("calls=%d records=%d saturated=%v, want 217 rows across three complete cursor pages", calls, len(records), saturated)
	}
}

func TestClient_GetOrderHistoryBoundedStopsAtOverallLimit(t *testing.T) {
	calls := 0
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			body := `{"code":"00000","msg":"success","data":{"list":[{"orderId":"first"},{"orderId":"second"}],"cursor":"more"}}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})})

	records, saturated, err := client.GetOrderHistoryBounded(context.Background(), GetOrderHistoryRequest{
		Category: bitgetPerpCategory,
		Symbol:   bitgetPerpSymbol,
		Limit:    "2",
	})
	if err != nil {
		t.Fatalf("GetOrderHistoryBounded: %v", err)
	}
	if !saturated || calls != 1 || len(records) != 2 {
		t.Fatalf("calls=%d records=%+v saturated=%v, want one bounded saturated page", calls, records, saturated)
	}
}

func TestClient_GetOpenOrdersConsumesCursorPages(t *testing.T) {
	calls := 0
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			if got := req.URL.Query().Get("limit"); got != "100" {
				t.Fatalf("limit=%q, want 100", got)
			}
			body := `{"code":"00000","msg":"success","data":{"list":[{"orderId":"first"}],"cursor":"next"}}`
			if req.URL.Query().Get("cursor") == "next" {
				body = `{"code":"00000","msg":"success","data":{"list":[{"orderId":"second"}],"cursor":""}}`
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})})

	records, err := client.GetOpenOrders(context.Background(), bitgetSpotCategory, bitgetSpotSymbol)
	if err != nil {
		t.Fatalf("GetOpenOrders: %v", err)
	}
	if calls != 2 || len(records) != 2 || records[0].OrderID != "first" || records[1].OrderID != "second" {
		t.Fatalf("calls=%d records=%+v, want both cursor pages", calls, records)
	}
}

func TestClient_GetOpenOrdersRejectsRepeatedCursorWithoutPartialData(t *testing.T) {
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			body := `{"code":"00000","msg":"success","data":{"list":[{"orderId":"first"}],"cursor":"same"}}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})})

	records, err := client.GetOpenOrders(context.Background(), bitgetSpotCategory, bitgetSpotSymbol)
	if err == nil || !strings.Contains(err.Error(), "repeated cursor") {
		t.Fatalf("error=%v, want repeated cursor", err)
	}
	if records != nil {
		t.Fatalf("records=%+v, want fail-closed empty result", records)
	}
}

func TestClient_GetOpenOrdersRejectsLaterPageErrorWithoutPartialData(t *testing.T) {
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			body := `{"code":"00000","msg":"success","data":{"list":[{"orderId":"first"}],"cursor":"next"}}`
			if req.URL.Query().Get("cursor") == "next" {
				body = `{"code":"40000","msg":"later page failed","data":{"list":[],"cursor":""}}`
			}
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})})

	records, err := client.GetOpenOrders(context.Background(), bitgetSpotCategory, bitgetSpotSymbol)
	if err == nil || !strings.Contains(err.Error(), "later page failed") {
		t.Fatalf("error=%v, want later-page venue error", err)
	}
	if records != nil {
		t.Fatalf("records=%+v, want fail-closed empty result", records)
	}
}

func TestClient_GetOpenOrdersRejectsEmptyNonTerminalPage(t *testing.T) {
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			body := `{"code":"00000","msg":"success","data":{"list":[],"cursor":"next"}}`
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})})

	records, err := client.GetOpenOrders(context.Background(), bitgetSpotCategory, bitgetSpotSymbol)
	if err == nil || !strings.Contains(err.Error(), "empty page") {
		t.Fatalf("error=%v, want empty non-terminal page rejection", err)
	}
	if records != nil {
		t.Fatalf("records=%+v, want fail-closed empty result", records)
	}
}

func TestClient_GetOpenOrdersRejectsExcessivePageCount(t *testing.T) {
	calls := 0
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			calls++
			body := fmt.Sprintf(`{"code":"00000","msg":"success","data":{"list":[{"orderId":"order-%d"}],"cursor":"cursor-%d"}}`, calls, calls)
			return &http.Response{StatusCode: http.StatusOK, Body: io.NopCloser(strings.NewReader(body)), Header: make(http.Header)}, nil
		})})

	records, err := client.GetOpenOrders(context.Background(), bitgetSpotCategory, bitgetSpotSymbol)
	if err == nil || !strings.Contains(err.Error(), "page safety limit") {
		t.Fatalf("error=%v, want page safety limit", err)
	}
	if records != nil {
		t.Fatalf("records=%+v, want fail-closed empty result", records)
	}
	if calls != privatePaginationMaxPages {
		t.Fatalf("calls=%d, want exactly %d bounded requests", calls, privatePaginationMaxPages)
	}
}

func TestClient_GetOrder(t *testing.T) {
	client := newLivePrivateClient(t)
	orderID := os.Getenv("BITGET_TEST_ORDER_ID")
	if orderID == "" {
		t.Skip("BITGET_TEST_ORDER_ID is required for GetOrder live test")
	}

	got, err := client.GetOrder(context.Background(), bitgetSpotCategory, bitgetSpotSymbol, orderID, "")
	if err != nil {
		skipIfBitgetAccountModeMismatch(t, err)
		t.Fatalf("GetOrder: %v", err)
	}
	if got == nil {
		t.Fatal("expected order record")
	}
}

func TestClient_GetOpenOrders(t *testing.T) {
	got, err := newLivePrivateClient(t).GetOpenOrders(context.Background(), bitgetSpotCategory, bitgetSpotSymbol)
	if err != nil {
		skipIfBitgetAccountModeMismatch(t, err)
		t.Fatalf("GetOpenOrders: %v", err)
	}
	if got == nil {
		t.Fatal("expected open orders slice")
	}
}

func TestClient_GetOrderHistory(t *testing.T) {
	got, err := newLivePrivateClient(t).GetOrderHistory(context.Background(), bitgetSpotCategory, bitgetSpotSymbol)
	if err != nil {
		skipIfBitgetAccountModeMismatch(t, err)
		t.Fatalf("GetOrderHistory: %v", err)
	}
	if got == nil {
		t.Fatal("expected order history slice")
	}
}

func TestClient_GetAccountAssets(t *testing.T) {
	got, err := newLivePrivateClient(t).GetAccountAssets(context.Background())
	if err != nil {
		skipIfBitgetPrivateReadUnavailable(t, err, "Bitget UTA account assets endpoint")
		t.Fatalf("GetAccountAssets: %v", err)
	}
	if got == nil {
		t.Fatal("expected account assets")
	}
}

func TestClient_GetAccountInfo(t *testing.T) {
	got, err := newLivePrivateClient(t).GetAccountInfo(context.Background())
	if err != nil {
		skipIfBitgetPrivateReadUnavailable(t, err, "Bitget UTA account info endpoint")
		t.Fatalf("GetAccountInfo: %v", err)
	}
	if got == nil {
		t.Fatal("expected account info")
	}
}

func TestClient_GetFundingAssets(t *testing.T) {
	got, err := newLivePrivateClient(t).GetFundingAssets(context.Background(), os.Getenv("BITGET_TEST_COIN"))
	if err != nil {
		skipIfBitgetPrivateReadUnavailable(t, err, "Bitget UTA funding assets endpoint")
		t.Fatalf("GetFundingAssets: %v", err)
	}
	if got == nil {
		t.Fatal("expected funding assets slice")
	}
}

func TestClient_GetFinancialRecords(t *testing.T) {
	got, err := newLivePrivateClient(t).GetFinancialRecords(context.Background(), FinancialRecordsRequest{
		Category: bitgetPerpCategory,
		Coin:     os.Getenv("BITGET_TEST_COIN"),
		Limit:    "10",
	})
	if err != nil {
		skipIfBitgetPrivateReadUnavailable(t, err, "Bitget UTA financial records endpoint")
		t.Fatalf("GetFinancialRecords: %v", err)
	}
	if got == nil {
		t.Fatal("expected financial records")
	}
}

func TestClient_GetAccountFeeRate(t *testing.T) {
	got, err := newLivePrivateClient(t).GetAccountFeeRate(context.Background(), bitgetSpotCategory, bitgetSpotSymbol)
	if err != nil {
		skipIfBitgetPrivateReadUnavailable(t, err, "Bitget UTA account fee rate endpoint")
		t.Fatalf("GetAccountFeeRate: %v", err)
	}
	if got == nil {
		t.Fatal("expected account fee rate")
	}
}

func TestClient_GetSwitchStatus(t *testing.T) {
	got, err := newLivePrivateClient(t).GetSwitchStatus(context.Background())
	if err != nil {
		skipIfBitgetPrivateReadUnavailable(t, err, "Bitget UTA switch status endpoint")
		t.Fatalf("GetSwitchStatus: %v", err)
	}
	if got == nil {
		t.Fatal("expected switch status")
	}
}

func TestClient_GetMaxTransferable(t *testing.T) {
	got, err := newLivePrivateClient(t).GetMaxTransferable(context.Background(), bitgetEnvOrDefault("BITGET_TEST_COIN", "USDT"))
	if err != nil {
		skipIfBitgetPrivateReadUnavailable(t, err, "Bitget UTA max transferable endpoint")
		t.Fatalf("GetMaxTransferable: %v", err)
	}
	if got == nil {
		t.Fatal("expected max transferable")
	}
}

func TestClient_GetOpenInterestLimit(t *testing.T) {
	got, err := newLivePrivateClient(t).GetOpenInterestLimit(context.Background(), bitgetPerpCategory, bitgetPerpSymbol)
	if err != nil {
		skipIfBitgetPrivateReadUnavailable(t, err, "Bitget UTA open interest limit endpoint")
		t.Fatalf("GetOpenInterestLimit: %v", err)
	}
	if got == nil {
		t.Fatal("expected open interest limit")
	}
}

func TestClient_GetCurrentPositions(t *testing.T) {
	got, err := newLivePrivateClient(t).GetCurrentPositions(context.Background(), bitgetPerpCategory, bitgetPerpSymbol)
	if err != nil {
		skipIfBitgetPrivateReadUnavailable(t, err, "Bitget UTA current positions endpoint")
		t.Fatalf("GetCurrentPositions: %v", err)
	}
	if got == nil {
		t.Fatal("expected current positions slice")
	}
}

func TestClient_SetLeverageBuildsUTARequest(t *testing.T) {
	var seenPath string
	var seenBody string
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			seenPath = req.URL.Path
			seenBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"code":"00000","msg":"success","data":{}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	err := client.SetLeverage(context.Background(), &SetLeverageRequest{
		Category:      bitgetPerpCategory,
		Symbol:        bitgetPerpSymbol,
		Leverage:      "3",
		Coin:          "USDT",
		PosSide:       "long",
		MarginMode:    "isolated",
		LongLeverage:  "4",
		ShortLeverage: "2",
	})
	if err != nil {
		t.Fatalf("SetLeverage: %v", err)
	}
	if seenPath != "/api/v3/account/set-leverage" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	for _, want := range []string{`"category":"USDT-FUTURES"`, `"symbol":"BTCUSDT"`, `"leverage":"3"`, `"coin":"USDT"`, `"posSide":"long"`, `"marginMode":"isolated"`, `"longLeverage":"4"`, `"shortLeverage":"2"`} {
		if !strings.Contains(seenBody, want) {
			t.Fatalf("expected leverage body to contain %s, got %s", want, seenBody)
		}
	}
}

func TestClient_SetHoldModeBuildsUTARequest(t *testing.T) {
	var seenPath string
	var seenBody string
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			body, _ := io.ReadAll(req.Body)
			seenPath = req.URL.Path
			seenBody = string(body)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"code":"00000","msg":"success","data":{}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	if err := client.SetHoldMode(context.Background(), "hedge_mode"); err != nil {
		t.Fatalf("SetHoldMode: %v", err)
	}
	if seenPath != "/api/v3/account/set-hold-mode" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	if !strings.Contains(seenBody, `"holdMode":"hedge_mode"`) {
		t.Fatalf("expected hold mode body to contain holdMode, got %s", seenBody)
	}
}

func TestClient_SetLeverage(t *testing.T) {
	client := requireBitgetLiveWrite(t)
	symbol := bitgetEnvOrDefault("BITGET_TEST_SYMBOL", bitgetPerpSymbol)
	leverage := bitgetEnvOrDefault("BITGET_TEST_LEVERAGE", "2")

	if err := client.SetLeverage(context.Background(), &SetLeverageRequest{Category: bitgetPerpCategory, Symbol: symbol, Leverage: leverage}); err != nil {
		t.Fatalf("SetLeverage: %v", err)
	}
}

func TestClient_SetHoldMode(t *testing.T) {
	client := requireBitgetLiveWrite(t)
	mode := bitgetEnvOrDefault("BITGET_TEST_HOLD_MODE", "one_way_mode")

	if err := client.SetHoldMode(context.Background(), mode); err != nil {
		t.Fatalf("SetHoldMode: %v", err)
	}
}
