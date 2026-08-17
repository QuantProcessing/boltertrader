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

func TestMainnetEnvironmentProfileIsFullyPopulated(t *testing.T) {
	mainnet := MainnetEnvironmentProfile()
	if mainnet.RESTBaseURL != defaultBaseURL {
		t.Fatalf("rest=%q, want %q", mainnet.RESTBaseURL, defaultBaseURL)
	}
	if mainnet.PublicWSURL != publicWSURL {
		t.Fatalf("public ws=%q, want %q", mainnet.PublicWSURL, publicWSURL)
	}
	if mainnet.PrivateWSURL != privateWSURL {
		t.Fatalf("private ws=%q, want %q", mainnet.PrivateWSURL, privateWSURL)
	}
}

// The signed GET and signed POST paths build their headers separately, so each
// needs its own proof that credentials actually reach the transport. A request
// that goes out unsigned comes back as a bitget auth error rather than anything
// that names the missing header.
func TestClient_SignsGET(t *testing.T) {
	var seen http.Header
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithEnvironmentProfile(MainnetEnvironmentProfile()).
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seen = req.Header.Clone()
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"code":"00000","msg":"success","data":{"userId":"u1","permType":"read","permissions":["uta"]}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	if _, err := client.GetAccountInfo(context.Background()); err != nil {
		t.Fatalf("GetAccountInfo: %v", err)
	}
	if seen.Get("ACCESS-KEY") != "key" {
		t.Fatalf("ACCESS-KEY=%q, want key", seen.Get("ACCESS-KEY"))
	}
	if seen.Get("ACCESS-SIGN") == "" || seen.Get("ACCESS-TIMESTAMP") == "" {
		t.Fatalf("request went out unsigned: %v", seen)
	}
	if got := seen.Get("ACCESS-SIGN"); strings.Contains(got, "secret") {
		t.Fatalf("signature leaked the secret key: %q", got)
	}
}

func TestClient_SignsPOST(t *testing.T) {
	var seen http.Header
	var seenMethod string
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithEnvironmentProfile(MainnetEnvironmentProfile()).
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenMethod = req.Method
			seen = req.Header.Clone()
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
	if seen.Get("ACCESS-KEY") != "key" {
		t.Fatalf("ACCESS-KEY=%q, want key", seen.Get("ACCESS-KEY"))
	}
	if seen.Get("ACCESS-SIGN") == "" || seen.Get("ACCESS-TIMESTAMP") == "" {
		t.Fatalf("request went out unsigned: %v", seen)
	}
}

func TestWSClientsUseEnvironmentProfiles(t *testing.T) {
	profile := EnvironmentProfile{
		PublicWSURL:  "wss://public.test/ws",
		PrivateWSURL: "wss://private.test/ws",
	}

	public := NewPublicWSClientWithProfile(profile)
	if public.url != profile.PublicWSURL {
		t.Fatalf("public ws url=%q", public.url)
	}

	private := NewPrivateWSClientWithProfile(profile)
	if private.url != profile.PrivateWSURL {
		t.Fatalf("private ws url=%q", private.url)
	}
}

func TestSettlementProductTypeConstants(t *testing.T) {
	if ProductTypeUSDTFutures != "USDT-FUTURES" || ProductTypeUSDCFutures != "USDC-FUTURES" {
		t.Fatalf("product constants usdt=%q usdc=%q", ProductTypeUSDTFutures, ProductTypeUSDCFutures)
	}
}
