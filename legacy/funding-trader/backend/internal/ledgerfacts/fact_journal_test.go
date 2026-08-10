package ledgerfacts_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerfacts"
	"github.com/shopspring/decimal"
)

func TestLedgerFactsAppendOnlyAndSupersedes(t *testing.T) {
	ctx := context.Background()
	store := ledgerfacts.NewMemoryStore()
	journal := ledgerfacts.NewJournal(store)
	scope := testScope()
	observed := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	first, err := journal.AppendFundingPayment(ctx, ledgerfacts.FundingPaymentFact{
		Scope: scope,
		Provenance: ledgerfacts.Provenance{
			SourceRef:      "gateway:payment:1",
			SourceRevision: "1",
			SourceVersion:  "gateway-v1",
			ObservedAt:     observed,
			EffectiveAt:    observed.Add(-time.Hour),
		},
		Payment: ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("1.25")},
	})
	if err != nil {
		t.Fatalf("append funding payment: %v", err)
	}
	if first.FactID == "" {
		t.Fatal("stable fact id is required")
	}

	again, err := journal.AppendFundingPayment(ctx, ledgerfacts.FundingPaymentFact{
		Scope:      scope,
		Provenance: first.Provenance,
		Payment:    first.Payment,
	})
	if err != nil {
		t.Fatalf("idempotent append: %v", err)
	}
	if again.FactID != first.FactID {
		t.Fatalf("idempotent append fact id=%q, want %q", again.FactID, first.FactID)
	}

	_, err = journal.AppendFundingPayment(ctx, ledgerfacts.FundingPaymentFact{
		Scope:      scope,
		Provenance: first.Provenance,
		Payment:    ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("9.99")},
	})
	if !errors.Is(err, ledgerfacts.ErrFactConflict) {
		t.Fatalf("conflicting idempotency append error=%v, want ErrFactConflict", err)
	}

	revised, err := journal.AppendSupersedingFundingPayment(ctx, first.FactID, ledgerfacts.FundingPaymentFact{
		Scope: scope,
		Provenance: ledgerfacts.Provenance{
			SourceRef:      "gateway:payment:1",
			SourceRevision: "2",
			SourceVersion:  "gateway-v1",
			ObservedAt:     observed.Add(time.Minute),
			EffectiveAt:    observed.Add(-time.Hour),
		},
		Payment: ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("1.35")},
	})
	if err != nil {
		t.Fatalf("append superseding fact: %v", err)
	}
	if revised.SupersedesFactID != first.FactID {
		t.Fatalf("supersedes=%q, want %q", revised.SupersedesFactID, first.FactID)
	}
	if _, err := store.Exec(ctx, "update ledger_funding_payment_facts set source_revision='3' where fact_id=$1", first.FactID); !errors.Is(err, ledgerfacts.ErrAppendOnlyViolation) {
		t.Fatalf("facts must reject update, got %v", err)
	}
	if _, err := journal.AppendSupersedingFundingPayment(ctx, revised.FactID, ledgerfacts.FundingPaymentFact{
		Scope: scope,
		Provenance: ledgerfacts.Provenance{
			SourceRef:      "gateway:payment:1",
			SourceRevision: "2",
			SourceVersion:  "gateway-v1",
			ObservedAt:     observed.Add(2 * time.Minute),
			EffectiveAt:    observed.Add(-time.Hour),
		},
		Payment: ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("1.45")},
	}); !errors.Is(err, ledgerfacts.ErrRevisionOrder) {
		t.Fatalf("non-increasing supersede revision error=%v, want ErrRevisionOrder", err)
	}
}

func TestFundingBatchRetryIgnoresWatermarkObservationTimeDrift(t *testing.T) {
	ctx := context.Background()
	journal := ledgerfacts.NewJournal(ledgerfacts.NewMemoryStore())
	scope := testScope()
	observed := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	batch := ledgerfacts.FundingIngestBatch{
		Scope: scope,
		Watermark: ledgerfacts.SyncWatermark{
			Scope:            scope,
			SourceRef:        "gateway:funding-payments",
			Cursor:           "",
			Revision:         "1",
			ObservedAt:       observed,
			EffectiveThrough: scope.WindowEnd,
			CompleteThrough:  scope.WindowEnd,
			Status:           ledgerfacts.CompleteEmpty,
			Verified:         true,
		},
	}

	_, first, err := journal.IngestFundingPayments(ctx, batch)
	if err != nil {
		t.Fatalf("initial ingest: %v", err)
	}
	batch.Watermark.ObservedAt = observed.Add(time.Minute)
	_, retried, err := journal.IngestFundingPayments(ctx, batch)
	if err != nil {
		t.Fatalf("retry with a later observation time: %v", err)
	}
	if retried.FactID != first.FactID || retried.SequenceID != first.SequenceID || !retried.ObservedAt.Equal(first.ObservedAt) {
		t.Fatalf("retry watermark=%+v, want original %+v", retried, first)
	}
}

