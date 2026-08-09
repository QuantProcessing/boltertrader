package spot

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/ratelimit"
)

func TestClient_DefaultRateLimiterKeysMatchBinanceSpotQuotas(t *testing.T) {
	client := NewClient()

	got := client.rateLimitKeys("/api/v3/klines")
	want := []string{"binance:api/v3/klines", "binance:global"}
	if len(got) != len(want) {
		t.Fatalf("expected %d keys, got %v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected key %d to be %q, got %q", i, want[i], got[i])
		}
	}

	if quota, ok := client.RateLimiter.Quota("binance:global"); !ok || quota.Limit != 6000 || quota.Interval != time.Minute {
		t.Fatalf("unexpected global quota: %+v ok=%v", quota, ok)
	}
	if quota, ok := client.RateLimiter.Quota("binance:api/v3/order"); !ok || quota.Limit != 3000 || quota.Interval != time.Minute {
		t.Fatalf("unexpected order quota: %+v ok=%v", quota, ok)
	}
	if quota, ok := client.RateLimiter.Quota("binance:api/v3/allOrders"); !ok || quota.Limit != 150 || quota.Interval != time.Minute {
		t.Fatalf("unexpected allOrders quota: %+v ok=%v", quota, ok)
	}
	if quota, ok := client.RateLimiter.Quota("binance:api/v3/klines"); !ok || quota.Limit != 600 || quota.Interval != time.Minute {
		t.Fatalf("unexpected klines quota: %+v ok=%v", quota, ok)
	}
}

func TestClient_CallWaitsForConfiguredSpotRateLimitBeforeRequest(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		_, _ = w.Write([]byte(`{"serverTime":123}`))
	}))
	defer server.Close()

	limiter := NewKeyedRateLimiter(map[string]RateLimitQuota{
		"binance:global": {Limit: 1, Interval: 80 * time.Millisecond},
	})
	client := NewClient().WithBaseURL(server.URL).WithRateLimiter(limiter)

	var out struct {
		ServerTime int64 `json:"serverTime"`
	}
	if err := client.Get(context.Background(), "/api/v3/time", nil, false, &out); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	start := time.Now()
	if err := client.Get(context.Background(), "/api/v3/time", nil, false, &out); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 70*time.Millisecond {
		t.Fatalf("expected second request to wait for rate limiter, waited %s", elapsed)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected 2 HTTP calls, got %d", got)
	}
}

func TestClientWithGatewayTransportDisablesSpotSDKLocalLimiter(t *testing.T) {
	var calls atomic.Int64
	transport, err := ratelimit.NewTransport(
		ratelimit.NewCoordinator(time.Now),
		vectorProviderFunc(func(*http.Request, ratelimit.RequestScope) []ratelimit.Key {
			return []ratelimit.Key{{Kind: ratelimit.VenueProfile, Value: "binance/test/binance.spot.demo.v1"}}
		}),
		ratelimit.PublicScope,
		roundTripFunc(func(*http.Request) (*http.Response, error) {
			calls.Add(1)
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader(`{"serverTime":123}`)),
				Header:     make(http.Header),
			}, nil
		}),
	)
	if err != nil {
		t.Fatal(err)
	}

	client := NewClient()
	if client.RateLimiter == nil {
		t.Fatal("standalone constructor unexpectedly disabled the SDK limiter")
	}
	client.RateLimiter = NewKeyedRateLimiter(map[string]RateLimitQuota{
		"binance:global": {Limit: 1, Interval: 80 * time.Millisecond},
	})
	client = client.WithGatewayTransport(transport)
	if client.RateLimiter != nil {
		t.Fatal("gateway transport did not disable the SDK-local limiter")
	}

	var out struct {
		ServerTime int64 `json:"serverTime"`
	}
	start := time.Now()
	if err := client.Get(context.Background(), "/api/v3/time", nil, false, &out); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if err := client.Get(context.Background(), "/api/v3/time", nil, false, &out); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 50*time.Millisecond {
		t.Fatalf("gateway transport still paid limiter delay: %s", elapsed)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected 2 HTTP calls through gateway transport, got %d", got)
	}
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

type vectorProviderFunc func(*http.Request, ratelimit.RequestScope) []ratelimit.Key

func (fn vectorProviderFunc) Keys(request *http.Request, scope ratelimit.RequestScope) []ratelimit.Key {
	return fn(request, scope)
}
