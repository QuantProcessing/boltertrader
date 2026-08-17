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
	source := &fakeSource{fact: Fact{
		Venue: "binance", Environment: "live", Product: "perp", InstrumentID: "btc-usdt", MarkPrice: "100.00", ObservedAt: now,
		BestBid: "99.99", BestBidSize: "1", BestAsk: "100.01", BestAskSize: "2",
		Bids: []Level{{Price: "99.99", Quantity: "1"}}, Asks: []Level{{Price: "100.01", Quantity: "2"}},
	}}
	service, err := NewService(source, budget, time.Second, clock)
	if err != nil {
		t.Fatal(err)
	}
	query := Query{Venue: "binance", Environment: "live", Product: "perp", InstrumentID: "btc-usdt"}

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

func TestServicePreservesExactDepthAndFundingFacts(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	fundingAt := now.Add(-time.Hour)
	nextFundingAt := now.Add(7 * time.Hour)
	fact := Fact{
		Venue: "binance", Environment: "live", Product: "perp", InstrumentID: "btc-usdt", BestBid: "100.1", BestBidSize: "2.5", BestAsk: "100.2", BestAskSize: "3.25",
		Bids: []Level{{Price: "100.1", Quantity: "2.5"}, {Price: "100", Quantity: "4"}},
		Asks: []Level{{Price: "100.2", Quantity: "3.25"}, {Price: "100.3", Quantity: "5"}}, ObservedAt: now,
		FundingRate: "0.0001", FundingObservedAt: now.Add(-time.Minute), FundingTime: fundingAt, NextFundingTime: nextFundingAt,
		FundingHistory: []FundingPoint{{Rate: "0.0002", ObservedAt: now.Add(-9 * time.Hour), FundingTime: now.Add(-8 * time.Hour)}},
	}
	budget, _ := NewCoordinator(1, time.Minute, func() time.Time { return now })
	service, _ := NewService(&fakeSource{fact: fact}, budget, time.Minute, func() time.Time { return now })
	got, err := service.Get(context.Background(), Query{Venue: "binance", Environment: "live", Product: "perp", InstrumentID: "btc-usdt"})
	if err != nil || got.BestBidSize != "2.5" || len(got.Bids) != 2 || got.Bids[0].Quantity != "2.5" || !got.NextFundingTime.Equal(nextFundingAt) || len(got.FundingHistory) != 1 {
		t.Fatalf("Get() = %#v, %v", got, err)
	}
}

func TestServiceFailsClosedForMissingStaleOrUnboundedFacts(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	valid := Fact{Venue: "binance", Environment: "live", Product: "perp", InstrumentID: "btc-usdt", BestBid: "100", BestBidSize: "1", BestAsk: "101", BestAskSize: "2", Bids: []Level{{Price: "100", Quantity: "1"}}, Asks: []Level{{Price: "101", Quantity: "2"}}, ObservedAt: now}
	for name, mutate := range map[string]func(*Fact){
		"missing observed time":     func(fact *Fact) { fact.ObservedAt = time.Time{} },
		"stale observed time":       func(fact *Fact) { fact.ObservedAt = now.Add(-2 * time.Minute) },
		"missing bid size":          func(fact *Fact) { fact.BestBidSize = "" },
		"empty asks":                func(fact *Fact) { fact.Asks = nil },
		"invalid quantity":          func(fact *Fact) { fact.Bids[0].Quantity = "0" },
		"unbounded depth":           func(fact *Fact) { fact.Bids = make([]Level, MaxDepthLevels+1) },
		"unbounded funding history": func(fact *Fact) { fact.FundingHistory = make([]FundingPoint, MaxFundingHistory+1) },
	} {
		t.Run(name, func(t *testing.T) {
			fact := valid
			fact.Bids = append([]Level(nil), valid.Bids...)
			fact.Asks = append([]Level(nil), valid.Asks...)
			mutate(&fact)
			budget, _ := NewCoordinator(1, time.Minute, func() time.Time { return now })
			service, _ := NewService(&fakeSource{fact: fact}, budget, time.Minute, func() time.Time { return now })
			if _, err := service.Get(context.Background(), Query{Venue: "binance", Environment: "live", Product: "perp", InstrumentID: "btc-usdt"}); err == nil {
				t.Fatal("Get() error = nil")
			}
		})
	}
}

func TestServiceFailsClosedOnIncompleteOrMismatchedCompositeIdentity(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	valid := Fact{Venue: "binance", Environment: "live", Product: "perp", InstrumentID: "btc-usdt", BestBid: "100", BestBidSize: "1", BestAsk: "101", BestAskSize: "2", Bids: []Level{{Price: "100", Quantity: "1"}}, Asks: []Level{{Price: "101", Quantity: "2"}}, ObservedAt: now}
	// demo is named on purpose. It was a real environment until the platform
	// collapsed to live alone, so a client still asking for it is exactly the
	// stale caller this guard exists to refuse — answering it with live data
	// under a name it did not request is the failure being pinned here.
	for name, query := range map[string]Query{
		"missing environment":    {Venue: "binance", Product: "perp", InstrumentID: "btc-usdt"},
		"missing product":        {Venue: "binance", Environment: "live", InstrumentID: "btc-usdt"},
		"mismatched environment": {Venue: "binance", Environment: "demo", Product: "perp", InstrumentID: "btc-usdt"},
		"mismatched product":     {Venue: "binance", Environment: "live", Product: "spot", InstrumentID: "btc-usdt"},
	} {
		t.Run(name, func(t *testing.T) {
			budget, _ := NewCoordinator(1, time.Minute, func() time.Time { return now })
			service, _ := NewService(&fakeSource{fact: valid}, budget, time.Minute, func() time.Time { return now })
			if _, err := service.Get(context.Background(), query); err == nil {
				t.Fatal("Get() error = nil")
			}
		})
	}
}
