package ratelimit

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

func TestMarketCoordinatorSingleflightsSameVenueOperationAndRequest(t *testing.T) {
	coordinator := NewMarketCoordinator(nil, time.Now)
	var calls atomic.Int32
	start := make(chan struct{})
	call := func(context.Context) (any, error) {
		calls.Add(1)
		<-start
		return "ok", nil
	}
	results := make(chan error, 2)
	for range 2 {
		go func() {
			_, err := coordinator.Do(t.Context(), exchange.VenueBybit, "linear_tickers", "linear", call)
			results <- err
		}()
	}
	deadline := time.Now().Add(time.Second)
	for calls.Load() == 0 && time.Now().Before(deadline) {
		time.Sleep(time.Millisecond)
	}
	close(start)
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	if err := <-results; err != nil {
		t.Fatal(err)
	}
	if calls.Load() != 1 {
		t.Fatalf("calls=%d want=1", calls.Load())
	}
}

func TestMarketCoordinatorHonorsRetryAfter(t *testing.T) {
	coordinator := NewMarketCoordinator(nil, time.Now)
	_, err := coordinator.Do(t.Context(), exchange.VenueBinance, "open_interest", "first", func(context.Context) (any, error) {
		return nil, exchange.NewError(exchange.KindRateLimit, exchange.ErrorDetails{RetryAfter: 50 * time.Millisecond})
	})
	if err == nil {
		t.Fatal("rate limit was swallowed")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 5*time.Millisecond)
	defer cancel()
	if _, err := coordinator.Do(ctx, exchange.VenueBinance, "open_interest", "second", func(context.Context) (any, error) { return nil, nil }); err == nil {
		t.Fatal("retry-after cooldown was ignored")
	}
}
