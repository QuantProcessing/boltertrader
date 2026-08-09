package ratelimit

import (
	"context"
	"errors"
	"sort"
	"strings"
	"sync"
	"time"
)

var ErrInvalidBudget = errors.New("invalid gateway rate-limit budget")

type KeyKind string

const (
	VenueProfile           KeyKind = "venue_profile"
	AuthenticatedPrincipal KeyKind = "authenticated_principal"
	RouteEndpoint          KeyKind = "route_endpoint"
)

type Key struct {
	Kind  KeyKind
	Value string
}

func (key Key) validate() error {
	switch key.Kind {
	case VenueProfile, AuthenticatedPrincipal, RouteEndpoint:
	default:
		return ErrInvalidBudget
	}
	if key.Value == "" || key.Value != strings.TrimSpace(key.Value) || len(key.Value) > 256 || strings.ContainsAny(key.Value, "\n\r\t") {
		return ErrInvalidBudget
	}
	return nil
}

type Quota struct {
	Limit    int
	Interval time.Duration
}

type window struct {
	quota Quota
	start time.Time
	used  int
}

type Coordinator struct {
	mu      sync.Mutex
	now     func() time.Time
	windows map[Key]*window
}

func NewCoordinator(now func() time.Time) *Coordinator {
	if now == nil {
		now = time.Now
	}
	return &Coordinator{now: now, windows: make(map[Key]*window)}
}

func (coordinator *Coordinator) Configure(key Key, quota Quota) error {
	if err := key.validate(); err != nil || quota.Limit <= 0 || quota.Interval <= 0 {
		return ErrInvalidBudget
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	coordinator.windows[key] = &window{quota: quota, start: coordinator.now()}
	return nil
}

func (coordinator *Coordinator) Acquire(ctx context.Context, keys []Key) error {
	if ctx == nil {
		return context.Canceled
	}
	ordered, err := normalizeKeys(keys)
	if err != nil {
		return err
	}
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		now := coordinator.now()
		coordinator.mu.Lock()
		wait := time.Duration(0)
		for _, key := range ordered {
			current, configured := coordinator.windows[key]
			if !configured {
				continue
			}
			if !now.Before(current.start.Add(current.quota.Interval)) {
				current.start = now
				current.used = 0
			}
			if current.used >= current.quota.Limit {
				delay := current.start.Add(current.quota.Interval).Sub(now)
				if delay > wait {
					wait = delay
				}
			}
		}
		if wait == 0 {
			for _, key := range ordered {
				if current := coordinator.windows[key]; current != nil {
					current.used++
				}
			}
			coordinator.mu.Unlock()
			return nil
		}
		coordinator.mu.Unlock()
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

func (coordinator *Coordinator) Used(key Key) int {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	current := coordinator.windows[key]
	if current == nil {
		return 0
	}
	now := coordinator.now()
	if !now.Before(current.start.Add(current.quota.Interval)) {
		return 0
	}
	return current.used
}

func normalizeKeys(keys []Key) ([]Key, error) {
	if len(keys) == 0 {
		return nil, ErrInvalidBudget
	}
	result := append([]Key(nil), keys...)
	for _, key := range result {
		if err := key.validate(); err != nil {
			return nil, err
		}
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].Kind == result[j].Kind {
			return result[i].Value < result[j].Value
		}
		return result[i].Kind < result[j].Kind
	})
	for index := 1; index < len(result); index++ {
		if result[index] == result[index-1] {
			return nil, ErrInvalidBudget
		}
	}
	return result, nil
}
