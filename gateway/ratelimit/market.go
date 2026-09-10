package ratelimit

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"golang.org/x/sync/singleflight"
)

type MarketObserver interface {
	RecordMarketREST(venue, operation, outcome string)
}

// MarketCoordinator coalesces public reads and observes application-level
// cooldowns. Actual weighted request budgets belong to the shared HTTP transport.
type MarketCoordinator struct {
	observer MarketObserver
	now      func() time.Time
	group    singleflight.Group

	mu       sync.Mutex
	cooldown map[string]time.Time
}

func NewMarketCoordinator(observer MarketObserver, now func() time.Time) *MarketCoordinator {
	if now == nil {
		now = time.Now
	}
	return &MarketCoordinator{observer: observer, now: now, cooldown: make(map[string]time.Time)}
}

func (coordinator *MarketCoordinator) Do(ctx context.Context, venue exchange.Venue, operation, collapseKey string, call func(context.Context) (any, error)) (any, error) {
	operation, collapseKey = strings.TrimSpace(operation), strings.TrimSpace(collapseKey)
	if coordinator == nil || ctx == nil || ctx.Err() != nil || venue == "" || operation == "" || collapseKey == "" || call == nil {
		return nil, fmt.Errorf("market request is invalid")
	}
	key := string(venue) + "|" + operation + "|" + collapseKey
	value, err, _ := coordinator.group.Do(key, func() (any, error) {
		if waitErr := coordinator.waitCooldown(ctx, venue, operation); waitErr != nil {
			coordinator.observe(venue, operation, "timeout")
			return nil, waitErr
		}
		result, callErr := call(ctx)
		outcome := "success"
		if callErr != nil {
			switch {
			case errors.Is(callErr, exchange.ErrRateLimit):
				outcome = "rate_limited"
				coordinator.applyRetryAfter(venue, operation, callErr)
			case errors.Is(callErr, context.DeadlineExceeded), errors.Is(ctx.Err(), context.DeadlineExceeded):
				outcome = "timeout"
			default:
				outcome = "error"
			}
		}
		coordinator.observe(venue, operation, outcome)
		return result, callErr
	})
	return value, err
}

func (coordinator *MarketCoordinator) applyRetryAfter(venue exchange.Venue, operation string, err error) {
	delay := time.Second
	var venueErr *exchange.Error
	if errors.As(err, &venueErr) && venueErr.Details().RetryAfter > 0 {
		delay = venueErr.Details().RetryAfter
	}
	coordinator.mu.Lock()
	key := string(venue) + "|" + operation
	until := coordinator.now().UTC().Add(delay)
	if until.After(coordinator.cooldown[key]) {
		coordinator.cooldown[key] = until
	}
	coordinator.mu.Unlock()
}

func (coordinator *MarketCoordinator) waitCooldown(ctx context.Context, venue exchange.Venue, operation string) error {
	coordinator.mu.Lock()
	until := coordinator.cooldown[string(venue)+"|"+operation]
	coordinator.mu.Unlock()
	wait := until.Sub(coordinator.now().UTC())
	if wait <= 0 {
		return nil
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func (coordinator *MarketCoordinator) observe(venue exchange.Venue, operation, outcome string) {
	if coordinator.observer != nil {
		coordinator.observer.RecordMarketREST(string(venue), operation, outcome)
	}
}
