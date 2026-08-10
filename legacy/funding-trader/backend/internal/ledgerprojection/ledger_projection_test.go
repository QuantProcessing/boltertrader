package ledgerprojection_test

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerfacts"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerprojection"
	"github.com/shopspring/decimal"
)

func TestLedgerEconomicConversionMetadataRoundTrip(t *testing.T) {
	meta := ledgerprojection.ConversionMetadata{
		ConversionSource: "mark-price",
		ConversionAsOf:   time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC),
		RoundingMode:     "half-even",
		Scale:            8,
		FormulaVersion:   "ledger-v5",
	}
	encoded, err := meta.MarshalJSON()
	if err != nil {
		t.Fatal(err)
	}
	var decoded ledgerprojection.ConversionMetadata
	if err := decoded.UnmarshalJSON(encoded); err != nil {
		t.Fatal(err)
	}
	if decoded != meta {
		t.Fatalf("metadata roundtrip=%+v, want %+v", decoded, meta)
	}
}

func TestLedgerFeeRebateSignedCashflows(t *testing.T) {
	fee := ledgerprojection.FeeExpense(ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("0.20")})
	rebate := ledgerprojection.RebateIncome(ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("0.05")})
	signed := ledgerprojection.AlreadySigned(ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("-0.03")})
	if !fee.Amount.Equal(decimal.RequireFromString("-0.20")) || !rebate.Amount.Equal(decimal.RequireFromString("0.05")) || !signed.Amount.Equal(decimal.RequireFromString("-0.03")) {
		t.Fatalf("signed cashflows fee=%s rebate=%s signed=%s", fee.Amount, rebate.Amount, signed.Amount)
	}
}

