package perp

import (
	"context"
	"reflect"
	"testing"
)

func TestPerpNewWSClientAndNewWsClientReturnCompatibleTypes(t *testing.T) {
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

func TestPerpWsMarketClientKeepsLegacyEmbeddedFieldName(t *testing.T) {
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

func TestPerpWsAccountClientKeepsLegacyEmbeddedFieldName(t *testing.T) {
	client := NewWsAccountClient(context.Background(), "api-key", "api-secret")

	if client.WsClient == nil {
		t.Fatal("expected legacy WsClient embedded field to be populated")
	}

	field, ok := reflect.TypeOf(client).Elem().FieldByName("WsClient")
	if !ok {
		t.Fatal("expected WsAccountClient to keep embedded field named WsClient")
	}
	if !field.Anonymous {
		t.Fatal("expected WsClient field to remain embedded")
	}
	if field.Type != reflect.TypeOf((*WSClient)(nil)) {
		t.Fatalf("expected embedded field type %v, got %v", reflect.TypeOf((*WSClient)(nil)), field.Type)
	}
}
