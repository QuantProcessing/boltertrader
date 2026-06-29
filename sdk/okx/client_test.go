package okx

import (
	"context"
	"net/http"
	"os"
	"testing"
	"time"

	"github.com/QuantProcessing/exchanges/internal/testenv"
)

const (
	okxLiveWriteFlag = "OKX_ENABLE_LIVE_WRITE_TESTS"
	okxSpotInstID    = "BTC-USDT"
	okxSwapInstID    = "BTC-USDT-SWAP"
)

func newLiveClient() *Client {
	return NewClient()
}

func newLivePrivateClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveCredentials(t, "OKX_API_KEY", "OKX_API_SECRET", "OKX_API_PASSPHRASE")
	return NewClient().WithCredentials(os.Getenv("OKX_API_KEY"), os.Getenv("OKX_API_SECRET"), os.Getenv("OKX_API_PASSPHRASE"))
}

func requireOKXLiveWrite(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveWrite(t, okxLiveWriteFlag, "OKX_API_KEY", "OKX_API_SECRET", "OKX_API_PASSPHRASE")
	return newLivePrivateClient(t)
}

func okxEnvOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func TestClient_WithCredentials(t *testing.T) {
	client := NewClient().WithCredentials("key", "secret", "passphrase")

	if client.ApiKey != "key" || client.SecretKey != "secret" || client.Passphrase != "passphrase" || client.Signer == nil {
		t.Fatalf("unexpected credentials: %+v", client)
	}
}

func TestClient_DefaultHTTPTimeout(t *testing.T) {
	client := NewClient()
	if client.HTTPClient.Timeout <= 0 {
		t.Fatal("expected default HTTP timeout")
	}
}

func TestClient_WithHTTPClient(t *testing.T) {
	httpClient := &http.Client{Timeout: 42 * time.Second}
	client := NewClient().WithHTTPClient(httpClient)
	if client.HTTPClient != httpClient {
		t.Fatal("WithHTTPClient did not install provided client")
	}
}

func TestClient_Do(t *testing.T) {
	data, err := newLiveClient().Do(context.Background(), MethodGet, "/api/v5/public/time", nil, false)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected response body")
	}
}
