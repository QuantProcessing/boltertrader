package fundingboard

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
	"github.com/QuantProcessing/boltertrader/routeprofile"
	"github.com/shopspring/decimal"
)

func TestProjectionReadsIndependentFieldsFromFactStore(t *testing.T) {
	now := time.Date(2026, 8, 25, 12, 0, 0, 0, time.UTC)
	store := markethub.NewFactStore(nil, func() time.Time { return now })
	fact := exchange.DerivativeFact{
		InstrumentID: "binance:live:perp:USDT:BTC-USDT", RouteProfileID: "binance.perp.usdt.v1",
		Environment: exchange.EnvironmentLive,
		Instrument:  exchange.InstrumentIdentity{Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: "BTC-USDT", NativeSymbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT"},
		FundingRate: exchange.OptionalDecimal{Value: decimal.RequireFromString("0.0001"), Valid: true}, FundingObservedAt: now.Add(-time.Minute),
		MarkPrice: exchange.OptionalDecimal{Value: decimal.NewFromInt(77000), Valid: true}, MarkObservedAt: now.Add(-10 * time.Second),
	}
	if err := store.Apply(exchange.DerivativePatch{
		Kind: exchange.EventSnapshot, Scope: exchange.CatalogScope{Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp, Settlement: "USDT"},
		NativeFamily: "perpetual", ObservedAt: now, Fields: exchange.DerivativeFieldIdentity | exchange.DerivativeFieldFunding | exchange.DerivativeFieldMark, Facts: []exchange.DerivativeFact{fact},
	}); err != nil {
		t.Fatal(err)
	}
	projection, err := NewProjection(store, nil, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	projection.now = func() time.Time { return now }
	snapshot, err := projection.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	var entry exchange.FundingBoardEntry
	for _, venue := range snapshot.Venues {
		if venue.RouteProfileID == fact.RouteProfileID && len(venue.Entries) == 1 {
			entry = venue.Entries[0]
		}
	}
	if !entry.RateValid || !entry.MarkPrice.Valid {
		t.Fatalf("projection lost values: %#v", entry)
	}
	if containsField(entry.StaleFields, "funding") || !containsField(entry.StaleFields, "mark") {
		t.Fatalf("freshness clocks were coupled: %#v", entry.StaleFields)
	}
}

func containsField(fields []string, wanted string) bool {
	for _, field := range fields {
		if field == wanted {
			return true
		}
	}
	return false
}
