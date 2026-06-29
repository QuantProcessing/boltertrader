package lighter

import (
	"context"
	"testing"
	"time"
)

func TestWebsocketClient_PlaceOrder(t *testing.T) {
	client := requireLighterLiveWrite(t, "LIGHTER_TEST_ORDER_PRICE", "LIGHTER_TEST_ORDER_BASE_AMOUNT")
	wsClient := newLiveWSClient(t)

	hash, err := wsClient.PlaceOrder(context.Background(), client, CreateOrderRequest{
		MarketId:      lighterMarketID(t),
		Price:         uint32(lighterIntEnv(t, "LIGHTER_TEST_ORDER_PRICE", 0)),
		BaseAmount:    lighterInt64Env(t, "LIGHTER_TEST_ORDER_BASE_AMOUNT", 0),
		IsAsk:         uint32(lighterIntEnv(t, "LIGHTER_TEST_ORDER_IS_ASK", 0)),
		OrderType:     OrderTypeLimit,
		TimeInForce:   OrderTimeInForcePostOnly,
		ClientOrderId: lighterInt64Env(t, "LIGHTER_TEST_CLIENT_ORDER_ID", time.Now().UnixNano()),
	})
	if err != nil {
		t.Fatalf("PlaceOrder: %v", err)
	}
	if hash == "" {
		t.Fatal("expected tx hash")
	}
}

func TestWebsocketClient_CancelOrder(t *testing.T) {
	client := requireLighterLiveWrite(t, "LIGHTER_TEST_ORDER_ID")
	wsClient := newLiveWSClient(t)

	hash, err := wsClient.CancelOrder(context.Background(), client, CancelOrderRequest{
		MarketId: lighterMarketID(t),
		OrderId:  lighterInt64Env(t, "LIGHTER_TEST_ORDER_ID", 0),
	})
	if err != nil {
		t.Fatalf("CancelOrder: %v", err)
	}
	if hash == "" {
		t.Fatal("expected tx hash")
	}
}

func TestWebsocketClient_ModifyOrder(t *testing.T) {
	client := requireLighterLiveWrite(t, "LIGHTER_TEST_ORDER_ID", "LIGHTER_TEST_ORDER_PRICE", "LIGHTER_TEST_ORDER_BASE_AMOUNT")
	wsClient := newLiveWSClient(t)

	hash, err := wsClient.ModifyOrder(context.Background(), client, ModifyOrderRequest{
		MarketId:   lighterMarketID(t),
		OrderIndex: lighterInt64Env(t, "LIGHTER_TEST_ORDER_ID", 0),
		BaseAmount: lighterInt64Env(t, "LIGHTER_TEST_ORDER_BASE_AMOUNT", 0),
		Price:      uint32(lighterIntEnv(t, "LIGHTER_TEST_ORDER_PRICE", 0)),
	})
	if err != nil {
		t.Fatalf("ModifyOrder: %v", err)
	}
	if hash == "" {
		t.Fatal("expected tx hash")
	}
}

func TestWebsocketClient_CancelAllOrders(t *testing.T) {
	client := requireLighterLiveWrite(t)
	wsClient := newLiveWSClient(t)

	hash, err := wsClient.CancelAllOrders(context.Background(), client, CancelAllOrdersRequest{MarketId: lighterMarketID(t)})
	if err != nil {
		t.Fatalf("CancelAllOrders: %v", err)
	}
	if hash == "" {
		t.Fatal("expected tx hash")
	}
}

func newLiveWSClient(t *testing.T) *WebsocketClient {
	t.Helper()
	wsClient := NewWebsocketClient(context.Background())
	if err := wsClient.Connect(); err != nil {
		t.Fatalf("Connect websocket: %v", err)
	}
	t.Cleanup(wsClient.Close)
	return wsClient
}
