package catalog

import (
	"context"
	"strings"
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
		Venues:     []Venue{{Venue: "binance", Environment: "demo", Ready: true}},
		Instruments: []Instrument{{
			InstrumentID: "binance:demo:perp:btc-usdt", Venue: "binance", Environment: "demo", Product: "perp",
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
			{Venue: "bybit", Environment: "testnet", Ready: true},
		},
		Instruments: []Instrument{
			{InstrumentID: "btc-usdt", Venue: "binance", Environment: "demo", Product: "spot", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Ready: true, Rules: TradeRules{PriceIncrement: "0.01", QuantityIncrement: "0.0001", MinQuantity: "0.0001"}},
			{InstrumentID: "btc-usdt", Venue: "bybit", Environment: "testnet", Product: "perp", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Ready: true, Rules: TradeRules{PriceIncrement: "0.1", QuantityIncrement: "0.001", MinQuantity: "0.001", MinNotional: "5"}},
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

func TestProjectionDerivesBrowserVenueContractFromExecutableRegistries(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	source := &countingSource{snapshot: Snapshot{
		Revision: "gateway-browser-1", ObservedAt: now,
		Venues: []Venue{{Venue: "binance", Environment: "demo", Ready: true}, {Venue: "bybit", Environment: "testnet", Ready: true}},
		Instruments: []Instrument{
			{InstrumentID: "btc-binance", Venue: "binance", Environment: "demo", Product: "perp", Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Ready: true, Rules: TradeRules{PriceIncrement: "0.1", QuantityIncrement: "0.001", MinQuantity: "0.001"}},
			{InstrumentID: "btc-bybit", Venue: "bybit", Environment: "testnet", Product: "perp", Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Ready: true, Rules: TradeRules{PriceIncrement: "0.1", QuantityIncrement: "0.001", MinQuantity: "0.001"}},
		},
	}}
	projection, _ := NewProjection(source, time.Minute, func() time.Time { return now })
	snapshot, err := projection.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, venue := range snapshot.Venues {
		if venue.NativeEnvironment != venue.Environment || len(venue.Products) != 2 || venue.CredentialSchema.SchemaVersion != 1 || venue.CredentialSchema.AuthKind == "" || len(venue.CredentialSchema.Fields) < 2 || len(venue.RouteProfiles) < 2 {
			t.Fatalf("incomplete browser venue projection: %+v", venue)
		}
		for _, field := range venue.CredentialSchema.Fields {
			if field.Key == "" || field.Label == "" || (field.ValueType != "string" && field.ValueType != "integer") || strings.Contains(strings.ToLower(field.Key), "value") {
				t.Fatalf("unsafe credential descriptor: %+v", field)
			}
		}
		for _, profile := range venue.RouteProfiles {
			if profile.RouteProfileID == "" || profile.RouteID == "" || profile.Label == "" {
				t.Fatalf("incomplete route profile: %+v", profile)
			}
		}
	}
}

func TestBrowserVenueProjectionCoversEverySupportedRegistryRow(t *testing.T) {
	tests := []struct {
		venue, environment string
		profiles           []string
	}{
		{"binance", "demo", []string{"binance.spot.demo.v1", "binance.perp.usdt.demo.v1"}},
		{"binance", "live", []string{"binance.spot.live.v1", "binance.perp.usdt.live.v1"}},
		{"okx", "demo", []string{"okx.spot.demo.v1", "okx.perp.usdt.demo.v1"}},
		{"okx", "live", []string{"okx.spot.live.v1", "okx.perp.usdt.live.v1"}},
		{"lighter", "testnet", []string{"lighter.spot.testnet.v1", "lighter.perp.usdc.testnet.v1"}},
		{"lighter", "live", []string{"lighter.spot.live.v1", "lighter.perp.usdc.live.v1"}},
		{"hyperliquid", "testnet", []string{"hyperliquid.spot.testnet.v1", "hyperliquid.perp.usdc.testnet.v1"}},
		{"hyperliquid", "live", []string{"hyperliquid.spot.live.v1", "hyperliquid.perp.usdc.live.v1"}},
		{"bybit", "demo", []string{"bybit.spot.demo.v1", "bybit.perp.usdt.demo.v1", "bybit.perp.usdc.demo.v1"}},
		{"bybit", "testnet", []string{"bybit.spot.testnet.v1", "bybit.perp.usdt.testnet.v1", "bybit.perp.usdc.testnet.v1"}},
		{"bybit", "live", []string{"bybit.spot.live.v1", "bybit.perp.usdt.live.v1", "bybit.perp.usdc.live.v1"}},
		{"bitget", "demo", []string{"bitget.spot.demo.v1", "bitget.perp.usdt.demo.v1", "bitget.perp.usdc.demo.v1"}},
		{"bitget", "live", []string{"bitget.spot.live.v1", "bitget.perp.usdt.live.v1", "bitget.perp.usdc.live.v1"}},
		{"gate", "testnet", []string{"gate.spot.testnet.v1", "gate.perp.usdt.testnet.v1"}},
		{"gate", "live", []string{"gate.spot.live.v1", "gate.perp.usdt.live.v1"}},
		{"aster", "testnet", []string{"aster.spot.testnet.v1", "aster.perp.usdt.testnet.v1"}},
		{"aster", "live", []string{"aster.spot.live.v1", "aster.perp.usdt.live.v1"}},
		{"nado", "testnet", []string{"nado.spot.usdt0.testnet.v1", "nado.perp.usdt0.testnet.v1"}},
		{"nado", "live", []string{"nado.spot.usdt0.live.v1", "nado.perp.usdt0.live.v1"}},
	}
	for _, test := range tests {
		t.Run(test.venue+"/"+test.environment, func(t *testing.T) {
			snapshot, err := enrichBrowserVenues(Snapshot{Venues: []Venue{{Venue: test.venue, Environment: test.environment, Ready: true}}})
			if err != nil {
				t.Fatal(err)
			}
			venue := snapshot.Venues[0]
			got := make([]string, 0, len(venue.RouteProfiles))
			for _, profile := range venue.RouteProfiles {
				got = append(got, profile.RouteProfileID)
			}
			if strings.Join(got, ",") != strings.Join(test.profiles, ",") || venue.NativeEnvironment != test.environment || len(venue.Products) != 2 {
				t.Fatalf("venue=%+v profiles=%v", venue, got)
			}
		})
	}
}

func TestBrowserCredentialProjectionCoversEveryExecutableVariant(t *testing.T) {
	want := map[string]string{
		"binance": "api_key_secret", "bybit": "api_key_secret", "gate": "api_key_secret",
		"okx": "api_key_secret_passphrase", "bitget": "api_key_secret_passphrase",
		"lighter": "lighter_signer", "hyperliquid": "evm_private_key", "aster": "aster_api_wallet", "nado": "nado_signer",
	}
	for venue, authKind := range want {
		schema, err := browserCredentialSchema(venue)
		if err != nil || schema.AuthKind != authKind || len(schema.Fields) < 2 {
			t.Fatalf("venue=%s schema=%+v err=%v", venue, schema, err)
		}
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
