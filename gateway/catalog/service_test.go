package catalog

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/shopspring/decimal"
)

func TestServicePublishesStablePublicIdentityAndResolvesCanonicalInstrument(t *testing.T) {
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	service, err := NewService(time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatalf("NewService: %v", err)
	}
	scope := Scope{Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp, Settlement: "USDT"}
	accountID := domain.AccountID("binance-perp")
	if err := service.RegisterAccount(accountID, scope); err != nil {
		t.Fatalf("RegisterAccount: %v", err)
	}
	native := testInstrument(exchange.VenueBinance, exchange.ProductPerp, "BTC-USDT", "BTCUSDT", "BTCUSDT", "USDT")

	snapshot, err := service.Publish(accountID, []exchange.Instrument{native})
	if err != nil {
		t.Fatalf("Publish: %v", err)
	}
	if snapshot.Generation != 1 || snapshot.Revision == "" || !snapshot.LoadedAt.Equal(now) {
		t.Fatalf("snapshot metadata = %+v", snapshot)
	}
	if len(snapshot.Instruments) != 1 {
		t.Fatalf("instruments = %d, want 1", len(snapshot.Instruments))
	}
	public := snapshot.Instruments[0]
	wantID := domain.InstrumentID("binance:live:perp:USDT:BTC-USDT")
	if public.ID != wantID || public.Symbol != "BTC-USDT" || public.Settlement != "USDT" {
		t.Fatalf("public instrument = %+v", public)
	}
	payload, err := json.Marshal(public)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(payload), "BTCUSDT") || strings.Contains(string(payload), "native") {
		t.Fatalf("public instrument leaked native identity: %s", payload)
	}

	resolved, err := service.Resolve(context.Background(), accountID, wantID)
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if resolved.Identity.NativeSymbol != native.Identity.NativeSymbol || resolved.Identity.NativeID != native.Identity.NativeID {
		t.Fatalf("resolved instrument = %+v, want canonical native identity %+v", resolved, native)
	}
}

func TestServiceUsesSettlementForNadoSpotStableIdentity(t *testing.T) {
	service, _ := NewService(time.Minute, time.Now)
	accountID := domain.AccountID("nado-spot")
	scope := Scope{Venue: exchange.VenueNado, Environment: exchange.EnvironmentLive, Product: exchange.ProductSpot, Settlement: "USDT0"}
	if err := service.RegisterAccount(accountID, scope); err != nil {
		t.Fatal(err)
	}
	instrument := testInstrument(exchange.VenueNado, exchange.ProductSpot, "BTC-USDT0", "BTC-USDT0", "1", "USDT0")
	snapshot, err := service.Publish(accountID, []exchange.Instrument{instrument})
	if err != nil {
		t.Fatal(err)
	}
	if got, want := snapshot.Instruments[0].ID.String(), "nado:live:spot:USDT0:BTC-USDT0"; got != want {
		t.Fatalf("instrument id = %q, want %q", got, want)
	}
}

func TestServiceRejectsNonCanonicalSettlementBeforeRegistration(t *testing.T) {
	service, _ := NewService(time.Minute, time.Now)
	err := service.RegisterAccount("nado-spot", Scope{
		Venue:       exchange.VenueNado,
		Environment: exchange.EnvironmentLive,
		Product:     exchange.ProductSpot,
		Settlement:  "usdt0",
	})
	if !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("lowercase settlement error = %v, want ErrInvalidSnapshot", err)
	}
	if _, err := service.Snapshot("nado-spot"); !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatalf("rejected registration left account state: %v", err)
	}
}

