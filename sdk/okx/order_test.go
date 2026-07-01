package okx

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

func TestClient_PlaceOrder(t *testing.T) {
	client := requireOKXLiveWrite(t)
	testenv.RequireEnv(t, "OKX_TEST_ORDER_SIZE", "OKX_TEST_ORDER_PRICE")
	price := os.Getenv("OKX_TEST_ORDER_PRICE")
	clOrdID := okxEnvOrDefault("OKX_TEST_CLIENT_ORDER_ID", "sdk-live-write-test")
	got, err := client.PlaceOrder(context.Background(), &OrderRequest{
		InstId:  okxEnvOrDefault("OKX_TEST_ORDER_INST_ID", okxSpotInstID),
		TdMode:  okxEnvOrDefault("OKX_TEST_TD_MODE", "cash"),
		ClOrdId: &clOrdID,
		Side:    okxEnvOrDefault("OKX_TEST_ORDER_SIDE", "buy"),
		OrdType: "limit",
		Sz:      os.Getenv("OKX_TEST_ORDER_SIZE"),
		Px:      &price,
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("unexpected place response: %+v", got)
	}
}

func TestClient_PlaceAlgoOrderBuildsPrivateRequest(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v5/trade/order-algo" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req AlgoOrderRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.InstId != okxSwapInstID || req.OrdType != "trigger" || req.TriggerPx == nil || *req.TriggerPx != "9" || req.OrderPx == nil || *req.OrderPx != "-1" {
			t.Fatalf("unexpected algo request: %+v", req)
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"algoId":"algo-1","algoClOrdId":"client-algo","sCode":"0"}]}`))
	}))
	defer srv.Close()

	client := NewClient().WithCredentials("key", "secret", "pass")
	client.BaseURL = srv.URL
	clientID := "client-algo"
	triggerPx := "9"
	orderPx := "-1"
	got, err := client.PlaceAlgoOrder(context.Background(), &AlgoOrderRequest{
		InstId:      okxSwapInstID,
		TdMode:      "cross",
		Side:        "sell",
		OrdType:     "trigger",
		Sz:          "1",
		AlgoClOrdId: &clientID,
		TriggerPx:   &triggerPx,
		OrderPx:     &orderPx,
	})
	if err != nil {
		t.Fatalf("PlaceAlgoOrder: %v", err)
	}
	if len(got) != 1 || got[0].AlgoId != "algo-1" || got[0].AlgoClOrdId != "client-algo" {
		t.Fatalf("unexpected algo place response: %+v", got)
	}
}

func TestClient_ModifyOrder(t *testing.T) {
	client := requireOKXLiveWrite(t)
	testenv.RequireEnv(t, "OKX_TEST_ORDER_ID", "OKX_TEST_ORDER_SIZE", "OKX_TEST_ORDER_PRICE")
	orderID := os.Getenv("OKX_TEST_ORDER_ID")
	size := os.Getenv("OKX_TEST_ORDER_SIZE")
	price := os.Getenv("OKX_TEST_ORDER_PRICE")
	got, err := client.ModifyOrder(context.Background(), &ModifyOrderRequest{
		InstId: okxEnvOrDefault("OKX_TEST_ORDER_INST_ID", okxSpotInstID),
		OrdId:  &orderID,
		NewSz:  &size,
		NewPx:  &price,
	})
	if err != nil {
		t.Fatalf("ModifyOrder: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("unexpected modify response: %+v", got)
	}
}

func TestClient_AmendAlgoOrderBuildsPrivateRequest(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v5/trade/amend-algos" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req AmendAlgoOrderRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.AlgoId != "algo-1" || req.InstId != okxSwapInstID || req.NewPx == nil || *req.NewPx != "12" {
			t.Fatalf("unexpected amend algo request: %+v", req)
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"algoId":"algo-1","algoClOrdId":"client-algo","sCode":"0"}]}`))
	}))
	defer srv.Close()

	client := NewClient().WithCredentials("key", "secret", "pass")
	client.BaseURL = srv.URL
	newPx := "12"
	got, err := client.AmendAlgoOrder(context.Background(), &AmendAlgoOrderRequest{
		AlgoId: "algo-1",
		InstId: okxSwapInstID,
		NewPx:  &newPx,
	})
	if err != nil {
		t.Fatalf("AmendAlgoOrder: %v", err)
	}
	if len(got) != 1 || got[0].AlgoId != "algo-1" {
		t.Fatalf("unexpected amend response: %+v", got)
	}
}

