package catalog

import (
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"
)

type countingSource struct {
	mu       sync.Mutex
	calls    int
	snapshot Snapshot
	err      error
}

func (source *countingSource) Fetch(context.Context) (Snapshot, error) {
	source.mu.Lock()
	defer source.mu.Unlock()
	source.calls++
	return source.snapshot, source.err
}

func TestProjectionReportsSourceFailureAsUnavailable(t *testing.T) {
	source := &countingSource{err: errors.New("Gateway request timed out")}
	projection, err := NewProjection(source, time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projection.Snapshot(context.Background()); err == nil {
		t.Fatal("Snapshot() error = nil, want source failure")
	}
	if status := projection.Status(); status.Ready || status.Reason != ReasonUnavailable {
		t.Fatalf("Status() = %#v, want unavailable source", status)
	}
}

func TestProjectionCoalescesGatewayFacts(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	source := &countingSource{snapshot: Snapshot{
		Revision:   "gateway-rev-1",
		ObservedAt: now,
		Venues:     []Venue{{Venue: "binance", Environment: "live", Ready: true}},
		Instruments: []Instrument{{
			InstrumentID: "binance:live:perp:btc-usdt", Venue: "binance", Environment: "live", Product: "perp",
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

// clockedSource observes its facts at the moment it is asked for them, the way a
// Gateway does. countingSource cannot stand in: its ObservedAt is frozen at
// construction, so refetching it would return facts that age anyway.
//
// It carries no venues or instruments because none of what follows depends on
// them — the subject is when the projection fetches, not what it fetched.
type clockedSource struct {
	calls int
	now   func() time.Time
}

func (source *clockedSource) Fetch(context.Context) (Snapshot, error) {
	source.calls++
	return Snapshot{Revision: "gateway-rev-1", ObservedAt: source.now()}, nil
}

// TestRefreshRenewsBeforeTheProjectionCanGoStale pins the difference between the
// projection's two entry points, which is the reason both exist.
//
// Readiness reports Status, and Status only reads — nothing fetches unless a
// caller asks. Left to Snapshot's demand-driven faulting, a service whose
// Gateway is perfectly reachable reports it unavailable from startup until
// unrelated client traffic happens to arrive. Nor can Snapshot on a timer fix
// that: it serves cached facts until they expire, so it would renew the
// projection strictly after the window in which Status already calls it stale.
func TestRefreshRenewsBeforeTheProjectionCanGoStale(t *testing.T) {
	const ttl = 30 * time.Second
	clock := time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC)
	now := func() time.Time { return clock }
	source := &clockedSource{now: now}
	projection, err := NewProjection(source, ttl, now)
	if err != nil {
		t.Fatal(err)
	}

	// Cold: the projection has never been asked for anything, so it knows
	// nothing. This is the state that reported a healthy stack unready.
	if status := projection.Status(); status.Ready || status.Reason != ReasonUnavailable {
		t.Fatalf("cold Status() = %#v, want unready with reason %q", status, ReasonUnavailable)
	}
	if err := projection.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() = %v", err)
	}
	if status := projection.Status(); !status.Ready || status.Age != 0 {
		t.Fatalf("Status() after Refresh = %#v, want ready at age 0", status)
	}

	// A third of the way to expiry the facts are still valid, so Snapshot serves
	// them from cache — and the projection goes on ageing toward the staleness
	// Status would then report.
	clock = clock.Add(ttl / 3)
	if _, err := projection.Snapshot(context.Background()); err != nil {
		t.Fatalf("Snapshot() = %v", err)
	}
	if source.calls != 1 {
		t.Fatalf("Snapshot refetched unexpired facts: Gateway fetches = %d, want 1", source.calls)
	}
	if age := projection.Status().Age; age != ttl/3 {
		t.Fatalf("Age after cached Snapshot = %v, want %v", age, ttl/3)
	}

	// Refresh at that same moment fetches, and the age it resets is what keeps a
	// maintained projection from ever reaching the TTL.
	if err := projection.Refresh(context.Background()); err != nil {
		t.Fatalf("Refresh() = %v", err)
	}
	if source.calls != 2 {
		t.Fatalf("Refresh served cached facts: Gateway fetches = %d, want 2", source.calls)
	}
	if status := projection.Status(); !status.Ready || status.Age != 0 {
		t.Fatalf("Status() after second Refresh = %#v, want ready at age 0", status)
	}
}

func TestProjectionRejectsUntrustedCatalogFacts(t *testing.T) {
	source := &countingSource{snapshot: Snapshot{
		Venues:      []Venue{{Venue: "binance", Environment: "live", Ready: true}},
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

func TestProjectionPreservesCanonicalInstrumentIdentityAndExactRules(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	source := &countingSource{snapshot: Snapshot{
		Revision: "gateway-rev-2", ObservedAt: now,
		Venues: []Venue{
			{Venue: "binance", Environment: "live", Ready: true},
			{Venue: "bybit", Environment: "live", Ready: true},
		},
		Instruments: []Instrument{
			{InstrumentID: "binance:live:spot:BTC-USDT", Venue: "binance", Environment: "live", Product: "spot", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Ready: true, Rules: TradeRules{PriceIncrement: "0.01", QuantityIncrement: "0.0001", MinQuantity: "0.0001"}},
			{InstrumentID: "bybit:live:perp:USDT:BTC-USDT", Venue: "bybit", Environment: "live", Product: "perp", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Ready: true, Rules: TradeRules{PriceIncrement: "0.1", QuantityIncrement: "0.001", MinQuantity: "0.001", MinNotional: "5"}},
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
	// The canonical id itself separates these rows. Venue and product remain
	// output facts; callers do not submit them as parallel identity inputs.
	if len(snapshot.Instruments) != 2 || snapshot.Instruments[1].Rules.MinNotional != "5" || snapshot.Instruments[1].Venue != "bybit" || snapshot.Instruments[1].Product != "perp" {
		t.Fatalf("Snapshot() = %#v", snapshot)
	}
	if resolved, ok := snapshot.Instrument("bybit:live:perp:USDT:BTC-USDT"); !ok || resolved.Symbol != "BTC-USDT" {
		t.Fatalf("resolved=%+v ok=%v", resolved, ok)
	}
}

func TestProjectionRejectsInstrumentIDReusedAcrossVenues(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	rules := TradeRules{PriceIncrement: "0.1", QuantityIncrement: "0.001", MinQuantity: "0.001"}
	snapshot := Snapshot{
		Revision: "gateway-rev-duplicate", ObservedAt: now,
		Venues: []Venue{
			{Venue: "binance", Environment: "live", Ready: true},
			{Venue: "bybit", Environment: "live", Ready: true},
		},
		Instruments: []Instrument{
			{InstrumentID: "shared-id", Venue: "binance", Environment: "live", Product: "perp", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Rules: rules, Ready: true},
			{InstrumentID: "shared-id", Venue: "bybit", Environment: "live", Product: "perp", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Rules: rules, Ready: true},
		},
	}
	projection, err := NewProjection(&countingSource{snapshot: snapshot}, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	if _, err := projection.Snapshot(context.Background()); err == nil || !strings.Contains(err.Error(), "duplicate Gateway instrument") {
		t.Fatalf("Snapshot() error=%v, want duplicate identity refusal", err)
	}
}

func TestProjectionAcceptsZeroOptionalMinimumsAndRejectsNegativeMinimums(t *testing.T) {
	valid := TradeRules{
		PriceIncrement: "0.1", QuantityIncrement: "0.001",
		MinQuantity: "0", MinNotional: "0",
	}
	if err := validateRules(valid); err != nil {
		t.Fatalf("validateRules(zero minimums) = %v", err)
	}

	for name, mutate := range map[string]func(*TradeRules){
		"minimum quantity": func(rules *TradeRules) { rules.MinQuantity = "-0.001" },
		"minimum notional": func(rules *TradeRules) { rules.MinNotional = "-1" },
	} {
		t.Run(name, func(t *testing.T) {
			rules := valid
			mutate(&rules)
			if err := validateRules(rules); err == nil {
				t.Fatal("validateRules() error = nil")
			}
		})
	}
}

func TestProjectionDerivesBrowserVenueContractFromExecutableRegistries(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	source := &countingSource{snapshot: Snapshot{
		Revision: "gateway-browser-1", ObservedAt: now,
		Venues: []Venue{{Venue: "binance", Environment: "live", Ready: true}, {Venue: "bybit", Environment: "live", Ready: true}},
		Instruments: []Instrument{
			{InstrumentID: "btc-binance", Venue: "binance", Environment: "live", Product: "perp", Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Ready: true, Rules: TradeRules{PriceIncrement: "0.1", QuantityIncrement: "0.001", MinQuantity: "0.001"}},
			{InstrumentID: "btc-bybit", Venue: "bybit", Environment: "live", Product: "perp", Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Ready: true, Rules: TradeRules{PriceIncrement: "0.1", QuantityIncrement: "0.001", MinQuantity: "0.001"}},
		},
	}}
	projection, _ := NewProjection(source, time.Minute, func() time.Time { return now })
	snapshot, err := projection.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, venue := range snapshot.Venues {
		if venue.NativeEnvironment != venue.Environment || len(venue.Products) != 1 || venue.CredentialSchema.SchemaVersion != 1 || venue.CredentialSchema.AuthKind == "" || len(venue.CredentialSchema.Fields) < 2 || len(venue.RouteProfiles) < 2 {
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
	// One row per venue covers every registry row, because the registry is no
	// longer keyed by environment: these IDs read `binance.spot.demo.v1` while
	// demo and testnet existed, and each venue needed a row per environment to
	// reach all of its profiles. `enrichBrowserVenues` resolves by venue,
	// product and settlement alone, so the environment below is only the input
	// that NativeEnvironment is asserted to echo.
	const environment = "live"
	tests := []struct {
		venue    string
		profiles []string
	}{
		{"binance", []string{"binance.spot.v1", "binance.perp.usdt.v1"}},
		{"okx", []string{"okx.spot.v1", "okx.perp.usdt.v1"}},
		{"lighter", []string{"lighter.spot.v1", "lighter.perp.usdc.v1"}},
		{"hyperliquid", []string{"hyperliquid.spot.v1", "hyperliquid.perp.usdc.v1"}},
		{"bybit", []string{"bybit.spot.v1", "bybit.perp.usdt.v1", "bybit.perp.usdc.v1"}},
		{"bitget", []string{"bitget.spot.v1", "bitget.perp.usdt.v1", "bitget.perp.usdc.v1"}},
		{"gate", []string{"gate.spot.v1", "gate.perp.usdt.v1"}},
		{"aster", []string{"aster.spot.v1", "aster.perp.usdt.v1"}},
		{"nado", []string{"nado.spot.usdt0.v1", "nado.perp.usdt0.v1"}},
	}
	for _, test := range tests {
		t.Run(test.venue, func(t *testing.T) {
			snapshot, err := enrichBrowserVenues(Snapshot{Venues: []Venue{{Venue: test.venue, Environment: environment, Ready: true}}})
			if err != nil {
				t.Fatal(err)
			}
			venue := snapshot.Venues[0]
			got := make([]string, 0, len(venue.RouteProfiles))
			for _, profile := range venue.RouteProfiles {
				got = append(got, profile.RouteProfileID)
			}
			if strings.Join(got, ",") != strings.Join(test.profiles, ",") || venue.NativeEnvironment != environment || len(venue.Products) != 1 {
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
		Revision: "gateway-rev", Venues: []Venue{{Venue: "binance", Environment: "live", Ready: true}},
		Instruments: []Instrument{{InstrumentID: "btc-usdt", Venue: "binance", Environment: "live", Product: "perp", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Ready: true, Rules: TradeRules{PriceIncrement: "0.1", QuantityIncrement: "0.001", MinQuantity: "0.001"}}},
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
				Revision: "gateway-rev", ObservedAt: observedAt, Venues: []Venue{{Venue: "binance", Environment: "live", Ready: true}},
				Instruments: []Instrument{{InstrumentID: "btc-usdt", Venue: "binance", Environment: "live", Product: "spot", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Ready: true, Rules: TradeRules{PriceIncrement: "0.1", QuantityIncrement: "0.001", MinQuantity: "0.001"}}},
			}
			projection, _ := NewProjection(&countingSource{snapshot: snapshot}, time.Minute, func() time.Time { return now })
			if _, err := projection.Snapshot(context.Background()); err == nil {
				t.Fatal("Snapshot() error = nil")
			}
		})
	}
}
