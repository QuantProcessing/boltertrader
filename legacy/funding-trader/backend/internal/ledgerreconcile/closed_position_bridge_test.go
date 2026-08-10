package ledgerreconcile

import (
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerfacts"
	"github.com/shopspring/decimal"
)

func TestBuildClosedPositionBridgeEmitsPerLegTargetsFactsAndTradingPnL(t *testing.T) {
	open, close := bridgeExecutions("test")

	bridge, err := BuildClosedPositionBridge(open, close, BuildOptions{ReportingAsset: "USDT"})
	if err != nil {
		t.Fatalf("BuildClosedPositionBridge returned error: %v", err)
	}

	if bridge.PositionID != "position-1" || bridge.LineageID != "lineage-open-1" || len(bridge.Legs) != 2 {
		t.Fatalf("bridge identity = %+v", bridge)
	}
	if !bridge.VerificationPrerequisitesMet {
		t.Fatal("flat closed position should be eligible for orchestration verification after funding watermark binding")
	}
	long := bridge.Leg(execution.LegRoleLong)
	short := bridge.Leg(execution.LegRoleShort)
	if long == nil || short == nil {
		t.Fatalf("missing long/short leg bridge: %+v", bridge.Legs)
	}

	assertTarget(t, long, "principal-1", "test", "acct-long", "okx-btc", "USDT", "position-1", open.CreatedAt, close.UpdatedAt)
	assertTarget(t, short, "principal-1", "test", "acct-short", "bybit-btc", "USDT", "position-1", open.CreatedAt, close.UpdatedAt)
	assertScope(t, long.Scope, "principal-1", "test", "acct-long", "okx-btc", open.CreatedAt, close.UpdatedAt)
	assertScope(t, short.Scope, "principal-1", "test", "acct-short", "bybit-btc", open.CreatedAt, close.UpdatedAt)

	if len(long.FillCashflows) != 2 || len(short.FillCashflows) != 2 {
		t.Fatalf("fill cashflows long=%d short=%d", len(long.FillCashflows), len(short.FillCashflows))
	}
	assertFillFact(t, long.FillCashflows[0], "open-long-fill", "0.11", "USDT", false)
	assertFillFact(t, long.FillCashflows[1], "close-long-fill", "0.12", "USDT", false)
	assertFillFact(t, short.FillCashflows[0], "open-short-fill", "0.09", "USDT", false)
	assertFillFact(t, short.FillCashflows[1], "close-short-fill", "0.10", "USDT", false)

	assertDecimal(t, long.PositionSnapshot.RealizedTradingPnl, "40")
	assertDecimal(t, short.PositionSnapshot.RealizedTradingPnl, "20")
	assertDecimal(t, long.PositionSnapshot.Fees, "0.23")
	assertDecimal(t, short.PositionSnapshot.Fees, "0.19")
	assertDecimal(t, long.PositionSnapshot.UnrealizedTradingPnl, "0")
	assertDecimal(t, short.PositionSnapshot.UnrealizedTradingPnl, "0")
	if long.PositionSnapshot.Verified || short.PositionSnapshot.Verified {
		t.Fatal("builder must not mark snapshots verified before funding watermark binding")
	}
	if long.Lineage.Active || short.Lineage.Active || long.Lineage.ClosedAt.IsZero() || short.Lineage.ClosedAt.IsZero() {
		t.Fatalf("lineage facts should be closed inactive: long=%+v short=%+v", long.Lineage, short.Lineage)
	}
	if long.LineageRevision.Status != "closed" || short.LineageRevision.Status != "closed" {
		t.Fatalf("lineage revision status long=%q short=%q", long.LineageRevision.Status, short.LineageRevision.Status)
	}
}

func TestBuildClosedPositionBridgeIgnoresAggregateObservationTimeForLedgerIdentity(t *testing.T) {
	open, close := bridgeExecutions("test")
	first, err := BuildClosedPositionBridge(open, close, BuildOptions{ReportingAsset: "USDT"})
	if err != nil {
		t.Fatalf("initial bridge: %v", err)
	}

	close.UpdatedAt = close.UpdatedAt.Add(time.Hour)
	retried, err := BuildClosedPositionBridge(open, close, BuildOptions{ReportingAsset: "USDT"})
	if err != nil {
		t.Fatalf("retried bridge: %v", err)
	}
	if !reflect.DeepEqual(retried, first) {
		t.Fatalf("aggregate observation time changed ledger bridge\nfirst:   %+v\nretried: %+v", first, retried)
	}
}

