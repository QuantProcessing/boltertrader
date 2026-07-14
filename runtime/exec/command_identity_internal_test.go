package exec

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/cache"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
	"github.com/shopspring/decimal"
)

func TestCommandEntropyFailureStopsBeforeDurableOrVenueSideEffects(t *testing.T) {
	entropyErr := errors.New("entropy unavailable")
	instrumentID := model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindSpot}
	newEngine := func(t *testing.T) (*Engine, *runtimetest.FakeExec, *cache.Cache, *journal.MemoryJournal) {
		t.Helper()
		client := runtimetest.NewFakeExec()
		client.SetModifySupported(true)
		orderCache := cache.New()
		commandJournal := journal.NewMemory()
		engine := New(
			client,
			orderCache,
			clock.NewSimulatedClock(time.Unix(1, 0)),
			"entropy",
		).WithJournal(commandJournal)
		engine.commandEntropy = failingEntropyReader{err: entropyErr}
		return engine, client, orderCache, commandJournal
	}
	request := func(clientID string) model.OrderRequest {
		return model.OrderRequest{
			AccountID:    "entropy",
			InstrumentID: instrumentID,
			ClientID:     clientID,
			Side:         enums.SideBuy,
			Type:         enums.TypeLimit,
			TIF:          enums.TifGTC,
			Quantity:     decimal.NewFromInt(1),
			Price:        decimal.NewFromInt(100),
		}
	}
	assertNoDurableSideEffects := func(t *testing.T, engine *Engine, commandJournal *journal.MemoryJournal) {
		t.Helper()
		if records := commandJournal.Records(); len(records) != 0 {
			t.Fatalf("journal records=%+v, want none after entropy failure", records)
		}
		if got := engine.InFlightCount(); got != 0 {
			t.Fatalf("in-flight commands=%d, want 0 after entropy failure", got)
		}
	}

	t.Run("submit", func(t *testing.T) {
		engine, client, orderCache, commandJournal := newEngine(t)
		venueCalls := 0
		client.OnSubmit(func(model.OrderRequest) { venueCalls++ })

		order, err := engine.Submit(context.Background(), request("entropy-submit"))
		if order != nil || !errors.Is(err, entropyErr) {
			t.Fatalf("submit order=%+v err=%v, want entropy error", order, err)
		}
		if venueCalls != 0 {
			t.Fatalf("submit venue calls=%d, want 0", venueCalls)
		}
		if _, ok := orderCache.OrderByClientIDForAccount("entropy", "entropy-submit"); ok {
			t.Fatal("submit entropy failure mutated order cache")
		}
		assertNoDurableSideEffects(t, engine, commandJournal)
	})

	t.Run("cancel", func(t *testing.T) {
		engine, client, orderCache, commandJournal := newEngine(t)
		original := model.Order{
			Request: request("entropy-cancel"), VenueOrderID: "venue-cancel",
			Status: enums.StatusNew, CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(1, 0),
		}
		orderCache.UpsertOrder(original)
		venueCalls := 0
		client.OnCancel(func(model.InstrumentID, string) { venueCalls++ })

		err := engine.Cancel(context.Background(), original.Request.ClientID)
		if !errors.Is(err, entropyErr) {
			t.Fatalf("cancel err=%v, want entropy error", err)
		}
		if venueCalls != 0 {
			t.Fatalf("cancel venue calls=%d, want 0", venueCalls)
		}
		got, ok := orderCache.OrderByClientIDForAccount("entropy", original.Request.ClientID)
		if !ok || !reflect.DeepEqual(got, original) {
			t.Fatalf("cancel cache order=(%+v,%v), want unchanged %+v", got, ok, original)
		}
		assertNoDurableSideEffects(t, engine, commandJournal)
	})

	t.Run("modify", func(t *testing.T) {
		engine, client, orderCache, commandJournal := newEngine(t)
		original := model.Order{
			Request: request("entropy-modify"), VenueOrderID: "venue-modify",
			Status: enums.StatusNew, CreatedAt: time.Unix(1, 0), UpdatedAt: time.Unix(1, 0),
		}
		orderCache.UpsertOrder(original)
		venueCalls := 0
		client.OnModify(func(model.InstrumentID, string, decimal.Decimal, decimal.Decimal) { venueCalls++ })

		order, err := engine.Modify(
			context.Background(),
			original.Request.ClientID,
			decimal.NewFromInt(101),
			decimal.NewFromInt(2),
		)
		if order != nil || !errors.Is(err, entropyErr) {
			t.Fatalf("modify order=%+v err=%v, want entropy error", order, err)
		}
		if venueCalls != 0 {
			t.Fatalf("modify venue calls=%d, want 0", venueCalls)
		}
		got, ok := orderCache.OrderByClientIDForAccount("entropy", original.Request.ClientID)
		if !ok || !reflect.DeepEqual(got, original) {
			t.Fatalf("modify cache order=(%+v,%v), want unchanged %+v", got, ok, original)
		}
		assertNoDurableSideEffects(t, engine, commandJournal)
	})
}

type failingEntropyReader struct {
	err error
}

func (r failingEntropyReader) Read([]byte) (int, error) {
	return 0, r.err
}
