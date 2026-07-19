package spot

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
	"sync/atomic"
	"testing"

	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

func TestClient_UserOpenOrders(t *testing.T) {
	account := os.Getenv("HYPERLIQUID_ACCOUNT_ADDR")
	orders, err := newLivePrivateClient(t).UserOpenOrders(context.Background(), account)
	if err != nil {
		t.Fatalf("UserOpenOrders: %v", err)
	}
	if orders == nil {
		t.Fatal("expected orders slice")
	}
}

func TestClientUserOpenOrdersUsesFrontendSchema(t *testing.T) {
	var seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = string(body)
		_, _ = w.Write([]byte(`[{"coin":"PURR/USDC","side":"B","limitPx":"1.01","sz":"1.5","oid":77,"cloid":"0xabc","timestamp":1700000000000,"origSz":"2","reduceOnly":false,"orderType":"Limit","tif":"Alo"}]`))
	}))
	defer srv.Close()
	base := hyperliquid.NewClient()
	base.BaseURL = srv.URL
	client := NewClient(base)

	orders, err := client.UserOpenOrders(context.Background(), "0xuser")
	if err != nil || len(orders) != 1 || orders[0].OrigSz != "2" || orders[0].Tif != "Alo" {
		t.Fatalf("orders=%+v err=%v", orders, err)
	}
	if !strings.Contains(seenBody, `"type":"frontendOpenOrders"`) {
		t.Fatalf("request=%s, want frontendOpenOrders", seenBody)
	}
}

func TestClientUserOpenOrdersDecodesMinimalFrontendFixture(t *testing.T) {
	var seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = string(body)
		_, _ = w.Write([]byte(`[{"coin":"PURR/USDC","side":"B","limitPx":"1.01","sz":"1.5","oid":77,"timestamp":1700000000000,"origSz":"2"}]`))
	}))
	defer srv.Close()
	base := hyperliquid.NewClient()
	base.BaseURL = srv.URL
	client := NewClient(base)

	orders, err := client.UserOpenOrders(context.Background(), "0xuser")
	if err != nil {
		t.Fatalf("UserOpenOrders: %v", err)
	}
	if len(orders) != 1 {
		t.Fatalf("orders=%+v, want one minimal frontend order", orders)
	}
	order := orders[0]
	if order.Coin != "PURR/USDC" || order.Side != "B" || order.LimitPx != "1.01" || order.Sz != "1.5" || order.Oid != 77 || order.Timestamp != 1700000000000 || order.OrigSz != "2" {
		t.Fatalf("minimal fields decoded incorrectly: %+v", order)
	}
	if order.Cliod != "" || order.ReduceOnly || order.OrderType != "" || order.Tif != "" || order.IsTrigger || order.TriggerPx != "" {
		t.Fatalf("missing optional fields acquired fabricated semantics: %+v", order)
	}
	if !strings.Contains(seenBody, `"type":"frontendOpenOrders"`) {
		t.Fatalf("request=%s, want frontendOpenOrders", seenBody)
	}
}

func TestClient_OrderStatus(t *testing.T) {
	var seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = string(body)
		_, _ = w.Write([]byte(`{"status":"order","order":{"order":{"coin":"PURR/USDC","side":"A","limitPx":"10","sz":"0.25","oid":100,"cloid":"0x1234567890abcdef1234567890abcdef","timestamp":1700000000000,"origSz":"1","reduceOnly":false,"orderType":"Limit","tif":"Alo","isTrigger":false,"triggerPx":"0"},"status":"filled","statusTimestamp":1700000001000}}`))
	}))
	defer srv.Close()
	base := hyperliquid.NewClient()
	base.BaseURL = srv.URL
	client := NewClient(base)

	status, err := client.OrderStatus(context.Background(), "0xabc", 100)
	if err != nil {
		t.Fatalf("OrderStatus: %v", err)
	}
	if status == nil || status.Oid != 100 || status.Status != "filled" || status.FilledSz != "0.75" || status.StatusTimestamp != 1700000001000 || status.OrderType != "Limit" || status.Tif != "Alo" || !status.HasReduceOnly {
		t.Fatalf("unexpected status: %+v", status)
	}
	if !strings.Contains(seenBody, `"type":"orderStatus"`) || !strings.Contains(seenBody, `"oid":100`) {
		t.Fatalf("unexpected order status body: %s", seenBody)
	}
}