func TestBuildClosedPositionBridgeAcceptsCanonicalPositionIDForOpenExecutionWithoutPersistedPositionID(t *testing.T) {
	open, close := bridgeExecutions("test")
	open.PositionID = ""
	close.PositionID = "position-" + open.ID

	bridge, err := BuildClosedPositionBridge(open, close, BuildOptions{ReportingAsset: "USDT"})
	if err != nil {
		t.Fatalf("BuildClosedPositionBridge returned error: %v", err)
	}
	if bridge.PositionID != close.PositionID {
		t.Fatalf("bridge position id = %q, want %q", bridge.PositionID, close.PositionID)
	}
	for _, leg := range bridge.Legs {
		if leg.SyncTarget.PositionID != close.PositionID || leg.PositionSnapshot.PositionID != close.PositionID {
			t.Fatalf("leg position binding = %+v", leg)
		}
	}
}

func TestBuildClosedPositionBridgeRejectsResidualIncompleteAndIdentityDrift(t *testing.T) {
	open, close := bridgeExecutions("test")

	incomplete := close
	incomplete.Legs[0].Filled = decimal.RequireFromString("0.08")
	incomplete.Fills[1].Quantity = decimal.RequireFromString("0.08")
	if _, err := BuildClosedPositionBridge(open, incomplete, BuildOptions{ReportingAsset: "USDT"}); !errors.Is(err, ErrIncompleteClose) {
		t.Fatalf("incomplete close error=%v, want %v", err, ErrIncompleteClose)
	}

	residual := close
	residual.DirectionalExposure = decimal.RequireFromString("0.01")
	if _, err := BuildClosedPositionBridge(open, residual, BuildOptions{ReportingAsset: "USDT"}); !errors.Is(err, ErrResidualExposure) {
		t.Fatalf("residual close error=%v, want %v", err, ErrResidualExposure)
	}

	drift := close
	drift.Legs[0].InstrumentID = "okx-btc-drift"
	if _, err := BuildClosedPositionBridge(open, drift, BuildOptions{ReportingAsset: "USDT"}); !errors.Is(err, ErrLegIdentityDrift) {
		t.Fatalf("identity drift error=%v, want %v", err, ErrLegIdentityDrift)
	}
}

func TestBuildClosedPositionBridgeRejectsWrongParentLineageTimeAndReportingAsset(t *testing.T) {
	open, close := bridgeExecutions("test")

	wrongParent := close
	wrongParent.ParentExecutionID = "other-open"
	if _, err := BuildClosedPositionBridge(open, wrongParent, BuildOptions{ReportingAsset: "USDT"}); !errors.Is(err, ErrLineageMismatch) {
		t.Fatalf("wrong parent error=%v, want %v", err, ErrLineageMismatch)
	}

	wrongLineage := close
	wrongLineage.LineageID = "other-lineage"
	if _, err := BuildClosedPositionBridge(open, wrongLineage, BuildOptions{ReportingAsset: "USDT"}); !errors.Is(err, ErrLineageMismatch) {
		t.Fatalf("wrong lineage error=%v, want %v", err, ErrLineageMismatch)
	}

	backdated := close
	backdated.CreatedAt = open.CreatedAt.Add(-time.Second)
	if _, err := BuildClosedPositionBridge(open, backdated, BuildOptions{ReportingAsset: "USDT"}); !errors.Is(err, ErrTimeOrder) {
		t.Fatalf("backdated close error=%v, want %v", err, ErrTimeOrder)
	}

	badAsset := close
	badAsset.Fills[0].Fee.Currency = "BTC"
	if _, err := BuildClosedPositionBridge(open, badAsset, BuildOptions{ReportingAsset: "USDT"}); !errors.Is(err, ErrReportingAsset) {
		t.Fatalf("reporting asset error=%v, want %v", err, ErrReportingAsset)
	}
}