func TestServiceIsolatesScopesAndSharesEquivalentAccountSnapshots(t *testing.T) {
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	service, _ := NewService(time.Minute, func() time.Time { return now })
	binanceScope := Scope{Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, Product: exchange.ProductSpot}
	okxScope := Scope{Venue: exchange.VenueOKX, Environment: exchange.EnvironmentLive, Product: exchange.ProductSpot}
	for _, registration := range []struct {
		id    domain.AccountID
		scope Scope
	}{
		{"binance-a", binanceScope},
		{"binance-b", binanceScope},
		{"okx-a", okxScope},
	} {
		if err := service.RegisterAccount(registration.id, registration.scope); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := service.Publish("binance-a", []exchange.Instrument{testInstrument(exchange.VenueBinance, exchange.ProductSpot, "ETH-USDT", "ETHUSDT", "ETHUSDT", "")}); err != nil {
		t.Fatal(err)
	}
	shared, err := service.Snapshot("binance-b")
	if err != nil || len(shared.Instruments) != 1 {
		t.Fatalf("shared snapshot = %+v, err=%v", shared, err)
	}
	if _, err := service.Snapshot("okx-a"); !errors.Is(err, ErrCatalogUnavailable) {
		t.Fatalf("isolated missing snapshot error = %v, want ErrCatalogUnavailable", err)
	}
	service.MarkRefreshError("okx-a")
	if status := service.Status("binance-a"); !status.ReadReady || status.LastError != "" {
		t.Fatalf("unrelated scope degraded by OKX error: %+v", status)
	}
}

func TestServiceRetainsLastKnownGoodAndRejectsStaleCommandResolution(t *testing.T) {
	now := time.Date(2026, 7, 22, 10, 0, 0, 0, time.UTC)
	service, _ := NewService(time.Minute, func() time.Time { return now })
	accountID := domain.AccountID("binance-spot")
	scope := Scope{Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, Product: exchange.ProductSpot}
	if err := service.RegisterAccount(accountID, scope); err != nil {
		t.Fatal(err)
	}
	instrument := testInstrument(exchange.VenueBinance, exchange.ProductSpot, "ETH-USDT", "ETHUSDT", "ETHUSDT", "")
	first, err := service.Publish(accountID, []exchange.Instrument{instrument})
	if err != nil {
		t.Fatal(err)
	}
	service.MarkRefreshError(accountID)
	lastKnownGood, err := service.Snapshot(accountID)
	if err != nil || lastKnownGood.Revision != first.Revision {
		t.Fatalf("last known good = %+v, err=%v", lastKnownGood, err)
	}
	status := service.Status(accountID)
	if !status.ReadReady || status.WriteReady || status.LastError != ReasonRefreshFailed {
		t.Fatalf("status after refresh failure = %+v", status)
	}

	now = now.Add(2 * time.Minute)
	status = service.Status(accountID)
	if status.ReadReady || status.WriteReady || !status.Stale || status.Reason != ReasonCatalogStale {
		t.Fatalf("stale status = %+v", status)
	}
	if _, err := service.Resolve(context.Background(), accountID, first.Instruments[0].ID); !errors.Is(err, ErrCatalogStale) {
		t.Fatalf("stale Resolve error = %v, want ErrCatalogStale", err)
	}
	if got, err := service.Snapshot(accountID); err != nil || got.Revision != first.Revision {
		t.Fatalf("stale read snapshot = %+v, err=%v", got, err)
	}
}

func TestServiceRejectsInvalidRefreshWithoutReplacingSnapshotAndReturnsDefensiveCopies(t *testing.T) {
	now := time.Now().UTC()
	service, _ := NewService(time.Minute, func() time.Time { return now })
	accountID := domain.AccountID("binance-spot")
	scope := Scope{Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, Product: exchange.ProductSpot}
	if err := service.RegisterAccount(accountID, scope); err != nil {
		t.Fatal(err)
	}
	original := testInstrument(exchange.VenueBinance, exchange.ProductSpot, "ETH-USDT", "ETHUSDT", "ETHUSDT", "")
	first, err := service.Publish(accountID, []exchange.Instrument{original})
	if err != nil {
		t.Fatal(err)
	}
	first.Instruments[0].Symbol = "MUTATED"
	bad := testInstrument(exchange.VenueOKX, exchange.ProductSpot, "ETH-USDT", "ETH-USDT", "ETH-USDT", "")
	if _, err := service.Publish(accountID, []exchange.Instrument{bad}); !errors.Is(err, ErrInvalidSnapshot) {
		t.Fatalf("invalid refresh error = %v, want ErrInvalidSnapshot", err)
	}
	got, err := service.Snapshot(accountID)
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Instruments) != 1 || got.Instruments[0].Symbol != "ETH-USDT" {
		t.Fatalf("snapshot was replaced or aliased: %+v", got)
	}

	now = now.Add(time.Second)
	second, err := service.Publish(accountID, []exchange.Instrument{original})
	if err != nil {
		t.Fatal(err)
	}
	if second.Generation <= got.Generation || second.Revision != got.Revision {
		t.Fatalf("equivalent refresh metadata first=%+v second=%+v", got, second)
	}
}

func testInstrument(venue exchange.Venue, product exchange.Product, symbol, nativeSymbol, nativeID, settlement string) exchange.Instrument {
	quote := "USDT"
	if strings.HasSuffix(symbol, "-USDC") {
		quote = "USDC"
	}
	if strings.HasSuffix(symbol, "-USDT0") {
		quote = "USDT0"
	}
	base := strings.TrimSuffix(strings.TrimSuffix(strings.TrimSuffix(symbol, "-USDT0"), "-USDT"), "-USDC")
	return exchange.Instrument{
		Identity: exchange.InstrumentIdentity{
			Venue: venue, Product: product, Symbol: symbol, BaseAsset: base, QuoteAsset: quote,
			SettleAsset: settlement, NativeSymbol: nativeSymbol, NativeID: nativeID,
		},
		Rules: exchange.InstrumentRules{
			PriceIncrement: decimal.RequireFromString("0.01"), QuantityIncrement: decimal.RequireFromString("0.001"),
			MinQuantity: decimal.RequireFromString("0.001"), MinNotional: exchange.OptionalDecimal{Value: decimal.RequireFromString("5"), Valid: true},
		},
	}
}
