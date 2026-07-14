package perp

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"go.uber.org/zap"
	"go.uber.org/zap/zaptest/observer"
)

func TestSignedRESTDebugLogRedactsSignatureAndResponseToken(t *testing.T) {
	const responseToken = "binance-perp-listen-key-secret"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprintf(w, `{"listenKey":%q}`, responseToken)
	}))
	defer server.Close()

	core, observed := observer.New(zap.DebugLevel)
	client := NewClient().WithBaseURL(server.URL).WithCredentials("api-key", "signing-secret")
	client.WithRateLimiter(nil)
	client.HTTPClient = server.Client()
	client.Logger = zap.New(core).Sugar()
	var result map[string]any
	if err := client.Get(context.Background(), "/fapi/v2/account", map[string]any{"symbol": "BTCUSDT"}, true, &result); err != nil {
		t.Fatalf("signed Get: %v", err)
	}

	logged := binancePerpObservedText(observed.All())
	if strings.Contains(logged, "signature=") || strings.Contains(logged, responseToken) || strings.Contains(logged, `"listenKey"`) {
		t.Fatalf("debug log leaked signed URL or response token: %s", logged)
	}
}

func TestWSDebugRequestSummaryRedactsCredentialsAndSignature(t *testing.T) {
	const secret = "binance-perp-ws-signature-secret"
	summary := wsDebugRequestSummary(map[string]any{
		"apiKey":    "binance-perp-ws-api-key-secret",
		"signature": secret,
	})
	if strings.Contains(summary, secret) || strings.Contains(summary, "api-key-secret") || strings.Contains(summary, "signature") {
		t.Fatalf("WS debug request summary leaked authentication material: %q", summary)
	}
}

func TestWSDebugResponseLogRedactsTokens(t *testing.T) {
	const token = "binance-perp-ws-listen-key-secret"
	core, observed := observer.New(zap.DebugLevel)
	client := NewWsAPIClient(context.Background())
	client.Debug = true
	client.Logger = zap.New(core).Sugar()

	client.handleMessage([]byte(`{"listenKey":"` + token + `"}`))

	if logged := binancePerpObservedText(observed.All()); strings.Contains(logged, token) || strings.Contains(logged, "listenKey") {
		t.Fatalf("WS debug response log leaked token: %s", logged)
	}
}

func TestSignedRESTTransportErrorRedactsNestedRequestSignature(t *testing.T) {
	const signingSecret = "binance-perp-signing-secret"
	sentinel := errors.New("synthetic transport failure")
	client := NewClient().WithBaseURL("https://example.invalid").WithCredentials("api-key", signingSecret)
	client.WithRateLimiter(nil)
	var signedURL, signature, timestamp string
	client.HTTPClient = &http.Client{Transport: binancePerpRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		signedURL = req.URL.String()
		signature = req.URL.Query().Get("signature")
		timestamp = req.URL.Query().Get("timestamp")
		return nil, fmt.Errorf("wrapped transport failure: %w", &url.Error{Op: req.Method, URL: signedURL, Err: sentinel})
	})}

	err := client.Get(context.Background(), "/fapi/v2/account", map[string]any{"symbol": "BTCUSDT"}, true, nil)
	if err == nil {
		t.Fatal("signed Get unexpectedly succeeded")
	}
	if signature == "" || timestamp == "" {
		t.Fatalf("transport did not observe signed request: signature=%q timestamp=%q", signature, timestamp)
	}
	for _, secret := range []string{signingSecret, signedURL, signature, timestamp, "signature=", "timestamp="} {
		if strings.Contains(err.Error(), secret) {
			t.Fatalf("transport error leaked %q: %v", secret, err)
		}
	}
	if !errors.Is(err, sentinel) {
		t.Fatalf("transport error lost cause: %v", err)
	}
}

type binancePerpRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn binancePerpRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func binancePerpObservedText(entries []observer.LoggedEntry) string {
	var logged strings.Builder
	for _, entry := range entries {
		_, _ = fmt.Fprintf(&logged, "%s %v\n", entry.Message, entry.ContextMap())
	}
	return logged.String()
}
