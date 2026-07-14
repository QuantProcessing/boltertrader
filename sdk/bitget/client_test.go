package sdk

import (
	"context"
	"io"
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

func newLiveClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveRead(t)
	return NewClient()
}

func newLivePrivateClient(t *testing.T) *Client {
	t.Helper()
	testenv.RequireLiveRead(t, "BITGET_API_KEY", "BITGET_SECRET_KEY", "BITGET_PASSPHRASE")
	return NewClient().WithCredentials(os.Getenv("BITGET_API_KEY"), os.Getenv("BITGET_SECRET_KEY"), os.Getenv("BITGET_PASSPHRASE"))
}

func requireBitgetLiveWrite(t *testing.T, vars ...string) *Client {
	t.Helper()
	required := append([]string{"BITGET_API_KEY", "BITGET_SECRET_KEY", "BITGET_PASSPHRASE"}, vars...)
	testenv.RequireLiveWrite(t, bitgetLiveWriteFlag, required...)
	return NewClient().WithCredentials(os.Getenv("BITGET_API_KEY"), os.Getenv("BITGET_SECRET_KEY"), os.Getenv("BITGET_PASSPHRASE"))
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

func TestEnvironmentProfiles(t *testing.T) {
	demo := DemoEnvironmentProfile()
	if demo.RESTBaseURL != "https://api.bitget.com" {
		t.Fatalf("demo rest=%q", demo.RESTBaseURL)
	}
	if demo.PublicWSURL != "wss://wspap.bitget.com/v3/ws/public" {
		t.Fatalf("demo public ws=%q", demo.PublicWSURL)
	}
	if demo.PrivateWSURL != "wss://wspap.bitget.com/v3/ws/private" {
		t.Fatalf("demo private ws=%q", demo.PrivateWSURL)
	}
	if !demo.PAPTrading {
		t.Fatalf("demo profile must set paptrading: %+v", demo)
	}

	testnet, err := NewTestnetEnvironmentProfile("https://testnet-api.bitget.example", "wss://testnet.bitget.example/public", "wss://testnet.bitget.example/private")
	if err != nil {
		t.Fatalf("testnet profile: %v", err)
	}
	if !testnet.OfficialTestnet || testnet.PAPTrading {
		t.Fatalf("testnet profile should be official and non-demo: %+v", testnet)
	}
}

func TestClient_WithEnvironmentProfileAddsPAPTradingHeader(t *testing.T) {
	var seenHeader string
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithEnvironmentProfile(DemoEnvironmentProfile()).
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenHeader = req.Header.Get("paptrading")
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"code":"00000","msg":"success","data":{"userId":"u1","permType":"read","permissions":["uta"]}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	if _, err := client.GetAccountInfo(context.Background()); err != nil {
		t.Fatalf("GetAccountInfo: %v", err)
	}
	if seenHeader != "1" {
		t.Fatalf("paptrading header=%q, want 1", seenHeader)
	}
}

func TestClient_WithEnvironmentProfileAddsPAPTradingHeaderToSignedPOST(t *testing.T) {
	var seenHeader string
	var seenMethod string
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithEnvironmentProfile(DemoEnvironmentProfile()).
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenMethod = req.Method
			seenHeader = req.Header.Get("paptrading")
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"code":"00000","msg":"success","data":{}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	var out responseEnvelope[map[string]any]
	if err := client.PostPrivateRaw(context.Background(), "/api/v3/trade/place-order", map[string]string{"symbol": "BTCUSDT"}, &out); err != nil {
		t.Fatalf("PostPrivateRaw: %v", err)
	}
	if seenMethod != http.MethodPost {
		t.Fatalf("method=%q, want POST", seenMethod)
	}
	if seenHeader != "1" {
		t.Fatalf("paptrading header=%q, want 1", seenHeader)
	}
}

func TestWSClientsUseEnvironmentProfiles(t *testing.T) {
	demo := DemoEnvironmentProfile()

	public := NewPublicWSClientWithProfile(demo)
	if public.url != "wss://wspap.bitget.com/v3/ws/public" {
		t.Fatalf("public ws url=%q", public.url)
	}

	private := NewPrivateWSClientWithProfile(demo)
	if private.url != "wss://wspap.bitget.com/v3/ws/private" {
		t.Fatalf("private ws url=%q", private.url)
	}
}

func TestSettlementProductTypeConstants(t *testing.T) {
	if ProductTypeUSDTFutures != "USDT-FUTURES" || ProductTypeUSDCFutures != "USDC-FUTURES" {
		t.Fatalf("product constants usdt=%q usdc=%q", ProductTypeUSDTFutures, ProductTypeUSDCFutures)
	}
}
