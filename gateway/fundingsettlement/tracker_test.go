package fundingsettlement

import (
	"context"
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

type memoryStore struct {
	records map[string]Record
	rates   []SettledRate
}

func (store *memoryStore) LoadTracking(context.Context) ([]Record, error) {
	result := make([]Record, 0, len(store.records))
	for _, record := range store.records {
		result = append(result, record)
	}
	return result, nil
}
func (store *memoryStore) SaveTracking(_ context.Context, record Record) error {
	if store.records == nil {
		store.records = map[string]Record{}
	}
	store.records[record.InstrumentID] = record
	return nil
}
func (store *memoryStore) DeleteTracking(_ context.Context, instrumentID string) error {
	delete(store.records, instrumentID)
	return nil
}
func (store *memoryStore) UpsertRate(_ context.Context, rate SettledRate) error {
	store.rates = append(store.rates, rate)
	return nil
}

type historyFunc func(context.Context, string, time.Time, time.Time) ([]Point, error)

func (function historyFunc) Settlements(ctx context.Context, instrumentID string, since, until time.Time) ([]Point, error) {
	return function(ctx, instrumentID, since, until)
}

func TestTrackerRetainsInactiveInstrumentUntilNextConfirmedSettlement(t *testing.T) {
	now := time.Date(2026, 8, 25, 8, 1, 31, 0, time.UTC)
	target := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	store := &memoryStore{records: map[string]Record{}}
	tracker, err := New(Options{Store: store, Now: func() time.Time { return now }, History: historyFunc(func(context.Context, string, time.Time, time.Time) ([]Point, error) {
		return []Point{{FundingTime: target, Rate: decimal.RequireFromString("0.0001"), IntervalHours: 8}}, nil
	})})
	if err != nil {
		t.Fatal(err)
	}
	if err := tracker.Track(t.Context(), "btc", "binance.perp.usdt.v1", "browser:1", target); err != nil {
		t.Fatal(err)
	}
	if err := tracker.Untrack(t.Context(), "btc", "browser:1"); err != nil {
		t.Fatal(err)
	}
	if store.records["btc"].State != "awaiting_settlement" {
		t.Fatalf("tracking=%+v", store.records["btc"])
	}
	if err := tracker.ProcessDue(t.Context()); err != nil {
		t.Fatal(err)
	}
	if len(store.rates) != 1 || store.rates[0].FundingTime != target {
		t.Fatalf("rates=%+v", store.rates)
	}
	if _, exists := store.records["btc"]; exists {
		t.Fatal("completed inactive tracking was retained")
	}
}

func TestTrackerRetriesThenPersistsGapWithoutInventingRate(t *testing.T) {
	target := time.Date(2026, 8, 25, 8, 0, 0, 0, time.UTC)
	now := target.Add(91 * time.Second)
	store := &memoryStore{records: map[string]Record{}}
	tracker, _ := New(Options{Store: store, Now: func() time.Time { return now }, History: historyFunc(func(context.Context, string, time.Time, time.Time) ([]Point, error) { return nil, nil })})
	_ = tracker.Track(t.Context(), "btc", "route", "position", target)
	if err := tracker.ProcessDue(t.Context()); err == nil {
		t.Fatal("missing settlement was accepted")
	}
	if got := store.records["btc"]; got.State != "retrying" || !got.RetryAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("retry=%+v", got)
	}
	now = target.Add(2*time.Hour + 91*time.Second)
	store.records["btc"] = func() Record {
		value := store.records["btc"]
		value.RetryAt = now
		tracker.records["btc"] = value
		return value
	}()
	if err := tracker.ProcessDue(t.Context()); err == nil {
		t.Fatal("gap was accepted")
	}
	if got := store.records["btc"]; got.State != "gap" || len(store.rates) != 0 {
		t.Fatalf("gap=%+v rates=%+v", got, store.rates)
	}
}

func TestTrackerRestoresDurableSchedule(t *testing.T) {
	record := Record{InstrumentID: "btc", RouteProfileID: "route", TrackingStartedAt: time.Now().UTC(), TargetNextFundingTime: time.Now().UTC().Add(time.Hour), State: "active", ActiveReasons: []string{"position"}, UpdatedAt: time.Now().UTC()}
	store := &memoryStore{records: map[string]Record{"btc": record}}
	tracker, _ := New(Options{Store: store, History: historyFunc(func(context.Context, string, time.Time, time.Time) ([]Point, error) { return nil, nil })})
	if err := tracker.Restore(t.Context()); err != nil {
		t.Fatal(err)
	}
	if tracker.records["btc"].RouteProfileID != "route" {
		t.Fatalf("restored=%+v", tracker.records)
	}
}
