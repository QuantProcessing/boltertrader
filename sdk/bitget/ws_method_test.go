package sdk

import (
	"context"
	"net/http"
	"net/url"
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

func TestWebsocketProxyReadsProjectProxyFallback(t *testing.T) {
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("NO_PROXY", "")
	t.Setenv("ALL_PROXY", "")
	t.Setenv("PROXY", "http://127.0.0.1:7897")

	proxyURL, err := websocketProxyFromEnvironment(&http.Request{URL: &url.URL{Scheme: "wss", Host: "wspap.bitget.com"}})
	if err != nil {
		t.Fatalf("websocketProxyFromEnvironment: %v", err)
	}
	if proxyURL == nil || proxyURL.String() != "http://127.0.0.1:7897" {
		t.Fatalf("proxy=%v, want project PROXY fallback", proxyURL)
	}
}