func TestClient_OrderStatusByCloidUsesStringOIDAndValidatesIdentity(t *testing.T) {
	const cloid = "0x1234567890abcdef1234567890abcdef"
	var seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = string(body)
		_, _ = w.Write([]byte(`{"status":"order","order":{"order":{"coin":"PURR/USDC","side":"B","limitPx":"10","sz":"1","oid":101,"cloid":"` + cloid + `","timestamp":1700000000000,"origSz":"1"},"status":"open","statusTimestamp":1700000001000}}`))
	}))
	defer srv.Close()
	base := hyperliquid.NewClient()
	base.BaseURL = srv.URL
	client := NewClient(base)

	status, err := client.OrderStatusByCloid(context.Background(), "0xabc", strings.ToUpper(cloid))
	if err != nil {
		t.Fatalf("OrderStatusByCloid: %v", err)
	}
	if status == nil || status.Cliod != cloid || status.Oid != 101 {
		t.Fatalf("unexpected status: %+v", status)
	}
	if !strings.Contains(seenBody, `"oid":"`+strings.ToUpper(cloid)+`"`) {
		t.Fatalf("unexpected order status body: %s", seenBody)
	}
}

func TestClient_OrderStatusUnknownOIDIsTypedNotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"unknownOid"}`))
	}))
	defer srv.Close()
	base := hyperliquid.NewClient()
	base.BaseURL = srv.URL
	client := NewClient(base)

	status, err := client.OrderStatus(context.Background(), "0xabc", 100)
	if status != nil || !errors.Is(err, hyperliquid.ErrOrderNotFound) {
		t.Fatalf("status=%+v err=%v, want typed order not found", status, err)
	}
}

func TestClient_OrderStatusRejectsMismatchedOID(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"order","order":{"order":{"coin":"PURR/USDC","side":"B","limitPx":"10","sz":"1","oid":999,"timestamp":1700000000000,"origSz":"1"},"status":"open","statusTimestamp":1700000001000}}`))
	}))
	defer srv.Close()
	base := hyperliquid.NewClient()
	base.BaseURL = srv.URL
	client := NewClient(base)

	status, err := client.OrderStatus(context.Background(), "0xabc", 100)
	if status != nil || err == nil || !strings.Contains(err.Error(), "identity mismatch") {
		t.Fatalf("status=%+v err=%v, want identity mismatch", status, err)
	}
}

