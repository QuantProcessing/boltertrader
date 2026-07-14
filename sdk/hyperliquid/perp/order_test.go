package perp

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
	"github.com/stretchr/testify/require"
)

func TestClient_UserOpenOrders(t *testing.T) {
	account := os.Getenv("HYPERLIQUID_ACCOUNT_ADDR")
	orders, err := newLivePrivateClient(t).UserOpenOrders(context.Background(), account)
	require.NoError(t, err)
	require.NotNil(t, orders)
}

func TestClientUserOpenOrdersForDexUsesFrontendSchema(t *testing.T) {
	var seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = string(body)
		_, _ = w.Write([]byte(`[{"coin":"COIN","side":"A","limitPx":"10","sz":"1.5","oid":77,"cloid":"0xabc","timestamp":1700000000000,"origSz":"2","reduceOnly":true,"orderType":"Limit","tif":"Alo"}]`))
	}))
	defer srv.Close()
	base := hyperliquid.NewClient()
	base.BaseURL = srv.URL
	client := NewClient(base)

	orders, err := client.UserOpenOrdersForDex(context.Background(), "0xuser", "testdex")
	require.NoError(t, err)
	require.Len(t, orders, 1)
	require.Equal(t, "testdex:COIN", orders[0].Coin)
	require.Equal(t, "2", orders[0].OrigSz)
	require.True(t, orders[0].ReduceOnly)
	require.Contains(t, seenBody, `"type":"frontendOpenOrders"`)
	require.Contains(t, seenBody, `"dex":"testdex"`)
}

func TestClientUserOpenOrdersDecodesMinimalFrontendFixtures(t *testing.T) {
	tests := []struct {
		name       string
		dex        string
		response   string
		wantOrders []Order
	}{
		{
			name:     "standard perp",
			response: `[{"coin":"BTC","side":"A","limitPx":"29792.0","sz":"5.0","oid":91490942,"timestamp":1681247412573,"origSz":"5.0"}]`,
			wantOrders: []Order{{
				Coin: "BTC", Side: "A", LimitPx: "29792.0", Sz: "5.0",
				Oid: 91490942, Timestamp: 1681247412573, OrigSz: "5.0",
			}},
		},
		{
			name:     "HIP-3 qualifies only unqualified coins",
			dex:      "testdex",
			response: `[{"coin":"COIN","side":"A","limitPx":"10","sz":"1.5","oid":77,"timestamp":1700000000000,"origSz":"2"},{"coin":"testdex:OTHER","side":"B","limitPx":"20","sz":"3","oid":78,"timestamp":1700000000001,"origSz":"4"}]`,
			wantOrders: []Order{
				{Coin: "testdex:COIN", Side: "A", LimitPx: "10", Sz: "1.5", Oid: 77, Timestamp: 1700000000000, OrigSz: "2"},
				{Coin: "testdex:OTHER", Side: "B", LimitPx: "20", Sz: "3", Oid: 78, Timestamp: 1700000000001, OrigSz: "4"},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var seenBody string
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				seenBody = string(body)
				_, _ = w.Write([]byte(tt.response))
			}))
			t.Cleanup(srv.Close)
			base := hyperliquid.NewClient()
			base.BaseURL = srv.URL
			client := NewClient(base)

			orders, err := client.UserOpenOrdersForDex(context.Background(), "0xuser", tt.dex)
			require.NoError(t, err)
			require.Equal(t, tt.wantOrders, orders)
			for _, order := range orders {
				require.Empty(t, order.Cliod)
				require.False(t, order.ReduceOnly)
				require.Empty(t, order.OrderType)
				require.Empty(t, order.Tif)
				require.False(t, order.IsTrigger)
				require.Empty(t, order.TriggerPx)
			}
			require.Contains(t, seenBody, `"type":"frontendOpenOrders"`)
			if tt.dex == "" {
				require.NotContains(t, seenBody, `"dex"`)
			} else {
				require.Contains(t, seenBody, `"dex":"`+tt.dex+`"`)
			}
		})
	}
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

