package spot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strconv"
	"strings"
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

func TestClient_OrderStatus(t *testing.T) {
	var seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = string(body)
		_, _ = w.Write([]byte(`{"order":{"coin":"PURR/USDC","side":"A","limitPx":"10","sz":"1","oid":100,"status":"open"}}`))
	}))
	defer srv.Close()
	base := hyperliquid.NewClient()
	base.BaseURL = srv.URL
	client := NewClient(base)

	status, err := client.OrderStatus(context.Background(), "0xabc", 100)
	if err != nil {
		t.Fatalf("OrderStatus: %v", err)
	}
	if status == nil || status.Oid != 100 {
		t.Fatalf("unexpected status: %+v", status)
	}
	if !strings.Contains(seenBody, `"type":"orderStatus"`) || !strings.Contains(seenBody, `"oid":100`) {
		t.Fatalf("unexpected order status body: %s", seenBody)
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
