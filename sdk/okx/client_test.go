package okx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

const (
	okxLiveWriteFlag = "OKX_ENABLE_LIVE_WRITE_TESTS"
	okxSpotInstID    = "BTC-USDT"
	okxSwapInstID    = "BTC-USDT-SWAP"
)

func newLiveClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveRead(t)
	return NewClient()
}

func newLivePrivateClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveRead(t, "OKX_API_KEY", "OKX_API_SECRET", "OKX_API_PASSPHRASE")
	return NewClient().WithCredentials(os.Getenv("OKX_API_KEY"), os.Getenv("OKX_API_SECRET"), os.Getenv("OKX_API_PASSPHRASE"))
}

func requireOKXLiveWrite(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveWrite(t, okxLiveWriteFlag, "OKX_API_KEY", "OKX_API_SECRET", "OKX_API_PASSPHRASE")
	return NewClient().WithCredentials(os.Getenv("OKX_API_KEY"), os.Getenv("OKX_API_SECRET"), os.Getenv("OKX_API_PASSPHRASE"))
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
	t.Parallel()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet || r.URL.Path != "/api/v5/public/time" {
			t.Fatalf("unexpected request: %s %s", r.Method, r.URL.Path)
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ts":"1700000000000"}]}`))
	}))
	defer srv.Close()

	client := NewClient()
	client.BaseURL = srv.URL
	data, err := client.Do(context.Background(), MethodGet, "/api/v5/public/time", nil, false)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	if len(data) == 0 {
		t.Fatal("expected response body")
	}
}
