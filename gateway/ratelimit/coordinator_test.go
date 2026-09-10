package ratelimit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestWeightedBudgetsAreAtomicAndConfigurationDoesNotResetUsage(t *testing.T) {
	now := time.Unix(100, 0)
	c := NewCoordinator(func() time.Time { return now })
	venue := Key{VenueBudget, "binance"}
	account := Key{EndpointBudget, "account"}
	if err := c.Configure(venue, Quota{10, time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err := c.Configure(account, Quota{4, time.Minute}); err != nil {
		t.Fatal(err)
	}
	charges := []Charge{{Key: venue, Cost: 3}, {Key: account, Cost: 3}}
	if delay, err := c.TryAcquire(context.Background(), charges); delay != 0 || err != nil {
		t.Fatalf("delay=%s err=%v", delay, err)
	}
	if err := c.Configure(account, Quota{4, time.Minute}); err != nil {
		t.Fatal(err)
	}
	if delay, err := c.TryAcquire(context.Background(), charges); delay != time.Minute || err != nil {
		t.Fatalf("delay=%s err=%v", delay, err)
	}
	if c.Used(venue) != 3 || c.Used(account) != 3 {
		t.Fatal("partial charge or configuration reset")
	}
	if err := c.Configure(account, Quota{5, time.Minute}); !errors.Is(err, ErrInvalidBudget) {
		t.Fatal(err)
	}
	now = now.Add(time.Minute)
	if delay, err := c.TryAcquire(context.Background(), charges); delay != 0 || err != nil {
		t.Fatalf("delay=%s err=%v", delay, err)
	}
}

func TestBudgetInvalidVectorsAndCanceledCallsDoNotConsume(t *testing.T) {
	c := NewCoordinator(nil)
	key := Key{VenueBudget, "binance"}
	if err := c.Configure(key, Quota{10, time.Minute}); err != nil {
		t.Fatal(err)
	}
	for _, charges := range [][]Charge{nil, {{Key: key, Cost: 1}, {Key: key, Cost: 1}}, {{Key: key, Cost: 0}}, {{Key: key, Cost: 11}}, {{Key: key, Cost: 1, Reserve: 10}}, {{Key: Key{VenueBudget, "unconfigured"}, Cost: 1}}} {
		if _, err := c.TryAcquire(context.Background(), charges); !errors.Is(err, ErrInvalidBudget) {
			t.Fatalf("vector=%+v err=%v", charges, err)
		}
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.TryAcquire(ctx, []Charge{{Key: key, Cost: 1}}); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	if c.Used(key) != 0 {
		t.Fatal("invalid/canceled request consumed budget")
	}
}
