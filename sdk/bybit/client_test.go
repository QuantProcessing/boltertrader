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

func TestEnvironmentProfiles(t *testing.T) {
	demo := DemoEnvironmentProfile()
	if demo.RESTBaseURL != "https://api-demo.bybit.com" {
		t.Fatalf("demo rest=%q", demo.RESTBaseURL)
	}
	if demo.PublicWSURL("linear") != "wss://stream.bybit.com/v5/public/linear" {
		t.Fatalf("demo public linear ws=%q", demo.PublicWSURL("linear"))
	}
	if demo.PrivateWSURL != "wss://stream-demo.bybit.com/v5/private" {
		t.Fatalf("demo private ws=%q", demo.PrivateWSURL)
	}
	if demo.SupportsWSTrade || demo.TradeWSURL != "" {
		t.Fatalf("demo must not expose trade ws: %+v", demo)
	}

	testnet := TestnetEnvironmentProfile()
	if testnet.RESTBaseURL != "https://api-testnet.bybit.com" {
		t.Fatalf("testnet rest=%q", testnet.RESTBaseURL)
	}
	if testnet.PublicWSURL("spot") != "wss://stream-testnet.bybit.com/v5/public/spot" {
		t.Fatalf("testnet spot ws=%q", testnet.PublicWSURL("spot"))
	}
	if !testnet.SupportsWSTrade || testnet.TradeWSURL != "wss://stream-testnet.bybit.com/v5/trade" {
		t.Fatalf("testnet trade ws=%+v", testnet)
	}
}

func TestClient_WithEnvironmentProfileUsesRESTBaseURL(t *testing.T) {
	client := NewClient().WithEnvironmentProfile(DemoEnvironmentProfile())

	if client.baseURL != "https://api-demo.bybit.com" {
		t.Fatalf("client baseURL=%q", client.baseURL)
	}
}

func TestWSClientsUseEnvironmentProfiles(t *testing.T) {
	demo := DemoEnvironmentProfile()

	public := NewPublicWSClientWithProfile(demo, "linear")
	if public.url != "wss://stream.bybit.com/v5/public/linear" {
		t.Fatalf("public ws url=%q", public.url)
	}

	private := NewPrivateWSClientWithProfile(demo)
	if private.url != "wss://stream-demo.bybit.com/v5/private" {
		t.Fatalf("private ws url=%q", private.url)
	}

	if _, err := NewTradeWSClientWithProfile(demo); err == nil {
		t.Fatal("expected Demo profile to reject WS Trade client")
	}

	trade, err := NewTradeWSClientWithProfile(TestnetEnvironmentProfile())
	if err != nil {
		t.Fatalf("testnet trade ws: %v", err)
	}
	if trade.url != "wss://stream-testnet.bybit.com/v5/trade" {
		t.Fatalf("trade ws url=%q", trade.url)
	}
}

func TestSettlementConstants(t *testing.T) {
	if SettleCoinUSDT != "USDT" || SettleCoinUSDC != "USDC" {
		t.Fatalf("settle constants usdt=%q usdc=%q", SettleCoinUSDT, SettleCoinUSDC)
	}
}
