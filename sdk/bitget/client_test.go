package sdk

import (
	"context"
	"net/http"
	"os"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/internal/testenv"
)

const (
	bitgetLiveWriteFlag = "BITGET_ENABLE_LIVE_WRITE_TESTS"
	bitgetSpotCategory  = "SPOT"
	bitgetPerpCategory  = "USDT-FUTURES"
	bitgetSpotSymbol    = "BTCUSDT"
	bitgetPerpSymbol    = "BTCUSDT"
)

func newLiveClient() *Client {
	return NewClient()
}

func newLivePrivateClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveCredentials(t, "BITGET_API_KEY", "BITGET_SECRET_KEY", "BITGET_PASSPHRASE")
	return NewClient().WithCredentials(os.Getenv("BITGET_API_KEY"), os.Getenv("BITGET_SECRET_KEY"), os.Getenv("BITGET_PASSPHRASE"))
}

func requireBitgetLiveWrite(t *testing.T, vars ...string) *Client {
	t.Helper()
	required := append([]string{"BITGET_API_KEY", "BITGET_SECRET_KEY", "BITGET_PASSPHRASE"}, vars...)
	testenv.RequireLiveWrite(t, bitgetLiveWriteFlag, required...)
	return newLivePrivateClient(t)
}

func bitgetEnvOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

func skipIfBitgetAccountModeMismatch(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "40084") || strings.Contains(lower, "classic account mode") || strings.Contains(lower, "unified account api is not supported") {
		t.Skip("Skipping: Bitget UTA live read requires unified account credentials; current credentials are classic account credentials")
	}
}

func skipIfBitgetPrivateReadUnavailable(t *testing.T, err error, endpoint string) {
	t.Helper()
	testenv.SkipIfTransientLiveNetworkError(t, err, endpoint)
	skipIfBitgetAccountModeMismatch(t, err)
}

func skipIfBitgetPrivateWSUnavailable(t *testing.T, err error) {
	t.Helper()
	if err == nil {
		return
	}
	if err == context.DeadlineExceeded {
		t.Skip("Skipping: Bitget private WS live endpoint did not complete login before the test deadline")
	}
	lower := strings.ToLower(err.Error())
	if strings.Contains(lower, "login timeout") || strings.Contains(lower, "context deadline exceeded") {
		t.Skip("Skipping: Bitget private WS live endpoint did not complete login before the test deadline")
	}
}

func TestClient_WithCredentials(t *testing.T) {
	client := NewClient().WithCredentials("key", "secret", "pass")

	if client.apiKey != "key" || client.secretKey != "secret" || client.passphrase != "pass" {
		t.Fatalf("unexpected credentials: %+v", client)
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

func TestClient_HasCredentials(t *testing.T) {
	if NewClient().HasCredentials() {
		t.Fatal("expected empty client to have no credentials")
	}
	if !NewClient().WithCredentials("key", "secret", "pass").HasCredentials() {
		t.Fatal("expected credentials to be detected")
	}
}