func TestClient_OrderStatusByCloidDecodesOfficialNestedResponse(t *testing.T) {
	const cloid = "0x1234567890abcdef1234567890abcdef"
	var seenBody string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		seenBody = string(body)
		_, _ = w.Write([]byte(`{"status":"order","order":{"order":{"coin":"BTC","side":"B","limitPx":"65000","sz":"0.004","oid":101,"cloid":"` + cloid + `","timestamp":1700000000000,"origSz":"0.01","reduceOnly":true,"orderType":"Stop Market","isTrigger":true,"triggerPx":"64000"},"status":"canceled","statusTimestamp":1700000001000}}`))
	}))
	defer srv.Close()
	base := hyperliquid.NewClient()
	base.BaseURL = srv.URL
	client := NewClient(base)

	status, err := client.OrderStatusByCloid(context.Background(), "0xabc", strings.ToUpper(cloid))
	require.NoError(t, err)
	require.NotNil(t, status)
	require.Equal(t, int64(101), status.Oid)
	require.Equal(t, cloid, status.Cliod)
	require.Equal(t, "0.006", status.FilledSz)
	require.True(t, status.ReduceOnly)
	require.True(t, status.HasReduceOnly)
	require.Equal(t, "Stop Market", status.OrderType)
	require.True(t, status.IsTrigger)
	require.Equal(t, "64000", status.TriggerPx)
	require.Equal(t, int64(1700000001000), status.StatusTimestamp)
	require.Contains(t, seenBody, `"oid":"`+strings.ToUpper(cloid)+`"`)
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
	require.Nil(t, status)
	require.ErrorIs(t, err, hyperliquid.ErrOrderNotFound)
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

func TestClient_PlaceOrdersReturnsVenueErrorResponseString(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"err","response":"Order must have minimum value of $10."}`))
	}))
	defer srv.Close()
	base := hyperliquid.NewClient().WithCredentials(strings.Repeat("01", 32), nil)
	base.BaseURL = srv.URL
	client := NewClient(base)

	_, err := client.PlaceOrders(context.Background(), []PlaceOrderRequest{{
		AssetID:   1,
		IsBuy:     true,
		Price:     1,
		Size:      0.1,
		OrderType: OrderType{Limit: &OrderTypeLimit{Tif: hyperliquid.TifGtc}},
	}})
	require.Error(t, err)
	require.Contains(t, err.Error(), "minimum value")
}

func TestClient_PlaceOrdersReturnsTypedPerOrderRejection(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"status":"ok","response":{"type":"order","data":{"statuses":[{"error":"Insufficient margin"}]}}}`))
	}))
	defer srv.Close()
	base := hyperliquid.NewClient().WithCredentials(strings.Repeat("01", 32), nil)
	base.BaseURL = srv.URL
	client := NewClient(base)

	_, err := client.PlaceOrders(context.Background(), []PlaceOrderRequest{{
		AssetID: 1, IsBuy: true, Price: 1, Size: 0.1,
		OrderType: OrderType{Limit: &OrderTypeLimit{Tif: hyperliquid.TifGtc}},
	}})
	require.True(t, errors.Is(err, hyperliquid.ErrOrderRejected), "err=%v", err)
	require.Contains(t, err.Error(), "Insufficient margin")
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

				base := hyperliquid.NewClient().WithCredentials(strings.Repeat("01", 32), nil)
				base.BaseURL = srv.URL
				client := NewClient(base)
				order := PlaceOrderRequest{
					AssetID: 1, IsBuy: true, Price: 10, Size: 0.1,
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
	base := hyperliquid.NewClient().WithCredentials(strings.Repeat("01", 32), nil)
	base.BaseURL = srv.URL
	client := NewClient(base)
	order := PlaceOrderRequest{AssetID: 1, IsBuy: true, Price: 10, Size: 0.1, OrderType: OrderType{Limit: &OrderTypeLimit{Tif: hyperliquid.TifGtc}}}

	status, err := client.PlaceOrder(context.Background(), order)
	require.Nil(t, status)
	require.ErrorContains(t, err, "no order status")
	oid := int64(1)
	status, err = client.ModifyOrder(context.Background(), ModifyOrderRequest{Oid: &oid, Order: order})
	require.Nil(t, status)
	require.ErrorContains(t, err, "no order status")
	cancelStatus, err := client.CancelOrder(context.Background(), CancelOrderRequest{AssetID: 1, OrderID: 1})
	require.Nil(t, cancelStatus)
	require.ErrorContains(t, err, "no order status")
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
