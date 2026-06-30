package perp

import (
	"context"
	"testing"

	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

func TestWSClientCompanion_NewWebsocketClient(t *testing.T) {
	base := hyperliquid.NewWebsocketClient(context.Background())
	client := NewWebsocketClient(base)
	if client.WebsocketClient != base {
		t.Fatal("expected base websocket client to be embedded")
	}
}
