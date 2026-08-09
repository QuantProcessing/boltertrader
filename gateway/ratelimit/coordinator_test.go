package ratelimit

import (
	"context"
	"errors"
	"testing"
	"time"
)

func TestCoordinatorAtomicallyReservesHierarchicalBudgets(t *testing.T) {
	now := time.Unix(100, 0)
	coordinator := NewCoordinator(func() time.Time { return now })
	profile := Key{Kind: VenueProfile, Value: "binance/test/binance.perp.usdt.demo.v1"}
	principal := Key{Kind: AuthenticatedPrincipal, Value: "binance/principal-fingerprint"}
	if err := coordinator.Configure(profile, Quota{Limit: 2, Interval: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Configure(principal, Quota{Limit: 1, Interval: time.Minute}); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Acquire(context.Background(), []Key{principal, profile}); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Millisecond)
	defer cancel()
	if err := coordinator.Acquire(ctx, []Key{profile, principal}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err=%v", err)
	}
	if used := coordinator.Used(profile); used != 1 {
		t.Fatalf("profile partially consumed while principal blocked: %d", used)
	}
	now = now.Add(time.Minute)
	if err := coordinator.Acquire(context.Background(), []Key{profile, principal}); err != nil {
		t.Fatal(err)
	}
}

func TestCoordinatorRejectsDuplicateAndInvalidKeys(t *testing.T) {
	coordinator := NewCoordinator(time.Now)
	key := Key{Kind: VenueProfile, Value: "binance/test/profile"}
	if err := coordinator.Configure(key, Quota{Limit: 1, Interval: time.Second}); err != nil {
		t.Fatal(err)
	}
	if err := coordinator.Acquire(context.Background(), []Key{key, key}); err == nil {
		t.Fatal("duplicate budget vector accepted")
	}
}
