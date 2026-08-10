package ledgerprojection_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerfacts"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerprojection"
	"github.com/QuantProcessing/funding-trader/backend/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

func TestPostgresStoreExposesDurableProjectionContract(t *testing.T) {
	var pool *pgxpool.Pool
	store := ledgerprojection.NewPostgresStore(pool)
	if store == nil {
		t.Fatal("postgres projection store is required")
	}
	requireProjectionStore(t, store)
}

func requireProjectionStore(t *testing.T, store interface {
	RebuildPortfolioFromReplay(context.Context, ledgerprojection.AuthoritativePortfolioReplay) ([]ledgerprojection.ClosedPositionProjection, ledgerprojection.PortfolioProjection, error)
	ClosedPositionForScope(context.Context, string, string, string, string) (ledgerprojection.ClosedPositionProjection, bool, error)
	Portfolio(context.Context, string, string, string) (ledgerprojection.PortfolioProjection, bool, error)
	Watermark(context.Context, string, string, string) (ledgerprojection.Watermark, bool, error)
}) {
	t.Helper()
}

func TestPostgresStoreAuthoritativePortfolioReplayAtomicity(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := ensureProjectionTables(ctx, pool); err != nil {
		t.Fatal(err)
	}
	principalID := "projection-pg-" + time.Now().UTC().Format("20060102150405.000000000")
	cleanup := func() {
		_, _ = pool.Exec(ctx, `delete from ledger_closed_position_projections where principal_id=$1`, principalID)
		_, _ = pool.Exec(ctx, `delete from ledger_portfolio_projections where principal_id=$1`, principalID)
	}
	cleanup()
	t.Cleanup(cleanup)

	store := ledgerprojection.NewPostgresStore(pool)
	facts := ledgerfacts.NewPostgresStore(pool)
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	scopeA := pgProjectionScope(principalID, "account-1", "BTC-USDT-PERP")
	scopeB := pgProjectionScope(principalID, "account-2", "ETH-USDT-PERP")
	replayA := persistClosedProjectionReplay(t, ctx, facts, scopeA, "pg-position-1", now, "1", decimal.RequireFromString("1.25"))
	replayB := persistClosedProjectionReplay(t, ctx, facts, scopeB, "pg-position-2", now.Add(time.Minute), "2", decimal.RequireFromString("2.00"))
	full := ledgerprojection.AuthoritativePortfolioReplay{
		PrincipalID:    principalID,
		Mode:           "test",
		ReportingAsset: "USDT",
		Scopes:         []ledgerfacts.Scope{scopeA, scopeB},
		Entries:        append(append([]ledgerfacts.ReplayEntry(nil), replayA...), replayB...),
	}
	closed, portfolio, err := store.RebuildPortfolioFromReplay(ctx, full)
	if err != nil {
		t.Fatal(err)
	}
	if len(closed) != 2 || portfolio.ClosedPositionCount != 2 {
		t.Fatalf("authoritative rebuild did not persist both scopes closed=%d portfolio=%+v", len(closed), portfolio)
	}
	if _, ok, err := store.ClosedPositionForScope(ctx, principalID, "test", "USDT", "pg-position-2"); err != nil || !ok {
		t.Fatalf("second scope closed projection missing ok=%v err=%v", ok, err)
	}
	forged := full
	forged.Entries = append([]ledgerfacts.ReplayEntry(nil), full.Entries...)
	for i := range forged.Entries {
		if forged.Entries[i].Funding != nil {
			copy := *forged.Entries[i].Funding
			copy.Payment.Amount = decimal.RequireFromString("999999")
			forged.Entries[i].Funding = &copy
			break
		}
	}
	if _, _, err := store.RebuildPortfolioFromReplay(ctx, forged); err == nil {
		t.Fatal("forged replay payload with persisted identity was accepted")
	}
	readPortfolio, ok, err := store.Portfolio(ctx, principalID, "test", "USDT")
	if err != nil || !ok {
		t.Fatalf("portfolio missing after forged replay rejection ok=%v err=%v", ok, err)
	}
	if !readPortfolio.CumulativeRealizedNetPnl.Amount.Equal(portfolio.CumulativeRealizedNetPnl.Amount) {
		t.Fatalf("forged replay mutated portfolio before=%+v after=%+v", portfolio, readPortfolio)
	}

	partial := ledgerprojection.AuthoritativePortfolioReplay{
		PrincipalID:    principalID,
		Mode:           "test",
		ReportingAsset: "USDT",
		Scopes:         []ledgerfacts.Scope{scopeA, scopeB},
		Entries:        replayA,
	}
	if _, _, err := store.RebuildPortfolioFromReplay(ctx, partial); err == nil {
		t.Fatal("partial authoritative replay was accepted")
	}
	readPortfolio, ok, err = store.Portfolio(ctx, principalID, "test", "USDT")
	if err != nil || !ok {
		t.Fatalf("portfolio missing after rejected rebuild ok=%v err=%v", ok, err)
	}
	if readPortfolio.ClosedPositionCount != 2 || !readPortfolio.CumulativeRealizedNetPnl.Amount.Equal(portfolio.CumulativeRealizedNetPnl.Amount) {
		t.Fatalf("rejected partial bundle mutated portfolio before=%+v after=%+v", portfolio, readPortfolio)
	}
	if _, ok, err := store.ClosedPositionForScope(ctx, principalID, "test", "USDT", "pg-position-2"); err != nil || !ok {
		t.Fatalf("rejected partial bundle deleted second scope ok=%v err=%v", ok, err)
	}
	omittedScope := ledgerprojection.AuthoritativePortfolioReplay{
		PrincipalID:    principalID,
		Mode:           "test",
		ReportingAsset: "USDT",
		Scopes:         []ledgerfacts.Scope{scopeA},
		Entries:        replayA,
	}
	if _, _, err := store.RebuildPortfolioFromReplay(ctx, omittedScope); err == nil {
		t.Fatal("persisted portfolio scope omission was accepted")
	}
	if _, ok, err := store.ClosedPositionForScope(ctx, principalID, "test", "USDT", "pg-position-2"); err != nil || !ok {
		t.Fatalf("scope-manifest rejection deleted second scope ok=%v err=%v", ok, err)
	}

	if _, err := ledgerfacts.NewJournal(facts).AppendPositionSnapshot(ctx, ledgerfacts.PositionSnapshotFact{
		Scope:          scopeA,
		PositionID:     "late-position",
		AsOf:           now,
		ReportingAsset: "USDT",
		Provenance:     pgProjectionProvenance("late-position", "late", now),
	}); err != nil {
		t.Fatalf("append same-scope fact after replay capture: %v", err)
	}
	if _, _, err := store.RebuildPortfolioFromReplay(ctx, full); err == nil {
		t.Fatal("stale replay was accepted after a same-scope fact append")
	}
	readPortfolio, ok, err = store.Portfolio(ctx, principalID, "test", "USDT")
	if err != nil || !ok {
		t.Fatalf("portfolio missing after stale replay rejection ok=%v err=%v", ok, err)
	}
	if readPortfolio.ClosedPositionCount != 2 || !readPortfolio.CumulativeRealizedNetPnl.Amount.Equal(portfolio.CumulativeRealizedNetPnl.Amount) {
		t.Fatalf("stale replay rejection mutated portfolio before=%+v after=%+v", portfolio, readPortfolio)
	}
}