func TestClient_PlaceOrder(t *testing.T) {
	client := requireHyperliquidLiveWrite(t, "HYPERLIQUID_SPOT_TEST_ASSET_ID", "HYPERLIQUID_TEST_ORDER_PRICE", "HYPERLIQUID_TEST_ORDER_SIZE")
	assetID := hyperliquidSpotAssetID(t)
	price := hyperliquidFloatEnv(t, "HYPERLIQUID_TEST_ORDER_PRICE")
	size := hyperliquidFloatEnv(t, "HYPERLIQUID_TEST_ORDER_SIZE")

	status, err := client.PlaceOrder(context.Background(), PlaceOrderRequest{
		AssetID: assetID,
		IsBuy:   hyperliquidEnvOrDefault("HYPERLIQUID_TEST_ORDER_SIDE", "buy") == "buy",
		Price:   price,
		Size:    size,
		OrderType: OrderType{Limit: &OrderTypeLimit{
			Tif: hyperliquid.TifGtc,
		}},
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if status == nil {
		t.Fatal("expected order status")
	}
}

func TestClientPlaceMarketOrderUsesFreshMidAndProtectedIOC(t *testing.T) {
	var calls []string
	base := hyperliquid.NewClient().WithCredentials(hyperliquidPrivateKeyForLocalSigning(), nil)
	base.BaseURL = "https://hyperliquid.test"
	base.Http = &http.Client{Transport: spotMarketRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		body, _ := io.ReadAll(req.Body)
		calls = append(calls, string(body))
		switch len(calls) {
		case 1:
			return spotMarketResponse(req, `{"tokens":[{"name":"PURR","szDecimals":2,"index":1},{"name":"USDC","szDecimals":6,"index":0}],"universe":[{"name":"PURR/USDC","index":7,"tokens":[1,0]}]}`), nil
		case 2:
			return spotMarketResponse(req, `{"PURR/USDC":"123.456789"}`), nil
		case 3:
			if !strings.Contains(string(body), `"a":10007`) ||
				!strings.Contains(string(body), `"p":"129.63"`) ||
				!strings.Contains(string(body), `"t":{"limit":{"tif":"Ioc"}}`) {
				t.Fatalf("unexpected protected market action: %s", body)
			}
			return spotMarketResponse(req, `{"status":"ok","response":{"type":"order","data":{"statuses":[{"filled":{"totalSz":"1","avgPx":"125","oid":7}}]}}}`), nil
		default:
			t.Fatalf("unexpected request %d: %s", len(calls), body)
		}
		return nil, nil
	})}

	status, err := NewClient(base).PlaceMarketOrder(context.Background(), MarketOrderRequest{
		Coin:  "PURR/USDC",
		IsBuy: true,
		Size:  1,
	})
	if err != nil {
		t.Fatalf("PlaceMarketOrder: %v", err)
	}
	if status == nil || status.Filled == nil || status.Filled.Oid != 7 || len(calls) != 3 {
		t.Fatalf("status=%+v calls=%d", status, len(calls))
	}
}

func TestClientPlaceMarketOrderReferenceFailureNeverSendsAction(t *testing.T) {
	var exchangeCalls atomic.Int32
	base := hyperliquid.NewClient().WithCredentials(hyperliquidPrivateKeyForLocalSigning(), nil)
	base.BaseURL = "https://hyperliquid.test"
	base.Http = &http.Client{Transport: spotMarketRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/exchange" {
			exchangeCalls.Add(1)
		}
		return spotMarketResponse(req, `{"tokens":[],"universe":[]}`), nil
	})}

	status, err := NewClient(base).PlaceMarketOrder(context.Background(), MarketOrderRequest{
		Coin: "PURR/USDC", IsBuy: true, Size: 1,
	})
	if status != nil || !errors.Is(err, hyperliquid.ErrMarketReferenceMalformed) {
		t.Fatalf("status=%+v err=%v, want malformed reference", status, err)
	}
	if exchangeCalls.Load() != 0 {
		t.Fatalf("exchange sends=%d, want zero", exchangeCalls.Load())
	}
}

func TestClientPlaceMarketOrderReferenceTransportFailureNeverSendsAction(t *testing.T) {
	var exchangeCalls atomic.Int32
	base := hyperliquid.NewClient().WithCredentials(hyperliquidPrivateKeyForLocalSigning(), nil)
	base.BaseURL = "https://hyperliquid.test"
	base.Http = &http.Client{Transport: spotMarketRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Path == "/exchange" {
			exchangeCalls.Add(1)
		}
		return nil, io.ErrClosedPipe
	})}

	status, err := NewClient(base).PlaceMarketOrder(context.Background(), MarketOrderRequest{
		Coin: "PURR/USDC", IsBuy: true, Size: 1,
	})
	if status != nil || !errors.Is(err, hyperliquid.ErrMarketReferenceUnavailable) {
		t.Fatalf("status=%+v err=%v, want unavailable reference", status, err)
	}
	if exchangeCalls.Load() != 0 {
		t.Fatalf("exchange sends=%d, want zero", exchangeCalls.Load())
	}
}

