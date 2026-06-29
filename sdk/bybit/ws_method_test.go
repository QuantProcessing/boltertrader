package sdk

import (
	"context"
	"strings"
	"testing"
)

func TestPublicWSClient_ConnectClosedClient(t *testing.T) {
	client := NewPublicWSClient("spot")
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err := client.Connect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "client closed") {
		t.Fatalf("expected closed client error, got %v", err)
	}
}

func TestPublicWSClient_Close(t *testing.T) {
	client := NewPublicWSClient("spot")
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !client.closed {
		t.Fatal("expected client to be closed")
	}
}

func TestPrivateWSClient_WithCredentials(t *testing.T) {
	client := NewPrivateWSClient()
	got := client.WithCredentials("api-key", "secret-key")

	if got != client {
		t.Fatal("WithCredentials should return receiver")
	}
	if client.apiKey != "api-key" || client.secretKey != "secret-key" {
		t.Fatalf("unexpected credentials: %+v", client)
	}
}

func TestPrivateWSClient_ConnectClosedClient(t *testing.T) {
	client := NewPrivateWSClient()
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err := client.Connect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "client closed") {
		t.Fatalf("expected closed client error, got %v", err)
	}
}

func TestPrivateWSClient_Close(t *testing.T) {
	client := NewPrivateWSClient()
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !client.closed {
		t.Fatal("expected client to be closed")
	}
}

func TestTradeWSClient_WithCredentials(t *testing.T) {
	client := NewTradeWSClient()
	got := client.WithCredentials("api-key", "secret-key")

	if got != client {
		t.Fatal("WithCredentials should return receiver")
	}
	if client.apiKey != "api-key" || client.secretKey != "secret-key" {
		t.Fatalf("unexpected credentials: %+v", client)
	}
}

func TestTradeWSClient_ConnectClosedClient(t *testing.T) {
	client := NewTradeWSClient()
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err := client.Connect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "client closed") {
		t.Fatalf("expected closed client error, got %v", err)
	}
}

func TestTradeWSClient_Close(t *testing.T) {
	client := NewTradeWSClient()
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !client.closed {
		t.Fatal("expected client to be closed")
	}
}
