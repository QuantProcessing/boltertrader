package hyperliquid

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gorilla/websocket"
)

func TestWebsocketClientUnsubscribeRemovesOnlyMatchingSubscription(t *testing.T) {
	upgrader := websocket.Upgrader{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		conn, err := upgrader.Upgrade(w, r, nil)
		if err != nil {
			return
		}
		defer conn.Close()
		for {
			if _, _, err := conn.ReadMessage(); err != nil {
				return
			}
		}
	}))
	defer server.Close()
	client := NewWebsocketClient(context.Background()).WithURL("ws" + strings.TrimPrefix(server.URL, "http"))
	defer client.Close()
	if err := client.Connect(); err != nil {
		t.Fatalf("Connect: %v", err)
	}
	btc := map[string]string{"type": "activeAssetCtx", "coin": "BTC"}
	eth := map[string]string{"type": "activeAssetCtx", "coin": "ETH"}

	if err := client.Subscribe("activeAssetCtx", btc, func(WsMessage) {}); err != nil {
		t.Fatalf("Subscribe(BTC): %v", err)
	}
	if err := client.Subscribe("activeAssetCtx", eth, func(WsMessage) {}); err != nil {
		t.Fatalf("Subscribe(ETH): %v", err)
	}
	if err := client.Unsubscribe("activeAssetCtx", eth); err != nil {
		t.Fatalf("Unsubscribe(ETH): %v", err)
	}

	if got := len(client.subscriptions["activeAssetCtx"]); got != 1 {
		t.Fatalf("expected BTC handler to remain after ETH unsubscribe, got %d handlers", got)
	}
	if _, ok := client.subscriptionPayloads["activeAssetCtx"]; !ok {
		t.Fatal("expected remaining activeAssetCtx subscription payload for reconnect replay")
	}
}
