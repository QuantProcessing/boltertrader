package perp

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
	"github.com/stretchr/testify/require"
)

func TestClient_UserOpenOrders(t *testing.T) {
	account := os.Getenv("HYPERLIQUID_ACCOUNT_ADDR")
	orders, err := newLivePrivateClient(t).UserOpenOrders(context.Background(), account)
	require.NoError(t, err)
	require.NotNil(t, orders)
}

func TestClient_OrderStatus(t *testing.T) {
	client := newLivePrivateClient(t)
	orderID := os.Getenv("HYPERLIQUID_TEST_ORDER_ID")
	if orderID == "" {
		t.Skip("HYPERLIQUID_TEST_ORDER_ID is required for OrderStatus live test")
	}

	status, err := client.OrderStatus(context.Background(), os.Getenv("HYPERLIQUID_ACCOUNT_ADDR"), hyperliquidInt64Env(t, "HYPERLIQUID_TEST_ORDER_ID"))
	require.NoError(t, err)
	require.NotNil(t, status)
}

func TestClient_PlaceOrder(t *testing.T) {
	client := requireHyperliquidLiveWrite(t, "HYPERLIQUID_PERP_TEST_ASSET_ID", "HYPERLIQUID_TEST_ORDER_PRICE", "HYPERLIQUID_TEST_ORDER_SIZE")

	status, err := client.PlaceOrder(context.Background(), PlaceOrderRequest{
		AssetID: hyperliquidPerpAssetID(t),
		IsBuy:   hyperliquidBoolEnv("HYPERLIQUID_TEST_ORDER_IS_BUY", true),
		Price:   hyperliquidFloatEnv(t, "HYPERLIQUID_TEST_ORDER_PRICE"),
		Size:    hyperliquidFloatEnv(t, "HYPERLIQUID_TEST_ORDER_SIZE"),
		OrderType: OrderType{Limit: &OrderTypeLimit{
			Tif: hyperliquid.TifGtc,
		}},
	})
	require.NoError(t, err)
	require.NotNil(t, status)
}

func TestClient_PlaceOrdersBuildsBatchAction(t *testing.T) {
	var seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = string(body)
		_, _ = w.Write([]byte(`{"status":"ok","response":{"type":"default","data":{"statuses":[{"resting":{"oid":100,"cloid":"client-1"}},{"resting":{"oid":101,"cloid":"client-2"}}]}}}`))
	}))
	defer srv.Close()
	base := hyperliquid.NewClient().WithCredentials(strings.Repeat("01", 32), nil)
	base.BaseURL = srv.URL
	client := NewClient(base)

	statuses, err := client.PlaceOrders(context.Background(), []PlaceOrderRequest{{
		AssetID:       1,
		IsBuy:         true,
		Price:         10,
		Size:          0.1,
		ClientOrderID: ptrString("client-1"),
		OrderType:     OrderType{Limit: &OrderTypeLimit{Tif: hyperliquid.TifGtc}},
	}, {
		AssetID:       1,
		IsBuy:         false,
		Price:         11,
		Size:          0.2,
		ClientOrderID: ptrString("client-2"),
		OrderType:     OrderType{Limit: &OrderTypeLimit{Tif: hyperliquid.TifIoc}},
	}})
	require.NoError(t, err)
	require.Len(t, statuses, 2)
	require.Contains(t, seenBody, `"type":"order"`)
	require.Contains(t, seenBody, `"orders":[`)
	require.Contains(t, seenBody, `"c":"client-1"`)
	require.Contains(t, seenBody, `"c":"client-2"`)
}

func TestClient_ModifyOrder(t *testing.T) {
	client := requireHyperliquidLiveWrite(t, "HYPERLIQUID_PERP_TEST_ASSET_ID", "HYPERLIQUID_TEST_ORDER_ID", "HYPERLIQUID_TEST_ORDER_PRICE", "HYPERLIQUID_TEST_ORDER_SIZE")
	oid := hyperliquidInt64Env(t, "HYPERLIQUID_TEST_ORDER_ID")

	status, err := client.ModifyOrder(context.Background(), ModifyOrderRequest{
		Oid: &oid,
		Order: PlaceOrderRequest{
			AssetID: hyperliquidPerpAssetID(t),
			IsBuy:   hyperliquidBoolEnv("HYPERLIQUID_TEST_ORDER_IS_BUY", true),
			Price:   hyperliquidFloatEnv(t, "HYPERLIQUID_TEST_ORDER_PRICE"),
			Size:    hyperliquidFloatEnv(t, "HYPERLIQUID_TEST_ORDER_SIZE"),
			OrderType: OrderType{Limit: &OrderTypeLimit{
				Tif: hyperliquid.TifGtc,
			}},
		},
	})
	require.NoError(t, err)
	require.NotNil(t, status)
}

func TestClient_CancelOrder(t *testing.T) {
	client := requireHyperliquidLiveWrite(t, "HYPERLIQUID_PERP_TEST_ASSET_ID", "HYPERLIQUID_TEST_ORDER_ID")

	status, err := client.CancelOrder(context.Background(), CancelOrderRequest{
		AssetID: hyperliquidPerpAssetID(t),
		OrderID: hyperliquidInt64Env(t, "HYPERLIQUID_TEST_ORDER_ID"),
	})
	require.NoError(t, err)
	require.NotNil(t, status)
}

func TestClient_CancelOrdersBuildsBatchAction(t *testing.T) {
	var seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = string(body)
		_, _ = w.Write([]byte(`{"status":"ok","response":{"type":"default","data":{"statuses":["success","success"]}}}`))
	}))
	defer srv.Close()
	base := hyperliquid.NewClient().WithCredentials(strings.Repeat("01", 32), nil)
	base.BaseURL = srv.URL
	client := NewClient(base)

	statuses, err := client.CancelOrders(context.Background(), []CancelOrderRequest{{AssetID: 1, OrderID: 100}, {AssetID: 1, OrderID: 101}})
	require.NoError(t, err)
	require.Equal(t, []string{"success", "success"}, statuses)
	require.Contains(t, seenBody, `"type":"cancel"`)
	require.Contains(t, seenBody, `"cancels":[`)
	require.Contains(t, seenBody, `"o":100`)
	require.Contains(t, seenBody, `"o":101`)
}

func hyperliquidFloatEnv(t *testing.T, key string) float64 {
	t.Helper()
	value, err := strconv.ParseFloat(os.Getenv(key), 64)
	require.NoError(t, err)
	return value
}

func ptrString(value string) *string {
	return &value
}

func hyperliquidInt64Env(t *testing.T, key string) int64 {
	t.Helper()
	value, err := strconv.ParseInt(os.Getenv(key), 10, 64)
	require.NoError(t, err)
	return value
}
