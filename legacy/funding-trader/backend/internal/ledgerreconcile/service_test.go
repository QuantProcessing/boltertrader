package ledgerreconcile

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerfacts"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerprojection"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgersync"
	"github.com/shopspring/decimal"
)

func TestServiceReconcilesCompleteFundingAndRebuildsAuthoritativePortfolio(t *testing.T) {
	ctx := context.Background()
	open, close := bridgeExecutions("test")
	factStore := ledgerfacts.NewMemoryStore()
	journal := ledgerfacts.NewJournal(factStore)
	projectionStore := ledgerprojection.NewMemoryStore()
	syncer := &recordingSyncer{journal: journal, status: ledgerfacts.Complete, verified: true, fundingAmount: decimal.RequireFromString("1.25")}
	service := NewService(syncer, journal, ledgerprojection.NewProjector(projectionStore))

	result, err := service.ReconcileClosedPosition(ctx, open, close, "USDT")
	if err != nil {
		t.Fatal(err)
	}

	if len(syncer.targets) != 2 || len(result.Watermarks) != 2 || len(result.Closed) != 1 {
		t.Fatalf("sync/rebuild counts targets=%d watermarks=%d closed=%d", len(syncer.targets), len(result.Watermarks), len(result.Closed))
	}
	closed := result.Closed[0]
	if !closed.Verified || closed.VerificationStatus != "verified" {
		t.Fatalf("closed projection verification = %s/%v", closed.VerificationStatus, closed.Verified)
	}
	assertDecimal(t, closed.RealizedTradingPnl.Amount, "60")
	assertDecimal(t, closed.ActualFundingCashFlow.Amount, "2.50")
	assertDecimal(t, closed.Fees.Amount, "-0.42")
	assertDecimal(t, closed.NetPnl.Amount, "62.08")
	if result.Portfolio.VerificationStatus != "verified" || result.Portfolio.ClosedPositionCount != 1 {
		t.Fatalf("portfolio = %+v", result.Portfolio)
	}
	assertWatermarkBinding(t, journal, result.Watermarks)
}

func TestServiceReconcilesCompleteEmptyFundingAsVerifiedOnlyWhenWatermarkVerified(t *testing.T) {
	ctx := context.Background()
	open, close := bridgeExecutions("test")
	journal := ledgerfacts.NewJournal(ledgerfacts.NewMemoryStore())
	service := NewService(
		&recordingSyncer{journal: journal, status: ledgerfacts.CompleteEmpty, verified: true},
		journal,
		ledgerprojection.NewProjector(ledgerprojection.NewMemoryStore()),
	)

	result, err := service.ReconcileClosedPosition(ctx, open, close, "USDT")
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Closed) != 1 || !result.Closed[0].Verified {
		t.Fatalf("complete_empty should verify with bound empty funding watermark: %+v", result.Closed)
	}
	assertDecimal(t, result.Closed[0].ActualFundingCashFlow.Amount, "0")
	assertDecimal(t, result.Closed[0].NetPnl.Amount, "59.58")

	unverifiedJournal := ledgerfacts.NewJournal(ledgerfacts.NewMemoryStore())
	unverified := NewService(
		&recordingSyncer{journal: unverifiedJournal, status: ledgerfacts.CompleteEmpty, verified: false},
		unverifiedJournal,
		ledgerprojection.NewProjector(ledgerprojection.NewMemoryStore()),
	)
	result, err = unverified.ReconcileClosedPosition(ctx, open, close, "USDT")
	if !errors.Is(err, ErrFundingNotFinal) {
		t.Fatalf("complete_empty unverified error=%v, want %v", err, ErrFundingNotFinal)
	}
	if len(result.Closed) != 1 || result.Closed[0].Verified || result.Closed[0].VerificationStatus == "verified" {
		t.Fatalf("unverified watermark was promoted: %+v", result.Closed)
	}
	if result.Closed[0].VerificationStatus != "provisional" || result.Portfolio.VerificationStatus != "provisional" {
		t.Fatalf("unverified complete_empty should be provisional: closed=%+v portfolio=%+v", result.Closed, result.Portfolio)
	}
}

