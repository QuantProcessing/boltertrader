package sdk

import (
	"net/http"
	"os"
	"strconv"
	"testing"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

const (
	bybitLiveWriteFlag = "BYBIT_ENABLE_LIVE_WRITE_TESTS"
	bybitSpotSymbol    = "BTCUSDT"
	bybitLinearSymbol  = "BTCUSDT"
)

func newLiveClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveRead(t)
	return NewClient()
}

func newLivePrivateClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveRead(t, "BYBIT_API_KEY", "BYBIT_SECRET_KEY")
	return NewClient().WithCredentials(os.Getenv("BYBIT_API_KEY"), os.Getenv("BYBIT_SECRET_KEY"))
}

func requireBybitLiveWrite(t *testing.T, vars ...string) *Client {
	t.Helper()
	required := append([]string{"BYBIT_API_KEY", "BYBIT_SECRET_KEY"}, vars...)
	testenv.RequireLiveWrite(t, bybitLiveWriteFlag, required...)
	return NewClient().WithCredentials(os.Getenv("BYBIT_API_KEY"), os.Getenv("BYBIT_SECRET_KEY"))
}

func bybitEnvOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func bybitEnvOrDefaultInt(t *testing.T, key string, fallback int) int {
	t.Helper()
	if value := os.Getenv(key); value != "" {
		parsed, err := strconv.Atoi(value)
		if err != nil {
			t.Fatalf("invalid %s: %v", key, err)
		}
		return parsed
	}
	return fallback
}

func TestClient_WithCredentials(t *testing.T) {
	client := NewClient().WithCredentials("key", "secret")

	if client.apiKey != "key" || client.secretKey != "secret" {
		t.Fatalf("unexpected credentials: %+v", client)
	}
}

func TestClient_HasCredentials(t *testing.T) {
	if NewClient().HasCredentials() {
		t.Fatal("expected empty client to have no credentials")
	}
	if !NewClient().WithCredentials("key", "secret").HasCredentials() {
		t.Fatal("expected credentials to be detected")
	}
}

func TestClient_WithBaseURL(t *testing.T) {
	client := NewClient().WithBaseURL("https://unit.test")

	if client.baseURL != "https://unit.test" {
		t.Fatalf("unexpected baseURL: %s", client.baseURL)
	}
}

func TestClient_WithHTTPClient(t *testing.T) {
	httpClient := &http.Client{}
	client := NewClient().WithHTTPClient(httpClient)

	if client.httpClient != httpClient {
		t.Fatal("WithHTTPClient did not install provided client")
	}
}
