package sdk

import (
	"context"
	"net/http"
	"net/url"
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

func TestWebsocketProxyReadsProjectProxyFallback(t *testing.T) {
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("PROXY", "http://127.0.0.1:7897")

	proxyURL, err := websocketProxyFromEnvironment(&http.Request{URL: &url.URL{Scheme: "wss", Host: "stream-testnet.bybit.com"}})
	if err != nil {
		t.Fatalf("websocketProxyFromEnvironment: %v", err)
	}
	if proxyURL == nil || proxyURL.String() != "http://127.0.0.1:7897" {
		t.Fatalf("proxy=%v, want project PROXY fallback", proxyURL)
	}
}