func TestClientPlaceMarketOrderPostSendUnknownIsAmbiguous(t *testing.T) {
	var calls atomic.Int32
	base := hyperliquid.NewClient().WithCredentials(hyperliquidPrivateKeyForLocalSigning(), nil)
	base.BaseURL = "https://hyperliquid.test"
	base.Http = &http.Client{Transport: spotMarketRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch calls.Add(1) {
		case 1:
			return spotMarketResponse(req, `{"tokens":[{"name":"PURR","szDecimals":2,"index":1},{"name":"USDC","szDecimals":6,"index":0}],"universe":[{"name":"PURR/USDC","index":7,"tokens":[1,0]}]}`), nil
		case 2:
			return spotMarketResponse(req, `{"PURR/USDC":"10"}`), nil
		default:
			return nil, io.ErrUnexpectedEOF
		}
	})}

	status, err := NewClient(base).PlaceMarketOrder(context.Background(), MarketOrderRequest{
		Coin: "PURR/USDC", IsBuy: false, Size: 1,
	})
	if status != nil || !errors.Is(err, hyperliquid.ErrMutationOutcomeUnknown) {
		t.Fatalf("status=%+v err=%v, want ambiguous mutation", status, err)
	}
}

func TestClientPlaceMarketOrderDefiniteVenueRejectIsNotAmbiguous(t *testing.T) {
	var calls atomic.Int32
	base := hyperliquid.NewClient().WithCredentials(hyperliquidPrivateKeyForLocalSigning(), nil)
	base.BaseURL = "https://hyperliquid.test"
	base.Http = &http.Client{Transport: spotMarketRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		switch calls.Add(1) {
		case 1:
			return spotMarketResponse(req, `{"tokens":[{"name":"PURR","szDecimals":2,"index":1},{"name":"USDC","szDecimals":6,"index":0}],"universe":[{"name":"PURR/USDC","index":7,"tokens":[1,0]}]}`), nil
		case 2:
			return spotMarketResponse(req, `{"PURR/USDC":"10"}`), nil
		default:
			return spotMarketResponse(req, `{"status":"ok","response":{"type":"order","data":{"statuses":[{"error":"Insufficient spot balance"}]}}}`), nil
		}
	})}

	status, err := NewClient(base).PlaceMarketOrder(context.Background(), MarketOrderRequest{
		Coin: "PURR/USDC", IsBuy: true, Size: 1,
	})
	if status != nil || !errors.Is(err, hyperliquid.ErrOrderRejected) ||
		errors.Is(err, hyperliquid.ErrMutationOutcomeUnknown) {
		t.Fatalf("status=%+v err=%v, want definite rejection", status, err)
	}
}

type spotMarketRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn spotMarketRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func spotMarketResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    req,
	}
}

func TestClient_PlaceOrdersBuildsBatchAction(t *testing.T) {
	var seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = string(body)
		_, _ = w.Write([]byte(`{"status":"ok","response":{"type":"default","data":{"statuses":[{"resting":{"oid":100,"cloid":"client-1"}},{"resting":{"oid":101,"cloid":"client-2"}}]}}}`))
	}))
	defer srv.Close()
	base := hyperliquid.NewClient().WithCredentials(hyperliquidPrivateKeyForLocalSigning(), nil)
	base.BaseURL = srv.URL
	client := NewClient(base)

	statuses, err := client.PlaceOrders(context.Background(), []PlaceOrderRequest{{
		AssetID:       1,
		IsBuy:         true,
		Price:         10,
		Size:          1,
		ClientOrderID: ptrString("client-1"),
		OrderType:     OrderType{Limit: &OrderTypeLimit{Tif: hyperliquid.TifGtc}},
	}, {
		AssetID:       1,
		IsBuy:         false,
		Price:         11,
		Size:          2,
		ClientOrderID: ptrString("client-2"),
		OrderType:     OrderType{Limit: &OrderTypeLimit{Tif: hyperliquid.TifIoc}},
	}})
	if err != nil {
		t.Fatalf("PlaceOrders: %v", err)
	}
	if len(statuses) != 2 {
		t.Fatalf("unexpected statuses: %+v", statuses)
	}
	if !strings.Contains(seenBody, `"type":"order"`) || !strings.Contains(seenBody, `"c":"client-2"`) {
		t.Fatalf("unexpected place orders body: %s", seenBody)
	}
}

