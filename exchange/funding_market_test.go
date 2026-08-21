package exchange

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestFundingMarketPatchValidationAndFieldMask(t *testing.T) {
	now := time.Date(2026, 8, 20, 12, 0, 0, 0, time.UTC)
	fact := FundingMarketFact{
		Instrument: InstrumentIdentity{
			Venue: VenueBinance, Product: ProductPerp, Symbol: "BTC-USDT",
			NativeSymbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT",
		},
		FundingRate:               OptionalDecimal{Value: decimal.RequireFromString("0.0001"), Valid: true},
		OpenInterestNotional:      OptionalDecimal{Value: decimal.RequireFromString("64000000"), Valid: true},
		OpenInterestNotionalAsset: "USDT",
		FundingObservedAt:         now,
		OpenInterestObservedAt:    now,
	}
	patch := FundingMarketPatch{
		Kind:       FundingMarketPatchSnapshot,
		Scope:      CatalogScope{Venue: VenueBinance, Product: ProductPerp, Environment: EnvironmentLive, Settlement: "USDT"},
		ObservedAt: now,
		Fields:     FundingMarketFieldAll,
		Facts:      []FundingMarketFact{fact},
	}
	if err := patch.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if !patch.Fields.Has(FundingMarketFieldFunding) || !patch.Fields.Has(FundingMarketFieldOpenInterestNotional) {
		t.Fatalf("field mask %b does not carry expected values", patch.Fields)
	}
	if fact.Key() != "binance|perp|symbol:BTCUSDT" {
		t.Fatalf("fact key = %q", fact.Key())
	}

	patch.Kind = FundingMarketPatchDelta
	patch.Fields = FundingMarketFieldMarkPrice
	patch.Facts[0].OpenInterestNotionalAsset = ""
	patch.Facts[0].OpenInterestNotional = OptionalDecimal{}
	if err := patch.Validate(); err != nil {
		t.Fatalf("partial delta Validate: %v", err)
	}
	patch.Fields = 0
	if err := patch.Validate(); err == nil {
		t.Fatal("zero field mask was accepted")
	}
}
