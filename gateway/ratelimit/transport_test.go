package ratelimit

import (
	"context"
	"net/http"
	"reflect"
	"testing"
	"time"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) { return fn(request) }

type vectorProviderFunc func(*http.Request, RequestScope) []Key

func (fn vectorProviderFunc) Keys(request *http.Request, scope RequestScope) []Key {
	return fn(request, scope)
}

func TestRequestVectorsShareVenueBudgetAndIsolateAuthenticatedPrincipals(t *testing.T) {
	profile := Key{Kind: VenueProfile, Value: "binance/test"}
	endpoint := func(request *http.Request) (Key, bool) {
		return Key{Kind: RouteEndpoint, Value: request.URL.Path}, true
	}
	one, err := NewRequestVectorProvider(profile, []byte("account-one"), endpoint)
	if err != nil {
		t.Fatal(err)
	}
	two, _ := NewRequestVectorProvider(profile, []byte("account-two"), endpoint)
	request, _ := http.NewRequest(http.MethodGet, "https://example.invalid/api/v3/order", nil)
	oneKeys, twoKeys := one.Keys(request, PrivateScope), two.Keys(request, PrivateScope)
	if oneKeys[0] != twoKeys[0] || oneKeys[1] == twoKeys[1] || oneKeys[2] != twoKeys[2] {
		t.Fatalf("one=%+v two=%+v", oneKeys, twoKeys)
	}
	fallback, _ := NewRequestVectorProvider(profile, nil, endpoint)
	if got := fallback.Keys(request, PrivateScope); !reflect.DeepEqual(got, []Key{profile, {Kind: RouteEndpoint, Value: "/api/v3/order"}}) {
		t.Fatalf("fallback keys=%+v", got)
	}
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
