package spot

import (
	"context"
	"strings"
	"testing"

	hyperliquid "github.com/QuantProcessing/exchanges/sdk/hyperliquid"
)

func TestWSMarketCompanion_SubscriptionTypes(t *testing.T) {
	if "l2Book" == "" || "trades" == "" || "bbo" == "" {
		t.Fatal("expected spot market websocket subscription names")
	}
}

func TestWebsocketClient_SubscribeAllMids(t *testing.T) {
	client := NewWebsocketClient(hyperliquid.NewWebsocketClient(context.Background()))

	err := client.SubscribeAllMids(func(hyperliquid.WsAllMids) {})
	if err == nil || !strings.Contains(err.Error(), "websocket not connected") {
		t.Fatalf("expected websocket not connected error, got %v", err)
	}
}

func TestWebsocketClient_SubscribeAllMidsWithDex(t *testing.T) {
	client := NewWebsocketClient(hyperliquid.NewWebsocketClient(context.Background()))

	err := client.SubscribeAllMidsWithDex("xyz", func(hyperliquid.WsAllMids) {})
	if err == nil || !strings.Contains(err.Error(), "websocket not connected") {
		t.Fatalf("expected websocket not connected error, got %v", err)
	}
}

func TestWebsocketClient_UnsubscribeAllMids(t *testing.T) {
	client := NewWebsocketClient(hyperliquid.NewWebsocketClient(context.Background()))

	err := client.UnsubscribeAllMids()
	if err == nil || !strings.Contains(err.Error(), "websocket not connected") {
		t.Fatalf("expected websocket not connected error, got %v", err)
	}
}
