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

// MarketCoordinator is the process-wide public REST gate. Every catalog,
// broad-feed bootstrap and FreshBBO fallback shares its venue budget and
// collapse group instead of creating a subsystem-local limiter.
type MarketCoordinator struct {
	budget   *Coordinator
	observer MarketObserver
	now      func() time.Time
	group    singleflight.Group

	mu         sync.Mutex
	configured map[Key]struct{}
	cooldown   map[string]time.Time
}

func NewMarketCoordinator(observer MarketObserver, now func() time.Time) *MarketCoordinator {
	if now == nil {
		now = time.Now
	}
	return &MarketCoordinator{budget: NewCoordinator(now), observer: observer, now: now, configured: make(map[Key]struct{}), cooldown: make(map[string]time.Time)}
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
		keys := []Key{{Kind: VenueProfile, Value: string(venue) + "/public"}, {Kind: RouteEndpoint, Value: string(venue) + "/" + operation}}
		coordinator.ensure(keys[0], Quota{Limit: 1200, Interval: time.Minute})
		coordinator.ensure(keys[1], Quota{Limit: 600, Interval: time.Minute})
		if acquireErr := coordinator.budget.Acquire(ctx, keys); acquireErr != nil {
			coordinator.observe(venue, operation, "timeout")
			return nil, acquireErr
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

func (coordinator *MarketCoordinator) ensure(key Key, quota Quota) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if _, exists := coordinator.configured[key]; exists {
		return
	}
	_ = coordinator.budget.Configure(key, quota)
	coordinator.configured[key] = struct{}{}
}

func (coordinator *MarketCoordinator) applyRetryAfter(venue exchange.Venue, operation string, err error) {
	delay := time.Second
	var venueErr *exchange.Error
	if errors.As(err, &venueErr) && venueErr.Details().RetryAfter > 0 {
		delay = venueErr.Details().RetryAfter
	}
	coordinator.mu.Lock()
	coordinator.cooldown[string(venue)+"|"+operation] = coordinator.now().UTC().Add(delay)
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