func TestServiceProjectsUnsupportedIncompleteAndUnavailableAsNonVerified(t *testing.T) {
	for _, tc := range []struct {
		name       string
		status     ledgerfacts.CompletenessStatus
		wantStatus string
		syncErr    error
	}{
		{name: "unsupported", status: ledgerfacts.Unsupported, wantStatus: "unsupported"},
		{name: "incomplete", status: ledgerfacts.Incomplete, wantStatus: "incomplete"},
		{name: "unavailable", status: ledgerfacts.TemporarilyUnavailable, wantStatus: "temporarily_unavailable", syncErr: errors.New("gateway funding payments temporarily unavailable")},
	} {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			open, close := bridgeExecutions("test")
			journal := ledgerfacts.NewJournal(ledgerfacts.NewMemoryStore())
			projectionStore := ledgerprojection.NewMemoryStore()
			service := NewService(
				&recordingSyncer{journal: journal, status: tc.status, verified: false, err: tc.syncErr, returnWatermarkWithErr: tc.syncErr != nil},
				journal,
				ledgerprojection.NewProjector(projectionStore),
			)

			result, err := service.ReconcileClosedPosition(ctx, open, close, "USDT")
			if !errors.Is(err, ErrFundingNotFinal) {
				t.Fatalf("non-final error=%v, want %v", err, ErrFundingNotFinal)
			}
			if len(result.Closed) != 1 || result.Closed[0].Verified || result.Closed[0].VerificationStatus != tc.wantStatus {
				t.Fatalf("non-final projection status mismatch: %+v", result.Closed)
			}
			if result.Portfolio.VerificationStatus != tc.wantStatus || result.Portfolio.ClosedPositionCount != 1 {
				t.Fatalf("non-final portfolio status mismatch: %+v", result.Portfolio)
			}
			assertWatermarkBinding(t, journal, result.Watermarks)
		})
	}
}

func TestServiceHardSyncErrorWithoutWatermarkDoesNotProject(t *testing.T) {
	ctx := context.Background()
	open, close := bridgeExecutions("test")
	journal := ledgerfacts.NewJournal(ledgerfacts.NewMemoryStore())
	projectionStore := ledgerprojection.NewMemoryStore()
	service := NewService(
		&recordingSyncer{journal: journal, status: ledgerfacts.Complete, verified: false, err: errors.New("gateway down")},
		journal,
		ledgerprojection.NewProjector(projectionStore),
	)

	result, err := service.ReconcileClosedPosition(ctx, open, close, "USDT")
	if err == nil || errors.Is(err, ErrFundingNotFinal) {
		t.Fatalf("hard sync error=%v, want original hard error without %v", err, ErrFundingNotFinal)
	}
	if len(result.Closed) != 0 || result.Portfolio.ClosedPositionCount != 0 || len(result.Watermarks) != 0 {
		t.Fatalf("hard sync error returned projection: %+v", result)
	}
	if stored, ok := projectionStore.Portfolio("principal-1", "test", "USDT"); ok {
		t.Fatalf("hard sync error wrote portfolio: %+v", stored)
	}
}

func TestServiceRerunIsIdempotent(t *testing.T) {
	ctx := context.Background()
	open, close := bridgeExecutions("test")
	journal := ledgerfacts.NewJournal(ledgerfacts.NewMemoryStore())
	syncer := &recordingSyncer{journal: journal, status: ledgerfacts.Complete, verified: true, fundingAmount: decimal.RequireFromString("1.25")}
	service := NewService(syncer, journal, ledgerprojection.NewProjector(ledgerprojection.NewMemoryStore()))

	first, err := service.ReconcileClosedPosition(ctx, open, close, "USDT")
	if err != nil {
		t.Fatal(err)
	}
	second, err := service.ReconcileClosedPosition(ctx, open, close, "USDT")
	if err != nil {
		t.Fatal(err)
	}

	if len(syncer.targets) != 4 {
		t.Fatalf("sync target calls = %d, want two per run", len(syncer.targets))
	}
	if len(first.Closed) != 1 || len(second.Closed) != 1 ||
		first.Closed[0].PositionID != second.Closed[0].PositionID ||
		!first.Closed[0].NetPnl.Amount.Equal(second.Closed[0].NetPnl.Amount) ||
		first.Portfolio.ClosedPositionCount != second.Portfolio.ClosedPositionCount {
		t.Fatalf("rerun changed result first=%+v second=%+v", first, second)
	}
	assertWatermarkBinding(t, journal, second.Watermarks)
}