func TestClient_PlaceOrdersReturnsVenueErrorResponseString(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"err","response":"Order must have minimum value of $10."}`))
	}))
	defer srv.Close()
	base := hyperliquid.NewClient().WithCredentials(hyperliquidPrivateKeyForLocalSigning(), nil)
	base.BaseURL = srv.URL
	client := NewClient(base)

	_, err := client.PlaceOrders(context.Background(), []PlaceOrderRequest{{
		AssetID:   1,
		IsBuy:     true,
		Price:     1,
		Size:      1,
		OrderType: OrderType{Limit: &OrderTypeLimit{Tif: hyperliquid.TifGtc}},
	}})
	if err == nil || !strings.Contains(err.Error(), "minimum value") {
		t.Fatalf("err=%v, want venue response string", err)
	}
}

func TestClient_PlaceOrdersReturnsTypedPerOrderRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","response":{"type":"order","data":{"statuses":[{"error":"Insufficient spot balance"}]}}}`))
	}))
	defer srv.Close()
	base := hyperliquid.NewClient().WithCredentials(hyperliquidPrivateKeyForLocalSigning(), nil)
	base.BaseURL = srv.URL
	client := NewClient(base)

	_, err := client.PlaceOrders(context.Background(), []PlaceOrderRequest{{
		AssetID: 1, IsBuy: true, Price: 1, Size: 1,
		OrderType: OrderType{Limit: &OrderTypeLimit{Tif: hyperliquid.TifGtc}},
	}})
	if !errors.Is(err, hyperliquid.ErrOrderRejected) || !strings.Contains(err.Error(), "Insufficient spot balance") {
		t.Fatalf("err=%v, want typed venue rejection", err)
	}
}

func TestClientOrderWritesRejectUnsupportedLimitTIFBeforeTransport(t *testing.T) {
	for _, tif := range []hyperliquid.Tif{hyperliquid.TifFok, hyperliquid.Tif("Bogus")} {
		for _, method := range []string{"place", "modify"} {
			t.Run(method+"_"+string(tif), func(t *testing.T) {
				var requests atomic.Int32
				srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
					requests.Add(1)
					_, _ = w.Write([]byte(`{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":100}}]}}}`))
				}))
				t.Cleanup(srv.Close)

				base := hyperliquid.NewClient().WithCredentials(hyperliquidPrivateKeyForLocalSigning(), nil)
				base.BaseURL = srv.URL
				client := NewClient(base)
				order := PlaceOrderRequest{
					AssetID: 1, IsBuy: true, Price: 10, Size: 1,
					OrderType: OrderType{Limit: &OrderTypeLimit{Tif: tif}},
				}

				var err error
				switch method {
				case "place":
					_, err = client.PlaceOrder(context.Background(), order)
				case "modify":
					oid := int64(100)
					_, err = client.ModifyOrder(context.Background(), ModifyOrderRequest{Oid: &oid, Order: order})
				}
				if err == nil || !strings.Contains(err.Error(), "unsupported limit TIF") {
					t.Fatalf("%s TIF=%q err=%v, want local unsupported limit TIF error", method, tif, err)
				}
				if got := requests.Load(); got != 0 {
					t.Fatalf("%s TIF=%q transport requests=%d, want 0", method, tif, got)
				}
			})
		}
	}
}

