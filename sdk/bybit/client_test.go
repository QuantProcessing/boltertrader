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

func TestMainnetEnvironmentProfileIsFullyPopulated(t *testing.T) {
	mainnet := MainnetEnvironmentProfile()
	for name, got := range map[string]string{
		"RESTBaseURL":  mainnet.RESTBaseURL,
		"PrivateWSURL": mainnet.PrivateWSURL,
		"TradeWSURL":   mainnet.TradeWSURL,
	} {
		if got == "" {
			t.Fatalf("%s is empty", name)
		}
	}
	if mainnet.RESTBaseURL != defaultBaseURL {
		t.Fatalf("rest=%q, want %q", mainnet.RESTBaseURL, defaultBaseURL)
	}
}

// PublicWSURL routes by category rather than returning one URL, and linear is
// the fallback for anything unrecognised. That dispatch is the part callers
// depend on: bybit serves each product class on its own socket, and reading the
// spot book off the linear stream produces an empty subscription, not an error.
func TestPublicWSURLDispatchesByCategory(t *testing.T) {
	mainnet := MainnetEnvironmentProfile()
	for category, want := range map[string]string{
		"spot":    publicWSURLSpot,
		"linear":  publicWSURLLinear,
		"inverse": publicWSURLInverse,
		"option":  publicWSURLOption,
		"":        publicWSURLLinear,
	} {
		if got := mainnet.PublicWSURL(category); got != want {
			t.Fatalf("PublicWSURL(%q) = %q, want %q", category, got, want)
		}
	}
}

func TestClient_WithEnvironmentProfileUsesRESTBaseURL(t *testing.T) {
	client := NewClient().WithEnvironmentProfile(EnvironmentProfile{RESTBaseURL: "https://rest.test"})

	if client.baseURL != "https://rest.test" {
		t.Fatalf("client baseURL=%q", client.baseURL)
	}
}

func TestWSClientsUseEnvironmentProfiles(t *testing.T) {
	profile := EnvironmentProfile{
		PublicLinearWSURL: "wss://public.test/linear",
		PrivateWSURL:      "wss://private.test",
		TradeWSURL:        "wss://trade.test",
	}

	public := NewPublicWSClientWithProfile(profile, "linear")
	if public.url != profile.PublicLinearWSURL {
		t.Fatalf("public ws url=%q", public.url)
	}

	private := NewPrivateWSClientWithProfile(profile)
	if private.url != profile.PrivateWSURL {
		t.Fatalf("private ws url=%q", private.url)
	}

	trade, err := NewTradeWSClientWithProfile(profile)
	if err != nil {
		t.Fatalf("trade ws: %v", err)
	}
	if trade.url != profile.TradeWSURL {
		t.Fatalf("trade ws url=%q", trade.url)
	}
}

// A caller may hand in a profile it assembled itself, and one with no trade URL
// would otherwise produce a client that dials the empty string — a connection
// error at order time rather than at construction. This is the one profile field
// whose absence is still reachable, so the guard stays.
func TestNewTradeWSClientWithProfileRejectsMissingTradeURL(t *testing.T) {
	if _, err := NewTradeWSClientWithProfile(EnvironmentProfile{RESTBaseURL: defaultBaseURL}); err == nil {
		t.Fatal("expected a profile with no trade WS URL to be rejected")
	}
}

func TestSettlementConstants(t *testing.T) {
	if SettleCoinUSDT != "USDT" || SettleCoinUSDC != "USDC" {
		t.Fatalf("settle constants usdt=%q usdc=%q", SettleCoinUSDT, SettleCoinUSDC)
	}
}