func TestServiceCommitLegFactsReusesExistingLogicalLineage(t *testing.T) {
	ctx := context.Background()
	open, close := bridgeExecutions("test")
	journal := ledgerfacts.NewJournal(ledgerfacts.NewMemoryStore())
	service := NewService(nil, journal, nil)

	bridge, err := BuildClosedPositionBridge(open, close, BuildOptions{ReportingAsset: "USDT"})
	if err != nil {
		t.Fatalf("initial bridge: %v", err)
	}
	long := bridge.Leg(execution.LegRoleLong)
	if long == nil {
		t.Fatal("missing long leg")
	}

	firstWatermark := ledgerfacts.SyncWatermark{
		Scope:            long.Scope,
		FactID:           "watermark-long-first",
		Status:           ledgerfacts.Complete,
		Verified:         true,
		SourceRef:        "source-1",
		Cursor:           "cursor-1",
		Revision:         "1",
		ObservedAt:       time.Now(),
		EffectiveThrough: long.Lineage.ClosedAt,
		CompleteThrough:  long.Lineage.ClosedAt,
	}
	if err := service.commitLegFacts(ctx, *long, firstWatermark); err != nil {
		t.Fatalf("initial commit: %v", err)
	}

	closeRetried := close
	closeRetried.ID = "close-2"
	retried, err := BuildClosedPositionBridge(open, closeRetried, BuildOptions{ReportingAsset: "USDT"})
	if err != nil {
		t.Fatalf("retried bridge: %v", err)
	}
	retriedLong := retried.Leg(execution.LegRoleLong)
	if retriedLong == nil {
		t.Fatal("missing retried long leg")
	}
	retriedWatermark := ledgerfacts.SyncWatermark{
		Scope:            retriedLong.Scope,
		FactID:           "watermark-long-second",
		Status:           ledgerfacts.Complete,
		Verified:         true,
		SourceRef:        "source-2",
		Cursor:           "cursor-2",
		Revision:         "2",
		ObservedAt:       time.Now(),
		EffectiveThrough: retriedLong.Lineage.ClosedAt,
		CompleteThrough:  retriedLong.Lineage.ClosedAt,
	}
	if err := service.commitLegFacts(ctx, *retriedLong, retriedWatermark); err != nil {
		t.Fatalf("retried commit: %v", err)
	}

	replayed, err := journal.Replay(ctx, retriedLong.Scope)
	if err != nil {
		t.Fatal(err)
	}
	lineages := 0
	for _, entry := range replayed {
		if entry.Lineage != nil && entry.Lineage.LineageID == long.Lineage.LineageID {
			lineages++
		}
	}
	if lineages != 1 {
		t.Fatalf("expected logical lineage to be reused, got %d lineage rows", lineages)
	}
}

func TestServiceRebuildUsesCompletePortfolioScopeManifestAcrossClosedPositions(t *testing.T) {
	ctx := context.Background()
	journal := ledgerfacts.NewJournal(ledgerfacts.NewMemoryStore())
	syncer := &recordingSyncer{journal: journal, status: ledgerfacts.CompleteEmpty, verified: true}
	service := NewService(syncer, journal, ledgerprojection.NewProjector(ledgerprojection.NewMemoryStore()))
	openOne, closeOne := bridgeExecutions("test")
	if _, err := service.ReconcileClosedPosition(ctx, openOne, closeOne, "USDT"); err != nil {
		t.Fatal(err)
	}
	openTwo, closeTwo := secondBridgeExecutions()
	second, err := service.ReconcileClosedPosition(ctx, openTwo, closeTwo, "USDT")
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Scopes) != 4 || len(second.Closed) != 2 || second.Portfolio.ClosedPositionCount != 2 {
		t.Fatalf("complete manifest was not rebuilt: scopes=%d closed=%d portfolio=%+v", len(second.Scopes), len(second.Closed), second.Portfolio)
	}
}

