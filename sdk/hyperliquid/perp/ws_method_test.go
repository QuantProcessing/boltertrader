package perp

import (
	"context"
	"errors"
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

func TestWebsocketClient_WithCredentials(t *testing.T) {
	client := newDisconnectedWSClient()
	got := client.WithCredentials(strings.Repeat("01", 32), "0xabc")

	if got != client {
		t.Fatal("expected WithCredentials to return the receiver")
	}
	if client.PrivateKey == nil {
		t.Fatal("expected private key to be set")
	}
	if client.AccountAddr != "0xabc" {
		t.Fatalf("unexpected account address: %s", client.AccountAddr)
	}
}

func TestWebsocketClient_SubscribeL2Book(t *testing.T) {
	err := newDisconnectedWSClient().SubscribeL2Book("BTC", func(hyperliquid.WsL2Book) {})
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_SubscribeTrades(t *testing.T) {
	err := newDisconnectedWSClient().SubscribeTrades("BTC", func([]hyperliquid.WsTrade) {})
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_SubscribeBbo(t *testing.T) {
	err := newDisconnectedWSClient().SubscribeBbo("BTC", func(hyperliquid.WsBbo) {})
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_SubscribeCandle(t *testing.T) {
	err := newDisconnectedWSClient().SubscribeCandle("BTC", "1m", func(hyperliquid.WsCandle) {})
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_SubscribeActiveAssetCtx(t *testing.T) {
	err := newDisconnectedWSClient().SubscribeActiveAssetCtx("BTC", func(hyperliquid.WsActiveAssetCtx) {})
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_SubscribeAllMids(t *testing.T) {
	err := newDisconnectedWSClient().SubscribeAllMids(func(hyperliquid.WsAllMids) {})
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_SubscribeAllMidsWithDex(t *testing.T) {
	err := newDisconnectedWSClient().SubscribeAllMidsWithDex("xyz", func(hyperliquid.WsAllMids) {})
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_SubscribeAllDexsAssetCtxs(t *testing.T) {
	err := newDisconnectedWSClient().SubscribeAllDexsAssetCtxs(func(hyperliquid.WsAllDexsAssetCtxs) {})
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_UnsubscribeL2Book(t *testing.T) {
	err := newDisconnectedWSClient().UnsubscribeL2Book("BTC")
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_UnsubscribeTrades(t *testing.T) {
	err := newDisconnectedWSClient().UnsubscribeTrades("BTC")
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_UnsubscribeBbo(t *testing.T) {
	err := newDisconnectedWSClient().UnsubscribeBbo("BTC")
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_UnsubscribeCandle(t *testing.T) {
	err := newDisconnectedWSClient().UnsubscribeCandle("BTC", "1m")
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_UnsubscribeActiveAssetCtx(t *testing.T) {
	err := newDisconnectedWSClient().UnsubscribeActiveAssetCtx("BTC")
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_UnsubscribeAllMids(t *testing.T) {
	err := newDisconnectedWSClient().UnsubscribeAllMids()
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_UnsubscribeAllMidsWithDex(t *testing.T) {
	err := newDisconnectedWSClient().UnsubscribeAllMidsWithDex("xyz")
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_UnsubscribeAllDexsAssetCtxs(t *testing.T) {
	err := newDisconnectedWSClient().UnsubscribeAllDexsAssetCtxs()
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

func TestWebsocketClient_SubscribeWebData2(t *testing.T) {
	err := newDisconnectedWSClient().SubscribeWebData2("0xabc", func(PerpPosition) {})
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_PlaceOrder(t *testing.T) {
	client := newDisconnectedWSClient().WithCredentials(strings.Repeat("01", 32), "0xabc")
	_, err := client.PlaceOrder(context.Background(), PlaceOrderRequest{
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
	client := newDisconnectedWSClient().WithCredentials(strings.Repeat("01", 32), "0xabc")
	_, err := client.CancelOrder(context.Background(), CancelOrderRequest{AssetID: 1, OrderID: 123})
	requireDisconnectedWSError(t, err)
}

func TestWebsocketClient_ModifyOrder(t *testing.T) {
	client := newDisconnectedWSClient().WithCredentials(strings.Repeat("01", 32), "0xabc")
	oid := int64(123)
	_, err := client.ModifyOrder(context.Background(), ModifyOrderRequest{
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
	if err != nil && errors.Is(err, hyperliquid.ErrCredentialsRequired) {
		t.Fatalf("expected credentials to be accepted, got %v", err)
	}
	requireDisconnectedWSError(t, err)
}