func TestClientSingleOrderMethodsRejectEmptyStatusesWithoutPanic(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","response":{"type":"default","data":{"statuses":[]}}}`))
	}))
	defer srv.Close()
	base := hyperliquid.NewClient().WithCredentials(hyperliquidPrivateKeyForLocalSigning(), nil)
	base.BaseURL = srv.URL
	client := NewClient(base)
	order := PlaceOrderRequest{AssetID: 1, IsBuy: true, Price: 10, Size: 1, OrderType: OrderType{Limit: &OrderTypeLimit{Tif: hyperliquid.TifGtc}}}

	if status, err := client.PlaceOrder(context.Background(), order); status != nil || err == nil || !strings.Contains(err.Error(), "no order status") {
		t.Fatalf("PlaceOrder status=%+v err=%v, want descriptive empty-status error", status, err)
	} else if errors.Is(err, hyperliquid.ErrOrderRejected) {
		t.Fatalf("PlaceOrder err=%v, malformed empty status must remain ambiguous", err)
	}
	oid := int64(1)
	if status, err := client.ModifyOrder(context.Background(), ModifyOrderRequest{Oid: &oid, Order: order}); status != nil || err == nil || !strings.Contains(err.Error(), "no order status") {
		t.Fatalf("ModifyOrder status=%+v err=%v, want descriptive empty-status error", status, err)
	} else if errors.Is(err, hyperliquid.ErrOrderRejected) {
		t.Fatalf("ModifyOrder err=%v, malformed empty status must remain ambiguous", err)
	}
	if status, err := client.CancelOrder(context.Background(), CancelOrderRequest{AssetID: 1, OrderID: 1}); status != nil || err == nil || !strings.Contains(err.Error(), "no order status") {
		t.Fatalf("CancelOrder status=%+v err=%v, want descriptive empty-status error", status, err)
	} else if errors.Is(err, hyperliquid.ErrOrderRejected) {
		t.Fatalf("CancelOrder err=%v, malformed empty status must remain ambiguous", err)
	}
}