func secondBridgeExecutions() (execution.Execution, execution.Execution) {
	open, close := bridgeExecutions("test")
	open.ID = "open-2"
	open.PositionID = "position-2"
	open.LineageID = "lineage-open-2"
	open.CreatedAt = open.CreatedAt.Add(24 * time.Hour)
	open.UpdatedAt = open.UpdatedAt.Add(24 * time.Hour)
	close.ID = "close-2"
	close.PositionID = open.PositionID
	close.ParentExecutionID = open.ID
	close.LineageID = open.LineageID
	close.CreatedAt = close.CreatedAt.Add(24 * time.Hour)
	close.UpdatedAt = close.UpdatedAt.Add(24 * time.Hour)
	for index := range open.Legs {
		open.Legs[index].LegID += "-2"
		open.Fills[index].LegID = open.Legs[index].LegID
		open.Fills[index].FillID += "-2"
		open.Fills[index].At = open.Fills[index].At.Add(24 * time.Hour)
		close.Legs[index].LegID += "-2"
		close.Fills[index].LegID = close.Legs[index].LegID
		close.Fills[index].FillID += "-2"
		close.Fills[index].At = close.Fills[index].At.Add(24 * time.Hour)
	}
	return open, close
}

func TestServiceRejectsWatermarkScopeDrift(t *testing.T) {
	ctx := context.Background()
	open, close := bridgeExecutions("test")
	journal := ledgerfacts.NewJournal(ledgerfacts.NewMemoryStore())
	service := NewService(
		&recordingSyncer{journal: journal, status: ledgerfacts.CompleteEmpty, verified: true, driftScope: true},
		journal,
		ledgerprojection.NewProjector(ledgerprojection.NewMemoryStore()),
	)

	_, err := service.ReconcileClosedPosition(ctx, open, close, "USDT")
	if !errors.Is(err, ErrWatermarkScopeMismatch) {
		t.Fatalf("scope drift error=%v, want %v", err, ErrWatermarkScopeMismatch)
	}
}

type recordingSyncer struct {
	journal                *ledgerfacts.Journal
	status                 ledgerfacts.CompletenessStatus
	verified               bool
	fundingAmount          decimal.Decimal
	err                    error
	returnWatermarkWithErr bool
	driftScope             bool
	targets                []ledgersync.SyncTarget
}

