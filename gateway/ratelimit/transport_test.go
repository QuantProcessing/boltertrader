package ratelimit

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/venuehttp"
)

type roundTripFunc func(*http.Request) (*http.Response, error)

func (fn roundTripFunc) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }
func budgetClient(t *testing.T, c *Coordinator, next http.RoundTripper) *http.Client {
	t.Helper()
	tr, err := NewTransport(c, next)
	if err != nil {
		t.Fatal(err)
	}
	return &http.Client{Transport: tr}
}
func budgetRequest(client *http.Client, venue, method, path, body string) error {
	req, _ := http.NewRequestWithContext(context.Background(), method, "https://example.invalid"+path, strings.NewReader(body))
	resp, err := venuehttp.DoHTTP(client, req, venue)
	if resp != nil {
		resp.Body.Close()
	}
	return err
}
func TestActualWeightedRequestsShareBudgetAndLeaveCancellationHeadroom(t *testing.T) {
	c := NewCoordinator(nil)
	calls := 0
	client := budgetClient(t, c, roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	}))
	account := WithPrincipal(client, "stable-account")
	// Each 5000-level spot depth costs 250 units. Separate clients share egress.
	for _, caller := range []*http.Client{client, account, WithPrincipal(client, "stable-account")} {
		if err := budgetRequest(caller, "binance", "GET", "/api/v3/depth?limit=5000&symbol=BTCUSDT", ""); err != nil {
			t.Fatal(err)
		}
	}
	err := budgetRequest(client, "binance", "GET", "/api/v3/depth?limit=5000", "")
	var local *venuehttp.LocalRateLimitError
	if !errors.As(err, &local) || local.RetryAfter <= 0 || local.Action != "read" || calls != 3 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	// Exhaust ordinary capacity without filling the separate cancel endpoint.
	key := Key{VenueBudget, "binance/egress"}
	if delay, err := c.TryAcquire(context.Background(), []Charge{{Key: key, Cost: 210}}); delay != 0 || err != nil {
		t.Fatal(delay, err)
	}
	err = budgetRequest(account, "binance", "POST", "/api/v3/order", "")
	if !errors.As(err, &local) || local.Action != "place" || calls != 3 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
	if err := budgetRequest(account, "binance", "DELETE", "/api/v3/order", ""); err != nil || calls != 4 {
		t.Fatalf("calls=%d err=%v", calls, err)
	}
}
func TestVenueRetryAfterBlocksEveryClientAndCannotBeShortened(t *testing.T) {
	now := time.Unix(100, 0)
	c := NewCoordinator(func() time.Time { return now })
	calls := 0
	client := budgetClient(t, c, roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 429, Header: http.Header{"Retry-After": []string{"20"}}, Body: http.NoBody}, nil
	}))
	if err := budgetRequest(client, "bybit", "GET", "/v5/market/time", ""); err != nil {
		t.Fatal(err)
	}
	c.Block(Key{VenueBudget, "bybit/egress"}, now.Add(time.Second))
	err := budgetRequest(WithPrincipal(client, "account"), "bybit", "POST", "/v5/order/cancel", `{}`)
	var local *venuehttp.LocalRateLimitError
	if !errors.As(err, &local) || local.RetryAfter != 20*time.Second || calls != 1 {
		t.Fatalf("calls=%d local=%+v err=%v", calls, local, err)
	}
	now = now.Add(20 * time.Second)
	if err := budgetRequest(client, "bybit", "GET", "/v5/market/time", ""); err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
}
func TestBybitHTTP200ResetBlocksSameAccountEndpointAcrossRebuild(t *testing.T) {
	now := time.Unix(100, 0)
	c := NewCoordinator(func() time.Time { return now })
	calls := 0
	client := budgetClient(t, c, roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Header: http.Header{"X-Bapi-Limit-Status": []string{"0"}, "X-Bapi-Limit-Reset-Timestamp": []string{strconv.FormatInt(now.Add(5*time.Second).UnixMilli(), 10)}}, Body: http.NoBody}, nil
	}))
	if err := budgetRequest(WithPrincipal(client, "account"), "bybit", "GET", "/v5/order/realtime", ""); err != nil {
		t.Fatal(err)
	}
	err := budgetRequest(WithPrincipal(client, "account"), "bybit", "GET", "/v5/order/realtime", "")
	var local *venuehttp.LocalRateLimitError
	if !errors.As(err, &local) || local.RetryAfter != 5*time.Second || calls != 1 {
		t.Fatal(err, calls)
	}
	if err := budgetRequest(WithPrincipal(client, "other-account"), "bybit", "GET", "/v5/order/realtime", ""); err != nil || calls != 2 {
		t.Fatal(err, calls)
	}
}
func TestConcurrentClientsCannotExceedEndpointBudget(t *testing.T) {
	now := time.Unix(100, 0)
	c := NewCoordinator(func() time.Time { return now })
	var calls atomic.Int64
	client := budgetClient(t, c, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		calls.Add(1)
		body, _ := io.ReadAll(r.Body)
		if string(body) != `{"category":"linear"}` {
			t.Errorf("body changed: %s", body)
		}
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	}))
	var wg sync.WaitGroup
	for i := 0; i < 40; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			err := budgetRequest(WithPrincipal(client, "one"), "bybit", "POST", "/v5/order/create", `{"category":"linear"}`)
			if err != nil && !errors.Is(err, venuehttp.ErrRateLimited) {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	if calls.Load() != 10 {
		t.Fatal(calls.Load())
	}
}
func TestMissingPolicyAndCanceledRequestNeverReachNetwork(t *testing.T) {
	c := NewCoordinator(nil)
	calls := 0
	client := budgetClient(t, c, roundTripFunc(func(*http.Request) (*http.Response, error) {
		calls++
		return &http.Response{StatusCode: 200, Body: http.NoBody}, nil
	}))
	req, _ := http.NewRequest("GET", "https://example.invalid/time", nil)
	if _, err := client.Do(req); err == nil {
		t.Fatal("missing SDK policy passed")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	req = req.WithContext(ctx)
	if _, err := venuehttp.DoHTTP(client, req, "binance"); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal(calls)
	}
}
