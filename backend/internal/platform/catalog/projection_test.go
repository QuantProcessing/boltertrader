package catalog

import (
	"context"
	"sync"
	"testing"
	"time"
)

type countingSource struct {
	mu       sync.Mutex
	calls    int
	snapshot Snapshot
}

func (source *countingSource) Fetch(context.Context) (Snapshot, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.calls++
	return source.snapshot, nil
}

func TestProjectionCoalescesGatewayFacts(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	source := &countingSource{snapshot: Snapshot{
		Revision:   "gateway-rev-1",
		ObservedAt: now,
		Venues:     []Venue{{Venue: "binance", Environment: "testnet", Ready: true}},
		Instruments: []Instrument{{
			InstrumentID: "binance:testnet:perp:btc-usdt", Venue: "binance", Environment: "testnet", Product: "perp",
			Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Ready: true,
			Rules: TradeRules{PriceIncrement: "0.1", QuantityIncrement: "0.001", MinQuantity: "0.001", MinNotional: "5"},
		}},
	}}
	projection, err := NewProjection(source, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}

	const consumers = 12
	var wait sync.WaitGroup
	wait.Add(consumers)
	for range consumers {
		go func() {
			defer wait.Done()
			snapshot, fetchErr := projection.Snapshot(context.Background())
			if fetchErr != nil || snapshot.Revision != "gateway-rev-1" {
				t.Errorf("Snapshot() = %#v, %v", snapshot, fetchErr)
			}
		}()
	}
	wait.Wait()
	if source.calls != 1 {
		t.Fatalf("Gateway fetch calls = %d, want 1", source.calls)
	}

	status := projection.Status()
	if !status.Ready || status.Revision != "gateway-rev-1" || status.Age != 0 {
		t.Fatalf("Status() = %#v", status)
	}
}

func TestProjectionRejectsUntrustedCatalogFacts(t *testing.T) {
	source := &countingSource{snapshot: Snapshot{
		Venues:      []Venue{{Venue: "binance", Environment: "testnet", Ready: true}},
		Instruments: []Instrument{{InstrumentID: "btc-usdt", Venue: "okx", Ready: true}},
	}}
	projection, err := NewProjection(source, time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projection.Snapshot(context.Background()); err == nil {
		t.Fatal("Snapshot() error = nil, want invalid Gateway facts")
	}
	if status := projection.Status(); status.Ready || status.Reason != ReasonInvalidFacts {
		t.Fatalf("Status() = %#v", status)
	}
}

func TestProjectionPreservesCompositeInstrumentIdentityAndExactRules(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	source := &countingSource{snapshot: Snapshot{
		Revision: "gateway-rev-2", ObservedAt: now,
		Venues: []Venue{
			{Venue: "binance", Environment: "demo", Ready: true},
			{Venue: "binance", Environment: "testnet", Ready: true},
		},
		Instruments: []Instrument{
			{InstrumentID: "btc-usdt", Venue: "binance", Environment: "demo", Product: "spot", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Ready: true, Rules: TradeRules{PriceIncrement: "0.01", QuantityIncrement: "0.0001", MinQuantity: "0.0001"}},
			{InstrumentID: "btc-usdt", Venue: "binance", Environment: "testnet", Product: "perp", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Ready: true, Rules: TradeRules{PriceIncrement: "0.1", QuantityIncrement: "0.001", MinQuantity: "0.001", MinNotional: "5"}},
		},
	}}
	projection, err := NewProjection(source, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := projection.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Instruments) != 2 || snapshot.Instruments[1].Rules.MinNotional != "5" || snapshot.Instruments[1].Environment != "testnet" || snapshot.Instruments[1].Product != "perp" {
		t.Fatalf("Snapshot() = %#v", snapshot)
	}
}

func TestProjectionRejectsMissingOrInexactInstrumentFacts(t *testing.T) {
	base := Snapshot{
		Revision: "gateway-rev", Venues: []Venue{{Venue: "binance", Environment: "demo", Ready: true}},
		Instruments: []Instrument{{InstrumentID: "btc-usdt", Venue: "binance", Environment: "demo", Product: "perp", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Ready: true, Rules: TradeRules{PriceIncrement: "0.1", QuantityIncrement: "0.001", MinQuantity: "0.001"}}},
	}
	for name, mutate := range map[string]func(*Snapshot){
		"missing product": func(snapshot *Snapshot) { snapshot.Instruments[0].Product = "" },
		"missing symbol":  func(snapshot *Snapshot) { snapshot.Instruments[0].Symbol = "" },
		"missing rules":   func(snapshot *Snapshot) { snapshot.Instruments[0].Rules.PriceIncrement = "" },
		"invalid rules":   func(snapshot *Snapshot) { snapshot.Instruments[0].Rules.MinQuantity = "not-a-decimal" },
	} {
		t.Run(name, func(t *testing.T) {
			snapshot := base
			snapshot.Instruments = append([]Instrument(nil), base.Instruments...)
			mutate(&snapshot)
			projection, _ := NewProjection(&countingSource{snapshot: snapshot}, time.Minute, time.Now)
			if _, err := projection.Snapshot(context.Background()); err == nil {
				t.Fatal("Snapshot() error = nil")
			}
		})
	}
}

func TestProjectionRejectsMissingOrStaleObservationTime(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	for name, observedAt := range map[string]time.Time{"missing": {}, "stale": now.Add(-2 * time.Minute), "future": now.Add(time.Second)} {
		t.Run(name, func(t *testing.T) {
			snapshot := Snapshot{
				Revision: "gateway-rev", ObservedAt: observedAt, Venues: []Venue{{Venue: "binance", Environment: "demo", Ready: true}},
				Instruments: []Instrument{{InstrumentID: "btc-usdt", Venue: "binance", Environment: "demo", Product: "spot", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Ready: true, Rules: TradeRules{PriceIncrement: "0.1", QuantityIncrement: "0.001", MinQuantity: "0.001"}}},
			}
			projection, _ := NewProjection(&countingSource{snapshot: snapshot}, time.Minute, func() time.Time { return now })
			if _, err := projection.Snapshot(context.Background()); err == nil {
				t.Fatal("Snapshot() error = nil")
			}
		})
	}
}
