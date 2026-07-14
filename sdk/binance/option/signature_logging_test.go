package option

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

func TestSignedRESTDebugLogRedactsSignature(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{}`))
	}))
	defer server.Close()

	core, observed := observer.New(zap.DebugLevel)
	client := NewClient().WithCredentials("api-key", "signing-secret")
	client.BaseURL = server.URL
	client.HTTPClient = server.Client()
	client.Logger = zap.New(core).Sugar()
	var result map[string]any
	if err := client.call(context.Background(), http.MethodGet, "/eapi/v1/account", map[string]any{"symbol": "BTCUSDT"}, true, &result); err != nil {
		t.Fatalf("signed call: %v", err)
	}

	var logged strings.Builder
	for _, entry := range observed.All() {
		_, _ = fmt.Fprintf(&logged, "%s %v\n", entry.Message, entry.ContextMap())
	}
	if text := logged.String(); strings.Contains(text, "signature=") {
		t.Fatalf("debug log leaked signed URL: %s", text)
	}
}

func TestSignedRESTTransportErrorRedactsNestedRequestSignature(t *testing.T) {
	const signingSecret = "binance-option-signing-secret"
	sentinel := errors.New("synthetic transport failure")
	client := NewClient().WithCredentials("api-key", signingSecret)
	client.BaseURL = "https://example.invalid"
	var signedURL, signature, timestamp string
	client.HTTPClient = &http.Client{Transport: binanceOptionRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		signedURL = req.URL.String()
		signature = req.URL.Query().Get("signature")
		timestamp = req.URL.Query().Get("timestamp")
		return nil, fmt.Errorf("wrapped transport failure: %w", &url.Error{Op: req.Method, URL: signedURL, Err: sentinel})
	})}

	err := client.call(context.Background(), http.MethodGet, "/eapi/v1/account", map[string]any{"symbol": "BTCUSDT"}, true, nil)
	if err == nil {
		t.Fatal("signed call unexpectedly succeeded")
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

type binanceOptionRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn binanceOptionRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
