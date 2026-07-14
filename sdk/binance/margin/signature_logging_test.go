package margin

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
	const responseToken = "binance-margin-sensitive-response-token"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v3/time" {
			_, _ = w.Write([]byte(`{"serverTime":123}`))
			return
		}
		_, _ = fmt.Fprintf(w, `{"token":%q}`, responseToken)
	}))
	defer server.Close()

	core, observed := observer.New(zap.DebugLevel)
	client := NewClient().WithBaseURL(server.URL).WithServerTimeBaseURL(server.URL).WithCredentials("api-key", "signing-secret")
	client.HTTPClient = server.Client()
	client.Logger = zap.New(core).Sugar()
	var result map[string]any
	if err := client.Get(context.Background(), "/sapi/v1/test", map[string]any{"symbol": "BTCUSDT"}, true, &result); err != nil {
		t.Fatalf("signed Get: %v", err)
	}

	var logged strings.Builder
	for _, entry := range observed.All() {
		_, _ = fmt.Fprintf(&logged, "%s %v\n", entry.Message, entry.ContextMap())
	}
	text := logged.String()
	if strings.Contains(text, "signature=") || strings.Contains(text, responseToken) {
		t.Fatalf("debug log leaked signed URL or response token: %s", text)
	}
}

func TestSignedRESTTransportErrorRedactsNestedRequestSignature(t *testing.T) {
	const signingSecret = "binance-margin-signing-secret"
	sentinel := errors.New("synthetic transport failure")
	client := NewClient().WithBaseURL("https://example.invalid").WithServerTimeBaseURL("https://example.invalid").WithCredentials("api-key", signingSecret)
	var signedURL, signature, timestamp string
	client.HTTPClient = &http.Client{Transport: binanceMarginRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Query().Get("signature") == "" {
			return nil, errors.New("server time unavailable")
		}
		signedURL = req.URL.String()
		signature = req.URL.Query().Get("signature")
		timestamp = req.URL.Query().Get("timestamp")
		return nil, fmt.Errorf("wrapped transport failure: %w", &url.Error{Op: req.Method, URL: signedURL, Err: sentinel})
	})}

	err := client.Get(context.Background(), "/sapi/v1/test", map[string]any{"symbol": "BTCUSDT"}, true, nil)
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

type binanceMarginRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn binanceMarginRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