func TestClient_CancelOrder(t *testing.T) {
	client := requireOKXLiveWrite(t)
	testenv.RequireEnv(t, "OKX_TEST_ORDER_ID")
	got, err := client.CancelOrder(context.Background(), okxEnvOrDefault("OKX_TEST_ORDER_INST_ID", okxSpotInstID), os.Getenv("OKX_TEST_ORDER_ID"), "")
	if err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("unexpected cancel response: %+v", got)
	}
}

func TestClient_CancelAlgoOrdersBuildsPrivateRequest(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v5/trade/cancel-algos" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		var req []AlgoCancelRequest
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if len(req) != 1 || req[0].AlgoId != "algo-1" || req[0].InstId != okxSwapInstID {
			t.Fatalf("unexpected cancel algo request: %+v", req)
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"algoId":"algo-1","algoClOrdId":"client-algo","sCode":"0"}]}`))
	}))
	defer srv.Close()

	client := NewClient().WithCredentials("key", "secret", "pass")
	client.BaseURL = srv.URL
	got, err := client.CancelAlgoOrders(context.Background(), []AlgoCancelRequest{{AlgoId: "algo-1", InstId: okxSwapInstID}})
	if err != nil {
		t.Fatalf("CancelAlgoOrders: %v", err)
	}
	if len(got) != 1 || got[0].AlgoId != "algo-1" {
		t.Fatalf("unexpected cancel response: %+v", got)
	}
}

func TestClient_SpreadRESTMethodsBuildRequests(t *testing.T) {
	t.Parallel()

	const spreadID = "BTC-USDT_BTC-USDT-SWAP"
	seen := map[string]bool{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		key := r.Method + " " + r.URL.Path
		seen[key] = true
		switch key {
		case http.MethodPost + " /api/v5/sprd/order":
			var req SpreadOrderRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode spread order: %v", err)
			}
			if req.SprdId != spreadID || req.OrdType != "post_only" || req.Px == nil || *req.Px != "10" {
				t.Fatalf("unexpected spread order request: %+v", req)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"sprd-ord-1","clOrdId":"spread-client","sCode":"0"}]}`))
		case http.MethodPost + " /api/v5/sprd/cancel-order":
			var req SpreadCancelRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode spread cancel: %v", err)
			}
			if req.SprdId != spreadID || req.OrdId != "sprd-ord-1" {
				t.Fatalf("unexpected spread cancel request: %+v", req)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"sprd-ord-1","clOrdId":"spread-client","sCode":"0"}]}`))
		case http.MethodPost + " /api/v5/sprd/mass-cancel":
			var req SpreadMassCancelRequest
			if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
				t.Fatalf("decode spread mass cancel: %v", err)
			}
			if req.SprdId != spreadID {
				t.Fatalf("unexpected spread mass cancel request: %+v", req)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ordId":"sprd-ord-1","clOrdId":"spread-client","sCode":"0"}]}`))
		case http.MethodGet + " /api/v5/sprd/order":
			if r.URL.Query().Get("sprdId") != spreadID || r.URL.Query().Get("ordId") != "sprd-ord-1" {
				t.Fatalf("unexpected spread order query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"sprdId":"BTC-USDT_BTC-USDT-SWAP","ordId":"sprd-ord-1","clOrdId":"spread-client","state":"live","side":"buy","ordType":"limit","sz":"1","accFillSz":"0","px":"10","uTime":"1000"}]}`))
		case http.MethodGet + " /api/v5/sprd/orders-pending":
			if r.URL.Query().Get("sprdId") != spreadID {
				t.Fatalf("unexpected spread active query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"sprdId":"BTC-USDT_BTC-USDT-SWAP","ordId":"sprd-ord-1","clOrdId":"spread-client","state":"live","side":"buy","ordType":"limit","sz":"1","accFillSz":"0","px":"10","uTime":"1000"}]}`))
		case http.MethodGet + " /api/v5/sprd/trades":
			if r.URL.Query().Get("sprdId") != spreadID || r.URL.Query().Get("limit") != "50" {
				t.Fatalf("unexpected spread trades query: %s", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"sprdId":"BTC-USDT_BTC-USDT-SWAP","tradeId":"trade-1","ordId":"sprd-ord-1","clOrdId":"spread-client","side":"buy","fillPx":"10","fillSz":"1","fee":"-0.01","feeCcy":"USDT","ts":"1000"}]}`))
		default:
			t.Fatalf("unexpected request: %s", key)
		}
	}))
	defer srv.Close()

	client := NewClient().WithCredentials("key", "secret", "pass")
	client.BaseURL = srv.URL
	clientOrderID := "spread-client"
	price := "10"
	orders, err := client.PlaceSpreadOrder(context.Background(), &SpreadOrderRequest{
		SprdId:  spreadID,
		ClOrdId: &clientOrderID,
		Side:    "buy",
		OrdType: "post_only",
		Sz:      "1",
		Px:      &price,
	})
	if err != nil {
		t.Fatalf("PlaceSpreadOrder: %v", err)
	}
	if len(orders) != 1 || orders[0].OrdId != "sprd-ord-1" {
		t.Fatalf("unexpected spread place response: %+v", orders)
	}
	if _, err := client.CancelSpreadOrder(context.Background(), spreadID, "sprd-ord-1", ""); err != nil {
		t.Fatalf("CancelSpreadOrder: %v", err)
	}
	if _, err := client.CancelAllSpreadOrders(context.Background(), spreadID); err != nil {
		t.Fatalf("CancelAllSpreadOrders: %v", err)
	}
	if got, err := client.GetSpreadOrder(context.Background(), spreadID, "sprd-ord-1", ""); err != nil || len(got) != 1 {
		t.Fatalf("GetSpreadOrder: got=%+v err=%v", got, err)
	}
	if got, err := client.GetSpreadOrders(context.Background(), &[]string{spreadID}[0]); err != nil || len(got) != 1 {
		t.Fatalf("GetSpreadOrders: got=%+v err=%v", got, err)
	}
	if got, err := client.GetSpreadTrades(context.Background(), &[]string{spreadID}[0], nil, 50); err != nil || len(got) != 1 {
		t.Fatalf("GetSpreadTrades: got=%+v err=%v", got, err)
	}
	for _, key := range []string{
		http.MethodPost + " /api/v5/sprd/order",
		http.MethodPost + " /api/v5/sprd/cancel-order",
		http.MethodPost + " /api/v5/sprd/mass-cancel",
		http.MethodGet + " /api/v5/sprd/order",
		http.MethodGet + " /api/v5/sprd/orders-pending",
		http.MethodGet + " /api/v5/sprd/trades",
	} {
		if !seen[key] {
			t.Fatalf("expected request %s", key)
		}
	}
}

func TestClient_CancelOrders(t *testing.T) {
	client := requireOKXLiveWrite(t)
	testenv.RequireEnv(t, "OKX_TEST_ORDER_ID")
	orderID := os.Getenv("OKX_TEST_ORDER_ID")
	got, err := client.CancelOrders(context.Background(), []CancelOrderRequest{{
		InstId: okxEnvOrDefault("OKX_TEST_ORDER_INST_ID", okxSpotInstID),
		OrdId:  &orderID,
	}})
	if err != nil {
		t.Fatalf("CancelOrders: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("unexpected cancel batch response: %+v", got)
	}
}

func TestClient_ClosePosition(t *testing.T) {
	got, err := requireOKXLiveWrite(t).ClosePosition(
		context.Background(),
		okxEnvOrDefault("OKX_TEST_CLOSE_POSITION_INST_ID", okxSwapInstID),
		okxEnvOrDefault("OKX_TEST_MARGIN_MODE", "cross"),
	)
	if err != nil {
		t.Fatalf("ClosePosition: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil close-position response")
	}
}

func TestClient_ClosePositionBuildsPrivateRequest(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || r.URL.Path != "/api/v5/trade/close-position" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		if r.Header.Get("OK-ACCESS-KEY") == "" || r.Header.Get("OK-ACCESS-SIGN") == "" || r.Header.Get("OK-ACCESS-PASSPHRASE") == "" {
			t.Fatalf("expected private OKX auth headers")
		}
		var req map[string]string
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req["instId"] != okxSwapInstID || req["mgnMode"] != "cross" || req["autoCxl"] != "true" {
			t.Fatalf("unexpected close-position request: %+v", req)
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","posSide":"net"}]}`))
	}))
	defer srv.Close()

	client := NewClient().WithCredentials("key", "secret", "pass")
	client.BaseURL = srv.URL
	got, err := client.ClosePosition(context.Background(), okxSwapInstID, "cross")
	if err != nil {
		t.Fatalf("ClosePosition: %v", err)
	}
	if len(got) != 1 || got[0].InstId != okxSwapInstID {
		t.Fatalf("unexpected close-position response: %+v", got)
	}
}

