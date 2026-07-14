package portfolio

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
)

func TestSignedTransportErrorRedactsNestedRequestSignature(t *testing.T) {
	const signingSecret = "binance-portfolio-signing-secret"
	sentinel := errors.New("synthetic transport failure")
	client := NewClient().WithBaseURL("https://example.invalid").WithServerTimeBaseURL("https://example.invalid").WithCredentials("api-key", signingSecret)
	var signedURL, signature, timestamp string
	client.HTTPClient = &http.Client{Transport: binancePortfolioRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL.Query().Get("signature") == "" {
			return nil, errors.New("server time unavailable")
		}
		signedURL = req.URL.String()
		signature = req.URL.Query().Get("signature")
		timestamp = req.URL.Query().Get("timestamp")
		return nil, fmt.Errorf("wrapped transport failure: %w", &url.Error{Op: req.Method, URL: signedURL, Err: sentinel})
	})}

	err := client.get(context.Background(), "/papi/v1/account", map[string]string{"asset": "USDT"}, true, nil)
	if err == nil {
		t.Fatal("signed request unexpectedly succeeded")
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

type binancePortfolioRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn binancePortfolioRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}