func (s *recordingSyncer) SyncTarget(ctx context.Context, target ledgersync.SyncTarget) (ledgersync.SyncResult, error) {
	s.targets = append(s.targets, target)
	if s.err != nil && !s.returnWatermarkWithErr {
		return ledgersync.SyncResult{}, s.err
	}
	scope := ledgerfacts.Scope{
		PrincipalID:         target.PrincipalID,
		Mode:                target.Mode,
		AccountID:           target.AccountID,
		InstrumentID:        target.InstrumentID,
		WindowStart:         target.WindowStart.UTC(),
		WindowEnd:           target.WindowEnd.UTC(),
		PaginationTraversal: "gateway_funding_payments:v1:test-" + target.AccountID,
	}
	if s.driftScope {
		scope.AccountID += "-drift"
	}
	watermark := ledgerfacts.SyncWatermark{
		Scope:            scope,
		SourceRef:        "gateway:funding-payments",
		Cursor:           "cursor-" + target.AccountID,
		Revision:         "7",
		ObservedAt:       target.WindowEnd.Add(-time.Minute),
		EffectiveThrough: target.WindowEnd,
		CompleteThrough:  target.WindowEnd,
		Status:           s.status,
		Verified:         s.verified,
	}
	if s.status == ledgerfacts.Complete || s.status == ledgerfacts.CompleteEmpty {
		payments := []ledgerfacts.FundingPaymentFact(nil)
		payment := ledgerfacts.FundingPaymentFact{
			PaymentID:        "payment-" + target.AccountID,
			PositionID:       target.PositionID,
			Venue:            "gateway",
			VenueEnvironment: "test",
			Product:          target.InstrumentID,
			FormulaVersion:   "upstream",
			Scope:            scope,
			Provenance: ledgerfacts.Provenance{
				SourceRef:      "payment-ref-" + target.AccountID,
				SourceRevision: "7",
				SourceVersion:  "test",
				ObservedAt:     target.WindowEnd.Add(-time.Minute),
				EffectiveAt:    target.WindowEnd,
			},
			Payment: ledgerfacts.Money{Asset: target.SettlementAsset, Amount: s.fundingAmount},
		}
		if s.status == ledgerfacts.Complete {
			payments = []ledgerfacts.FundingPaymentFact{payment}
		}
		_, committed, err := s.journal.IngestFundingPayments(ctx, ledgerfacts.FundingIngestBatch{
			Scope:     scope,
			Payments:  payments,
			Watermark: watermark,
		})
		if err != nil {
			return ledgersync.SyncResult{}, err
		}
		return ledgersync.SyncResult{Status: committed.Status, Verified: committed.Verified, Payments: len(payments), Watermark: committed}, s.err
	}
	committed, err := s.journal.AppendSyncWatermark(ctx, watermark)
	if err != nil {
		return ledgersync.SyncResult{}, err
	}
	return ledgersync.SyncResult{Status: committed.Status, Verified: committed.Verified, Watermark: committed}, s.err
}

func assertWatermarkBinding(t *testing.T, journal *ledgerfacts.Journal, watermarks []ledgerfacts.SyncWatermark) {
	t.Helper()
	for _, watermark := range watermarks {
		entries, err := journal.Replay(context.Background(), watermark.Scope)
		if err != nil {
			t.Fatal(err)
		}
		var fills, positions, revisions int
		for _, entry := range entries {
			switch {
			case entry.Fill != nil:
				fills++
				if entry.Fill.SyncWatermarkID != watermark.FactID || !sameTestScope(entry.Fill.Scope, watermark.Scope) {
					t.Fatalf("fill not bound to exact watermark: fill=%+v watermark=%+v", entry.Fill, watermark)
				}
			case entry.Position != nil:
				positions++
				if entry.Position.SyncWatermarkID != watermark.FactID || !sameTestScope(entry.Position.Scope, watermark.Scope) {
					t.Fatalf("position not bound to exact watermark: position=%+v watermark=%+v", entry.Position, watermark)
				}
			case entry.RevisionFact != nil:
				revisions++
				if entry.RevisionFact.SyncWatermarkID != watermark.FactID || !sameTestScope(entry.RevisionFact.Scope, watermark.Scope) {
					t.Fatalf("revision not bound to exact watermark: revision=%+v watermark=%+v", entry.RevisionFact, watermark)
				}
			}
		}
		if fills != 2 || positions != 1 || revisions != 1 {
			t.Fatalf("bound fact counts for %s: fills=%d positions=%d revisions=%d entries=%s", watermark.Scope.AccountID, fills, positions, revisions, describeEntries(entries))
		}
	}
}

func sameTestScope(left, right ledgerfacts.Scope) bool {
	return left.PrincipalID == right.PrincipalID &&
		left.Mode == right.Mode &&
		left.AccountID == right.AccountID &&
		left.InstrumentID == right.InstrumentID &&
		left.WindowStart.Equal(right.WindowStart) &&
		left.WindowEnd.Equal(right.WindowEnd) &&
		left.PaginationTraversal == right.PaginationTraversal
}

func describeEntries(entries []ledgerfacts.ReplayEntry) string {
	return fmt.Sprintf("%d entries at %s", len(entries), time.Now().UTC().Format(time.RFC3339))
}
