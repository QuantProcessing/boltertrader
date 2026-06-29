package spot

import (
	"context"
	"testing"
)

func TestNewWSClientAndNewWsClientReturnCompatibleTypes(t *testing.T) {
	modern := NewWSClient(context.Background(), "wss://example.com/ws")
	legacy := NewWsClient(context.Background(), "wss://example.com/ws")
	t.Cleanup(modern.Close)
	t.Cleanup(legacy.Close)

	var legacyTyped *WsClient = modern
	var modernTyped *WSClient = legacy

	if legacyTyped != modern {
		t.Fatalf("legacy alias should reference the same concrete type")
	}
	if modernTyped != legacy {
		t.Fatalf("new constructor should remain assignable from the compatibility alias")
	}
	if modern.URL != legacy.URL {
		t.Fatalf("constructors should initialize equivalent clients, got %q and %q", modern.URL, legacy.URL)
	}
}

func TestWsMarketClientKeepsLegacyEmbeddedFieldName(t *testing.T) {
	client := NewWsMarketClient(context.Background())
	t.Cleanup(client.Close)

	if client.WsClient == nil {
		t.Fatal("expected legacy embedded WsClient field to remain available")
	}
	var modernTyped *WSClient = client.WsClient
	if modernTyped != client.WsClient {
		t.Fatal("legacy embedded field should still reference the WSClient implementation")
	}
}
