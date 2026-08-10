package postgres

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerfacts"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerprojection"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

func TestLedgerStorePostgresAppendOnlyDuplicateReplayAndProjection(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	installLedgerV5ForTest(t, ctx, pool)

	facts := ledgerfacts.NewPostgresStore(pool)
	scope := ledgerStoreTestScope()
	batch := ledgerfacts.FundingIngestBatch{
		Scope: scope,
		Payments: []ledgerfacts.FundingPaymentFact{{
			Scope:            scope,
			PositionID:       "position-1",
			Venue:            "bybit",
			VenueEnvironment: "testnet",
			Product:          "perpetual",
			FormulaVersion:   "ledger-v5",
			Provenance:       ledgerStoreTestProvenance("payment-1", "1"),
			Payment:          ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("1.25")},
		}},
		Watermark: ledgerfacts.SyncWatermark{
			Scope:            scope,
			SourceRef:        "gateway:funding",
			Cursor:           "page-1",
			Revision:         "1",
			ObservedAt:       time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
			EffectiveThrough: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
			CompleteThrough:  time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
			Status:           ledgerfacts.Complete,
			Verified:         true,
		},
	}
	inserted, watermark, err := facts.IngestFundingPayments(ctx, batch)
	if err != nil {
		t.Fatalf("ingest funding batch: %v", err)
	}
	if _, _, err := facts.IngestFundingPayments(ctx, batch); err != nil {
		t.Fatalf("idempotent restart ingest: %v", err)
	}
	retry := batch
	retry.Watermark.ObservedAt = retry.Watermark.ObservedAt.Add(time.Minute)
	_, retriedWatermark, err := facts.IngestFundingPayments(ctx, retry)
	if err != nil {
		t.Fatalf("restart ingest with a later watermark observation time: %v", err)
	}
	if retriedWatermark.FactID != watermark.FactID || retriedWatermark.SequenceID != watermark.SequenceID ||
		!retriedWatermark.ObservedAt.Equal(watermark.ObservedAt) {
		t.Fatalf("retried watermark=%+v, want original %+v", retriedWatermark, watermark)
	}
	paymentRetry := batch
	paymentRetry.Payments = append([]ledgerfacts.FundingPaymentFact(nil), batch.Payments...)
	paymentRetry.Payments[0].Provenance.ObservedAt = paymentRetry.Payments[0].Provenance.ObservedAt.Add(time.Minute)
	paymentRetry.Watermark.ObservedAt = paymentRetry.Watermark.ObservedAt.Add(time.Minute)
	retriedPayments, _, err := facts.IngestFundingPayments(ctx, paymentRetry)
	if err != nil {
		t.Fatalf("restart ingest with a later payment observation time: %v", err)
	}
	if len(retriedPayments) != 1 || retriedPayments[0].FactID != inserted[0].FactID ||
		retriedPayments[0].SequenceID != inserted[0].SequenceID ||
		!retriedPayments[0].Provenance.ObservedAt.Equal(inserted[0].Provenance.ObservedAt) {
		t.Fatalf("retried payments=%+v, want original %+v", retriedPayments, inserted)
	}
	batch.Payments[0].Payment.Amount = decimal.RequireFromString("9.99")
	if _, _, err := facts.IngestFundingPayments(ctx, batch); !errors.Is(err, ledgerfacts.ErrFactConflict) {
		t.Fatalf("conflicting duplicate error=%v, want ErrFactConflict", err)
	}
	if _, err := pool.Exec(ctx, "update ledger_funding_payment_facts set amount=0 where fact_id=$1", inserted[0].FactID); err == nil {
		t.Fatal("expected append-only update trigger to reject funding fact update")
	}
	if _, err := pool.Exec(ctx, "delete from ledger_sync_watermarks where fact_id=$1", watermark.FactID); err == nil {
		t.Fatal("expected append-only delete trigger to reject watermark delete")
	}
	replayed, err := ledgerfacts.NewJournal(facts).Replay(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	fillFact := ledgerfacts.FillCashflowFact{
		Scope:          scope,
		PositionID:     "position-1",
		LineageID:      "lineage-replay",
		FillID:         "fill-1",
		Provenance:     ledgerStoreTestProvenance("fill-1", "2"),
		Fee:            ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("0.20")},
		Rebate:         ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("0.05")},
		SourceAsset:    "USDT",
		ReportingAsset: "USDT",
		Conversion:     ledgerfacts.ConversionMetadata{Source: "native", AsOf: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC), RoundingMode: "half-even", Scale: 8, FormulaVersion: "ledger-v5"},
	}
	storedFill, err := ledgerfacts.NewJournal(facts).AppendFillCashflow(ctx, fillFact)
	if err != nil {
		t.Fatalf("append fill: %v", err)
	}
	retriedFill, err := ledgerfacts.NewJournal(facts).AppendFillCashflow(ctx, fillFact)
	if err != nil {
		t.Fatalf("retry fill: %v", err)
	}
	if retriedFill.SequenceID != storedFill.SequenceID || retriedFill.ReportingAsset != storedFill.ReportingAsset {
		t.Fatalf("fill retry did not return stored row first=%+v retry=%+v", storedFill, retriedFill)
	}
	positionFact := ledgerfacts.PositionSnapshotFact{
		Scope:                 scope,
		PositionID:            "position-1",
		Provenance:            ledgerStoreTestProvenance("position-1", "3"),
		Closed:                true,
		AsOf:                  time.Date(2026, 8, 1, 12, 5, 0, 0, time.UTC),
		ReportingAsset:        "USDT",
		RealizedTradingPnl:    decimal.RequireFromString("2"),
		UnrealizedTradingPnl:  decimal.Zero,
		ActualFundingCashFlow: decimal.RequireFromString("1.25"),
		Fees:                  decimal.RequireFromString("-0.20"),
		Verified:              true,
		Residual:              ledgerfacts.Residual{BlockerClass: ""},
		Conversion:            ledgerfacts.ConversionMetadata{Source: "native", AsOf: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC), RoundingMode: "half-even", Scale: 8, FormulaVersion: "ledger-v5"},
	}
	storedPosition, err := ledgerfacts.NewJournal(facts).AppendPositionSnapshot(ctx, positionFact)
	if err != nil {
		t.Fatalf("append position snapshot: %v", err)
	}
	retriedPosition, err := ledgerfacts.NewJournal(facts).AppendPositionSnapshot(ctx, positionFact)
	if err != nil {
		t.Fatalf("retry position snapshot: %v", err)
	}
	if retriedPosition.SequenceID != storedPosition.SequenceID || !retriedPosition.AsOf.Equal(storedPosition.AsOf) {
		t.Fatalf("position retry did not return stored row first=%+v retry=%+v", storedPosition, retriedPosition)
	}
	lineage, err := facts.AcquireLineage(ctx, ledgerfacts.LineageFact{
		Scope:            scope,
		LineageID:        "lineage-replay",
		PositionID:       "position-1",
		VenueEnvironment: "testnet",
		Product:          "perpetual",
		Provenance:       ledgerStoreTestProvenance("lineage-replay", "4"),
	})
	if err != nil {
		t.Fatalf("append lineage: %v", err)
	}
	revisionFact := ledgerfacts.LineageRevisionFact{
		Scope:         scope,
		LineageFactID: lineage.FactID,
		Revision:      "5",
		Status:        "verified",
		Provenance:    ledgerStoreTestProvenance("lineage-revision", "5"),
	}
	storedRevision, err := ledgerfacts.NewJournal(facts).AppendLineageRevision(ctx, revisionFact)
	if err != nil {
		t.Fatalf("append lineage revision: %v", err)
	}
	retriedRevision, err := ledgerfacts.NewJournal(facts).AppendLineageRevision(ctx, revisionFact)
	if err != nil {
		t.Fatalf("retry lineage revision: %v", err)
	}
	if retriedRevision.SequenceID != storedRevision.SequenceID || retriedRevision.LineageFactID != storedRevision.LineageFactID {
		t.Fatalf("lineage revision retry did not return stored row first=%+v retry=%+v", storedRevision, retriedRevision)
	}
	replayed, err = ledgerfacts.NewJournal(facts).Replay(ctx, scope)
	if err != nil {
		t.Fatal(err)
	}
	kinds := make(map[string]bool)
	for _, entry := range replayed {
		kinds[entry.Kind] = true
	}
	for _, want := range []string{"funding", "fill", "position", "lineage", "lineage_revision", "watermark"} {
		if !kinds[want] {
			t.Fatalf("replay missing %s: %+v", want, replayed)
		}
	}
	for _, entry := range replayed {
		if entry.Lineage != nil && (entry.Lineage.VenueEnvironment != "testnet" || entry.Lineage.Product != "perpetual") {
			t.Fatalf("lineage metadata not replayed: %+v", entry.Lineage)
		}
	}

	projections := ledgerprojection.NewPostgresStore(pool)
	closedItems, portfolio, err := projections.RebuildPortfolioFromReplay(ctx, ledgerprojection.AuthoritativePortfolioReplay{
		PrincipalID:    scope.PrincipalID,
		Mode:           scope.Mode,
		ReportingAsset: "USDT",
		Scopes:         []ledgerfacts.Scope{scope},
		Entries:        replayed,
	})
	if err != nil {
		t.Fatalf("rebuild projection from replay: %v", err)
	}
	if len(closedItems) != 1 {
		t.Fatalf("closed projection count=%d, want 1", len(closedItems))
	}
	closed := closedItems[0]
	if !closed.NetPnl.Amount.Equal(decimal.RequireFromString("3.10")) || !portfolio.CumulativeRealizedNetPnl.Amount.Equal(closed.NetPnl.Amount) {
		t.Fatalf("projection formula mismatch closed=%+v portfolio=%+v", closed, portfolio)
	}
	readClosed, ok, err := projections.ClosedPositionForScope(ctx, scope.PrincipalID, scope.Mode, "USDT", "position-1")
	if err != nil || !ok {
		t.Fatalf("read closed projection ok=%v err=%v", ok, err)
	}
	if !readClosed.AsOf.Equal(time.Date(2026, 8, 1, 12, 5, 0, 0, time.UTC)) ||
		readClosed.Conversion.ConversionSource != "native" ||
		readClosed.Conversion.FormulaVersion != "ledger-v5" ||
		readClosed.Watermark.Status != ledgerfacts.Complete ||
		!readClosed.Watermark.Verified {
		t.Fatalf("persisted closed metadata mismatch: %+v", readClosed)
	}
	stalePositionID := "stale-" + scope.PrincipalID
	if _, err := pool.Exec(ctx, `
		insert into ledger_closed_position_projections(
		  position_id, principal_id, mode, account_id, instrument_id, reporting_asset,
		  verified, verification_status
		) values($1,$2,$3,$4,$5,$6,false,'provisional')
	`, stalePositionID, scope.PrincipalID, scope.Mode, scope.AccountID, scope.InstrumentID, "USDT"); err != nil {
		t.Fatalf("seed stale derived projection: %v", err)
	}
	_, restarted, err := projections.RebuildPortfolioFromReplay(ctx, ledgerprojection.AuthoritativePortfolioReplay{
		PrincipalID:    scope.PrincipalID,
		Mode:           scope.Mode,
		ReportingAsset: "USDT",
		Scopes:         []ledgerfacts.Scope{scope},
		Entries:        replayed,
	})
	if err != nil {
		t.Fatalf("rebuild projection: %v", err)
	}
	if !restarted.CumulativeRealizedNetPnl.Amount.Equal(portfolio.CumulativeRealizedNetPnl.Amount) {
		t.Fatalf("restart rebuild double counted before=%+v after=%+v", portfolio, restarted)
	}
	if _, ok, err := projections.ClosedPositionForScope(ctx, scope.PrincipalID, scope.Mode, "USDT", stalePositionID); err != nil || ok {
		t.Fatalf("stale derived projection survived authoritative replay ok=%v err=%v", ok, err)
	}
	if _, ok, err := projections.ClosedPositionForScope(ctx, scope.PrincipalID, scope.Mode, "USDT", "position-1"); err != nil || !ok {
		t.Fatalf("authoritative replay lost persisted closed position ok=%v err=%v", ok, err)
	}
}

