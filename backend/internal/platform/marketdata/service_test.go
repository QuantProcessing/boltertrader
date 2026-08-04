package marketdata

import (
	"context"
	"sync"
	"testing"
	"time"
)

type fakeSource struct {
	mu    sync.Mutex
	calls int
	fact  Fact
}

func (source *fakeSource) Fetch(context.Context, Query) (Fact, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.calls++
	return source.fact, nil
}

func TestServiceCoalescesConsumersAndUsesSharedBudget(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	clock := func() time.Time { return now }
	budget, err := NewCoordinator(1, time.Minute, clock)
	if err != nil {
		t.Fatal(err)
	}
	source := &fakeSource{fact: Fact{Venue: "binance", InstrumentID: "btc-usdt", MarkPrice: "100.00", ObservedAt: now}}
	service, err := NewService(source, budget, time.Second, clock)
	if err != nil {
		t.Fatal(err)
	}
	query := Query{Venue: "binance", InstrumentID: "btc-usdt"}

	const consumers = 10
	var wait sync.WaitGroup
	wait.Add(consumers)
	for range consumers {
		go func() {
			defer wait.Done()
			fact, fetchErr := service.Get(context.Background(), query)
			if fetchErr != nil || fact.MarkPrice != "100.00" {
				t.Errorf("Get() = %#v, %v", fact, fetchErr)
			}
		}()
	}
	wait.Wait()
	if source.calls != 1 {
		t.Fatalf("source calls = %d, want 1", source.calls)
	}
	if state := budget.Status("binance"); state.Used != 1 || state.Remaining != 0 {
		t.Fatalf("budget Status() = %#v", state)
	}
}

func TestCoordinatorFailsFastWhenVenueBudgetIsExhausted(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	budget, err := NewCoordinator(1, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if err := budget.Acquire(context.Background(), "binance"); err != nil {
		t.Fatal(err)
	}
	if err := budget.Acquire(context.Background(), "binance"); err != ErrRateLimited {
		t.Fatalf("Acquire() error = %v, want ErrRateLimited", err)
	}
}
