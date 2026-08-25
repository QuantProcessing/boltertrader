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