func TestBuildClosedPositionBridgeCarriesLiveAndTestProvenanceWithoutFundingInference(t *testing.T) {
	for _, mode := range []string{"test", "live"} {
		t.Run(mode, func(t *testing.T) {
			open, close := bridgeExecutions(mode)
			bridge, err := BuildClosedPositionBridge(open, close, BuildOptions{ReportingAsset: "USDT"})
			if err != nil {
				t.Fatal(err)
			}
			for _, leg := range bridge.Legs {
				if leg.Scope.Mode != mode || leg.PositionSnapshot.Provenance.SourceVersion != mode {
					t.Fatalf("mode provenance lost for %s: scope=%+v position=%+v", mode, leg.Scope, leg.PositionSnapshot.Provenance)
				}
				if !leg.PositionSnapshot.ActualFundingCashFlow.IsZero() {
					t.Fatalf("builder inferred funding cashflow for %s: %s", mode, leg.PositionSnapshot.ActualFundingCashFlow)
				}
			}
		})
	}
}

func TestBuildClosedPositionBridgeRejectsClosedUPnLThatIsNotExactlyZero(t *testing.T) {
	open, close := bridgeExecutions("test")

	_, err := BuildClosedPositionBridge(open, close, BuildOptions{
		ReportingAsset:       "USDT",
		UnrealizedTradingPnl: decimal.RequireFromString("0.00000001"),
	})
	if !errors.Is(err, ErrClosedUnrealizedPnL) {
		t.Fatalf("closed upnl error=%v, want %v", err, ErrClosedUnrealizedPnL)
	}
}

func bridgeExecutions(mode string) (execution.Execution, execution.Execution) {
	openAt := time.Date(2026, 8, 1, 10, 0, 0, 0, time.UTC)
	closeAt := openAt.Add(2 * time.Hour)
	open := execution.Execution{
		ID:                   "open-1",
		PrincipalID:          "principal-1",
		Mode:                 mode,
		Operation:            execution.OperationOpen,
		PositionID:           "position-1",
		LineageID:            "lineage-open-1",
		State:                execution.StateHedged,
		CreatedAt:            openAt,
		UpdatedAt:            openAt.Add(time.Minute),
		InitialLongQuantity:  decimal.RequireFromString("0.10"),
		InitialShortQuantity: decimal.RequireFromString("0.10"),
		DirectionalExposure:  decimal.Zero,
		Legs: []execution.Leg{
			{
				LegIdentity: execution.LegIdentity{AccountID: "acct-long", Venue: "okx", InstrumentID: "okx-btc", VenueEnvironment: "demo"},
				LegID:       "open-long-leg", Role: execution.LegRoleLong, Status: execution.LegStatusFilled, State: execution.LegStatusFilled,
				Side: execution.SideBuy, Filled: decimal.RequireFromString("0.10"), Quantity: decimal.RequireFromString("0.10"),
			},
			{
				LegIdentity: execution.LegIdentity{AccountID: "acct-short", Venue: "bybit", InstrumentID: "bybit-btc", VenueEnvironment: "testnet"},
				LegID:       "open-short-leg", Role: execution.LegRoleShort, Status: execution.LegStatusFilled, State: execution.LegStatusFilled,
				Side: execution.SideSell, Filled: decimal.RequireFromString("0.10"), Quantity: decimal.RequireFromString("0.10"),
			},
		},
		Fills: []execution.Fill{
			{
				LegIdentity: execution.LegIdentity{AccountID: "acct-long", Venue: "okx", InstrumentID: "okx-btc", VenueEnvironment: "demo"},
				FillID:      "open-long-fill", LegID: "open-long-leg", Role: execution.LegRoleLong,
				Quantity: decimal.RequireFromString("0.10"), Price: decimal.RequireFromString("10000"),
				Fee: execution.Money{Amount: decimal.RequireFromString("0.11"), Currency: "USDT"}, At: openAt.Add(time.Minute),
			},
			{
				LegIdentity: execution.LegIdentity{AccountID: "acct-short", Venue: "bybit", InstrumentID: "bybit-btc", VenueEnvironment: "testnet"},
				FillID:      "open-short-fill", LegID: "open-short-leg", Role: execution.LegRoleShort,
				Quantity: decimal.RequireFromString("0.10"), Price: decimal.RequireFromString("10000"),
				Fee: execution.Money{Amount: decimal.RequireFromString("0.09"), Currency: "USDT"}, At: openAt.Add(time.Minute),
			},
		},
	}
	close := execution.Execution{
		ID:                   "close-1",
		PrincipalID:          open.PrincipalID,
		Mode:                 mode,
		Operation:            execution.OperationClose,
		PositionID:           open.PositionID,
		ParentExecutionID:    open.ID,
		LineageID:            open.LineageID,
		State:                execution.StateClosed,
		CreatedAt:            closeAt,
		UpdatedAt:            closeAt.Add(time.Minute),
		InitialLongQuantity:  open.InitialLongQuantity,
		InitialShortQuantity: open.InitialShortQuantity,
		DirectionalExposure:  decimal.Zero,
		Legs: []execution.Leg{
			{
				LegIdentity: open.Legs[0].LegIdentity,
				LegID:       "close-long-leg", Role: execution.LegRoleLong, Status: execution.LegStatusFilled, State: execution.LegStatusFilled,
				Side: execution.SideSell, Filled: decimal.RequireFromString("0.10"), Quantity: decimal.RequireFromString("0.10"), ReduceOnly: true,
			},
			{
				LegIdentity: open.Legs[1].LegIdentity,
				LegID:       "close-short-leg", Role: execution.LegRoleShort, Status: execution.LegStatusFilled, State: execution.LegStatusFilled,
				Side: execution.SideBuy, Filled: decimal.RequireFromString("0.10"), Quantity: decimal.RequireFromString("0.10"), ReduceOnly: true,
			},
		},
		Fills: []execution.Fill{
			{
				LegIdentity: open.Legs[0].LegIdentity,
				FillID:      "close-long-fill", LegID: "close-long-leg", Role: execution.LegRoleLong,
				Quantity: decimal.RequireFromString("0.10"), Price: decimal.RequireFromString("10400"),
				Fee: execution.Money{Amount: decimal.RequireFromString("0.12"), Currency: "USDT"}, At: closeAt.Add(time.Minute),
			},
			{
				LegIdentity: open.Legs[1].LegIdentity,
				FillID:      "close-short-fill", LegID: "close-short-leg", Role: execution.LegRoleShort,
				Quantity: decimal.RequireFromString("0.10"), Price: decimal.RequireFromString("9800"),
				Fee: execution.Money{Amount: decimal.RequireFromString("0.10"), Currency: "USDT"}, At: closeAt.Add(time.Minute),
			},
		},
	}
	return open, close
}

