package ratelimit

import (
	"context"
	"errors"
	"strings"
	"sync"
	"time"
)

var ErrInvalidBudget = errors.New("invalid gateway rate-limit budget")

type KeyKind string

const (
	VenueBudget    KeyKind = "venue"
	EndpointBudget KeyKind = "endpoint"
)

type Key struct {
	Kind  KeyKind
	Value string
}

func (key Key) validate() error {
	switch key.Kind {
	case VenueBudget, EndpointBudget:
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
	quota        Quota
	start        time.Time
	used         int
	blockedUntil time.Time
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
	if existing := coordinator.windows[key]; existing != nil {
		if existing.quota != quota {
			return ErrInvalidBudget
		}
		return nil
	}
	coordinator.windows[key] = &window{quota: quota, start: coordinator.now()}
	return nil
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

// Charge reserves weighted capacity atomically with the other dimensions.
// Reserve leaves headroom for more urgent callers sharing the same quota.
type Charge struct {
	Key     Key
	Cost    int
	Reserve int
}

func (coordinator *Coordinator) TryAcquire(ctx context.Context, charges []Charge) (time.Duration, error) {
	if ctx == nil {
		return 0, context.Canceled
	}
	if err := ctx.Err(); err != nil {
		return 0, err
	}
	if len(charges) == 0 {
		return 0, ErrInvalidBudget
	}
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	seen := make(map[Key]bool, len(charges))
	now := coordinator.now()
	var delay time.Duration
	for _, charge := range charges {
		current := coordinator.windows[charge.Key]
		if current == nil || seen[charge.Key] || charge.Cost <= 0 || charge.Reserve < 0 || charge.Cost > current.quota.Limit-charge.Reserve {
			return 0, ErrInvalidBudget
		}
		seen[charge.Key] = true
		if !now.Before(current.start.Add(current.quota.Interval)) {
			current.start = now
			current.used = 0
		}
		if current.used+charge.Cost > current.quota.Limit-charge.Reserve {
			delay = max(delay, current.start.Add(current.quota.Interval).Sub(now))
		}
		delay = max(delay, current.blockedUntil.Sub(now))
	}
	if delay > 0 {
		return delay, nil
	}
	for _, charge := range charges {
		coordinator.windows[charge.Key].used += charge.Cost
	}
	return 0, nil
}
func (coordinator *Coordinator) Block(key Key, until time.Time) {
	coordinator.mu.Lock()
	defer coordinator.mu.Unlock()
	if current := coordinator.windows[key]; current != nil && until.After(current.blockedUntil) {
		current.blockedUntil = until
	}
}