func TestFundingBatchRetryIgnoresPaymentObservationTimeDrift(t *testing.T) {
	ctx := context.Background()
	journal := ledgerfacts.NewJournal(ledgerfacts.NewMemoryStore())
	scope := testScope()
	observed := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	batch := ledgerfacts.FundingIngestBatch{
		Scope: scope,
		Payments: []ledgerfacts.FundingPaymentFact{{
			PaymentID:        "payment-observation-retry",
			PositionID:       "position-observation-retry",
			Venue:            "bybit",
			VenueEnvironment: "demo",
			Product:          "perpetual",
			FormulaVersion:   "actual-funding-payment-v1",
			Scope:            scope,
			Provenance: ledgerfacts.Provenance{
				SourceRef:      "gateway:payment:observation-retry",
				SourceRevision: "1",
				SourceVersion:  "gateway-v1",
				ObservedAt:     observed,
				EffectiveAt:    observed.Add(-time.Hour),
			},
			Payment: ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("1.25")},
		}},
		Watermark: ledgerfacts.SyncWatermark{
			Scope:            scope,
			SourceRef:        "gateway:funding-payments",
			Revision:         "1",
			ObservedAt:       observed,
			EffectiveThrough: scope.WindowEnd,
			CompleteThrough:  scope.WindowEnd,
			Status:           ledgerfacts.Complete,
			Verified:         true,
		},
	}

	firstPayments, firstWatermark, err := journal.IngestFundingPayments(ctx, batch)
	if err != nil {
		t.Fatalf("initial ingest: %v", err)
	}
	batch.Payments[0].Provenance.ObservedAt = observed.Add(time.Minute)
	batch.Watermark.ObservedAt = observed.Add(time.Minute)
	retriedPayments, retriedWatermark, err := journal.IngestFundingPayments(ctx, batch)
	if err != nil {
		t.Fatalf("retry with later payment observation time: %v", err)
	}
	if len(retriedPayments) != 1 || retriedPayments[0].FactID != firstPayments[0].FactID ||
		retriedPayments[0].SequenceID != firstPayments[0].SequenceID ||
		!retriedPayments[0].Provenance.ObservedAt.Equal(firstPayments[0].Provenance.ObservedAt) {
		t.Fatalf("retried payments=%+v, want original %+v", retriedPayments, firstPayments)
	}
	if retriedWatermark.FactID != firstWatermark.FactID || retriedWatermark.SequenceID != firstWatermark.SequenceID {
		t.Fatalf("retried watermark=%+v, want original %+v", retriedWatermark, firstWatermark)
	}
}

func TestLedgerFactsSupersessionChainAllowsLatestAndRejectsFork(t *testing.T) {
	ctx := context.Background()
	journal := ledgerfacts.NewJournal(ledgerfacts.NewMemoryStore())
	scope := testScope()
	observed := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	first, err := journal.AppendFundingPayment(ctx, ledgerfacts.FundingPaymentFact{
		PaymentID:        "payment-1",
		Venue:            "binance",
		VenueEnvironment: "testnet",
		Product:          "perpetual",
		FormulaVersion:   "ledger-v5",
		Scope:            scope,
		Provenance:       ledgerfacts.Provenance{SourceRef: "payment-1", SourceRevision: "1", SourceVersion: "gateway-v1", ObservedAt: observed, EffectiveAt: observed},
		Payment:          ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("1")},
	})
	if err != nil {
		t.Fatal(err)
	}
	second, err := journal.AppendSupersedingFundingPayment(ctx, first.FactID, ledgerfacts.FundingPaymentFact{
		PaymentID:        "payment-1",
		Venue:            "binance",
		VenueEnvironment: "testnet",
		Product:          "perpetual",
		FormulaVersion:   "ledger-v5",
		Scope:            scope,
		Provenance:       ledgerfacts.Provenance{SourceRef: "payment-1", SourceRevision: "2", SourceVersion: "gateway-v1", ObservedAt: observed, EffectiveAt: observed},
		Payment:          ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("2")},
	})
	if err != nil {
		t.Fatal(err)
	}
	third, err := journal.AppendSupersedingFundingPayment(ctx, second.FactID, ledgerfacts.FundingPaymentFact{
		PaymentID:        "payment-1",
		Venue:            "binance",
		VenueEnvironment: "testnet",
		Product:          "perpetual",
		FormulaVersion:   "ledger-v5",
		Scope:            scope,
		Provenance:       ledgerfacts.Provenance{SourceRef: "payment-1", SourceRevision: "3", SourceVersion: "gateway-v1", ObservedAt: observed, EffectiveAt: observed},
		Payment:          ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("3")},
	})
	if err != nil {
		t.Fatalf("valid supersession chain rejected: %v", err)
	}
	if third.SupersedesFactID != second.FactID {
		t.Fatalf("third supersedes=%q, want %q", third.SupersedesFactID, second.FactID)
	}
	if _, err := journal.AppendSupersedingFundingPayment(ctx, second.FactID, ledgerfacts.FundingPaymentFact{
		PaymentID:        "payment-1",
		Venue:            "binance",
		VenueEnvironment: "testnet",
		Product:          "perpetual",
		FormulaVersion:   "ledger-v5",
		Scope:            scope,
		Provenance:       ledgerfacts.Provenance{SourceRef: "payment-1", SourceRevision: "4", SourceVersion: "gateway-v1", ObservedAt: observed, EffectiveAt: observed},
		Payment:          ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("4")},
	}); !errors.Is(err, ledgerfacts.ErrFactConflict) {
		t.Fatalf("successor fork error=%v, want ErrFactConflict", err)
	}
}

