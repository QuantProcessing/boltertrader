package spot

import (
	"context"
	"strings"
	"sync"
	"time"
)

const binanceSpotRateLimiterGlobalKey = "binance:global"

type RateLimitQuota struct {
	Limit    int
	Interval time.Duration
}

type KeyedRateLimiter struct {
	mu      sync.Mutex
	quotas  map[string]RateLimitQuota
	windows map[string][]time.Time
}

func NewKeyedRateLimiter(quotas map[string]RateLimitQuota) *KeyedRateLimiter {
	copied := make(map[string]RateLimitQuota, len(quotas))
	for key, quota := range quotas {
		copied[key] = quota
	}
	return &KeyedRateLimiter{
		quotas:  copied,
		windows: make(map[string][]time.Time, len(copied)),
	}
}

func NewBinanceSpotRateLimiter() *KeyedRateLimiter {
	return NewKeyedRateLimiter(map[string]RateLimitQuota{
		binanceSpotRateLimiterGlobalKey:      {Limit: 6000, Interval: time.Minute},
		"binance:api/v3/order":               {Limit: 3000, Interval: time.Minute},
		"binance:api/v3/allOrders":           {Limit: 150, Interval: time.Minute},
		"binance:api/v3/klines":              {Limit: 600, Interval: time.Minute},
		"binance:api/v3/depth":               {Limit: 6000, Interval: time.Minute},
		"binance:api/v3/ticker/24hr":         {Limit: 6000, Interval: time.Minute},
		"binance:api/v3/ticker/bookTicker":   {Limit: 6000, Interval: time.Minute},
		"binance:api/v3/exchangeInfo":        {Limit: 6000, Interval: time.Minute},
		"binance:api/v3/account":             {Limit: 6000, Interval: time.Minute},
		"binance:api/v3/openOrders":          {Limit: 3000, Interval: time.Minute},
		"binance:api/v3/myTrades":            {Limit: 300, Interval: time.Minute},
		"binance:api/v3/userDataStream":      {Limit: 6000, Interval: time.Minute},
		"binance:api/v3/order/cancelReplace": {Limit: 3000, Interval: time.Minute},
	})
}

func (l *KeyedRateLimiter) Quota(key string) (RateLimitQuota, bool) {
	if l == nil {
		return RateLimitQuota{}, false
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	quota, ok := l.quotas[key]
	return quota, ok
}

func (l *KeyedRateLimiter) Wait(ctx context.Context, keys []string) error {
	if l == nil {
		return nil
	}
	for _, key := range dedupeRateLimitKeys(keys) {
		if err := l.waitKey(ctx, key); err != nil {
			return err
		}
	}
	return nil
}

func (l *KeyedRateLimiter) waitKey(ctx context.Context, key string) error {
	for {
		wait, ok := l.reserveOrDelay(key, time.Now())
		if !ok || wait <= 0 {
			return nil
		}
		timer := time.NewTimer(wait)
		select {
		case <-ctx.Done():
			if !timer.Stop() {
				<-timer.C
			}
			return ctx.Err()
		case <-timer.C:
		}
	}
}

func (l *KeyedRateLimiter) reserveOrDelay(key string, now time.Time) (time.Duration, bool) {
	l.mu.Lock()
	defer l.mu.Unlock()

	quota, ok := l.quotas[key]
	if !ok || quota.Limit <= 0 || quota.Interval <= 0 {
		return 0, false
	}

	window := l.windows[key]
	cutoff := now.Add(-quota.Interval)
	keepFrom := 0
	for keepFrom < len(window) && !window[keepFrom].After(cutoff) {
		keepFrom++
	}
	if keepFrom > 0 {
		copy(window, window[keepFrom:])
		window = window[:len(window)-keepFrom]
	}

	if len(window) < quota.Limit {
		window = append(window, now)
		l.windows[key] = window
		return 0, true
	}

	l.windows[key] = window
	return window[0].Add(quota.Interval).Sub(now), true
}

func dedupeRateLimitKeys(keys []string) []string {
	if len(keys) == 0 {
		return nil
	}
	out := make([]string, 0, len(keys))
	seen := make(map[string]struct{}, len(keys))
	for _, key := range keys {
		if key == "" {
			continue
		}
		if _, ok := seen[key]; ok {
			continue
		}
		seen[key] = struct{}{}
		out = append(out, key)
	}
	return out
}

func binanceSpotRateLimitKey(endpoint string) string {
	return "binance:" + strings.TrimPrefix(endpoint, "/")
}