func TestClient_GetOrder(t *testing.T) {
	client := newLivePrivateClient(t)
	orderID := os.Getenv("OKX_TEST_ORDER_ID")
	clientOrderID := os.Getenv("OKX_TEST_CLIENT_ORDER_ID")
	if orderID == "" && clientOrderID == "" {
		t.Skip("skipping private read: set OKX_TEST_ORDER_ID or OKX_TEST_CLIENT_ORDER_ID to query a real order")
	}
	got, err := client.GetOrder(context.Background(), okxEnvOrDefault("OKX_TEST_ORDER_INST_ID", okxSpotInstID), orderID, clientOrderID)
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if len(got) == 0 {
		t.Fatalf("unexpected order response: %+v", got)
	}
}

func TestClient_GetOrders(t *testing.T) {
	instType := "SPOT"
	instID := okxSpotInstID
	got, err := newLivePrivateClient(t).GetOrders(context.Background(), &instType, &instID)
	if err != nil {
		t.Fatalf("GetOrders: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil pending orders slice")
	}
}

func TestClient_GetAlgoOrderBuildsPrivateQuery(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/trade/order-algo" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("algoId") != "algo-1" || r.URL.Query().Get("algoClOrdId") != "client-algo" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","instType":"SWAP","algoId":"algo-1","algoClOrdId":"client-algo","state":"live","side":"sell","ordType":"trigger","sz":"1","triggerPx":"9","orderPx":"-1","cTime":"1000","uTime":"1000"}]}`))
	}))
	defer srv.Close()

	client := NewClient().WithCredentials("key", "secret", "pass")
	client.BaseURL = srv.URL
	got, err := client.GetAlgoOrder(context.Background(), "algo-1", "client-algo")
	if err != nil {
		t.Fatalf("GetAlgoOrder: %v", err)
	}
	if len(got) != 1 || got[0].AlgoId != "algo-1" || got[0].AlgoClOrdId != "client-algo" {
		t.Fatalf("unexpected algo order response: %+v", got)
	}
}

func TestClient_GetPendingAlgoOrdersBuildsPrivateQuery(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/trade/orders-algo-pending" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		q := r.URL.Query()
		if q.Get("instType") != "SWAP" || q.Get("instId") != okxSwapInstID || q.Get("ordType") != "trigger" || q.Get("algoId") != "algo-1" || q.Get("algoClOrdId") != "client-algo" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instId":"BTC-USDT-SWAP","instType":"SWAP","algoId":"algo-1","algoClOrdId":"client-algo","state":"live","side":"sell","ordType":"trigger","sz":"1","triggerPx":"9","orderPx":"-1","cTime":"1000","uTime":"1000"}]}`))
	}))
	defer srv.Close()

	client := NewClient().WithCredentials("key", "secret", "pass")
	client.BaseURL = srv.URL
	got, err := client.GetPendingAlgoOrders(context.Background(), "SWAP", okxSwapInstID, "trigger", "algo-1", "client-algo")
	if err != nil {
		t.Fatalf("GetPendingAlgoOrders: %v", err)
	}
	if len(got) != 1 || got[0].AlgoId != "algo-1" || got[0].State != "live" {
		t.Fatalf("unexpected pending algo response: %+v", got)
	}
}

