package sdk

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"
)

type rawRoundTripFunc func(*http.Request) (*http.Response, error)

func (f rawRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func TestClient_PostPrivateRaw(t *testing.T) {
	var seenPath string
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenPath = req.URL.Path
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"code":"00000","msg":"success","data":{}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	var out responseEnvelope[map[string]any]
	if err := client.PostPrivateRaw(context.Background(), "/api/v2/spot/trade/batch-orders", map[string]any{"symbol": "BTCUSDT"}, &out); err != nil {
		t.Fatalf("PostPrivateRaw returned error: %v", err)
	}
	if seenPath != "/api/v2/spot/trade/batch-orders" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
}

func TestClient_GetPrivateRaw(t *testing.T) {
	var seenPath string
	var seenQuery string
	client := NewClient().
		WithCredentials("key", "secret", "passphrase").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenPath = req.URL.Path
			seenQuery = req.URL.RawQuery
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"code":"00000","msg":"success","data":{}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	var out responseEnvelope[map[string]any]
	if err := client.GetPrivateRaw(context.Background(), "/api/v2/spot/trade/orderInfo", map[string]string{"orderId": "1"}, &out); err != nil {
		t.Fatalf("GetPrivateRaw returned error: %v", err)
	}
	if seenPath != "/api/v2/spot/trade/orderInfo" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	if !strings.Contains(seenQuery, "orderId=1") {
		t.Fatalf("unexpected query: %s", seenQuery)
	}
}
