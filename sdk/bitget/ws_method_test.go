package sdk

import (
	"context"
	"strings"
	"testing"
)

func TestPublicWSClient_ConnectClosedClient(t *testing.T) {
	client := NewPublicWSClient()
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	err := client.Connect(context.Background())
	if err == nil || !strings.Contains(err.Error(), "client closed") {
		t.Fatalf("expected closed client error, got %v", err)
	}
}

func TestPublicWSClient_Close(t *testing.T) {
	client := NewPublicWSClient()
	if err := client.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !client.closed {
		t.Fatal("expected client to be closed")
	}
}

func TestPrivateWSClient_WithCredentials(t *testing.T) {
	client := NewPrivateWSClient()
	got := client.WithCredentials("api-key", "secret-key", "passphrase")

	if got != client {
		t.Fatal("WithCredentials should return receiver")
	}
	if client.apiKey != "api-key" || client.secretKey != "secret-key" || client.passphrase != "passphrase" {
		t.Fatalf("unexpected credentials: %+v", client)
	}
}

func TestPrivateWSClient_WithClassicMode(t *testing.T) {
	client := NewPrivateWSClient()
	got := client.WithClassicMode()

	if got != client {
		t.Fatal("WithClassicMode should return receiver")
	}
	if client.url != classicWSURL || !client.useSeconds {
		t.Fatalf("unexpected classic mode client: %+v", client)
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
