package ratelimit

import (
	"context"
	"net/http"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

type vectorProviderFunc func(*http.Request, RequestScope) []Key

func (fn vectorProviderFunc) Keys(request *http.Request, scope RequestScope) []Key {
	return fn(request, scope)
}

func TestTransportAcquiresAdapterVectorBeforeNetwork(t *testing.T) {
	coordinator := NewCoordinator(time.Now)
	key := Key{Kind: VenueProfile, Value: "binance/test/profile"}
	if err := coordinator.Configure(key, Quota{Limit: 1, Interval: time.Hour}); err != nil {
		t.Fatal(err)
	}
	calls := 0
	transport, err := NewTransport(coordinator, vectorProviderFunc(func(*http.Request, RequestScope) []Key { return []Key{key} }), PrivateScope, roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: http.StatusNoContent, Body: http.NoBody}, nil
	}))
	if err != nil {
		t.Fatal(err)
	}
	request, _ := http.NewRequestWithContext(context.Background(), http.MethodGet, "https://example.invalid", nil)
	if _, err := transport.RoundTrip(request); err != nil || calls != 1 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	canceled, cancel := context.WithCancel(context.Background())
	cancel()
	request, _ = http.NewRequestWithContext(canceled, http.MethodGet, "https://example.invalid", nil)
	if _, err := transport.RoundTrip(request); err == nil || calls != 1 {
		t.Fatalf("canceled calls=%d err=%v", calls, err)
	}
}