func TestPostgresStoreClosedPositionIdentityIsScopeIsolated(t *testing.T) {
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	if err := ensureProjectionTables(ctx, pool); err != nil {
		t.Fatal(err)
	}

	suffix := time.Now().UTC().Format("20060102150405.000000000")
	positionID := "shared-position-" + suffix
	principalA := "projection-scope-a-" + suffix
	principalB := "projection-scope-b-" + suffix
	cleanup := func() {
		_, _ = pool.Exec(ctx, `delete from ledger_closed_position_projections where principal_id in ($1,$2)`, principalA, principalB)
		_, _ = pool.Exec(ctx, `delete from ledger_portfolio_projections where principal_id in ($1,$2)`, principalA, principalB)
	}
	cleanup()
	t.Cleanup(cleanup)

	facts := ledgerfacts.NewPostgresStore(pool)
	store := ledgerprojection.NewPostgresStore(pool)
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	scopeA := pgProjectionScope(principalA, "account-a", "BTC-USDT-PERP")
	scopeB := pgProjectionScope(principalB, "account-b", "ETH-USDT-PERP")
	replayA := persistClosedProjectionReplay(t, ctx, facts, scopeA, positionID, now, "scope-a", decimal.RequireFromString("1.25"))
	replayB := persistClosedProjectionReplay(t, ctx, facts, scopeB, positionID, now.Add(time.Minute), "scope-b", decimal.RequireFromString("2.50"))

	for _, bundle := range []ledgerprojection.AuthoritativePortfolioReplay{
		{PrincipalID: principalA, Mode: "test", ReportingAsset: "USDT", Scopes: []ledgerfacts.Scope{scopeA}, Entries: replayA},
		{PrincipalID: principalB, Mode: "test", ReportingAsset: "USDT", Scopes: []ledgerfacts.Scope{scopeB}, Entries: replayB},
	} {
		if _, _, err := store.RebuildPortfolioFromReplay(ctx, bundle); err != nil {
			t.Fatalf("rebuild scoped projection: %v", err)
		}
	}

	projectionA, ok, err := store.ClosedPositionForScope(ctx, principalA, "test", "USDT", positionID)
	if err != nil || !ok {
		t.Fatalf("scope A projection missing ok=%v err=%v", ok, err)
	}
	projectionB, ok, err := store.ClosedPositionForScope(ctx, principalB, "test", "USDT", positionID)
	if err != nil || !ok {
		t.Fatalf("scope B projection missing ok=%v err=%v", ok, err)
	}
	if projectionA.Scope.AccountID != "account-a" || projectionB.Scope.AccountID != "account-b" ||
		projectionA.ActualFundingCashFlow.Amount.Equal(projectionB.ActualFundingCashFlow.Amount) {
		t.Fatalf("same position id overwrote scoped projections A=%+v B=%+v", projectionA, projectionB)
	}
	for _, query := range []struct {
		principalID string
		mode        string
		asset       string
	}{
		{principalID: principalA, mode: "live", asset: "USDT"},
		{principalID: principalA, mode: "test", asset: "USD"},
	} {
		if _, found, err := store.ClosedPositionForScope(ctx, query.principalID, query.mode, query.asset, positionID); err != nil || found {
			t.Fatalf("projection leaked outside exact scope query=%+v found=%v err=%v", query, found, err)
		}
	}
}