func TestLedgerFactsSupersessionForFillPositionAndLineageRevision(t *testing.T) {
	ctx := context.Background()
	journal := ledgerfacts.NewJournal(ledgerfacts.NewMemoryStore())
	scope := testScope()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	firstFill, err := journal.AppendFillCashflow(ctx, ledgerfacts.FillCashflowFact{
		Scope:          scope,
		PositionID:     "position-1",
		FillID:         "fill-1",
		Provenance:     ledgerfacts.Provenance{SourceRef: "fill-1", SourceRevision: "1", SourceVersion: "gateway-v1", ObservedAt: now, EffectiveAt: now},
		Fee:            ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("0.20")},
		Rebate:         ledgerfacts.Money{Asset: "USDT", Amount: decimal.Zero},
		SourceAsset:    "USDT",
		ReportingAsset: "USDT",
	})
	if err != nil {
		t.Fatal(err)
	}
	secondFill, err := journal.AppendSupersedingFillCashflow(ctx, firstFill.FactID, ledgerfacts.FillCashflowFact{
		Scope:          scope,
		PositionID:     "position-1",
		FillID:         "fill-1",
		Provenance:     ledgerfacts.Provenance{SourceRef: "fill-1", SourceRevision: "2", SourceVersion: "gateway-v1", ObservedAt: now, EffectiveAt: now},
		Fee:            ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("0.10")},
		Rebate:         ledgerfacts.Money{Asset: "USDT", Amount: decimal.Zero},
		SourceAsset:    "USDT",
		ReportingAsset: "USDT",
	})
	if err != nil {
		t.Fatal(err)
	}
	if secondFill.SupersedesFactID != firstFill.FactID {
		t.Fatalf("fill supersedes=%q, want %q", secondFill.SupersedesFactID, firstFill.FactID)
	}

	firstPosition, err := journal.AppendPositionSnapshot(ctx, ledgerfacts.PositionSnapshotFact{
		Scope:                scope,
		PositionID:           "position-1",
		Provenance:           ledgerfacts.Provenance{SourceRef: "position-1", SourceRevision: "1", SourceVersion: "gateway-v1", ObservedAt: now, EffectiveAt: now},
		ReportingAsset:       "USDT",
		UnrealizedTradingPnl: decimal.RequireFromString("1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	secondPosition, err := journal.AppendSupersedingPositionSnapshot(ctx, firstPosition.FactID, ledgerfacts.PositionSnapshotFact{
		Scope:                scope,
		PositionID:           "position-1",
		Provenance:           ledgerfacts.Provenance{SourceRef: "position-1", SourceRevision: "2", SourceVersion: "gateway-v1", ObservedAt: now, EffectiveAt: now},
		ReportingAsset:       "USDT",
		UnrealizedTradingPnl: decimal.RequireFromString("2"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if secondPosition.SupersedesFactID != firstPosition.FactID {
		t.Fatalf("position supersedes=%q, want %q", secondPosition.SupersedesFactID, firstPosition.FactID)
	}

	lineage, err := journal.AcquireLineage(ctx, ledgerfacts.LineageFact{
		Scope:            scope,
		LineageID:        "lineage-1",
		PositionID:       "position-1",
		VenueEnvironment: "testnet",
		Product:          "perpetual",
		Provenance:       ledgerfacts.Provenance{SourceRef: "lineage-1", SourceRevision: "1", SourceVersion: "gateway-v1", ObservedAt: now, EffectiveAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	firstRevision, err := journal.AppendLineageRevision(ctx, ledgerfacts.LineageRevisionFact{
		Scope:         scope,
		LineageFactID: lineage.FactID,
		Revision:      "1",
		Status:        "verified",
		Provenance:    ledgerfacts.Provenance{SourceRef: "lineage-revision-1", SourceRevision: "1", SourceVersion: "gateway-v1", ObservedAt: now, EffectiveAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	secondRevision, err := journal.AppendSupersedingLineageRevision(ctx, firstRevision.FactID, ledgerfacts.LineageRevisionFact{
		Scope:         scope,
		LineageFactID: lineage.FactID,
		Revision:      "2",
		Status:        "revised",
		Provenance:    ledgerfacts.Provenance{SourceRef: "lineage-revision-1", SourceRevision: "2", SourceVersion: "gateway-v1", ObservedAt: now, EffectiveAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	if secondRevision.SupersedesFactID != firstRevision.FactID {
		t.Fatalf("revision supersedes=%q, want %q", secondRevision.SupersedesFactID, firstRevision.FactID)
	}
}

func TestLedgerFactsRawAppendRejectsSupersessionBypass(t *testing.T) {
	ctx := context.Background()
	journal := ledgerfacts.NewJournal(ledgerfacts.NewMemoryStore())
	scope := testScope()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if _, err := journal.AppendFundingPayment(ctx, ledgerfacts.FundingPaymentFact{
		SupersedesFactID: "funding-old",
		Scope:            scope,
		Provenance:       ledgerfacts.Provenance{SourceRef: "funding", SourceRevision: "2", SourceVersion: "test", ObservedAt: now, EffectiveAt: now},
		Payment:          ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("1")},
	}); !errors.Is(err, ledgerfacts.ErrFactConflict) {
		t.Fatalf("raw funding supersession error=%v, want ErrFactConflict", err)
	}
	if _, err := journal.AppendFillCashflow(ctx, ledgerfacts.FillCashflowFact{
		SupersedesFactID: "fill-old",
		Scope:            scope,
		FillID:           "fill-1",
		Provenance:       ledgerfacts.Provenance{SourceRef: "fill", SourceRevision: "2", SourceVersion: "test", ObservedAt: now, EffectiveAt: now},
		Fee:              ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("1")},
		Rebate:           ledgerfacts.Money{Asset: "USDT", Amount: decimal.Zero},
		SourceAsset:      "USDT",
		ReportingAsset:   "USDT",
	}); !errors.Is(err, ledgerfacts.ErrFactConflict) {
		t.Fatalf("raw fill supersession error=%v, want ErrFactConflict", err)
	}
	if _, err := journal.AppendPositionSnapshot(ctx, ledgerfacts.PositionSnapshotFact{
		SupersedesFactID: "position-old",
		Scope:            scope,
		PositionID:       "position-1",
		Provenance:       ledgerfacts.Provenance{SourceRef: "position", SourceRevision: "2", SourceVersion: "test", ObservedAt: now, EffectiveAt: now},
		ReportingAsset:   "USDT",
	}); !errors.Is(err, ledgerfacts.ErrFactConflict) {
		t.Fatalf("raw position supersession error=%v, want ErrFactConflict", err)
	}
	if _, err := journal.AppendLineageRevision(ctx, ledgerfacts.LineageRevisionFact{
		SupersedesFactID: "revision-old",
		Scope:            scope,
		LineageFactID:    "lineage-missing",
		Revision:         "2",
		Status:           "verified",
		Provenance:       ledgerfacts.Provenance{SourceRef: "revision", SourceRevision: "2", SourceVersion: "test", ObservedAt: now, EffectiveAt: now},
	}); !errors.Is(err, ledgerfacts.ErrFactConflict) {
		t.Fatalf("raw revision supersession error=%v, want ErrFactConflict", err)
	}
}

func TestLedgerFactsAllClassIdempotencyAndMetadata(t *testing.T) {
	ctx := context.Background()
	journal := ledgerfacts.NewJournal(ledgerfacts.NewMemoryStore())
	scope := testScope()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	watermark := ledgerfacts.SyncWatermark{FactID: "wm-1"}
	funding := ledgerfacts.FundingPaymentFact{
		SyncWatermarkID:  watermark.FactID,
		PaymentID:        "payment-1",
		Venue:            "binance",
		VenueEnvironment: "testnet",
		Product:          "perpetual",
		FormulaVersion:   "ledger-v5",
		Scope:            scope,
		Provenance:       ledgerfacts.Provenance{SourceRef: "payment-1", SourceRevision: "1", SourceVersion: "gateway-v1", ObservedAt: now, EffectiveAt: now},
		Payment:          ledgerfacts.Money{Asset: "USDT", Amount: decimal.Zero},
	}
	if _, err := journal.AppendFundingPayment(ctx, funding); err != nil {
		t.Fatal(err)
	}
	fill := ledgerfacts.FillCashflowFact{
		SyncWatermarkID: watermark.FactID,
		PositionID:      "position-1",
		LineageID:       "lineage-1",
		SourceAsset:     "BNB",
		ReportingAsset:  "USDT",
		Conversion:      ledgerfacts.ConversionMetadata{Source: "mark", AsOf: now, RoundingMode: "half-even", Scale: 8, FormulaVersion: "ledger-v5"},
		Scope:           scope,
		FillID:          "fill-1",
		Provenance:      ledgerfacts.Provenance{SourceRef: "fill-1", SourceRevision: "1", SourceVersion: "gateway-v1", ObservedAt: now, EffectiveAt: now},
		Fee:             ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("0.20")},
		Rebate:          ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("0.05")},
	}
	if _, err := journal.AppendFillCashflow(ctx, fill); err != nil {
		t.Fatal(err)
	}
	position := ledgerfacts.PositionSnapshotFact{
		SyncWatermarkID:       watermark.FactID,
		Closed:                true,
		AsOf:                  now,
		Residual:              ledgerfacts.Residual{PendingCompensation: 0},
		Conversion:            ledgerfacts.ConversionMetadata{Source: "native", AsOf: now, RoundingMode: "none", Scale: 8, FormulaVersion: "ledger-v5"},
		Scope:                 scope,
		PositionID:            "position-1",
		Provenance:            ledgerfacts.Provenance{SourceRef: "position-1", SourceRevision: "1", SourceVersion: "gateway-v1", ObservedAt: now, EffectiveAt: now},
		ReportingAsset:        "USDT",
		RealizedTradingPnl:    decimal.RequireFromString("2"),
		UnrealizedTradingPnl:  decimal.Zero,
		ActualFundingCashFlow: decimal.RequireFromString("1"),
		Fees:                  decimal.RequireFromString("-0.15"),
		Verified:              true,
	}
	if _, err := journal.AppendPositionSnapshot(ctx, position); err != nil {
		t.Fatal(err)
	}
	lineage := ledgerfacts.LineageFact{
		VenueEnvironment: "testnet",
		Product:          "perpetual",
		Scope:            scope,
		LineageID:        "lineage-1",
		PositionID:       "position-1",
		Provenance:       ledgerfacts.Provenance{SourceRef: "lineage-1", SourceRevision: "1", SourceVersion: "gateway-v1", ObservedAt: now, EffectiveAt: now},
	}
	storedLineage, err := journal.AcquireLineage(ctx, lineage)
	if err != nil {
		t.Fatal(err)
	}
	revision := ledgerfacts.LineageRevisionFact{
		SyncWatermarkID: watermark.FactID,
		LineageFactID:   storedLineage.FactID,
		Scope:           scope,
		Revision:        "1",
		Status:          "verified",
		Provenance:      ledgerfacts.Provenance{SourceRef: "lineage-revision-1", SourceRevision: "1", SourceVersion: "gateway-v1", ObservedAt: now, EffectiveAt: now},
	}
	if _, err := journal.AppendLineageRevision(ctx, revision); err != nil {
		t.Fatal(err)
	}
	replayed, err := journal.Replay(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) < 5 {
		t.Fatalf("replay entries=%+v, want all fact classes", replayed)
	}
}

func TestFundingPaymentCompletenessScopes(t *testing.T) {
	ctx := context.Background()
	journal := ledgerfacts.NewJournal(ledgerfacts.NewMemoryStore())
	scope := testScope()
	observed := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)

	for i, status := range []ledgerfacts.CompletenessStatus{
		ledgerfacts.Unsupported,
		ledgerfacts.TemporarilyUnavailable,
		ledgerfacts.Incomplete,
		ledgerfacts.CompleteEmpty,
		ledgerfacts.Complete,
	} {
		watermark, err := journal.AppendSyncWatermark(ctx, ledgerfacts.SyncWatermark{
			Scope:            scope,
			SourceRef:        "gateway:" + string(status),
			Cursor:           "page-" + string(rune('1'+i)),
			Revision:         "1",
			ObservedAt:       observed,
			EffectiveThrough: observed,
			CompleteThrough:  observed.Add(-time.Minute),
			Status:           status,
			Verified:         false,
		})
		if err != nil {
			t.Fatalf("append watermark status %s: %v", status, err)
		}
		if watermark.Status != status {
			t.Fatalf("watermark status=%s, want %s", watermark.Status, status)
		}
	}

	if _, err := journal.AppendFundingPayment(ctx, ledgerfacts.FundingPaymentFact{
		Scope:      scope,
		Provenance: ledgerfacts.Provenance{SourceRef: "gateway:missing-cashflow", SourceRevision: "1", SourceVersion: "gateway-v1", ObservedAt: observed, EffectiveAt: observed},
	}); !errors.Is(err, ledgerfacts.ErrCompletenessViolation) {
		t.Fatalf("missing payment cashflow error=%v, want ErrCompletenessViolation", err)
	}

	boundPayments, boundWatermark, err := journal.IngestFundingPayments(ctx, ledgerfacts.FundingIngestBatch{
		Scope: scope,
		Payments: []ledgerfacts.FundingPaymentFact{{
			Scope:      scope,
			Provenance: ledgerfacts.Provenance{SourceRef: "gateway:payment-proof", SourceRevision: "7", SourceVersion: "gateway-v1", ObservedAt: observed, EffectiveAt: observed},
			Payment:    ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("0.50")},
		}},
		Watermark: ledgerfacts.SyncWatermark{
			Scope:            scope,
			SourceRef:        "gateway:funding-payments",
			Cursor:           "page-2",
			Revision:         "7",
			ObservedAt:       observed,
			EffectiveThrough: observed,
			CompleteThrough:  observed.Add(-time.Minute),
			Status:           ledgerfacts.Complete,
			Verified:         true,
		},
	})
	if err != nil {
		t.Fatalf("ingest verified complete funding batch: %v", err)
	}
	if len(boundPayments) != 1 || boundPayments[0].SyncWatermarkID != boundWatermark.FactID {
		t.Fatalf("funding payment watermark binding lost payments=%+v watermark=%+v", boundPayments, boundWatermark)
	}
	replayed, err := journal.Replay(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	foundBoundPayment := false
	for _, entry := range replayed {
		if entry.Funding != nil && entry.Funding.FactID == boundPayments[0].FactID {
			foundBoundPayment = entry.Funding.SyncWatermarkID == boundWatermark.FactID
		}
	}
	if !foundBoundPayment {
		t.Fatal("memory replay did not preserve funding payment watermark binding")
	}
	revisionScope := scope
	revisedPayments, revisedWatermark, err := journal.IngestFundingPayments(ctx, ledgerfacts.FundingIngestBatch{
		Scope: revisionScope,
		Payments: []ledgerfacts.FundingPaymentFact{{
			SupersedesFactID: boundPayments[0].FactID,
			Scope:            revisionScope,
			Provenance:       ledgerfacts.Provenance{SourceRef: "gateway:payment-proof", SourceRevision: "8", SourceVersion: "gateway-v1", ObservedAt: observed.Add(time.Minute), EffectiveAt: observed},
			Payment:          ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("0.75")},
		}},
		Watermark: ledgerfacts.SyncWatermark{
			Scope: revisionScope, SourceRef: "gateway:funding-payments", Cursor: "page-2-revised", Revision: "8",
			ObservedAt: observed.Add(time.Minute), EffectiveThrough: observed.Add(time.Minute), CompleteThrough: observed,
			Status: ledgerfacts.Complete, Verified: true,
		},
	})
	if err != nil {
		t.Fatalf("ingest superseding funding batch: %v", err)
	}
	if len(revisedPayments) != 1 || revisedPayments[0].SupersedesFactID != boundPayments[0].FactID || revisedPayments[0].SyncWatermarkID != revisedWatermark.FactID {
		t.Fatalf("superseding batch provenance mismatch payments=%+v watermark=%+v", revisedPayments, revisedWatermark)
	}
	_, watermark, err := journal.IngestFundingPayments(ctx, ledgerfacts.FundingIngestBatch{
		Scope:    scope,
		Payments: nil,
		Watermark: ledgerfacts.SyncWatermark{
			Scope:            scope,
			SourceRef:        "gateway:funding-payments-empty",
			Cursor:           "page-3",
			Revision:         "8",
			ObservedAt:       observed,
			EffectiveThrough: observed,
			CompleteThrough:  observed.Add(-time.Minute),
			Status:           ledgerfacts.CompleteEmpty,
			Verified:         true,
		},
	})
	if err != nil {
		t.Fatalf("ingest verified complete_empty funding batch: %v", err)
	}
	if _, _, err := journal.IngestFundingPayments(ctx, ledgerfacts.FundingIngestBatch{
		Scope:    scope,
		Payments: nil,
		Watermark: ledgerfacts.SyncWatermark{
			Scope:            scope,
			SourceRef:        "gateway:complete-without-items",
			Cursor:           "page-4",
			Revision:         "9",
			ObservedAt:       observed,
			EffectiveThrough: observed,
			CompleteThrough:  observed.Add(-time.Minute),
			Status:           ledgerfacts.Complete,
			Verified:         true,
		},
	}); !errors.Is(err, ledgerfacts.ErrCompletenessViolation) {
		t.Fatalf("complete without items error=%v, want ErrCompletenessViolation", err)
	}
	if _, _, err := journal.IngestFundingPayments(ctx, ledgerfacts.FundingIngestBatch{
		Scope: scope,
		Payments: []ledgerfacts.FundingPaymentFact{{
			Scope:      scope,
			Provenance: ledgerfacts.Provenance{SourceRef: "gateway:payment-empty-conflict", SourceRevision: "10", SourceVersion: "gateway-v1", ObservedAt: observed, EffectiveAt: observed},
			Payment:    ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("0.25")},
		}},
		Watermark: ledgerfacts.SyncWatermark{
			Scope:            scope,
			SourceRef:        "gateway:empty-with-items",
			Cursor:           "page-5",
			Revision:         "10",
			ObservedAt:       observed,
			EffectiveThrough: observed,
			CompleteThrough:  observed.Add(-time.Minute),
			Status:           ledgerfacts.CompleteEmpty,
			Verified:         true,
		},
	}); !errors.Is(err, ledgerfacts.ErrCompletenessViolation) {
		t.Fatalf("complete_empty with items error=%v, want ErrCompletenessViolation", err)
	}
	if _, err := journal.AppendSyncWatermark(ctx, ledgerfacts.SyncWatermark{
		Scope:            scope,
		SourceRef:        "gateway:bare-verified",
		Cursor:           "page-6",
		Revision:         "11",
		ObservedAt:       observed,
		EffectiveThrough: observed,
		CompleteThrough:  observed.Add(-time.Minute),
		Status:           ledgerfacts.Complete,
		Verified:         true,
	}); !errors.Is(err, ledgerfacts.ErrCompletenessViolation) {
		t.Fatalf("bare verified complete watermark error=%v, want ErrCompletenessViolation", err)
	}
	if watermark.FactID == "" || !watermark.Verified || watermark.Cursor != "page-3" || watermark.Revision != "8" {
		t.Fatalf("watermark provenance not preserved: %+v", watermark)
	}
}

func TestFundingBatchConflictIsAtomicInMemory(t *testing.T) {
	ctx := context.Background()
	journal := ledgerfacts.NewJournal(ledgerfacts.NewMemoryStore())
	scope := testScope()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if _, err := journal.AppendSyncWatermark(ctx, ledgerfacts.SyncWatermark{
		Scope: scope, SourceRef: "gateway:atomic", Cursor: "page-1", Revision: "1",
		ObservedAt: now, EffectiveThrough: now, CompleteThrough: now,
		Status: ledgerfacts.Incomplete,
	}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := journal.IngestFundingPayments(ctx, ledgerfacts.FundingIngestBatch{
		Scope: scope,
		Payments: []ledgerfacts.FundingPaymentFact{{
			Scope:      scope,
			Provenance: ledgerfacts.Provenance{SourceRef: "payment-must-rollback", SourceRevision: "1", SourceVersion: "gateway-v1", ObservedAt: now, EffectiveAt: now},
			Payment:    ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("1")},
		}},
		Watermark: ledgerfacts.SyncWatermark{
			Scope: scope, SourceRef: "gateway:atomic", Cursor: "page-1", Revision: "1",
			ObservedAt: now, EffectiveThrough: now, CompleteThrough: now,
			Status: ledgerfacts.Complete, Verified: true,
		},
	}); !errors.Is(err, ledgerfacts.ErrFactConflict) {
		t.Fatalf("conflicting watermark batch error=%v, want ErrFactConflict", err)
	}
	replayed, err := journal.Replay(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range replayed {
		if entry.Funding != nil && entry.Funding.Provenance.SourceRef == "payment-must-rollback" {
			t.Fatalf("failed atomic batch leaked funding fact: %+v", entry.Funding)
		}
	}
}

func TestLineageStableIDIncludesSourceRef(t *testing.T) {
	ctx := context.Background()
	journal := ledgerfacts.NewJournal(ledgerfacts.NewMemoryStore())
	scope := testScope()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	first, err := journal.AcquireLineage(ctx, ledgerfacts.LineageFact{
		Scope: scope, LineageID: "lineage-source", PositionID: "position-1",
		Provenance: ledgerfacts.Provenance{SourceRef: "source-a", SourceRevision: "1", SourceVersion: "execution-v1", ObservedAt: now, EffectiveAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := journal.ReleaseLineageWithRevision(ctx, first.FactID, ledgerfacts.LineageRevisionFact{
		Scope: scope, Revision: "2",
		Provenance: ledgerfacts.Provenance{SourceRef: "release-a", SourceRevision: "2", SourceVersion: "execution-v1", ObservedAt: now, EffectiveAt: now},
	}, now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	second, err := journal.AcquireLineage(ctx, ledgerfacts.LineageFact{
		Scope: scope, LineageID: "lineage-source", PositionID: "position-2",
		Provenance: ledgerfacts.Provenance{SourceRef: "source-b", SourceRevision: "1", SourceVersion: "execution-v1", ObservedAt: now, EffectiveAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	if second.FactID == first.FactID {
		t.Fatalf("lineage fact id ignored source ref: %q", first.FactID)
	}
}

func TestLedgerLineageLeaseAcquireRelease(t *testing.T) {
	ctx := context.Background()
	store := ledgerfacts.NewMemoryStore()
	journal := ledgerfacts.NewJournal(store)
	scope := testScope()
	opened, err := journal.AcquireLineage(ctx, ledgerfacts.LineageFact{
		Scope:      scope,
		LineageID:  "lineage-1",
		PositionID: "position-1",
		Provenance: ledgerfacts.Provenance{
			SourceRef:      "execution:open:1",
			SourceRevision: "1",
			SourceVersion:  "execution-v1",
			ObservedAt:     time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
			EffectiveAt:    time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		},
	})
	if err != nil {
		t.Fatalf("acquire lineage: %v", err)
	}
	if _, err := journal.AcquireLineage(ctx, ledgerfacts.LineageFact{
		Scope:      scope,
		LineageID:  "lineage-2",
		PositionID: "position-2",
		Provenance: opened.Provenance,
	}); !errors.Is(err, ledgerfacts.ErrActiveLineageExists) {
		t.Fatalf("second active lineage error=%v, want ErrActiveLineageExists", err)
	}
	if _, err := store.Exec(ctx, "update ledger_lineage_facts set position_id='tampered' where fact_id=$1", opened.FactID); !errors.Is(err, ledgerfacts.ErrAppendOnlyViolation) {
		t.Fatalf("lineage business update error=%v, want ErrAppendOnlyViolation", err)
	}
	if _, err := journal.ReleaseLineageWithRevision(ctx, opened.FactID, ledgerfacts.LineageRevisionFact{
		Scope:    scope,
		Revision: "2",
		Status:   "released",
		Reason:   "close verified",
		Provenance: ledgerfacts.Provenance{
			SourceRef:      "execution:close:1",
			SourceRevision: "2",
			SourceVersion:  "execution-v1",
			ObservedAt:     time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC),
			EffectiveAt:    time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC),
		},
	}, time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC)); err != nil {
		t.Fatalf("release lineage: %v", err)
	}
	if _, err := journal.AcquireLineage(ctx, ledgerfacts.LineageFact{
		Scope:      scope,
		LineageID:  "lineage-2",
		PositionID: "position-2",
		Provenance: ledgerfacts.Provenance{
			SourceRef:      "execution:open:2",
			SourceRevision: "1",
			SourceVersion:  "execution-v1",
			ObservedAt:     time.Date(2026, 8, 1, 14, 0, 0, 0, time.UTC),
			EffectiveAt:    time.Date(2026, 8, 1, 14, 0, 0, 0, time.UTC),
		},
	}); err != nil {
		t.Fatalf("acquire after release: %v", err)
	}
}

func TestLedgerLineageReleaseIsAtomicAndIdempotent(t *testing.T) {
	ctx := context.Background()
	journal := ledgerfacts.NewJournal(ledgerfacts.NewMemoryStore())
	scope := testScope()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	lineage, err := journal.AcquireLineage(ctx, ledgerfacts.LineageFact{
		Scope: scope, LineageID: "lineage-atomic", PositionID: "position-atomic",
		Provenance: ledgerfacts.Provenance{SourceRef: "open", SourceRevision: "1", SourceVersion: "execution-v1", ObservedAt: now, EffectiveAt: now},
	})
	if err != nil {
		t.Fatal(err)
	}
	revision := ledgerfacts.LineageRevisionFact{
		Scope: scope, Revision: "2", Status: "released",
		Provenance: ledgerfacts.Provenance{SourceRef: "close", SourceRevision: "2", SourceVersion: "execution-v1", ObservedAt: now.Add(time.Hour), EffectiveAt: now.Add(time.Hour)},
	}
	closedAt := now.Add(time.Hour)
	first, err := journal.ReleaseLineageWithRevision(ctx, lineage.FactID, revision, closedAt)
	if err != nil {
		t.Fatal(err)
	}
	retry, err := journal.ReleaseLineageWithRevision(ctx, lineage.FactID, revision, closedAt)
	if err != nil {
		t.Fatalf("idempotent release retry: %v", err)
	}
	if retry.FactID != first.FactID || retry.SequenceID != first.SequenceID {
		t.Fatalf("release retry did not return stored revision first=%+v retry=%+v", first, retry)
	}
	if _, err := journal.ReleaseLineageWithRevision(ctx, lineage.FactID, revision, closedAt.Add(time.Minute)); !errors.Is(err, ledgerfacts.ErrFactConflict) {
		t.Fatalf("conflicting closed_at retry error=%v, want ErrFactConflict", err)
	}
	replayed, err := journal.Replay(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	var revisions int
	for _, entry := range replayed {
		if entry.RevisionFact != nil && entry.RevisionFact.FactID == first.FactID {
			revisions++
		}
		if entry.Lineage != nil && entry.Lineage.FactID == lineage.FactID {
			if entry.Lineage.Active || !entry.Lineage.ClosedAt.Equal(closedAt) {
				t.Fatalf("lineage release state mismatch: %+v", entry.Lineage)
			}
		}
	}
	if revisions != 1 {
		t.Fatalf("release revision count=%d, want 1", revisions)
	}
}

func TestLedgerProjectionReplayOrder(t *testing.T) {
	ctx := context.Background()
	journal := ledgerfacts.NewJournal(ledgerfacts.NewMemoryStore())
	scope := testScope()
	base := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	appendFact := func(sourceRevision string, marker string) {
		t.Helper()
		_, err := journal.AppendFundingPayment(ctx, ledgerfacts.FundingPaymentFact{
			Scope: scope,
			Provenance: ledgerfacts.Provenance{
				SourceRef:      "gateway:" + sourceRevision,
				SourceRevision: sourceRevision,
				SourceVersion:  "gateway-v1",
				ObservedAt:     base,
				EffectiveAt:    base,
			},
			Payment: ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString(marker)},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	appendFact("10", "10")
	appendFact("2", "2")
	if _, err := journal.AppendSyncWatermark(ctx, ledgerfacts.SyncWatermark{
		Scope:            scope,
		SourceRef:        "gateway:funding-payments",
		Cursor:           "page-1",
		Revision:         "5",
		ObservedAt:       base,
		EffectiveThrough: base,
		CompleteThrough:  base,
		Status:           ledgerfacts.Complete,
		Verified:         false,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.AppendSyncWatermark(ctx, ledgerfacts.SyncWatermark{
		Scope:            scope,
		SourceRef:        "gateway:funding-payments",
		Cursor:           "page-2",
		Revision:         "11",
		ObservedAt:       base,
		EffectiveThrough: base,
		CompleteThrough:  base,
		Status:           ledgerfacts.Complete,
		Verified:         false,
	}); err != nil {
		t.Fatal(err)
	}

	replayed, err := journal.Replay(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(replayed))
	for _, entry := range replayed {
		got = append(got, entry.OrderKey)
	}
	want := []string{"fact:2", "watermark:5:page-1", "fact:10", "watermark:11:page-2"}
	if len(got) != len(want) {
		t.Fatalf("replay order=%v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("replay order=%v, want %v", got, want)
		}
	}
}

func TestLedgerFactsReplayExactScopeWindowAndTraversal(t *testing.T) {
	ctx := context.Background()
	journal := ledgerfacts.NewJournal(ledgerfacts.NewMemoryStore())
	scope := testScope()
	otherWindow := scope
	otherWindow.WindowEnd = otherWindow.WindowEnd.Add(time.Hour)
	otherTraversal := scope
	otherTraversal.PaginationTraversal = "reverse:all-pages"
	observed := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	for _, scoped := range []ledgerfacts.Scope{scope, otherWindow, otherTraversal} {
		_, err := journal.AppendFundingPayment(ctx, ledgerfacts.FundingPaymentFact{
			Scope: scoped,
			Provenance: ledgerfacts.Provenance{
				SourceRef:      "gateway:" + scoped.PaginationTraversal + scoped.WindowEnd.Format(time.RFC3339),
				SourceRevision: "1",
				SourceVersion:  "gateway-v1",
				ObservedAt:     observed,
				EffectiveAt:    observed,
			},
			Payment: ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("1")},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	replayed, err := journal.Replay(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 1 || replayed[0].Funding == nil || !ledgerfactsMoneyEqual(replayed[0].Funding.Payment, "1") {
		t.Fatalf("exact-scope replay leaked adjacent scope: %+v", replayed)
	}
}

func TestLedgerFactsReplayAllInstrumentsIsolation(t *testing.T) {
	ctx := context.Background()
	journal := ledgerfacts.NewJournal(ledgerfacts.NewMemoryStore())
	allScope := testScope()
	allScope.InstrumentID = ledgerfacts.AllInstruments
	oneScope := testScope()
	observed := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	if !allScope.AllInstruments() {
		t.Fatal("all-instruments scope must use the canonical sentinel")
	}
	for _, scoped := range []ledgerfacts.Scope{allScope, oneScope} {
		_, err := journal.AppendFundingPayment(ctx, ledgerfacts.FundingPaymentFact{
			Scope: scoped,
			Provenance: ledgerfacts.Provenance{
				SourceRef:      "gateway:" + scoped.InstrumentID,
				SourceRevision: "1",
				SourceVersion:  "gateway-v1",
				ObservedAt:     observed,
				EffectiveAt:    observed,
			},
			Payment: ledgerfacts.Money{Asset: "USDT", Amount: decimal.Zero},
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	replayed, err := journal.Replay(ctx, allScope)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 1 || replayed[0].Funding == nil || replayed[0].Funding.Scope.InstrumentID != ledgerfacts.AllInstruments {
		t.Fatalf("all-instruments replay leaked single instrument facts: %+v", replayed)
	}
}

func testScope() ledgerfacts.Scope {
	return ledgerfacts.Scope{
		PrincipalID:         "principal-1",
		Mode:                "test",
		AccountID:           "account-1",
		InstrumentID:        "BTC-USDT-PERP",
		WindowStart:         time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		WindowEnd:           time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		PaginationTraversal: "forward:all-pages",
	}
}

func ledgerfactsMoneyEqual(got ledgerfacts.Money, want string) bool {
	return got.Amount.Equal(decimal.RequireFromString(want))
}
