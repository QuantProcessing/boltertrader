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
		WithCredentials("key", "secret").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenPath = req.URL.Path
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"retCode":0,"retMsg":"OK","result":{}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	var out responseEnvelope[map[string]any]
	if err := client.PostPrivateRaw(context.Background(), "/v5/order/create-batch", map[string]any{"category": "linear"}, &out); err != nil {
		t.Fatalf("PostPrivateRaw returned error: %v", err)
	}
	if seenPath != "/v5/order/create-batch" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
}

func TestClient_GetPrivateRaw(t *testing.T) {
	var seenPath string
	var seenQuery string
	client := NewClient().
		WithCredentials("key", "secret").
		WithHTTPClient(&http.Client{Transport: rawRoundTripFunc(func(req *http.Request) (*http.Response, error) {
			seenPath = req.URL.Path
			seenQuery = req.URL.RawQuery
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"retCode":0,"retMsg":"OK","result":{"ok":true}}`)),
				Header:     make(http.Header),
			}, nil
		})})

	var out responseEnvelope[map[string]any]
	if err := client.GetPrivateRaw(context.Background(), "/v5/order/realtime", map[string]string{"category": "linear"}, &out); err != nil {
		t.Fatalf("GetPrivateRaw returned error: %v", err)
	}
	if seenPath != "/v5/order/realtime" {
		t.Fatalf("unexpected path: %s", seenPath)
	}
	if !strings.Contains(seenQuery, "category=linear") {
		t.Fatalf("unexpected query: %s", seenQuery)
	}
}