func persistClosedProjectionReplay(
	t *testing.T,
	ctx context.Context,
	facts *ledgerfacts.PostgresStore,
	scope ledgerfacts.Scope,
	positionID string,
	asOf time.Time,
	revision string,
	funding decimal.Decimal,
) []ledgerfacts.ReplayEntry {
	t.Helper()
	journal := ledgerfacts.NewJournal(facts)
	_, watermark, err := journal.IngestFundingPayments(ctx, ledgerfacts.FundingIngestBatch{
		Scope: scope,
		Payments: []ledgerfacts.FundingPaymentFact{{
			Scope:            scope,
			PositionID:       positionID,
			Venue:            "gateway",
			VenueEnvironment: "test",
			Product:          "perpetual",
			FormulaVersion:   "ledger-v5",
			Provenance:       pgProjectionProvenance("funding:"+positionID, revision, asOf),
			Payment:          ledgerfacts.Money{Asset: "USDT", Amount: funding},
		}},
		Watermark: ledgerfacts.SyncWatermark{
			Scope:            scope,
			SourceRef:        "watermark:" + positionID,
			Cursor:           "complete:" + revision,
			Revision:         revision,
			ObservedAt:       asOf,
			EffectiveThrough: asOf,
			CompleteThrough:  asOf,
			Status:           ledgerfacts.Complete,
			Verified:         true,
		},
	})
	if err != nil {
		t.Fatalf("persist funding facts: %v", err)
	}
	conversion := ledgerfacts.ConversionMetadata{
		Source:         "native",
		AsOf:           scope.WindowStart,
		RoundingMode:   "half-even",
		Scale:          8,
		FormulaVersion: "ledger-v5",
	}
	if _, err := journal.AppendFillCashflow(ctx, ledgerfacts.FillCashflowFact{
		SyncWatermarkID: watermark.FactID,
		Scope:           scope,
		PositionID:      positionID,
		LineageID:       "lineage-" + positionID,
		FillID:          "fill-" + positionID,
		Provenance:      pgProjectionProvenance("fill:"+positionID, revision, asOf),
		Fee:             ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("0.20")},
		Rebate:          ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("0.05")},
		SourceAsset:     "USDT",
		ReportingAsset:  "USDT",
		Conversion:      conversion,
	}); err != nil {
		t.Fatalf("persist fill fact: %v", err)
	}
	if _, err := journal.AppendPositionSnapshot(ctx, ledgerfacts.PositionSnapshotFact{
		SyncWatermarkID:      watermark.FactID,
		Scope:                scope,
		PositionID:           positionID,
		Provenance:           pgProjectionProvenance("position:"+positionID, revision, asOf),
		Closed:               true,
		AsOf:                 asOf,
		ReportingAsset:       "USDT",
		RealizedTradingPnl:   decimal.RequireFromString("4"),
		UnrealizedTradingPnl: decimal.Zero,
		Verified:             true,
		Conversion:           conversion,
	}); err != nil {
		t.Fatalf("persist position fact: %v", err)
	}
	lineage, err := journal.AcquireLineage(ctx, ledgerfacts.LineageFact{
		Scope:            scope,
		LineageID:        "lineage-" + positionID,
		PositionID:       positionID,
		VenueEnvironment: "test",
		Product:          "perpetual",
		Provenance:       pgProjectionProvenance("lineage:"+positionID, revision, asOf),
	})
	if err != nil {
		t.Fatalf("persist lineage fact: %v", err)
	}
	if _, err := journal.ReleaseLineageWithRevision(ctx, lineage.FactID, ledgerfacts.LineageRevisionFact{
		Scope:      scope,
		Revision:   revision,
		Provenance: pgProjectionProvenance("lineage-release:"+positionID, revision, asOf),
	}, asOf); err != nil {
		t.Fatalf("persist lineage release: %v", err)
	}
	replayed, err := journal.Replay(ctx, scope)
	if err != nil {
		t.Fatalf("replay persisted projection facts: %v", err)
	}
	return replayed
}

