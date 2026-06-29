package hyperliquid

import (
	"context"
	"testing"
)

func TestWebsocketClientUnsubscribeRemovesOnlyMatchingSubscription(t *testing.T) {
	client := NewWebsocketClient(context.Background())
	btc := map[string]string{"type": "activeAssetCtx", "coin": "BTC"}
	eth := map[string]string{"type": "activeAssetCtx", "coin": "ETH"}

	_ = client.Subscribe("activeAssetCtx", btc, func(WsMessage) {})
	_ = client.Subscribe("activeAssetCtx", eth, func(WsMessage) {})
	_ = client.Unsubscribe("activeAssetCtx", eth)

	if got := len(client.subscriptions["activeAssetCtx"]); got != 1 {
		t.Fatalf("expected BTC handler to remain after ETH unsubscribe, got %d handlers", got)
	}
	if _, ok := client.subscriptionPayloads["activeAssetCtx"]; !ok {
		t.Fatal("expected remaining activeAssetCtx subscription payload for reconnect replay")
	}
}