func TestClientSingleOrderMethodsKeepMalformedStatusesAmbiguous(t *testing.T) {
	cloid := "client-1"
	oid := int64(1)
	order := PlaceOrderRequest{
		AssetID: 1, IsBuy: true, Price: 10, Size: 1, ClientOrderID: &cloid,
		OrderType: OrderType{Limit: &OrderTypeLimit{Tif: hyperliquid.TifGtc}},
	}
	tests := []struct {
		name     string
		response string
		call     func(*Client) error
	}{
		{"place extra cardinality", `{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":1,"cloid":"client-1"}},{"resting":{"oid":2,"cloid":"client-1"}}]}}}`, func(client *Client) error { _, err := client.PlaceOrder(context.Background(), order); return err }},
		{"place empty shape", `{"status":"ok","response":{"type":"order","data":{"statuses":[{}]}}}`, func(client *Client) error { _, err := client.PlaceOrder(context.Background(), order); return err }},
		{"place invalid resting oid", `{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":0,"cloid":"client-1"}}]}}}`, func(client *Client) error { _, err := client.PlaceOrder(context.Background(), order); return err }},
		{"place malformed filled values", `{"status":"ok","response":{"type":"order","data":{"statuses":[{"filled":{"oid":1,"totalSz":"bad","avgPx":"10"}}]}}}`, func(client *Client) error { _, err := client.PlaceOrder(context.Background(), order); return err }},
		{"place cloid mismatch", `{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":1,"cloid":"other"}}]}}}`, func(client *Client) error { _, err := client.PlaceOrder(context.Background(), order); return err }},
		{"modify extra cardinality", `{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":1,"cloid":"client-1"}},{"resting":{"oid":2,"cloid":"client-1"}}]}}}`, func(client *Client) error {
			_, err := client.ModifyOrder(context.Background(), ModifyOrderRequest{Oid: &oid, Order: order})
			return err
		}},
		{"modify empty shape", `{"status":"ok","response":{"type":"order","data":{"statuses":[{}]}}}`, func(client *Client) error {
			_, err := client.ModifyOrder(context.Background(), ModifyOrderRequest{Oid: &oid, Order: order})
			return err
		}},
		{"modify invalid filled oid", `{"status":"ok","response":{"type":"order","data":{"statuses":[{"filled":{"oid":0,"totalSz":"1","avgPx":"10"}}]}}}`, func(client *Client) error {
			_, err := client.ModifyOrder(context.Background(), ModifyOrderRequest{Oid: &oid, Order: order})
			return err
		}},
		{"modify malformed filled values", `{"status":"ok","response":{"type":"order","data":{"statuses":[{"filled":{"oid":1,"totalSz":"1","avgPx":"bad"}}]}}}`, func(client *Client) error {
			_, err := client.ModifyOrder(context.Background(), ModifyOrderRequest{Oid: &oid, Order: order})
			return err
		}},
		{"modify cloid mismatch", `{"status":"ok","response":{"type":"order","data":{"statuses":[{"resting":{"oid":1,"cloid":"other"}}]}}}`, func(client *Client) error {
			_, err := client.ModifyOrder(context.Background(), ModifyOrderRequest{Oid: &oid, Order: order})
			return err
		}},
		{"cancel extra cardinality", `{"status":"ok","response":{"type":"default","data":{"statuses":["success","success"]}}}`, func(client *Client) error {
			_, err := client.CancelOrder(context.Background(), CancelOrderRequest{AssetID: 1, OrderID: 1})
			return err
		}},
		{"cancel arbitrary string", `{"status":"ok","response":{"type":"default","data":{"statuses":["unexpected"]}}}`, func(client *Client) error {
			_, err := client.CancelOrder(context.Background(), CancelOrderRequest{AssetID: 1, OrderID: 1})
			return err
		}},
		{"cancel malformed object", `{"status":"ok","response":{"type":"default","data":{"statuses":[{}]}}}`, func(client *Client) error {
			_, err := client.CancelOrder(context.Background(), CancelOrderRequest{AssetID: 1, OrderID: 1})
			return err
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(test.response))
			}))
			t.Cleanup(srv.Close)
			base := hyperliquid.NewClient().WithCredentials(hyperliquidPrivateKeyForLocalSigning(), nil)
			base.BaseURL = srv.URL
			err := test.call(NewClient(base))
			if err == nil {
				t.Fatal("malformed status unexpectedly succeeded")
			}
			if errors.Is(err, hyperliquid.ErrOrderRejected) {
				t.Fatalf("err=%v, malformed status must remain ambiguous", err)
			}
		})
	}
}

func TestClientCancelAndModifyExposeTypedOrderRejection(t *testing.T) {
	tests := []struct {
		name     string
		response string
		call     func(*Client) error
	}{
		{
			name:     "cancel",
			response: `{"status":"ok","response":{"type":"default","data":{"statuses":[{"error":"Order was never placed"}]}}}`,
			call: func(client *Client) error {
				_, err := client.CancelOrder(context.Background(), CancelOrderRequest{AssetID: 1, OrderID: 1})
				return err
			},
		},
		{
			name:     "modify",
			response: `{"status":"ok","response":{"type":"order","data":{"statuses":[{"error":"Insufficient spot balance"}]}}}`,
			call: func(client *Client) error {
				oid := int64(1)
				_, err := client.ModifyOrder(context.Background(), ModifyOrderRequest{Oid: &oid, Order: PlaceOrderRequest{
					AssetID: 1, IsBuy: true, Price: 10, Size: 1,
					OrderType: OrderType{Limit: &OrderTypeLimit{Tif: hyperliquid.TifGtc}},
				}})
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = w.Write([]byte(test.response))
			}))
			defer srv.Close()
			base := hyperliquid.NewClient().WithCredentials(hyperliquidPrivateKeyForLocalSigning(), nil)
			base.BaseURL = srv.URL
			err := test.call(NewClient(base))
			if !errors.Is(err, hyperliquid.ErrOrderRejected) {
				t.Fatalf("err=%v, want ErrOrderRejected", err)
			}
		})
	}
}