func TestLedgerStorePostgresBatchAndReleaseRollback(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	installLedgerV5ForTest(t, ctx, pool)
	facts := ledgerfacts.NewPostgresStore(pool)
	scope := ledgerStoreTestScope()
	if _, _, err := facts.IngestFundingPayments(ctx, ledgerfacts.FundingIngestBatch{
		Scope: scope,
		Payments: []ledgerfacts.FundingPaymentFact{{
			Scope:      scope,
			Provenance: ledgerStoreTestProvenance("should-rollback", "1"),
			Payment:    ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("1")},
		}, {
			Scope:      scope,
			Provenance: ledgerStoreTestProvenance("should-rollback", "1"),
			Payment:    ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("2")},
		}},
		Watermark: ledgerfacts.SyncWatermark{Scope: scope, SourceRef: "gateway:bad", Cursor: "bad", Revision: "1", Status: ledgerfacts.Complete, Verified: true},
	}); !errors.Is(err, ledgerfacts.ErrFactConflict) {
		t.Fatalf("bad batch error=%v, want ErrFactConflict", err)
	}
	var count int
	if err := pool.QueryRow(ctx, "select count(*) from ledger_funding_payment_facts").Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed batch left %d funding facts", count)
	}

	lineage, err := facts.AcquireLineage(ctx, ledgerfacts.LineageFact{
		Scope:      scope,
		LineageID:  "lineage-1",
		PositionID: "position-1",
		Provenance: ledgerStoreTestProvenance("lineage", "1"),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		create or replace function ledger_test_reject_lineage_release()
		returns trigger language plpgsql as $$
		begin
		  raise exception 'forced lineage release failure';
		end;
		$$;
		create trigger ledger_test_reject_lineage_release
		before update on ledger_lineage_facts
		for each row execute function ledger_test_reject_lineage_release()
	`); err != nil {
		t.Fatal(err)
	}
	failedRevision := ledgerfacts.LineageRevisionFact{
		Scope:      scope,
		Revision:   "rollback",
		Provenance: ledgerStoreTestProvenance("lineage-release-rollback", "rollback"),
	}
	if _, err := ledgerfacts.NewJournal(facts).ReleaseLineageWithRevision(
		ctx,
		lineage.FactID,
		failedRevision,
		time.Date(2026, 8, 1, 12, 30, 0, 0, time.UTC),
	); err == nil {
		t.Fatal("forced lineage release unexpectedly succeeded")
	}
	if err := pool.QueryRow(ctx, "select count(*) from ledger_lineage_revision_facts where lineage_fact_id=$1", lineage.FactID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 0 {
		t.Fatalf("failed journal release left %d revision facts, want 0", count)
	}
	var active bool
	if err := pool.QueryRow(ctx, "select active from ledger_lineage_facts where fact_id=$1", lineage.FactID).Scan(&active); err != nil {
		t.Fatal(err)
	}
	if !active {
		t.Fatal("failed journal release deactivated lineage")
	}
	if _, err := pool.Exec(ctx, `
		drop trigger ledger_test_reject_lineage_release on ledger_lineage_facts;
		drop function ledger_test_reject_lineage_release();
	`); err != nil {
		t.Fatal(err)
	}

	firstRelease, err := ledgerfacts.NewJournal(facts).ReleaseLineageWithRevision(ctx, lineage.FactID, ledgerfacts.LineageRevisionFact{
		Scope:    scope,
		Revision: "1",
		Provenance: ledgerfacts.Provenance{
			SourceRef:      "lineage-release",
			SourceRevision: "1",
			SourceVersion:  "test",
			ObservedAt:     time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC),
			EffectiveAt:    time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC),
		},
	}, time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("release lineage: %v", err)
	}
	retryRelease, err := ledgerfacts.NewJournal(facts).ReleaseLineageWithRevision(ctx, lineage.FactID, ledgerfacts.LineageRevisionFact{
		Scope:    scope,
		Revision: "1",
		Provenance: ledgerfacts.Provenance{
			SourceRef:      "lineage-release",
			SourceRevision: "1",
			SourceVersion:  "test",
			ObservedAt:     time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC),
			EffectiveAt:    time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC),
		},
	}, time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC))
	if err != nil {
		t.Fatalf("exact release retry should be idempotent: %v", err)
	}
	if retryRelease.FactID != firstRelease.FactID || retryRelease.SequenceID != firstRelease.SequenceID {
		t.Fatalf("release retry did not return stored row first=%+v retry=%+v", firstRelease, retryRelease)
	}
	if retryRelease.Status != "released" {
		t.Fatalf("release retry status=%q, want default released", retryRelease.Status)
	}
	if _, err := facts.ReleaseLineageWithRevision(ctx, lineage.FactID, ledgerfacts.LineageRevisionFact{
		Scope:    scope,
		Revision: "1",
		Provenance: ledgerfacts.Provenance{
			SourceRef:      "lineage-release",
			SourceRevision: "1",
			SourceVersion:  "test",
			ObservedAt:     time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC),
			EffectiveAt:    time.Date(2026, 8, 1, 13, 0, 0, 0, time.UTC),
		},
	}, time.Date(2026, 8, 1, 13, 1, 0, 0, time.UTC)); !errors.Is(err, ledgerfacts.ErrFactConflict) {
		t.Fatalf("conflicting release closed_at error=%v, want ErrFactConflict", err)
	}
	_, err = facts.ReleaseLineageWithRevision(ctx, lineage.FactID, ledgerfacts.LineageRevisionFact{
		Scope:    scope,
		Revision: "2",
		Status:   "released-again",
		Provenance: ledgerfacts.Provenance{
			SourceRef:      "lineage-release-second",
			SourceRevision: "2",
			SourceVersion:  "test",
			ObservedAt:     time.Date(2026, 8, 1, 14, 0, 0, 0, time.UTC),
			EffectiveAt:    time.Date(2026, 8, 1, 14, 0, 0, 0, time.UTC),
		},
	}, time.Date(2026, 8, 1, 14, 0, 0, 0, time.UTC))
	if !errors.Is(err, ledgerfacts.ErrFactConflict) {
		t.Fatalf("second release error=%v, want ErrFactConflict", err)
	}
	if err := pool.QueryRow(ctx, "select count(*) from ledger_lineage_revision_facts where lineage_fact_id=$1", lineage.FactID).Scan(&count); err != nil {
		t.Fatal(err)
	}
	if count != 1 {
		t.Fatalf("failed release left %d revision facts, want 1", count)
	}
}

func TestLedgerStorePostgresWatermarkCountMismatchRollsBack(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	installLedgerV5ForTest(t, ctx, pool)
	if _, err := pool.Exec(ctx, `
		create or replace function ledger_test_drop_sync_watermark()
		returns trigger language plpgsql as $$
		begin
		  new.sync_watermark_id = null;
		  return new;
		end;
		$$;
		create trigger ledger_test_drop_sync_watermark
		before insert on ledger_funding_payment_facts
		for each row
		when (new.source_ref = 'mismatch-payment')
		execute function ledger_test_drop_sync_watermark();
	`); err != nil {
		t.Fatal(err)
	}
	facts := ledgerfacts.NewPostgresStore(pool)
	scope := ledgerStoreTestScope()
	_, _, err = facts.IngestFundingPayments(ctx, ledgerfacts.FundingIngestBatch{
		Scope: scope,
		Payments: []ledgerfacts.FundingPaymentFact{{
			Scope:      scope,
			PositionID: "position-1",
			Provenance: ledgerfacts.Provenance{
				SourceRef:      "mismatch-payment",
				SourceRevision: "1",
				SourceVersion:  "test",
				ObservedAt:     time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
				EffectiveAt:    time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
			},
			Payment: ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("1")},
		}},
		Watermark: ledgerfacts.SyncWatermark{
			Scope:            scope,
			SourceRef:        "gateway:mismatch",
			Cursor:           "page-1",
			Revision:         "1",
			ObservedAt:       time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
			EffectiveThrough: time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
			CompleteThrough:  time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
			Status:           ledgerfacts.Complete,
			Verified:         true,
		},
	})
	if !errors.Is(err, ledgerfacts.ErrCompletenessViolation) {
		t.Fatalf("mismatched watermark count error=%v, want ErrCompletenessViolation", err)
	}
	var fundingCount, watermarkCount int
	if err := pool.QueryRow(ctx, "select count(*) from ledger_funding_payment_facts").Scan(&fundingCount); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "select count(*) from ledger_sync_watermarks").Scan(&watermarkCount); err != nil {
		t.Fatal(err)
	}
	if fundingCount != 0 || watermarkCount != 0 {
		t.Fatalf("mismatched batch was not rolled back funding=%d watermarks=%d", fundingCount, watermarkCount)
	}
}

func TestLedgerStorePostgresBatchValidatorRejectsBeforeWrites(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	installLedgerV5ForTest(t, ctx, pool)
	facts := ledgerfacts.NewPostgresStore(pool)
	scope := ledgerStoreTestScope()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	validWatermark := func(ref string, status ledgerfacts.CompletenessStatus, verified bool) ledgerfacts.SyncWatermark {
		return ledgerfacts.SyncWatermark{
			Scope:            scope,
			SourceRef:        ref,
			Cursor:           "page-1",
			Revision:         "1",
			ObservedAt:       now,
			EffectiveThrough: now,
			CompleteThrough:  now,
			Status:           status,
			Verified:         verified,
		}
	}
	validPayment := func(ref string) ledgerfacts.FundingPaymentFact {
		return ledgerfacts.FundingPaymentFact{
			Scope:      scope,
			PositionID: "position-1",
			Provenance: ledgerfacts.Provenance{
				SourceRef:      ref,
				SourceRevision: "1",
				SourceVersion:  "test",
				ObservedAt:     now,
				EffectiveAt:    now,
			},
			Payment: ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("1")},
		}
	}
	if _, _, err := facts.IngestFundingPayments(ctx, ledgerfacts.FundingIngestBatch{
		Scope:     scope,
		Payments:  []ledgerfacts.FundingPaymentFact{validPayment("historical")},
		Watermark: validWatermark("historical", ledgerfacts.Complete, true),
	}); err != nil {
		t.Fatal(err)
	}
	cases := []struct {
		name    string
		batch   ledgerfacts.FundingIngestBatch
		wantErr error
	}{
		{
			name: "payment scope mismatch",
			batch: func() ledgerfacts.FundingIngestBatch {
				payment := validPayment("bad-payment-scope")
				payment.Scope.AccountID = "other-account"
				return ledgerfacts.FundingIngestBatch{Scope: scope, Payments: []ledgerfacts.FundingPaymentFact{payment}, Watermark: validWatermark("bad-payment-scope", ledgerfacts.Complete, true)}
			}(),
			wantErr: ledgerfacts.ErrScopeMismatch,
		},
		{
			name: "watermark scope mismatch",
			batch: func() ledgerfacts.FundingIngestBatch {
				watermark := validWatermark("bad-watermark-scope", ledgerfacts.Complete, true)
				watermark.Scope.AccountID = "other-account"
				return ledgerfacts.FundingIngestBatch{Scope: scope, Payments: []ledgerfacts.FundingPaymentFact{validPayment("bad-watermark-scope")}, Watermark: watermark}
			}(),
			wantErr: ledgerfacts.ErrScopeMismatch,
		},
		{
			name:    "invalid status",
			batch:   ledgerfacts.FundingIngestBatch{Scope: scope, Payments: []ledgerfacts.FundingPaymentFact{validPayment("bad-status")}, Watermark: validWatermark("bad-status", ledgerfacts.CompletenessStatus("bogus"), false)},
			wantErr: ledgerfacts.ErrCompletenessViolation,
		},
		{
			name: "empty asset",
			batch: func() ledgerfacts.FundingIngestBatch {
				payment := validPayment("bad-asset")
				payment.Payment.Asset = ""
				return ledgerfacts.FundingIngestBatch{Scope: scope, Payments: []ledgerfacts.FundingPaymentFact{payment}, Watermark: validWatermark("bad-asset", ledgerfacts.Complete, true)}
			}(),
			wantErr: ledgerfacts.ErrCompletenessViolation,
		},
		{
			name:    "same scope historical complete empty",
			batch:   ledgerfacts.FundingIngestBatch{Scope: scope, Payments: nil, Watermark: validWatermark("historical-empty", ledgerfacts.CompleteEmpty, true)},
			wantErr: nil,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, _, err := facts.IngestFundingPayments(ctx, tc.batch)
			if tc.wantErr == nil {
				if err != nil {
					t.Fatalf("ingest error=%v, want nil", err)
				}
				return
			}
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ingest error=%v, want %v", err, tc.wantErr)
			}
		})
	}
}

func TestLedgerStorePostgresAuthenticatedReadQueriesStayScoped(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	installLedgerV5ForTest(t, ctx, pool)

	facts := ledgerfacts.NewPostgresStore(pool)
	scope := ledgerStoreTestScope()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	payment := func(id, source, revision, amount string, effectiveAt time.Time) ledgerfacts.FundingPaymentFact {
		provenance := ledgerStoreTestProvenance(source, revision)
		provenance.EffectiveAt = effectiveAt
		return ledgerfacts.FundingPaymentFact{
			Scope: scope, PositionID: "position-1", PaymentID: id,
			Provenance: provenance,
			Payment:    ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString(amount)},
		}
	}
	batch := ledgerfacts.FundingIngestBatch{
		Scope: scope,
		Payments: []ledgerfacts.FundingPaymentFact{
			payment("payment-1", "gateway-fact:source-1", "1", "1.25", now),
			payment("payment-2", "gateway-fact:source-2", "2", "-0.50", now),
			payment("payment-3", "gateway-fact:source-3", "3", "0.75", now.Add(-time.Minute)),
		},
		Watermark: ledgerfacts.SyncWatermark{
			Scope: scope, SourceRef: "gateway:funding", Cursor: "done", Revision: "1",
			ObservedAt: now, EffectiveThrough: now, CompleteThrough: now,
			Status: ledgerfacts.Complete, Verified: true,
		},
	}
	inserted, ingestedWatermark, err := facts.IngestFundingPayments(ctx, batch)
	if err != nil {
		t.Fatal(err)
	}

	items, err := facts.FundingPayments(ctx, ledgerfacts.FundingPaymentQuery{
		PrincipalID: scope.PrincipalID, Mode: scope.Mode, AccountID: scope.AccountID,
		InstrumentID: scope.InstrumentID, Limit: 2,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(items) != 2 || items[0].FactID != inserted[1].FactID || items[1].FactID != inserted[0].FactID {
		t.Fatalf("first keyset funding page=%+v", items)
	}
	secondPage, err := facts.FundingPayments(ctx, ledgerfacts.FundingPaymentQuery{
		PrincipalID: scope.PrincipalID, Mode: scope.Mode, AccountID: scope.AccountID,
		InstrumentID: scope.InstrumentID, Limit: 2,
		CursorEffectiveAt: items[1].Provenance.EffectiveAt,
		CursorSequenceID:  items[1].SequenceID,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(secondPage) != 1 || secondPage[0].FactID != inserted[2].FactID {
		t.Fatalf("second keyset funding page=%+v", secondPage)
	}
	queryWatermark, found, err := facts.SyncWatermarkForFundingQuery(ctx, ledgerfacts.FundingPaymentQuery{
		PrincipalID: scope.PrincipalID, Mode: scope.Mode, AccountID: scope.AccountID,
		InstrumentID: scope.InstrumentID, From: scope.WindowStart, To: scope.WindowEnd,
	})
	if err != nil || !found || queryWatermark.FactID != ingestedWatermark.FactID {
		t.Fatalf("exact query watermark=%+v found=%v err=%v", queryWatermark, found, err)
	}
	if _, _, err := facts.SyncWatermarkForFundingQuery(ctx, ledgerfacts.FundingPaymentQuery{
		PrincipalID: scope.PrincipalID, Mode: scope.Mode, AccountID: scope.AccountID,
		InstrumentID: scope.InstrumentID,
	}); !errors.Is(err, ledgerfacts.ErrScopeMismatch) {
		t.Fatalf("open-ended watermark query err=%v, want ErrScopeMismatch", err)
	}
	_, found, err = facts.SyncWatermarkForFundingQuery(ctx, ledgerfacts.FundingPaymentQuery{
		PrincipalID: scope.PrincipalID, Mode: scope.Mode, AccountID: scope.AccountID,
		InstrumentID: scope.InstrumentID, From: scope.WindowStart.Add(time.Hour), To: scope.WindowEnd,
	})
	if err != nil || found {
		t.Fatalf("mismatched query window found=%v err=%v", found, err)
	}
	other, err := facts.FundingPayments(ctx, ledgerfacts.FundingPaymentQuery{
		PrincipalID: "other-principal", Mode: scope.Mode, Limit: 10,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(other) != 0 {
		t.Fatalf("cross-principal funding facts leaked: %+v", other)
	}
	resolved, found, err := facts.ResolveFundingFactID(ctx, scope, "gateway-fact:source-1")
	if err != nil || !found || resolved != inserted[0].FactID {
		t.Fatalf("resolve funding fact id=%q found=%v err=%v", resolved, found, err)
	}

	unsupportedScope := scope
	unsupportedScope.InstrumentID = "ETH-USDT-PERP"
	unsupported, err := facts.AppendSyncWatermark(ctx, ledgerfacts.SyncWatermark{
		Scope: unsupportedScope, SourceRef: "gateway:funding", Revision: "2",
		ObservedAt: now, Status: ledgerfacts.Unsupported,
	})
	if err != nil {
		t.Fatal(err)
	}
	latest, found, err := facts.LatestSyncWatermark(ctx, unsupportedScope.PrincipalID, unsupportedScope.Mode, unsupportedScope.AccountID, unsupportedScope.InstrumentID)
	if err != nil || !found || latest.FactID != unsupported.FactID || latest.Status != ledgerfacts.Unsupported {
		t.Fatalf("latest watermark=%+v found=%v err=%v", latest, found, err)
	}

	if _, err := pool.Exec(ctx, `
		insert into ledger_closed_position_projections(
		  position_id, principal_id, mode, account_id, instrument_id, reporting_asset,
		  verified, verification_status
		) values
		  ('position-shared','principal-1','test','account-1','BTC-USDT-PERP','USDT',true,'verified'),
		  ('position-shared','other-principal','test','account-2','ETH-USDT-PERP','USDT',true,'verified'),
		  ('position-shared','principal-1','live','account-live','BTC-USDT-PERP','USDT',true,'verified'),
		  ('position-shared','principal-1','test','account-usd','BTC-USD-PERP','USD',true,'verified')
	`); err != nil {
		t.Fatal(err)
	}
	projections := ledgerprojection.NewPostgresStore(pool)
	closed, err := projections.ClosedPositions(ctx, "principal-1", "test", "USDT")
	if err != nil || len(closed) != 1 || closed[0].PositionID != "position-shared" || closed[0].Scope.AccountID != "account-1" {
		t.Fatalf("scoped closed positions=%+v err=%v", closed, err)
	}
	for _, query := range []struct {
		principalID string
		mode        string
		asset       string
		accountID   string
	}{
		{principalID: "principal-1", mode: "test", asset: "USDT", accountID: "account-1"},
		{principalID: "other-principal", mode: "test", asset: "USDT", accountID: "account-2"},
		{principalID: "principal-1", mode: "live", asset: "USDT", accountID: "account-live"},
		{principalID: "principal-1", mode: "test", asset: "USD", accountID: "account-usd"},
	} {
		item, found, err := projections.ClosedPositionForScope(ctx, query.principalID, query.mode, query.asset, "position-shared")
		if err != nil || !found || item.Scope.AccountID != query.accountID {
			t.Fatalf("exact scoped closed position query=%+v item=%+v found=%v err=%v", query, item, found, err)
		}
	}
	if _, found, err := projections.ClosedPositionForScope(ctx, "missing-principal", "test", "USDT", "position-shared"); err != nil || found {
		t.Fatalf("out-of-scope closed position found=%v err=%v", found, err)
	}
}

func installLedgerV5ForTest(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	dropLedgerFixture(t, ctx, pool)
	applyProductModeOnlyForTest(t, ctx, pool)
	if err := NewMigrationRunner(pool).Up(ctx); err != nil {
		t.Fatalf("apply v5: %v", err)
	}
}

func ledgerStoreTestScope() ledgerfacts.Scope {
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

func ledgerStoreTestProvenance(ref, revision string) ledgerfacts.Provenance {
	return ledgerfacts.Provenance{
		SourceRef:      ref,
		SourceRevision: revision,
		SourceVersion:  "test",
		ObservedAt:     time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		EffectiveAt:    time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
	}
}
