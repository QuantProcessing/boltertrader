package perp

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

func TestClient_DefaultRateLimiterKeysMatchBinanceFuturesQuotas(t *testing.T) {
	client := NewClient()

	got := client.rateLimitKeys("/fapi/v1/klines")
	want := []string{"binance:fapi/v1/klines", "binance:global"}
	if len(got) != len(want) {
		t.Fatalf("expected %d keys, got %v", len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("expected key %d to be %q, got %q", i, want[i], got[i])
		}
	}

	if quota, ok := client.RateLimiter.Quota("binance:global"); !ok || quota.Limit != 2400 || quota.Interval != time.Minute {
		t.Fatalf("unexpected global quota: %+v ok=%v", quota, ok)
	}
	if quota, ok := client.RateLimiter.Quota("binance:fapi/v1/order"); !ok || quota.Limit != 1200 || quota.Interval != time.Minute {
		t.Fatalf("unexpected order quota: %+v ok=%v", quota, ok)
	}
	if quota, ok := client.RateLimiter.Quota("binance:fapi/v1/allOrders"); !ok || quota.Limit != 60 || quota.Interval != time.Minute {
		t.Fatalf("unexpected allOrders quota: %+v ok=%v", quota, ok)
	}
	if quota, ok := client.RateLimiter.Quota("binance:fapi/v1/commissionRate"); !ok || quota.Limit != 120 || quota.Interval != time.Minute {
		t.Fatalf("unexpected commissionRate quota: %+v ok=%v", quota, ok)
	}
}

func TestClient_CallWaitsForConfiguredRateLimitBeforeRequest(t *testing.T) {
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
	if err := client.Get(context.Background(), "/fapi/v1/time", nil, false, &out); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	start := time.Now()
	if err := client.Get(context.Background(), "/fapi/v1/time", nil, false, &out); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if elapsed := time.Since(start); elapsed < 70*time.Millisecond {
		t.Fatalf("expected second request to wait for rate limiter, waited %s", elapsed)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected 2 HTTP calls, got %d", got)
	}
}

func TestKeyedRateLimiterRespectsContextCancellation(t *testing.T) {
	limiter := NewKeyedRateLimiter(map[string]RateLimitQuota{
		"binance:global": {Limit: 1, Interval: time.Hour},
	})
	if err := limiter.Wait(context.Background(), []string{"binance:global"}); err != nil {
		t.Fatalf("initial Wait: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if err := limiter.Wait(ctx, []string{"binance:global"}); err == nil {
		t.Fatal("expected context deadline error")
	}
}

func TestClientWithGatewayTransportDisablesPerpSDKLocalLimiter(t *testing.T) {
	var calls atomic.Int64
	transport, err := ratelimit.NewTransport(
		ratelimit.NewCoordinator(time.Now),
		vectorProviderFunc(func(*http.Request, ratelimit.RequestScope) []ratelimit.Key {
			return []ratelimit.Key{{Kind: ratelimit.VenueProfile, Value: "binance/test/binance.perp.usdt.demo.v1"}}
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
	if err := client.Get(context.Background(), "/fapi/v1/time", nil, false, &out); err != nil {
		t.Fatalf("first Get: %v", err)
	}
	if err := client.Get(context.Background(), "/fapi/v1/time", nil, false, &out); err != nil {
		t.Fatalf("second Get: %v", err)
	}
	if elapsed := time.Since(start); elapsed >= 50*time.Millisecond {
		t.Fatalf("gateway transport still paid limiter delay: %s", elapsed)
	}
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected 2 HTTP calls through gateway transport, got %d", got)
	}
}

func TestClientWithNilGatewayTransportKeepsPerpSDKLocalLimiter(t *testing.T) {
	client := NewClient().WithGatewayTransport(nil)
	if client.RateLimiter == nil {
		t.Fatal("nil gateway transport disabled the SDK-local limiter")
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
