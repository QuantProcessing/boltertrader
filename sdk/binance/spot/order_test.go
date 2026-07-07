package spot

import (
	"context"
	"encoding/json"
	"os"
	"strconv"
	"testing"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

func TestClient_PlaceOrder(t *testing.T) {
	client := requireBinanceSpotLiveWrite(t)
	testenv.RequireEnv(t, "BINANCE_SPOT_TEST_ORDER_QTY", "BINANCE_SPOT_TEST_ORDER_PRICE")

	got, err := client.PlaceOrder(context.Background(), PlaceOrderParams{
		Symbol:           envOrDefault("BINANCE_SPOT_TEST_SYMBOL", binanceSpotTestSymbol),
		Side:             envOrDefault("BINANCE_SPOT_TEST_ORDER_SIDE", "BUY"),
		Type:             "LIMIT",
		TimeInForce:      "GTC",
		Quantity:         os.Getenv("BINANCE_SPOT_TEST_ORDER_QTY"),
		Price:            os.Getenv("BINANCE_SPOT_TEST_ORDER_PRICE"),
		NewClientOrderID: envOrDefault("BINANCE_SPOT_TEST_CLIENT_ORDER_ID", "sdk-live-write-test"),
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if got.OrderID == 0 {
		t.Fatalf("unexpected order response: %+v", got)
	}
}

func TestClient_CancelOrder(t *testing.T) {
	client := requireBinanceSpotLiveWrite(t)
	testenv.RequireEnv(t, "BINANCE_SPOT_TEST_CANCEL_ORDER_ID")
	orderID, err := strconv.ParseInt(os.Getenv("BINANCE_SPOT_TEST_CANCEL_ORDER_ID"), 10, 64)
	if err != nil {
		t.Fatalf("parse BINANCE_SPOT_TEST_CANCEL_ORDER_ID: %v", err)
	}

	got, err := client.CancelOrder(context.Background(), envOrDefault("BINANCE_SPOT_TEST_SYMBOL", binanceSpotTestSymbol), orderID, "")
	if err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if got.OrderID != orderID {
		t.Fatalf("unexpected cancel response: %+v", got)
	}
}

func TestClient_ModifyOrder(t *testing.T) {
	client := requireBinanceSpotLiveWrite(t)
	testenv.RequireEnv(t, "BINANCE_SPOT_TEST_CANCEL_ORDER_ID", "BINANCE_SPOT_TEST_ORDER_QTY", "BINANCE_SPOT_TEST_ORDER_PRICE")
	orderID, err := strconv.ParseInt(os.Getenv("BINANCE_SPOT_TEST_CANCEL_ORDER_ID"), 10, 64)
	if err != nil {
		t.Fatalf("parse BINANCE_SPOT_TEST_CANCEL_ORDER_ID: %v", err)
	}

	got, err := client.ModifyOrder(context.Background(), CancelReplaceOrderParams{
		Symbol:            envOrDefault("BINANCE_SPOT_TEST_SYMBOL", binanceSpotTestSymbol),
		Side:              envOrDefault("BINANCE_SPOT_TEST_ORDER_SIDE", "BUY"),
		Type:              "LIMIT",
		CancelReplaceMode: "STOP_ON_FAILURE",
		TimeInForce:       "GTC",
		Quantity:          os.Getenv("BINANCE_SPOT_TEST_ORDER_QTY"),
		Price:             os.Getenv("BINANCE_SPOT_TEST_ORDER_PRICE"),
		CancelOrderID:     orderID,
	})
	if err != nil {
		t.Fatalf("ModifyOrder: %v", err)
	}
	if got.NewOrderStatus == "" {
		t.Fatalf("unexpected modify response: %+v", got)
	}
}

func TestClient_GetOrder(t *testing.T) {
	client := newLivePrivateClient(t)
	testenv.RequireEnv(t, "BINANCE_SPOT_TEST_ORDER_ID")
	orderID, err := strconv.ParseInt(os.Getenv("BINANCE_SPOT_TEST_ORDER_ID"), 10, 64)
	if err != nil {
		t.Fatalf("parse BINANCE_SPOT_TEST_ORDER_ID: %v", err)
	}

	got, err := client.GetOrder(context.Background(), envOrDefault("BINANCE_SPOT_TEST_SYMBOL", binanceSpotTestSymbol), orderID, "")
	if err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	if got.OrderID != orderID {
		t.Fatalf("unexpected order response: %+v", got)
	}
}

func TestClient_GetOpenOrders(t *testing.T) {
	got, err := newLivePrivateClient(t).GetOpenOrders(context.Background(), envOrDefault("BINANCE_SPOT_TEST_SYMBOL", binanceSpotTestSymbol))
	if err != nil {
		t.Fatalf("GetOpenOrders: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil open orders slice")
	}
}

func TestClient_AllOrders(t *testing.T) {
	got, err := newLivePrivateClient(t).AllOrders(context.Background(), envOrDefault("BINANCE_SPOT_TEST_SYMBOL", binanceSpotTestSymbol), 5, 0, 0, 0)
	if err != nil {
		t.Fatalf("AllOrders: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil all orders slice")
	}
}

func TestClient_MyTrades(t *testing.T) {
	got, err := newLivePrivateClient(t).MyTrades(context.Background(), envOrDefault("BINANCE_SPOT_TEST_SYMBOL", binanceSpotTestSymbol), 5, 0, 0, 0)
	if err != nil {
		t.Fatalf("MyTrades: %v", err)
	}
	if got == nil {
		t.Fatal("expected non-nil trades slice")
	}
}

func TestCancelReplaceOrderResponse_UnmarshalJSON(t *testing.T) {
	var got CancelReplaceOrderResponse
	err := json.Unmarshal([]byte(`{
		"cancelResult":"SUCCESS",
		"newOrderResult":"SUCCESS",
		"cancelResponse":{"symbol":"BTCUSDT","orderId":1},
		"newOrderResponse":{"symbol":"BTCUSDT","orderId":2}
	}`), &got)
	if err != nil {
		t.Fatalf("UnmarshalJSON returned error: %v", err)
	}

	if got.CancelResult != "SUCCESS" || got.NewOrderStatus != "SUCCESS" {
		t.Fatalf("unexpected status fields: %#v", got)
	}
	if got.CancelResponse == nil || got.CancelResponse.OrderID != 1 {
		t.Fatalf("unexpected cancel response: %#v", got.CancelResponse)
	}
	if got.NewOrderResponse == nil || got.NewOrderResponse.OrderID != 2 {
		t.Fatalf("unexpected new order response: %#v", got.NewOrderResponse)
	}
}

func TestOrderResponseUnmarshalFullFills(t *testing.T) {
	var got OrderResponse
	err := json.Unmarshal([]byte(`{
		"symbol":"ETHUSDT",
		"orderId":555,
		"clientOrderId":"c-fill",
		"status":"FILLED",
		"executedQty":"0.0100",
		"cummulativeQuoteQty":"30.00",
		"fills":[
			{"price":"3000.00","qty":"0.0100","commission":"0.003","commissionAsset":"BNB","tradeId":456}
		]
	}`), &got)
	if err != nil {
		t.Fatalf("UnmarshalJSON returned error: %v", err)
	}
	if len(got.Fills) != 1 {
		t.Fatalf("fills len=%d, want 1", len(got.Fills))
	}
	fill := got.Fills[0]
	if fill.TradeID != 456 || fill.Price != "3000.00" || fill.Qty != "0.0100" || fill.CommissionAsset != "BNB" {
		t.Fatalf("unexpected fill: %+v", fill)
	}
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}
