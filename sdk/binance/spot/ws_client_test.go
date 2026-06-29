package spot

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"

	"github.com/gorilla/websocket"
)

func TestSpotNewWSClientAndNewWsClientReturnCompatibleTypes(t *testing.T) {
	newClient := NewWSClient(context.Background(), "wss://example.com/ws")
	legacyClient := NewWsClient(context.Background(), "wss://example.com/ws")

	if newClient == nil {
		t.Fatal("NewWSClient returned nil")
	}
	if legacyClient == nil {
		t.Fatal("NewWsClient returned nil")
	}

	var canonical *WSClient = legacyClient
	var legacy *WsClient = newClient

	if reflect.TypeOf(canonical) != reflect.TypeOf(legacy) {
		t.Fatalf("expected compatible client types, got %T and %T", canonical, legacy)
	}
}

func TestSpotWsMarketClientKeepsLegacyEmbeddedFieldName(t *testing.T) {
	client := NewWsMarketClient(context.Background())

	if client.WsClient == nil {
		t.Fatal("expected legacy WsClient embedded field to be populated")
	}

	field, ok := reflect.TypeOf(client).Elem().FieldByName("WsClient")
	if !ok {
		t.Fatal("expected WsMarketClient to keep embedded field named WsClient")
	}
	if !field.Anonymous {
		t.Fatal("expected WsClient field to remain embedded")
	}
	if field.Type != reflect.TypeOf((*WSClient)(nil)) {
		t.Fatalf("expected embedded field type %v, got %v", reflect.TypeOf((*WSClient)(nil)), field.Type)
	}
}

func TestWsClient_IsConnected(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	wsURL := newSpotWSServer(t, func(conn *websocket.Conn, _ *http.Request) {
		defer conn.Close()
		time.Sleep(500 * time.Millisecond)
	})

	client := NewWsMarketClient(ctx)
	client.WsClient.URL = wsURL + "/ws"

	// Before connection
	if client.IsConnected() {
		t.Error("Client should not be connected before Connect() is called")
	}

	if err := client.Connect(); err != nil {
		t.Fatalf("Failed to connect: %v", err)
	}
	if client.IsConnected() {
		t.Error("Client should not open a socket before any stream is subscribed")
	}
	if err := client.SubscribeBookTicker("BTCUSDT", func(*BookTickerEvent) error { return nil }); err != nil {
		t.Fatalf("SubscribeBookTicker: %v", err)
	}
	if !client.IsConnected() {
		t.Error("Client should be connected after a stream is subscribed")
	}

	// After close
	client.Close()
	time.Sleep(100 * time.Millisecond) // Give it time to close

	if client.IsConnected() {
		t.Error("Client should not be connected after Close() is called")
	}
}