func ensureProjectionTables(ctx context.Context, pool *pgxpool.Pool) error {
	if _, err := pool.Exec(ctx, migrations.LedgerFactsAndProjections().SQL); err != nil {
		return err
	}
	rows, err := pool.Query(ctx, `
		select a.attname
		from pg_constraint c
		join unnest(c.conkey) with ordinality as k(attnum, ord) on true
		join pg_attribute a on a.attrelid=c.conrelid and a.attnum=k.attnum
		where c.conrelid='ledger_closed_position_projections'::regclass and c.contype='p'
		order by k.ord
	`)
	if err != nil {
		return err
	}
	defer rows.Close()
	var columns []string
	for rows.Next() {
		var column string
		if err := rows.Scan(&column); err != nil {
			return err
		}
		columns = append(columns, column)
	}
	if err := rows.Err(); err != nil {
		return err
	}
	switch strings.Join(columns, ",") {
	case "position_id":
		_, err = pool.Exec(ctx, migrations.LedgerProjectionScopeIsolation().SQL)
		return err
	case "principal_id,mode,reporting_asset,position_id":
		return nil
	default:
		return fmt.Errorf("unexpected ledger closed-position primary key: %v", columns)
	}
}

func pgProjectionScope(principalID, accountID, instrumentID string) ledgerfacts.Scope {
	return ledgerfacts.Scope{
		PrincipalID:         principalID,
		Mode:                "test",
		AccountID:           accountID,
		InstrumentID:        instrumentID,
		WindowStart:         time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		WindowEnd:           time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		PaginationTraversal: "forward",
	}
}

func pgProjectionProvenance(sourceRef, revision string, asOf time.Time) ledgerfacts.Provenance {
	return ledgerfacts.Provenance{
		SourceRef:      sourceRef,
		SourceRevision: revision,
		SourceVersion:  "test",
		ObservedAt:     asOf,
		EffectiveAt:    asOf,
	}
}