func TestClient_ModifyOrder(t *testing.T) {
	client := requireHyperliquidLiveWrite(t, "HYPERLIQUID_SPOT_TEST_ASSET_ID", "HYPERLIQUID_TEST_ORDER_ID", "HYPERLIQUID_TEST_ORDER_PRICE", "HYPERLIQUID_TEST_ORDER_SIZE")
	oid := hyperliquidInt64Env(t, "HYPERLIQUID_TEST_ORDER_ID")

	status, err := client.ModifyOrder(context.Background(), ModifyOrderRequest{
		Oid: &oid,
		Order: PlaceOrderRequest{
			AssetID: hyperliquidSpotAssetID(t),
			IsBuy:   hyperliquidEnvOrDefault("HYPERLIQUID_TEST_ORDER_SIDE", "buy") == "buy",
			Price:   hyperliquidFloatEnv(t, "HYPERLIQUID_TEST_ORDER_PRICE"),
			Size:    hyperliquidFloatEnv(t, "HYPERLIQUID_TEST_ORDER_SIZE"),
			OrderType: OrderType{Limit: &OrderTypeLimit{
				Tif: hyperliquid.TifGtc,
			}},
		},
	})
	if err != nil {
		t.Fatalf("ModifyOrder: %v", err)
	}
	if status == nil {
		t.Fatal("expected modify status")
	}
}

func TestClient_CancelOrdersBuildsBatchAction(t *testing.T) {
	var seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = string(body)
		_, _ = w.Write([]byte(`{"status":"ok","response":{"type":"default","data":{"statuses":["success","success"]}}}`))
	}))
	defer srv.Close()
	base := hyperliquid.NewClient().WithCredentials(hyperliquidPrivateKeyForLocalSigning(), nil)
	base.BaseURL = srv.URL
	client := NewClient(base)

	statuses, err := client.CancelOrders(context.Background(), []CancelOrderRequest{{AssetID: 1, OrderID: 100}, {AssetID: 1, OrderID: 101}})
	if err != nil {
		t.Fatalf("CancelOrders: %v", err)
	}
	if len(statuses) != 2 || statuses[0] != "success" {
		t.Fatalf("unexpected statuses: %+v", statuses)
	}
	if !strings.Contains(seenBody, `"type":"cancel"`) || !strings.Contains(seenBody, `"o":101`) {
		t.Fatalf("unexpected cancel orders body: %s", seenBody)
	}
}

func TestClient_CancelOrder(t *testing.T) {
	client := requireHyperliquidLiveWrite(t, "HYPERLIQUID_SPOT_TEST_ASSET_ID", "HYPERLIQUID_TEST_ORDER_ID")

	status, err := client.CancelOrder(context.Background(), CancelOrderRequest{
		AssetID: hyperliquidSpotAssetID(t),
		OrderID: hyperliquidInt64Env(t, "HYPERLIQUID_TEST_ORDER_ID"),
	})
	if err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if status == nil {
		t.Fatal("expected cancel status")
	}
}

func ptrString(value string) *string {
	return &value
}

func hyperliquidSpotAssetID(t *testing.T) int {
	t.Helper()
	raw := os.Getenv("HYPERLIQUID_SPOT_TEST_ASSET_ID")
	value, err := strconv.Atoi(raw)
	if err != nil {
		t.Fatalf("parse HYPERLIQUID_SPOT_TEST_ASSET_ID: %v", err)
	}
	return value
}

func hyperliquidFloatEnv(t *testing.T, key string) float64 {
	t.Helper()
	value, err := strconv.ParseFloat(os.Getenv(key), 64)
	if err != nil {
		t.Fatalf("parse %s: %v", key, err)
	}
	return value
}

func hyperliquidInt64Env(t *testing.T, key string) int64 {
	t.Helper()
	value, err := strconv.ParseInt(os.Getenv(key), 10, 64)
	if err != nil {
		t.Fatalf("parse %s: %v", key, err)
	}
	return value
}