func TestLedgerProjectionRebuildsFromImmutableReplay(t *testing.T) {
	store := ledgerprojection.NewMemoryStore()
	projector := ledgerprojection.NewProjector(store)
	scope := testProjectionScope("account-1", "BTC-USDT-PERP")
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	entries := testClosedProjectionEntries(scope, "position-1", now, "1", ledgerfacts.Complete, decimal.RequireFromString("1.25"))
	for i := range entries {
		if entries[i].Position != nil {
			entries[i].Position.RealizedTradingPnl = decimal.RequireFromString("2")
		}
	}
	bundle := ledgerprojection.AuthoritativePortfolioReplay{
		PrincipalID:    scope.PrincipalID,
		Mode:           scope.Mode,
		ReportingAsset: "USDT",
		Scopes:         []ledgerfacts.Scope{scope},
		Entries:        entries,
	}
	closedItems, portfolio, err := projector.RebuildPortfolioFromReplay(context.Background(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	if len(closedItems) != 1 {
		t.Fatalf("closed projection count=%d, want 1", len(closedItems))
	}
	closed := closedItems[0]
	if !closed.NetPnl.Amount.Equal(decimal.RequireFromString("3.10")) {
		t.Fatalf("closed net=%s, want 3.10", closed.NetPnl.Amount)
	}
	if !portfolio.CumulativeRealizedNetPnl.Amount.Equal(decimal.RequireFromString("3.10")) ||
		!portfolio.FundingTotal.Amount.Equal(decimal.RequireFromString("1.25")) ||
		!portfolio.FeeTotal.Amount.Equal(decimal.RequireFromString("-0.15")) {
		t.Fatalf("portfolio derived totals mismatch: %+v", portfolio)
	}
	_, replayed, err := projector.RebuildPortfolioFromReplay(context.Background(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.CumulativeRealizedNetPnl.Amount.Equal(portfolio.CumulativeRealizedNetPnl.Amount) {
		t.Fatalf("rebuild is not idempotent before=%+v after=%+v", portfolio, replayed)
	}
}

func TestClosedPositionUPnLZeroAfterVerifiedThrough(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	scope := testProjectionScope("account-1", "BTC-USDT-PERP")
	rebuild := func(entries []ledgerfacts.ReplayEntry) ([]ledgerprojection.ClosedPositionProjection, error) {
		closed, _, err := ledgerprojection.NewProjector(ledgerprojection.NewMemoryStore()).RebuildPortfolioFromReplay(context.Background(), ledgerprojection.AuthoritativePortfolioReplay{
			PrincipalID:    "principal-1",
			Mode:           "test",
			ReportingAsset: "USDT",
			Scopes:         []ledgerfacts.Scope{scope},
			Entries:        entries,
		})
		return closed, err
	}
	verified := testClosedProjectionEntries(scope, "position-1", now, "1", ledgerfacts.Complete, decimal.RequireFromString("1.00"))
	closed, err := rebuild(verified)
	if err != nil {
		t.Fatal(err)
	}
	if len(closed) != 1 || !closed[0].Verified || !closed[0].UnrealizedTradingPnl.Amount.IsZero() || !closed[0].ClosedTradingUPnL.Amount.IsZero() {
		t.Fatalf("verified-through closed position retained UPnL: %+v", closed)
	}
	for i := range verified {
		if verified[i].Position != nil {
			verified[i].Position.UnrealizedTradingPnl = decimal.RequireFromString("0.01")
		}
	}
	if _, err := rebuild(verified); err == nil {
		t.Fatal("verified-through closed position accepted non-zero UPnL")
	}
}

func TestAuthoritativePortfolioReplayTwoScopesAndIdempotency(t *testing.T) {
	store := ledgerprojection.NewMemoryStore()
	projector := ledgerprojection.NewProjector(store)
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	scopeA := testProjectionScope("account-1", "BTC-USDT-PERP")
	scopeB := testProjectionScope("account-2", "ETH-USDT-PERP")
	bundle := ledgerprojection.AuthoritativePortfolioReplay{
		PrincipalID:    "principal-1",
		Mode:           "test",
		ReportingAsset: "USDT",
		Scopes:         []ledgerfacts.Scope{scopeA, scopeB},
		Entries: append(testClosedProjectionEntries(scopeA, "position-1", now, "1", ledgerfacts.Complete, decimal.RequireFromString("1.25")),
			testClosedProjectionEntries(scopeB, "position-2", now.Add(time.Minute), "2", ledgerfacts.Complete, decimal.RequireFromString("2.00"))...),
	}
	closed, portfolio, err := projector.RebuildPortfolioFromReplay(context.Background(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	if len(closed) != 2 || portfolio.ClosedPositionCount != 2 || portfolio.OpenPositionCount != 0 {
		t.Fatalf("lineage-derived counts closed=%d portfolio=%+v", len(closed), portfolio)
	}
	if !portfolio.FundingTotal.Amount.Equal(decimal.RequireFromString("3.25")) ||
		!portfolio.CumulativeRealizedNetPnl.Amount.Equal(decimal.RequireFromString("10.95")) {
		t.Fatalf("portfolio totals mismatch: %+v", portfolio)
	}
	_, replayed, err := projector.RebuildPortfolioFromReplay(context.Background(), bundle)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.CumulativeRealizedNetPnl.Amount.Equal(portfolio.CumulativeRealizedNetPnl.Amount) ||
		replayed.ClosedPositionCount != portfolio.ClosedPositionCount {
		t.Fatalf("authoritative rebuild is not idempotent before=%+v after=%+v", portfolio, replayed)
	}
}

func TestAuthoritativePortfolioReplayUsesLatestConversionAsOfAcrossPositions(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	scopeA := testProjectionScope("account-1", "BTC-USDT-PERP")
	scopeB := testProjectionScope("account-2", "ETH-USDT-PERP")
	positionA := testClosedProjectionEntries(scopeA, "position-1", now, "1", ledgerfacts.Complete, decimal.RequireFromString("1.25"))
	positionB := testClosedProjectionEntries(scopeB, "position-2", now.Add(time.Minute), "2", ledgerfacts.Complete, decimal.RequireFromString("2.00"))
	setConversion := func(entries []ledgerfacts.ReplayEntry, asOf time.Time, source string) {
		t.Helper()
		for i := range entries {
			if entries[i].Position != nil {
				entries[i].Position.Conversion = ledgerfacts.ConversionMetadata{
					Source:         source,
					AsOf:           asOf,
					RoundingMode:   "half-even",
					Scale:          8,
					FormulaVersion: "ledger-v5",
				}
			}
		}
	}
	setConversion(positionA, now, "mark-price")
	setConversion(positionB, now.Add(time.Minute), "mark-price")

	rebuild := func(entries []ledgerfacts.ReplayEntry) (ledgerprojection.PortfolioProjection, error) {
		_, portfolio, err := ledgerprojection.NewProjector(ledgerprojection.NewMemoryStore()).RebuildPortfolioFromReplay(context.Background(), ledgerprojection.AuthoritativePortfolioReplay{
			PrincipalID:    "principal-1",
			Mode:           "test",
			ReportingAsset: "USDT",
			Scopes:         []ledgerfacts.Scope{scopeA, scopeB},
			Entries:        entries,
		})
		return portfolio, err
	}

	portfolio, err := rebuild(append(positionA, positionB...))
	if err != nil {
		t.Fatal(err)
	}
	if !portfolio.Conversion.ConversionAsOf.Equal(now.Add(time.Minute)) {
		t.Fatalf("portfolio conversionAsOf=%s, want latest %s", portfolio.Conversion.ConversionAsOf, now.Add(time.Minute))
	}

	setConversion(positionB, now.Add(time.Minute), "index-price")
	if _, err := rebuild(append(positionA, positionB...)); err == nil {
		t.Fatal("portfolio accepted mixed conversion policies")
	}
}

func TestAuthoritativePortfolioReplayRejectsPartialBundleWithoutMutation(t *testing.T) {
	store := ledgerprojection.NewMemoryStore()
	projector := ledgerprojection.NewProjector(store)
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	scopeA := testProjectionScope("account-1", "BTC-USDT-PERP")
	scopeB := testProjectionScope("account-2", "ETH-USDT-PERP")
	valid := ledgerprojection.AuthoritativePortfolioReplay{
		PrincipalID:    "principal-1",
		Mode:           "test",
		ReportingAsset: "USDT",
		Scopes:         []ledgerfacts.Scope{scopeA},
		Entries:        testClosedProjectionEntries(scopeA, "position-1", now, "1", ledgerfacts.Complete, decimal.RequireFromString("1.25")),
	}
	if _, _, err := projector.RebuildPortfolioFromReplay(context.Background(), valid); err != nil {
		t.Fatal(err)
	}
	before, ok := store.Portfolio("principal-1", "test", "USDT")
	if !ok {
		t.Fatal("seed portfolio missing")
	}
	partial := ledgerprojection.AuthoritativePortfolioReplay{
		PrincipalID:    "principal-1",
		Mode:           "test",
		ReportingAsset: "USDT",
		Scopes:         []ledgerfacts.Scope{scopeA, scopeB},
		Entries:        testClosedProjectionEntries(scopeA, "position-9", now, "9", ledgerfacts.Complete, decimal.RequireFromString("9")),
	}
	if _, _, err := projector.RebuildPortfolioFromReplay(context.Background(), partial); err == nil {
		t.Fatal("partial authoritative bundle was accepted")
	}
	after, ok := store.Portfolio("principal-1", "test", "USDT")
	if !ok || !after.CumulativeRealizedNetPnl.Amount.Equal(before.CumulativeRealizedNetPnl.Amount) {
		t.Fatalf("rejected bundle mutated portfolio before=%+v after=%+v ok=%v", before, after, ok)
	}
}

func TestAuthoritativePortfolioReplayRejectsMalformedFactSets(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	scope := testProjectionScope("account-1", "BTC-USDT-PERP")
	base := func() []ledgerfacts.ReplayEntry {
		return testClosedProjectionEntries(scope, "position-1", now, "1", ledgerfacts.Complete, decimal.RequireFromString("1.00"))
	}
	reject := func(t *testing.T, entries []ledgerfacts.ReplayEntry) {
		t.Helper()
		_, _, err := ledgerprojection.NewProjector(ledgerprojection.NewMemoryStore()).RebuildPortfolioFromReplay(context.Background(), ledgerprojection.AuthoritativePortfolioReplay{
			PrincipalID:    "principal-1",
			Mode:           "test",
			ReportingAsset: "USDT",
			Scopes:         []ledgerfacts.Scope{scope},
			Entries:        entries,
		})
		if err == nil {
			t.Fatal("malformed authoritative replay was accepted")
		}
	}

	t.Run("duplicate fact", func(t *testing.T) {
		entries := base()
		entries = append(entries, entries[0])
		reject(t, entries)
	})
	t.Run("kind does not match fact", func(t *testing.T) {
		entries := base()
		entries[0].Kind = "fill"
		reject(t, entries)
	})
	t.Run("entry carries multiple facts", func(t *testing.T) {
		entries := base()
		entries[0].Position = entries[2].Position
		reject(t, entries)
	})
	t.Run("missing stable fact id", func(t *testing.T) {
		entries := base()
		entries[0].Funding.FactID = ""
		reject(t, entries)
	})
	t.Run("dangling supersession", func(t *testing.T) {
		entries := base()
		entries[0].Funding.SupersedesFactID = "missing-funding-fact"
		reject(t, entries)
	})
}

func TestProjectionEvidenceWatermarksAndFundingZeroSemantics(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	scopeA := testProjectionScope("account-1", "BTC-USDT-PERP")
	scopeB := testProjectionScope("account-2", "ETH-USDT-PERP")
	tests := []struct {
		name       string
		entries    []ledgerfacts.ReplayEntry
		positionID string
		wantStatus string
		wantOK     bool
	}{
		{
			name:       "missing funding is provisional, not verified zero",
			entries:    testClosedProjectionEntriesWithoutFunding(scopeA, "position-1", now, "1", ledgerfacts.Complete),
			positionID: "position-1",
			wantStatus: "provisional",
			wantOK:     false,
		},
		{
			name:       "complete empty verifies zero funding",
			entries:    testClosedProjectionEntriesWithoutFunding(scopeA, "position-1", now, "1", ledgerfacts.CompleteEmpty),
			positionID: "position-1",
			wantStatus: "verified",
			wantOK:     true,
		},
		{
			name: "complete funding for other position does not verify",
			entries: append(testClosedProjectionEntriesWithoutFunding(scopeA, "position-1", now, "1", ledgerfacts.Complete),
				testClosedProjectionEntries(scopeA, "position-2", now, "2", ledgerfacts.Complete, decimal.RequireFromString("1.00"))...),
			positionID: "position-1",
			wantStatus: "provisional",
			wantOK:     false,
		},
		{
			name:       "position watermark id isolates exact scope",
			entries:    append(testClosedProjectionEntries(scopeA, "position-1", now, "1", ledgerfacts.Incomplete, decimal.RequireFromString("1.00")), testWatermark(scopeB, "foreign-watermark", now, ledgerfacts.Complete, "9")),
			positionID: "position-1",
			wantStatus: "incomplete",
			wantOK:     false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := ledgerprojection.NewMemoryStore()
			_, _, err := ledgerprojection.NewProjector(store).RebuildPortfolioFromReplay(context.Background(), ledgerprojection.AuthoritativePortfolioReplay{
				PrincipalID:    "principal-1",
				Mode:           "test",
				ReportingAsset: "USDT",
				Scopes:         []ledgerfacts.Scope{scopeA, scopeB},
				Entries:        append(tt.entries, testPosition(scopeB, "scope-b-open", now, false, "b")...),
			})
			if err != nil {
				t.Fatal(err)
			}
			closed, ok := store.ClosedPosition(tt.positionID)
			if !ok {
				t.Fatalf("closed projection %s missing", tt.positionID)
			}
			if closed.Verified != tt.wantOK || closed.VerificationStatus != tt.wantStatus {
				t.Fatalf("verification status=%s verified=%v, want %s/%v: %+v", closed.VerificationStatus, closed.Verified, tt.wantStatus, tt.wantOK, closed)
			}
		})
	}
}

func TestProjectionStatusPrecedenceRevisionsAndResidualAggregation(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	for _, tt := range []struct {
		name       string
		status     ledgerfacts.CompletenessStatus
		revised    bool
		residual   ledgerfacts.Residual
		wantStatus string
	}{
		{name: "verified", status: ledgerfacts.Complete, wantStatus: "verified"},
		{name: "revised", status: ledgerfacts.Complete, revised: true, wantStatus: "revised"},
		{name: "provisional", status: ledgerfacts.Complete, wantStatus: "provisional", residual: ledgerfacts.Residual{}},
		{name: "incomplete", status: ledgerfacts.Incomplete, wantStatus: "incomplete"},
		{name: "temporarily unavailable", status: ledgerfacts.TemporarilyUnavailable, wantStatus: "temporarily_unavailable"},
		{name: "unsupported", status: ledgerfacts.Unsupported, wantStatus: "unsupported"},
		{name: "blocked", status: ledgerfacts.Complete, residual: ledgerfacts.Residual{OpenOrderCount: 1}, wantStatus: "blocked"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			scope := testProjectionScope("account-1", "BTC-USDT-PERP")
			entries := testClosedProjectionEntries(scope, "position-1", now, "1", tt.status, decimal.RequireFromString("1.00"))
			if tt.revised {
				entries = append(entries, ledgerfacts.ReplayEntry{Kind: "position", Revision: "2", SequenceID: 99, Position: &ledgerfacts.PositionSnapshotFact{
					FactID:               "position-revision",
					SupersedesFactID:     "position-position-1",
					SyncWatermarkID:      "watermark-1",
					Scope:                scope,
					PositionID:           "position-1",
					Closed:               true,
					AsOf:                 now,
					ReportingAsset:       "USDT",
					RealizedTradingPnl:   decimal.RequireFromString("4"),
					UnrealizedTradingPnl: decimal.Zero,
					Verified:             true,
				}})
			}
			if tt.residual.OpenOrderCount != 0 {
				for i := range entries {
					if entries[i].Position != nil {
						entries[i].Position.Residual = tt.residual
					}
				}
			}
			if tt.name == "provisional" {
				for i := range entries {
					if entries[i].Funding != nil {
						entries = append(entries[:i], entries[i+1:]...)
						break
					}
				}
			}
			store := ledgerprojection.NewMemoryStore()
			_, portfolio, err := ledgerprojection.NewProjector(store).RebuildPortfolioFromReplay(context.Background(), ledgerprojection.AuthoritativePortfolioReplay{
				PrincipalID:    "principal-1",
				Mode:           "test",
				ReportingAsset: "USDT",
				Scopes:         []ledgerfacts.Scope{scope},
				Entries:        entries,
			})
			if err != nil {
				t.Fatal(err)
			}
			if portfolio.VerificationStatus != tt.wantStatus {
				t.Fatalf("portfolio status=%s, want %s: %+v", portfolio.VerificationStatus, tt.wantStatus, portfolio)
			}
			if tt.wantStatus == "blocked" && (portfolio.Residual.OpenOrderCount != 1 || portfolio.Residual.BlockerClass == "") {
				t.Fatalf("residual not aggregated into portfolio: %+v", portfolio.Residual)
			}
		})
	}
}

func TestProjectionRequiresLineageAndExactWatermarkBindings(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	scope := testProjectionScope("account-1", "BTC-USDT-PERP")
	base := func() []ledgerfacts.ReplayEntry {
		return testClosedProjectionEntries(scope, "position-1", now, "1", ledgerfacts.Complete, decimal.RequireFromString("1.00"))
	}
	rebuild := func(entries []ledgerfacts.ReplayEntry) (ledgerprojection.ClosedPositionProjection, error) {
		store := ledgerprojection.NewMemoryStore()
		closed, _, err := ledgerprojection.NewProjector(store).RebuildPortfolioFromReplay(context.Background(), ledgerprojection.AuthoritativePortfolioReplay{
			PrincipalID:    "principal-1",
			Mode:           "test",
			ReportingAsset: "USDT",
			Scopes:         []ledgerfacts.Scope{scope},
			Entries:        entries,
		})
		if err != nil || len(closed) == 0 {
			return ledgerprojection.ClosedPositionProjection{}, err
		}
		return closed[0], nil
	}

	t.Run("missing lineage is rejected", func(t *testing.T) {
		entries := base()
		for i := range entries {
			if entries[i].Lineage != nil {
				entries = append(entries[:i], entries[i+1:]...)
				break
			}
		}
		if _, err := rebuild(entries); err == nil {
			t.Fatal("projection accepted a position without lineage evidence")
		}
	})

	t.Run("unbound fill remains provisional", func(t *testing.T) {
		entries := base()
		for i := range entries {
			if entries[i].Fill != nil {
				entries[i].Fill.SyncWatermarkID = ""
			}
		}
		closed, err := rebuild(entries)
		if err != nil {
			t.Fatal(err)
		}
		if closed.Verified || closed.VerificationStatus != "provisional" {
			t.Fatalf("unbound fill status=%s verified=%v", closed.VerificationStatus, closed.Verified)
		}
	})

	t.Run("undeclared funding watermark is rejected", func(t *testing.T) {
		entries := base()
		for i := range entries {
			if entries[i].Funding != nil {
				entries[i].Funding.SyncWatermarkID = "missing-watermark"
			}
		}
		if _, err := rebuild(entries); err == nil {
			t.Fatal("projection accepted an undeclared funding watermark")
		}
	})
}

func TestProjectionSupersessionUsesAttributedPosition(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	scope := testProjectionScope("account-1", "BTC-USDT-PERP")
	for _, factKind := range []string{"funding", "fill"} {
		t.Run(factKind, func(t *testing.T) {
			entries := testClosedProjectionEntries(scope, "position-1", now, "1", ledgerfacts.Complete, decimal.RequireFromString("1.00"))
			switch factKind {
			case "funding":
				var superseded string
				for i := range entries {
					if entries[i].Funding != nil {
						superseded = entries[i].Funding.FactID
						entries[i].Funding.PositionID = ""
						break
					}
				}
				entries = append(entries, ledgerfacts.ReplayEntry{Kind: "funding", Revision: "2", SequenceID: 99, Funding: &ledgerfacts.FundingPaymentFact{
					FactID:           "funding-position-1-revised",
					SupersedesFactID: superseded,
					SyncWatermarkID:  "watermark-1",
					Scope:            scope,
					Payment:          ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("2.00")},
				}})
			case "fill":
				var superseded string
				for i := range entries {
					if entries[i].Fill != nil {
						superseded = entries[i].Fill.FactID
						entries[i].Fill.PositionID = ""
						break
					}
				}
				entries = append(entries, ledgerfacts.ReplayEntry{Kind: "fill", Revision: "2", SequenceID: 99, Fill: &ledgerfacts.FillCashflowFact{
					FactID:           "fill-position-1-revised",
					SupersedesFactID: superseded,
					SyncWatermarkID:  "watermark-1",
					Scope:            scope,
					ReportingAsset:   "USDT",
					Fee:              ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("0.10")},
					Rebate:           ledgerfacts.Money{Asset: "USDT", Amount: decimal.Zero},
				}})
			}
			store := ledgerprojection.NewMemoryStore()
			closed, _, err := ledgerprojection.NewProjector(store).RebuildPortfolioFromReplay(context.Background(), ledgerprojection.AuthoritativePortfolioReplay{
				PrincipalID:    "principal-1",
				Mode:           "test",
				ReportingAsset: "USDT",
				Scopes:         []ledgerfacts.Scope{scope},
				Entries:        entries,
			})
			if err != nil {
				t.Fatal(err)
			}
			if len(closed) != 1 || !closed[0].Verified || closed[0].VerificationStatus != "revised" {
				t.Fatalf("empty-position supersession was not attributed to revised projection: %+v", closed)
			}
		})
	}
}

func TestLedgerProjectionReplayOrder(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	scopeA := testProjectionScope("account-1", "BTC-USDT-PERP")
	scopeB := testProjectionScope("account-2", "ETH-USDT-PERP")
	legA := testClosedProjectionEntries(scopeA, "position-1", now, "1", ledgerfacts.Complete, decimal.RequireFromString("1.00"))
	legB := testClosedProjectionEntries(scopeB, "position-1", now, "2", ledgerfacts.Complete, decimal.RequireFromString("2.00"))
	for i := range legA {
		if legA[i].Lineage != nil {
			legA[i].Lineage.LineageID = "logical-lineage-1"
			legA[i].Lineage.FactID = "lineage-leg-a"
		}
	}
	for i := range legB {
		switch {
		case legB[i].Funding != nil:
			legB[i].Funding.FactID = "funding-leg-b"
		case legB[i].Fill != nil:
			legB[i].Fill.FactID = "fill-leg-b"
		case legB[i].Position != nil:
			legB[i].Position.FactID = "position-leg-b"
		case legB[i].Lineage != nil:
			legB[i].Lineage.FactID = "lineage-leg-b"
			legB[i].Lineage.LineageID = "logical-lineage-1"
		}
	}
	entries := append(legA, legB...)
	reversed := append([]ledgerfacts.ReplayEntry(nil), entries...)
	for left, right := 0, len(reversed)-1; left < right; left, right = left+1, right-1 {
		reversed[left], reversed[right] = reversed[right], reversed[left]
	}
	rebuild := func(replay []ledgerfacts.ReplayEntry) (ledgerprojection.ClosedPositionProjection, ledgerprojection.PortfolioProjection) {
		t.Helper()
		store := ledgerprojection.NewMemoryStore()
		closed, portfolio, err := ledgerprojection.NewProjector(store).RebuildPortfolioFromReplay(context.Background(), ledgerprojection.AuthoritativePortfolioReplay{
			PrincipalID:    "principal-1",
			Mode:           "test",
			ReportingAsset: "USDT",
			Scopes:         []ledgerfacts.Scope{scopeA, scopeB},
			Entries:        replay,
		})
		if err != nil {
			t.Fatal(err)
		}
		if len(closed) != 1 {
			t.Fatalf("logical two-leg position produced %d closed rows, want 1", len(closed))
		}
		return closed[0], portfolio
	}
	closed, portfolio := rebuild(entries)
	replayedClosed, replayedPortfolio := rebuild(reversed)
	if !closed.RealizedTradingPnl.Amount.Equal(decimal.RequireFromString("8")) ||
		!closed.ActualFundingCashFlow.Amount.Equal(decimal.RequireFromString("3")) ||
		!closed.Fees.Amount.Equal(decimal.RequireFromString("-0.30")) ||
		!closed.NetPnl.Amount.Equal(decimal.RequireFromString("10.70")) {
		t.Fatalf("two-leg aggregation mismatch: %+v", closed)
	}
	if portfolio.ClosedPositionCount != 1 || portfolio.OpenPositionCount != 0 {
		t.Fatalf("logical lineage counts mismatch: %+v", portfolio)
	}
	if !replayedClosed.NetPnl.Amount.Equal(closed.NetPnl.Amount) ||
		!replayedPortfolio.TotalNetPnl.Amount.Equal(portfolio.TotalNetPnl.Amount) ||
		replayedPortfolio.VerificationStatus != portfolio.VerificationStatus {
		t.Fatalf("replay order changed projection first=%+v/%+v replay=%+v/%+v", closed, portfolio, replayedClosed, replayedPortfolio)
	}
}

func TestProjectionCollapsesEquivalentCloseRetryBundlesWithoutDoubleCounting(t *testing.T) {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	firstScope := testProjectionScope("account-1", "BTC-USDT-PERP")
	retryScope := firstScope
	retryScope.WindowEnd = retryScope.WindowEnd.Add(time.Hour)
	first := testClosedProjectionEntriesWithoutFunding(firstScope, "position-1", now, "1", ledgerfacts.CompleteEmpty)
	retry := testClosedProjectionEntriesWithoutFunding(retryScope, "position-1", now.Add(time.Hour), "2", ledgerfacts.CompleteEmpty)
	prepare := func(entries []ledgerfacts.ReplayEntry, suffix string, sequenceOffset int64) {
		t.Helper()
		for index := range entries {
			entries[index].SequenceID += sequenceOffset
			drift := time.Duration(sequenceOffset) * time.Minute
			switch {
			case entries[index].Funding != nil:
				entries[index].Funding.FactID += "-" + suffix
				entries[index].Funding.PaymentID = "funding-1"
				entries[index].Funding.Provenance.SourceRef = "funding:position-1:payment-1"
				entries[index].Funding.Provenance.SourceRevision = "close-source-" + suffix
				entries[index].Funding.Provenance.SourceVersion = "test"
				entries[index].Funding.Provenance.ObservedAt = now.Add(drift)
			case entries[index].Fill != nil:
				entries[index].Fill.FactID += "-" + suffix
				entries[index].Fill.FillID = "fill-1"
				entries[index].Fill.LineageID = "lineage-1"
				entries[index].Fill.Provenance.SourceRef = "execution-close:open-1:close-1:long:fill:fill-1"
				entries[index].Fill.Provenance.SourceRevision = "close-source-" + suffix
				entries[index].Fill.Provenance.SourceVersion = "test"
				entries[index].Fill.Provenance.ObservedAt = now.Add(drift)
				entries[index].Fill.Provenance.EffectiveAt = now.Add(30 * time.Minute)
				entries[index].Fill.Conversion = ledgerfacts.ConversionMetadata{
					Source: "native", AsOf: now.Add(drift),
					RoundingMode: "none", FormulaVersion: "close-v1",
				}
			case entries[index].Position != nil:
				entries[index].Position.FactID += "-" + suffix
				entries[index].Position.Provenance.SourceRef = "execution-close:open-1:close-1:long"
				entries[index].Position.Provenance.SourceRevision = "close-source-" + suffix
				entries[index].Position.Provenance.SourceVersion = "test"
				entries[index].Position.Provenance.ObservedAt = now.Add(drift)
				entries[index].Position.Provenance.EffectiveAt = now.Add(drift)
				entries[index].Position.Conversion = ledgerfacts.ConversionMetadata{
					Source: "native", AsOf: entries[index].Position.AsOf,
					RoundingMode: "none", FormulaVersion: "close-v1",
				}
			case entries[index].Lineage != nil:
				entries[index].Lineage.FactID += "-" + suffix
				entries[index].Lineage.Scope = testProjectionLineageScope(entries[index].Lineage.Scope)
				entries[index].Lineage.LineageID = "lineage-1"
				entries[index].Lineage.Provenance.SourceRef = "execution-close:open-1:close-1:long"
				entries[index].Lineage.Provenance.SourceRevision = "close-source-" + suffix
				entries[index].Lineage.Provenance.SourceVersion = "test"
				entries[index].Lineage.Provenance.ObservedAt = now.Add(drift)
				entries[index].Lineage.Provenance.EffectiveAt = now.Add(drift)
			case entries[index].Watermark != nil:
				entries[index].Watermark.SourceRef = "gateway:funding-payments"
				entries[index].Watermark.Cursor = "funding-cursor-1"
				entries[index].Watermark.Revision = "funding-revision-1"
			}
		}
	}
	prepare(first, "first", 0)
	prepare(retry, "retry", 10)
	first = append(first, testLineageRevisionEntry(first, "first", "closed", "close-final")...)
	retry = append(retry, testLineageRevisionEntry(retry, "retry", "closed", "close-final")...)

	closed, portfolio, err := ledgerprojection.NewProjector(ledgerprojection.NewMemoryStore()).RebuildPortfolioFromReplay(
		context.Background(),
		ledgerprojection.AuthoritativePortfolioReplay{
			PrincipalID: "principal-1", Mode: "test", ReportingAsset: "USDT",
			Scopes:  []ledgerfacts.Scope{firstScope, retryScope},
			Entries: append(first, retry...),
		},
	)
	if err != nil {
		t.Fatalf("equivalent close retry replay: %v", err)
	}
	if len(closed) != 1 || !closed[0].RealizedTradingPnl.Amount.Equal(decimal.RequireFromString("4")) ||
		!closed[0].Fees.Amount.Equal(decimal.RequireFromString("-0.15")) ||
		!closed[0].NetPnl.Amount.Equal(decimal.RequireFromString("3.85")) {
		t.Fatalf("retry bundle changed closed economics: %+v", closed)
	}
	if portfolio.ClosedPositionCount != 1 || !portfolio.TotalNetPnl.Amount.Equal(decimal.RequireFromString("3.85")) {
		t.Fatalf("retry bundle changed portfolio economics: %+v", portfolio)
	}
	if portfolio.VerificationStatus != "verified" {
		t.Fatalf("retry lineage revision polluted portfolio status: %+v", portfolio)
	}
}

func TestProjectionRejectsCloseRetryBundleEconomicDifferences(t *testing.T) {
	reject := func(t *testing.T, withFunding bool, mutateRetry func([]ledgerfacts.ReplayEntry) []ledgerfacts.ReplayEntry) {
		t.Helper()
		now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
		firstScope := testProjectionScope("account-1", "BTC-USDT-PERP")
		retryScope := firstScope
		retryScope.WindowEnd = retryScope.WindowEnd.Add(time.Hour)
		status := ledgerfacts.CompleteEmpty
		if withFunding {
			status = ledgerfacts.Complete
		}
		first := testRetryProjectionEntries(firstScope, "position-1", now, "1", status, withFunding)
		retry := testRetryProjectionEntries(retryScope, "position-1", now.Add(time.Hour), "2", status, withFunding)
		prepareRetryEntries(t, first, "first", 0)
		prepareRetryEntries(t, retry, "retry", 10)
		first = append(first, testLineageRevisionEntry(first, "first", "closed", "close-final")...)
		retry = append(retry, testLineageRevisionEntry(retry, "retry", "closed", "close-final")...)
		retry = mutateRetry(retry)

		_, _, err := ledgerprojection.NewProjector(ledgerprojection.NewMemoryStore()).RebuildPortfolioFromReplay(
			context.Background(),
			ledgerprojection.AuthoritativePortfolioReplay{
				PrincipalID: "principal-1", Mode: "test", ReportingAsset: "USDT",
				Scopes:  []ledgerfacts.Scope{firstScope, retryScope},
				Entries: append(first, retry...),
			},
		)
		if err == nil {
			t.Fatal("close retry bundle with economic difference was accepted")
		}
	}

	t.Run("realized pnl", func(t *testing.T) {
		reject(t, false, func(entries []ledgerfacts.ReplayEntry) []ledgerfacts.ReplayEntry {
			for i := range entries {
				if entries[i].Position != nil {
					entries[i].Position.RealizedTradingPnl = decimal.RequireFromString("4.01")
				}
			}
			return entries
		})
	})
	t.Run("fee", func(t *testing.T) {
		reject(t, false, func(entries []ledgerfacts.ReplayEntry) []ledgerfacts.ReplayEntry {
			for i := range entries {
				if entries[i].Fill != nil {
					entries[i].Fill.Fee.Amount = decimal.RequireFromString("0.21")
				}
			}
			return entries
		})
	})
	t.Run("funding amount", func(t *testing.T) {
		reject(t, true, func(entries []ledgerfacts.ReplayEntry) []ledgerfacts.ReplayEntry {
			for i := range entries {
				if entries[i].Funding != nil {
					entries[i].Funding.Payment.Amount = decimal.RequireFromString("1.01")
				}
			}
			return entries
		})
	})
	t.Run("funding collection", func(t *testing.T) {
		reject(t, true, func(entries []ledgerfacts.ReplayEntry) []ledgerfacts.ReplayEntry {
			for _, entry := range entries {
				if entry.Funding != nil {
					extra := entry
					extra.SequenceID += 100
					extra.Funding = &ledgerfacts.FundingPaymentFact{}
					*extra.Funding = *entry.Funding
					extra.Funding.FactID = "funding-extra-retry"
					extra.Funding.PaymentID = "funding-extra"
					entries = append(entries, extra)
					return entries
				}
			}
			return entries
		})
	})
	t.Run("fill id", func(t *testing.T) {
		reject(t, false, func(entries []ledgerfacts.ReplayEntry) []ledgerfacts.ReplayEntry {
			for i := range entries {
				if entries[i].Fill != nil {
					entries[i].Fill.FillID = "fill-2"
				}
			}
			return entries
		})
	})
	t.Run("watermark status", func(t *testing.T) {
		reject(t, false, func(entries []ledgerfacts.ReplayEntry) []ledgerfacts.ReplayEntry {
			for i := range entries {
				if entries[i].Watermark != nil {
					entries[i].Watermark.Status = ledgerfacts.Incomplete
					entries[i].Watermark.Verified = false
				}
			}
			return entries
		})
	})
	t.Run("watermark verified", func(t *testing.T) {
		reject(t, false, func(entries []ledgerfacts.ReplayEntry) []ledgerfacts.ReplayEntry {
			for i := range entries {
				if entries[i].Watermark != nil {
					entries[i].Watermark.Verified = false
				}
			}
			return entries
		})
	})
	t.Run("lineage active", func(t *testing.T) {
		reject(t, false, func(entries []ledgerfacts.ReplayEntry) []ledgerfacts.ReplayEntry {
			for i := range entries {
				if entries[i].Lineage != nil {
					entries[i].Lineage.Active = true
				}
			}
			return entries
		})
	})
	t.Run("lineage id", func(t *testing.T) {
		reject(t, false, func(entries []ledgerfacts.ReplayEntry) []ledgerfacts.ReplayEntry {
			for i := range entries {
				if entries[i].Lineage != nil {
					entries[i].Lineage.LineageID = "lineage-2"
				}
			}
			return entries
		})
	})
	t.Run("position closed", func(t *testing.T) {
		reject(t, false, func(entries []ledgerfacts.ReplayEntry) []ledgerfacts.ReplayEntry {
			for i := range entries {
				if entries[i].Position != nil {
					entries[i].Position.Closed = false
				}
			}
			return entries
		})
	})
	t.Run("position residual", func(t *testing.T) {
		reject(t, false, func(entries []ledgerfacts.ReplayEntry) []ledgerfacts.ReplayEntry {
			for i := range entries {
				if entries[i].Position != nil {
					entries[i].Position.Residual.PendingCompensation = 1
				}
			}
			return entries
		})
	})
	t.Run("lineage revision status", func(t *testing.T) {
		reject(t, false, func(entries []ledgerfacts.ReplayEntry) []ledgerfacts.ReplayEntry {
			for i := range entries {
				if entries[i].RevisionFact != nil {
					entries[i].RevisionFact.Status = "revised"
				}
			}
			return entries
		})
	})
}

func testRetryProjectionEntries(scope ledgerfacts.Scope, positionID string, asOf time.Time, revision string, status ledgerfacts.CompletenessStatus, withFunding bool) []ledgerfacts.ReplayEntry {
	if withFunding {
		return testClosedProjectionEntries(scope, positionID, asOf, revision, status, decimal.RequireFromString("1.00"))
	}
	return testClosedProjectionEntriesWithoutFunding(scope, positionID, asOf, revision, status)
}

func testLineageRevisionEntry(entries []ledgerfacts.ReplayEntry, suffix, status, reason string) []ledgerfacts.ReplayEntry {
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	for _, entry := range entries {
		if entry.Lineage == nil {
			continue
		}
		drift := time.Duration(entry.SequenceID) * time.Minute
		return []ledgerfacts.ReplayEntry{{
			Kind:       "lineage_revision",
			Revision:   entry.Revision,
			SequenceID: entry.SequenceID + 1,
			RevisionFact: &ledgerfacts.LineageRevisionFact{
				FactID:          "lineage-revision-" + suffix,
				SyncWatermarkID: "watermark-" + suffix,
				LineageFactID:   entry.Lineage.FactID,
				Scope:           entry.Lineage.Scope,
				Revision:        "lineage-close-v1",
				Status:          status,
				Reason:          reason,
				Provenance: ledgerfacts.Provenance{
					SourceRef:      "execution-close:open-1:close-1:long:revision",
					SourceRevision: "close-source-" + suffix,
					SourceVersion:  "test",
					ObservedAt:     now.Add(drift),
					EffectiveAt:    now.Add(drift),
				},
			},
		}}
	}
	return nil
}

func prepareRetryEntries(t *testing.T, entries []ledgerfacts.ReplayEntry, suffix string, sequenceOffset int64) {
	t.Helper()
	now := time.Date(2026, 8, 1, 12, 0, 0, 0, time.UTC)
	for index := range entries {
		entries[index].SequenceID += sequenceOffset
		drift := time.Duration(sequenceOffset) * time.Minute
		switch {
		case entries[index].Funding != nil:
			entries[index].Funding.FactID += "-" + suffix
			entries[index].Funding.PaymentID = "funding-1"
			entries[index].Funding.Provenance.SourceRef = "funding:position-1:payment-1"
			entries[index].Funding.Provenance.SourceRevision = "close-source-" + suffix
			entries[index].Funding.Provenance.SourceVersion = "test"
			entries[index].Funding.Provenance.ObservedAt = now.Add(drift)
		case entries[index].Fill != nil:
			entries[index].Fill.FactID += "-" + suffix
			entries[index].Fill.FillID = "fill-1"
			entries[index].Fill.LineageID = "lineage-1"
			entries[index].Fill.Provenance.SourceRef = "execution-close:open-1:close-1:long:fill:fill-1"
			entries[index].Fill.Provenance.SourceRevision = "close-source-" + suffix
			entries[index].Fill.Provenance.SourceVersion = "test"
			entries[index].Fill.Provenance.ObservedAt = now.Add(drift)
			entries[index].Fill.Provenance.EffectiveAt = now.Add(30 * time.Minute)
			entries[index].Fill.Conversion = ledgerfacts.ConversionMetadata{
				Source: "native", AsOf: now.Add(drift),
				RoundingMode: "none", FormulaVersion: "close-v1",
			}
		case entries[index].Position != nil:
			entries[index].Position.FactID += "-" + suffix
			entries[index].Position.Provenance.SourceRef = "execution-close:open-1:close-1:long"
			entries[index].Position.Provenance.SourceRevision = "close-source-" + suffix
			entries[index].Position.Provenance.SourceVersion = "test"
			entries[index].Position.Provenance.ObservedAt = now.Add(drift)
			entries[index].Position.Provenance.EffectiveAt = now.Add(drift)
			entries[index].Position.Conversion = ledgerfacts.ConversionMetadata{
				Source: "native", AsOf: entries[index].Position.AsOf,
				RoundingMode: "none", FormulaVersion: "close-v1",
			}
		case entries[index].Lineage != nil:
			entries[index].Lineage.FactID += "-" + suffix
			entries[index].Lineage.Scope = testProjectionLineageScope(entries[index].Lineage.Scope)
			entries[index].Lineage.LineageID = "lineage-1"
			entries[index].Lineage.Provenance.SourceRef = "execution-close:open-1:close-1:long"
			entries[index].Lineage.Provenance.SourceRevision = "close-source-" + suffix
			entries[index].Lineage.Provenance.SourceVersion = "test"
			entries[index].Lineage.Provenance.ObservedAt = now.Add(drift)
			entries[index].Lineage.Provenance.EffectiveAt = now.Add(drift)
		case entries[index].Watermark != nil:
			entries[index].Watermark.SourceRef = "gateway:funding-payments"
			entries[index].Watermark.Cursor = "funding-cursor-1"
			entries[index].Watermark.Revision = "funding-revision-1"
		}
	}
}

func testProjectionScope(accountID, instrumentID string) ledgerfacts.Scope {
	return ledgerfacts.Scope{
		PrincipalID:         "principal-1",
		Mode:                "test",
		AccountID:           accountID,
		InstrumentID:        instrumentID,
		WindowStart:         time.Date(2026, 8, 1, 0, 0, 0, 0, time.UTC),
		WindowEnd:           time.Date(2026, 8, 2, 0, 0, 0, 0, time.UTC),
		PaginationTraversal: "forward",
	}
}

func testProjectionLineageScope(scope ledgerfacts.Scope) ledgerfacts.Scope {
	return ledgerfacts.Scope{
		PrincipalID:  scope.PrincipalID,
		Mode:         scope.Mode,
		AccountID:    scope.AccountID,
		InstrumentID: scope.InstrumentID,
	}
}

func testClosedProjectionEntries(scope ledgerfacts.Scope, positionID string, asOf time.Time, revision string, status ledgerfacts.CompletenessStatus, funding decimal.Decimal) []ledgerfacts.ReplayEntry {
	return append([]ledgerfacts.ReplayEntry{
		{Kind: "funding", Revision: revision, SequenceID: 1, Funding: &ledgerfacts.FundingPaymentFact{
			FactID:          "funding-" + positionID,
			SyncWatermarkID: "watermark-" + revision,
			Scope:           scope,
			PositionID:      positionID,
			Payment:         ledgerfacts.Money{Asset: "USDT", Amount: funding},
		}},
	}, testClosedProjectionEntriesWithoutFunding(scope, positionID, asOf, revision, status)...)
}

func testClosedProjectionEntriesWithoutFunding(scope ledgerfacts.Scope, positionID string, asOf time.Time, revision string, status ledgerfacts.CompletenessStatus) []ledgerfacts.ReplayEntry {
	return []ledgerfacts.ReplayEntry{
		{Kind: "fill", Revision: revision, SequenceID: 2, Fill: &ledgerfacts.FillCashflowFact{
			FactID:          "fill-" + positionID,
			SyncWatermarkID: "watermark-" + revision,
			Scope:           scope,
			PositionID:      positionID,
			ReportingAsset:  "USDT",
			Fee:             ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("0.20")},
			Rebate:          ledgerfacts.Money{Asset: "USDT", Amount: decimal.RequireFromString("0.05")},
		}},
		{Kind: "position", Revision: revision, SequenceID: 3, Position: &ledgerfacts.PositionSnapshotFact{
			FactID:               "position-" + positionID,
			SyncWatermarkID:      "watermark-" + revision,
			Scope:                scope,
			PositionID:           positionID,
			Closed:               true,
			AsOf:                 asOf,
			ReportingAsset:       "USDT",
			RealizedTradingPnl:   decimal.RequireFromString("4"),
			UnrealizedTradingPnl: decimal.Zero,
			Verified:             true,
		}},
		{Kind: "lineage", Revision: revision, SequenceID: 4, Lineage: &ledgerfacts.LineageFact{
			FactID:     "lineage-" + positionID,
			Scope:      scope,
			LineageID:  "lineage-" + positionID,
			PositionID: positionID,
			Active:     false,
			ClosedAt:   asOf,
		}},
		testWatermark(scope, "watermark-"+revision, asOf, status, revision),
	}
}

func testPosition(scope ledgerfacts.Scope, positionID string, asOf time.Time, closed bool, revision string) []ledgerfacts.ReplayEntry {
	return []ledgerfacts.ReplayEntry{
		{Kind: "position", Revision: revision, SequenceID: 1, Position: &ledgerfacts.PositionSnapshotFact{
			FactID:               "position-" + positionID,
			SyncWatermarkID:      "watermark-" + revision,
			Scope:                scope,
			PositionID:           positionID,
			Closed:               closed,
			AsOf:                 asOf,
			ReportingAsset:       "USDT",
			RealizedTradingPnl:   decimal.Zero,
			UnrealizedTradingPnl: decimal.RequireFromString("0.50"),
			Verified:             false,
		}},
		{Kind: "lineage", Revision: revision, SequenceID: 2, Lineage: &ledgerfacts.LineageFact{
			FactID:     "lineage-" + positionID,
			Scope:      scope,
			LineageID:  "lineage-" + positionID,
			PositionID: positionID,
			Active:     !closed,
			ClosedAt: func() time.Time {
				if closed {
					return asOf
				}
				return time.Time{}
			}(),
		}},
		testWatermark(scope, "watermark-"+revision, asOf, ledgerfacts.Incomplete, revision),
	}
}

func testWatermark(scope ledgerfacts.Scope, factID string, completeThrough time.Time, status ledgerfacts.CompletenessStatus, revision string) ledgerfacts.ReplayEntry {
	return ledgerfacts.ReplayEntry{Kind: "watermark", Revision: revision, Marker: factID, SequenceID: 5, Watermark: &ledgerfacts.SyncWatermark{
		FactID:          factID,
		Scope:           scope,
		Status:          status,
		Verified:        status == ledgerfacts.Complete || status == ledgerfacts.CompleteEmpty,
		CompleteThrough: completeThrough,
	}}
}
