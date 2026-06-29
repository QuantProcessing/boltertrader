package subaccount

import (
	"os"
	"testing"

	"github.com/QuantProcessing/exchanges/internal/testenv"
)

const binanceSubAccountLiveWriteFlag = "BINANCE_ENABLE_LIVE_WRITE_TESTS"

func newLivePrivateClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveCredentials(t, "BINANCE_API_KEY", "BINANCE_SECRET_KEY")
	return NewClient().WithCredentials(
		os.Getenv("BINANCE_API_KEY"),
		os.Getenv("BINANCE_SECRET_KEY"),
	)
}

func requireBinanceSubAccountLiveWrite(t *testing.T, vars ...string) *Client {
	t.Helper()
	required := append([]string{"BINANCE_API_KEY", "BINANCE_SECRET_KEY"}, vars...)
	testenv.RequireLiveWrite(t, binanceSubAccountLiveWriteFlag, required...)
	return newLivePrivateClient(t)
}

func subAccountEnvOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func TestClient_WithCredentials(t *testing.T) {
	client := NewClient().WithCredentials("key", "secret")

	if client.APIKey != "key" || client.SecretKey != "secret" {
		t.Fatalf("unexpected credentials: %+v", client)
	}
}

func TestClient_WithBaseURL(t *testing.T) {
	client := NewClient().WithBaseURL("https://unit.test")

	if client.BaseURL != "https://unit.test" {
		t.Fatalf("unexpected baseURL: %s", client.BaseURL)
	}
}