func TestClient_GetFillsBuildsPrivateQuery(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/v5/trade/fills" {
			t.Fatalf("unexpected path: %s", r.URL.Path)
		}
		if r.URL.Query().Get("instType") != "SPOT" || r.URL.Query().Get("instId") != okxSpotInstID || r.URL.Query().Get("ordId") != "100" || r.URL.Query().Get("limit") != "50" {
			t.Fatalf("unexpected query: %s", r.URL.RawQuery)
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"instType":"SPOT","instId":"BTC-USDT","tradeId":"trade-1","ordId":"100","clOrdId":"client-1","side":"buy","fillPx":"10","fillSz":"0.5","fee":"-0.01","feeCcy":"USDT","ts":"1000"}]}`))
	}))
	defer srv.Close()

	client := NewClient().WithCredentials("key", "secret", "pass")
	client.BaseURL = srv.URL
	instType := "SPOT"
	instID := okxSpotInstID
	orderID := "100"
	got, err := client.GetFills(context.Background(), &instType, &instID, &orderID, 50)
	if err != nil {
		t.Fatalf("GetFills: %v", err)
	}
	if len(got) != 1 || got[0].TradeId != "trade-1" || got[0].FillPx != "10" {
		t.Fatalf("unexpected fills response: %+v", got)
	}
}
