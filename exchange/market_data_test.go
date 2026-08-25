package exchange

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestApplyDerivativePatchPreservesAbsentFieldsAndClocks(t *testing.T) {
	oldFunding := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	newMark := oldFunding.Add(time.Second)
	current := DerivativeFact{
		FundingRate:       OptionalDecimal{Value: decimal.RequireFromString("0.0001"), Valid: true},
		FundingObservedAt: oldFunding,
		MarkPrice:         OptionalDecimal{Value: decimal.RequireFromString("77000"), Valid: true},
		MarkObservedAt:    oldFunding,
	}
	patch := DerivativeFact{
		MarkPrice:      OptionalDecimal{Value: decimal.RequireFromString("77100"), Valid: true},
		MarkObservedAt: newMark,
	}

	got := ApplyDerivativePatch(current, patch, DerivativeFieldMark)
	if !got.FundingRate.Valid || !got.FundingRate.Value.Equal(current.FundingRate.Value) || !got.FundingObservedAt.Equal(oldFunding) {
		t.Fatalf("mark patch changed funding: %#v", got)
	}
	if !got.MarkPrice.Value.Equal(patch.MarkPrice.Value) || !got.MarkObservedAt.Equal(newMark) {
		t.Fatalf("mark patch was not applied: %#v", got)
	}
}

func TestApplyDerivativePatchAdvancesFundingClockForScheduleOnlyPatch(t *testing.T) {
	initialAt := time.Date(2026, 8, 25, 1, 0, 0, 0, time.UTC)
	scheduleAt := initialAt.Add(2 * time.Second)
	current := DerivativeFact{
		FundingRate:          OptionalDecimal{Value: decimal.RequireFromString("0.0001"), Valid: true},
		FundingIntervalHours: OptionalInt{Value: 8, Valid: true},
		FundingObservedAt:    initialAt,
	}

	current = ApplyDerivativePatch(current, DerivativeFact{
		FundingIntervalHours: OptionalInt{Value: 4, Valid: true},
		NextFundingTime:      scheduleAt.Add(4 * time.Hour),
		FundingObservedAt:    scheduleAt,
	}, DerivativeFieldFundingInterval|DerivativeFieldNextFundingTime)
	if !current.FundingObservedAt.Equal(scheduleAt) || current.FundingIntervalHours.Value != 4 {
		t.Fatalf("schedule patch did not advance funding clock: %#v", current)
	}

	lateAt := initialAt.Add(time.Second)
	current = ApplyDerivativePatch(current, DerivativeFact{
		FundingRate:       OptionalDecimal{Value: decimal.RequireFromString("0.9"), Valid: true},
		FundingObservedAt: lateAt,
	}, DerivativeFieldFunding)
	if !current.FundingRate.Value.Equal(decimal.RequireFromString("0.0001")) || !current.FundingObservedAt.Equal(scheduleAt) {
		t.Fatalf("late funding patch replaced newer schedule: %#v", current)
	}
}

func TestDerivativeFactFreshnessUsesIndependentClocks(t *testing.T) {
	now := time.Date(2026, 8, 25, 1, 0, 10, 0, time.UTC)
	fact := DerivativeFact{
		FundingRate: OptionalDecimal{Value: decimal.NewFromInt(1), Valid: true}, FundingObservedAt: now.Add(-time.Minute),
		MarkPrice: OptionalDecimal{Value: decimal.NewFromInt(2), Valid: true}, MarkObservedAt: now.Add(-time.Second),
	}
	fresh := fact.Freshness(now, map[MarketDataset]time.Duration{DatasetFunding: 30 * time.Second, DatasetMark: 2 * time.Second})
	if fresh.Funding || !fresh.Mark {
		t.Fatalf("unexpected freshness: %#v", fresh)
	}
}

func TestDerivativePatchValidateRequiresCanonicalIdentity(t *testing.T) {
	at := time.Now().UTC()
	patch := DerivativePatch{
		Kind:         EventSnapshot,
		Scope:        CatalogScope{Venue: VenueBinance, Environment: EnvironmentLive, Product: ProductPerp, Settlement: "USDT"},
		NativeFamily: "perpetual", Generation: 1, ObservedAt: at,
		Fields: DerivativeFieldIdentity,
		Facts: []DerivativeFact{{
			InstrumentID: "binance:perp:BTC-USDT", RouteProfileID: "binance-perp-usdt", Environment: EnvironmentLive,
			Instrument: InstrumentIdentity{Venue: VenueBinance, Product: ProductPerp, Symbol: "BTC-USDT", NativeSymbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT"},
		}},
	}
	if err := patch.Validate(); err != nil {
		t.Fatal(err)
	}
	patch.Facts[0].InstrumentID = ""
	if err := patch.Validate(); err == nil {
		t.Fatal("missing canonical instrument ID was accepted")
	}
}
