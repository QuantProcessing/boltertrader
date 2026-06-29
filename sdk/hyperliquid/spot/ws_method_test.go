package spot

import (
	"context"
	"strings"
	"testing"

	hyperliquid "github.com/QuantProcessing/exchanges/sdk/hyperliquid"
)

func newDisconnectedWSClient() *WebsocketClient {
	return NewWebsocketClient(hyperliquid.NewWebsocketClient(context.Background()))
}

func requireDisconnectedWSError(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "websocket not connected") {
		t.Fatalf("expected websocket not connected error, got %v", err)
	}
}

func TestWebsocketClient_SubscribeL2Book(t *testing.T) {
	err := newDisconnectedWSClient().SubscribeL2Book("@1", func(hyperliquid.WsL2Book) {})
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_SubscribeTrades(t *testing.T) {
	err := newDisconnectedWSClient().SubscribeTrades("@1", func([]hyperliquid.WsTrade) {})
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_SubscribeBbo(t *testing.T) {
	err := newDisconnectedWSClient().SubscribeBbo("@1", func(hyperliquid.WsBbo) {})
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_SubscribeCandle(t *testing.T) {
	err := newDisconnectedWSClient().SubscribeCandle("@1", "1m", func(hyperliquid.WsCandle) {})
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_UnsubscribeL2Book(t *testing.T) {
	err := newDisconnectedWSClient().UnsubscribeL2Book("@1")
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_UnsubscribeTrades(t *testing.T) {
	err := newDisconnectedWSClient().UnsubscribeTrades("@1")
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_UnsubscribeBbo(t *testing.T) {
	err := newDisconnectedWSClient().UnsubscribeBbo("@1")
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_UnsubscribeCandle(t *testing.T) {
	err := newDisconnectedWSClient().UnsubscribeCandle("@1", "1m")
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_SubscribeOrderUpdates(t *testing.T) {
	err := newDisconnectedWSClient().SubscribeOrderUpdates("0xabc", func([]hyperliquid.WsOrderUpdate) {})
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_SubscribeUserEvents(t *testing.T) {
	err := newDisconnectedWSClient().SubscribeUserEvents("0xabc", func(hyperliquid.WsUserEvent) {})
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_SubscribeUserFills(t *testing.T) {
	err := newDisconnectedWSClient().SubscribeUserFills("0xabc", func(hyperliquid.WsUserFills) {})
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_PlaceOrder(t *testing.T) {
	_, err := newDisconnectedWSClient().PlaceOrder(context.Background(), PlaceOrderRequest{})
	if err != hyperliquid.ErrCredentialsRequired {
		t.Fatalf("expected credentials error, got %v", err)
	}

	client := newDisconnectedWSClient()
	client.WithCredentials(strings.Repeat("01", 32), nil)
	_, err = client.PlaceOrder(context.Background(), PlaceOrderRequest{
		AssetID: 1,
		IsBuy:   true,
		Price:   100,
		Size:    1,
		OrderType: OrderType{Limit: &OrderTypeLimit{
			Tif: hyperliquid.TifGtc,
		}},
	})
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_CancelOrder(t *testing.T) {
	_, err := newDisconnectedWSClient().CancelOrder(context.Background(), CancelOrderRequest{})
	if err != hyperliquid.ErrCredentialsRequired {
		t.Fatalf("expected credentials error, got %v", err)
	}

	client := newDisconnectedWSClient()
	client.WithCredentials(strings.Repeat("01", 32), nil)
	_, err = client.CancelOrder(context.Background(), CancelOrderRequest{AssetID: 1, OrderID: 123})
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_ModifyOrder(t *testing.T) {
	_, err := newDisconnectedWSClient().ModifyOrder(context.Background(), ModifyOrderRequest{})
	if err != hyperliquid.ErrCredentialsRequired {
		t.Fatalf("expected credentials error, got %v", err)
	}

	client := newDisconnectedWSClient()
	client.WithCredentials(strings.Repeat("01", 32), nil)
	oid := int64(123)
	_, err = client.ModifyOrder(context.Background(), ModifyOrderRequest{
		Oid: &oid,
		Order: PlaceOrderRequest{
			AssetID: 1,
			IsBuy:   true,
			Price:   101,
			Size:    2,
			OrderType: OrderType{Limit: &OrderTypeLimit{
				Tif: hyperliquid.TifGtc,
			}},
		},
	})
	requireDisconnectedWSError(t, err)
}
