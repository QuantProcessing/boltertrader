package okx

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
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

func TestDefaultEndpointURLs(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		env     Environment
		profile DemoHostProfile
		rest    string
		ws      string
	}{
		{name: "production", env: Production, rest: BaseURL, ws: WSBaseURL},
		{name: "zero environment defaults production", rest: BaseURL, ws: WSBaseURL},
		{name: "simulated global", env: Simulated, profile: DemoHostProfileGlobal, rest: DemoRESTBaseURL, ws: WSDemoBaseURL},
		{name: "simulated zero profile defaults global", env: Simulated, rest: DemoRESTBaseURL, ws: WSDemoBaseURL},
		{name: "simulated eea", env: Simulated, profile: DemoHostProfileEEA, rest: DemoEEARESTBaseURL, ws: WSDemoEEABaseURL},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := DefaultEndpointURLs(tt.env, tt.profile)
			if err != nil {
				t.Fatalf("DefaultEndpointURLs: %v", err)
			}
			if got.REST != tt.rest || got.WSPublic != tt.ws {
				t.Fatalf("endpoints=%+v, want rest=%s ws=%s", got, tt.rest, tt.ws)
			}
		})
	}
}

func TestDefaultEndpointURLsRejectsCustomWithoutOverrides(t *testing.T) {
	t.Parallel()

	_, err := DefaultEndpointURLs(Simulated, DemoHostProfileCustom)
	if err == nil || !strings.Contains(err.Error(), "custom demo host profile") {
		t.Fatalf("err=%v, want custom profile error", err)
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

func TestClient_DoAddsSimulatedTradingHeader(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-simulated-trading"); got != "1" {
			t.Fatalf("x-simulated-trading=%q, want 1", got)
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ts":"1700000000000"}]}`))
	}))
	defer srv.Close()

	client := NewClient().WithEnvironment(Simulated).WithBaseURL(srv.URL)
	if _, err := client.Do(context.Background(), MethodGet, "/api/v5/public/time", nil, false); err != nil {
		t.Fatalf("Do: %v", err)
	}
}

func TestClient_DoProductionOmitsSimulatedTradingHeader(t *testing.T) {
	t.Parallel()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if got := r.Header.Get("x-simulated-trading"); got != "" {
			t.Fatalf("x-simulated-trading=%q, want empty", got)
		}
		_, _ = w.Write([]byte(`{"code":"0","msg":"","data":[{"ts":"1700000000000"}]}`))
	}))
	defer srv.Close()

	client := NewClient().WithBaseURL(srv.URL)
	if _, err := client.Do(context.Background(), MethodGet, "/api/v5/public/time", nil, false); err != nil {
		t.Fatalf("Do: %v", err)
	}
}

func TestClient_DoCustomDemoProfileRequiresBaseURL(t *testing.T) {
	t.Parallel()

	client := NewClient().WithEnvironment(Simulated).WithDemoHostProfile(DemoHostProfileCustom)
	_, err := client.Do(context.Background(), MethodGet, "/api/v5/public/time", nil, false)
	if err == nil || !strings.Contains(err.Error(), "custom demo host profile") {
		t.Fatalf("err=%v, want custom profile error", err)
	}
}