func assertTarget(t *testing.T, leg *LegBridge, principalID, mode, accountID, instrumentID, asset, positionID string, start, end time.Time) {
	t.Helper()
	target := leg.SyncTarget
	if target.PrincipalID != principalID || target.Mode != mode || target.AccountID != accountID ||
		target.InstrumentID != instrumentID || target.SettlementAsset != asset || target.PositionID != positionID ||
		!target.WindowStart.Equal(start) || !target.WindowEnd.Equal(end) {
		t.Fatalf("target = %+v", target)
	}
}

func assertScope(t *testing.T, scope ledgerfacts.Scope, principalID, mode, accountID, instrumentID string, start, end time.Time) {
	t.Helper()
	if scope.PrincipalID != principalID || scope.Mode != mode || scope.AccountID != accountID ||
		scope.InstrumentID != instrumentID || !scope.WindowStart.Equal(start.UTC()) || !scope.WindowEnd.Equal(end.UTC()) {
		t.Fatalf("scope = %+v", scope)
	}
}

func assertFillFact(t *testing.T, fact ledgerfacts.FillCashflowFact, fillID, fee, asset string, alreadySigned bool) {
	t.Helper()
	if fact.FillID != fillID || fact.Fee.Asset != asset || fact.ReportingAsset != asset || fact.AlreadySigned != alreadySigned {
		t.Fatalf("fill fact = %+v", fact)
	}
	assertDecimal(t, fact.Fee.Amount, fee)
}

func assertDecimal(t *testing.T, got decimal.Decimal, want string) {
	t.Helper()
	expected := decimal.RequireFromString(want)
	if !got.Equal(expected) {
		t.Fatalf("decimal = %s, want %s", got, expected)
	}
}
