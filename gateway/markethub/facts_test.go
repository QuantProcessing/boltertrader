package markethub

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

func TestFactStoreAbsentPatchDoesNotInventObservationClock(t *testing.T) {
	store := NewFactStore(nil, nil)
	at := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	fact := derivativeTestFact("binance:perp:BTC-USDT", "binance-perp-usdt")
	if err := store.Apply(derivativeTestPatch(exchange.EventSnapshot, exchange.DerivativeFieldIdentity, at, fact)); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(derivativeTestPatch(exchange.EventDelta, exchange.DerivativeFieldMark, at.Add(time.Second), fact)); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(fact.InstrumentID)
	if got.MarkPrice.Valid || !got.MarkObservedAt.IsZero() {
		t.Fatalf("absent mark became observed: %+v", got)
	}
}

func TestFactStoreMetricsAgeAdvancesWhileFeedIsSilent(t *testing.T) {
	base := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	var clock atomic.Int64
	clock.Store(base.UnixNano())
	observer := &factObserver{facts: make(chan observedFact, 16)}
	store := NewFactStore(observer, func() time.Time { return time.Unix(0, clock.Load()).UTC() })
	fact := derivativeTestFact("binance:perp:BTC-USDT", "binance-perp-usdt")
	fact.MarkPrice = exchange.OptionalDecimal{Value: decimal.NewFromInt(100), Valid: true}
	fact.MarkObservedAt = base
	if err := store.Apply(derivativeTestPatch(exchange.EventSnapshot, exchange.DerivativeFieldIdentity|exchange.DerivativeFieldMark, base, fact)); err != nil {
		t.Fatal(err)
	}
	for len(observer.facts) > 0 {
		<-observer.facts
	}
	clock.Store(base.Add(10 * time.Second).UnixNano())
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go store.RunMetrics(ctx, time.Millisecond)
	deadline := time.After(time.Second)
	for {
		select {
		case observed := <-observer.facts:
			if observed.dataset == string(exchange.DatasetMark) && observed.age >= 10*time.Second {
				return
			}
		case <-deadline:
			t.Fatal("silent fact age metric did not advance")
		}
	}
}

type observedFact struct {
	dataset  string
	age      time.Duration
	coverage float64
}

type factObserver struct{ facts chan observedFact }

func (*factObserver) ObserveMarketFeed(string, string, string, string, uint64) {}
func (*factObserver) RecordMarketGap(string, string, string, bool)             {}
func (*factObserver) SetMarketHot(string, string, string, int)                 {}
func (observer *factObserver) SetMarketFact(_, _, dataset string, age time.Duration, coverage float64) {
	observer.facts <- observedFact{dataset: dataset, age: age, coverage: coverage}
}

func TestFactStoreRejectsLateFieldWithoutRefreshingOtherFields(t *testing.T) {
	store := NewFactStore(nil, nil)
	newer := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	base := derivativeTestFact("binance:perp:BTC-USDT", "binance-perp-usdt")
	base.MarkPrice = exchange.OptionalDecimal{Value: decimal.NewFromInt(100), Valid: true}
	base.MarkObservedAt = newer
	if err := store.Apply(derivativeTestPatch(exchange.EventSnapshot, exchange.DerivativeFieldIdentity|exchange.DerivativeFieldMark, newer, base)); err != nil {
		t.Fatal(err)
	}

	late := base
	late.MarkPrice = exchange.OptionalDecimal{Value: decimal.NewFromInt(90), Valid: true}
	late.MarkObservedAt = newer.Add(-time.Second)
	late.FundingRate = exchange.OptionalDecimal{Value: decimal.RequireFromString("0.0001"), Valid: true}
	late.FundingObservedAt = newer.Add(time.Second)
	if err := store.Apply(derivativeTestPatch(exchange.EventDelta, exchange.DerivativeFieldMark|exchange.DerivativeFieldFunding, newer.Add(time.Second), late)); err != nil {
		t.Fatal(err)
	}

	fact, ok := store.Get(base.InstrumentID)
	if !ok {
		t.Fatal("fact missing")
	}
	if !fact.MarkPrice.Value.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("late mark won: %s", fact.MarkPrice.Value)
	}
	if !fact.FundingRate.Valid || !fact.FundingObservedAt.Equal(newer.Add(time.Second)) {
		t.Fatalf("funding patch missing: %#v", fact)
	}
}

func TestFactStoreSnapshotReplacesOnlyItsNativeOwner(t *testing.T) {
	store := NewFactStore(nil, nil)
	at := time.Now().UTC()
	binance := derivativeTestFact("binance:perp:BTC-USDT", "binance-perp-usdt")
	bybit := derivativeTestFact("bybit:perp:ETH-USDT", "bybit-perp-usdt")
	bybit.Instrument.Venue = exchange.VenueBybit
	if err := store.Apply(derivativeTestPatch(exchange.EventSnapshot, exchange.DerivativeFieldIdentity, at, binance)); err != nil {
		t.Fatal(err)
	}
	patch := derivativeTestPatch(exchange.EventSnapshot, exchange.DerivativeFieldIdentity, at, bybit)
	patch.Scope.Venue, patch.NativeFamily = exchange.VenueBybit, "linear"
	if err := store.Apply(patch); err != nil {
		t.Fatal(err)
	}
	if err := store.Apply(derivativeTestPatch(exchange.EventSnapshot, exchange.DerivativeFieldIdentity, at.Add(time.Second))); err != nil {
		t.Fatal(err)
	}
	facts, err := store.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(facts) != 1 || facts[0].InstrumentID != bybit.InstrumentID {
		t.Fatalf("unexpected facts: %#v", facts)
	}
}

func derivativeTestPatch(kind exchange.EventKind, fields exchange.DerivativeField, at time.Time, facts ...exchange.DerivativeFact) exchange.DerivativePatch {
	return exchange.DerivativePatch{Kind: kind, Scope: exchange.CatalogScope{Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp, Settlement: "USDT"}, NativeFamily: "perpetual", Generation: 1, ObservedAt: at, Fields: fields, Facts: facts}
}

func TestFactStoreFencesGenerationsAndRequiresSnapshot(t *testing.T) {
	store := NewFactStore(nil, nil)
	at := time.Now().UTC()
	fact := derivativeTestFact("binance:perp:BTC-USDT", "binance-perp-usdt")
	fact.MarkPrice = exchange.OptionalDecimal{Value: decimal.NewFromInt(100), Valid: true}
	fact.MarkObservedAt = at
	initial := derivativeTestPatch(exchange.EventSnapshot, exchange.DerivativeFieldIdentity|exchange.DerivativeFieldMark, at, fact)
	if err := store.Apply(initial); err != nil {
		t.Fatal(err)
	}
	invalidNext := derivativeTestPatch(exchange.EventDelta, exchange.DerivativeFieldMark, at.Add(time.Second), fact)
	invalidNext.Generation = 2
	if err := store.Apply(invalidNext); err == nil {
		t.Fatal("new generation delta was accepted without snapshot")
	}
	next := derivativeTestPatch(exchange.EventSnapshot, exchange.DerivativeFieldIdentity|exchange.DerivativeFieldMark, at.Add(time.Second), fact)
	next.Generation = 2
	next.Facts[0].MarkPrice = exchange.OptionalDecimal{Value: decimal.NewFromInt(200), Valid: true}
	next.Facts[0].MarkObservedAt = at.Add(time.Second)
	if err := store.Apply(next); err != nil {
		t.Fatal(err)
	}
	late := initial
	late.Facts[0].MarkPrice = exchange.OptionalDecimal{Value: decimal.NewFromInt(50), Valid: true}
	late.Facts[0].MarkObservedAt = at.Add(2 * time.Second)
	if err := store.Apply(late); err != nil {
		t.Fatal(err)
	}
	got, _ := store.Get(fact.InstrumentID)
	if !got.MarkPrice.Value.Equal(decimal.NewFromInt(200)) {
		t.Fatalf("old generation overwrote current mark: %s", got.MarkPrice.Value)
	}
}

func derivativeTestFact(id, route string) exchange.DerivativeFact {
	return exchange.DerivativeFact{InstrumentID: id, RouteProfileID: route, Environment: exchange.EnvironmentLive, Instrument: exchange.InstrumentIdentity{Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: "BTC-USDT", NativeSymbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT"}}
}
