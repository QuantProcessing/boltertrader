package execution

import (
	"testing"
	"time"

	"github.com/shopspring/decimal"
)

func TestPreviewPassesWithRoundedLegsAndExpectedEconomics(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	input := validPreviewInput(now)

	preview, err := Preview(input)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}

	if preview.Blocked() {
		t.Fatalf("expected preview to pass, blockers=%v", preview.Blockers)
	}
	if preview.ExpiresAt.Sub(preview.PreviewedAt) != 30*time.Second {
		t.Fatalf("expected 30s ttl, got %s", preview.ExpiresAt.Sub(preview.PreviewedAt))
	}
	assertDecimal(t, preview.SingleLegNotional, "500")
	assertDecimal(t, preview.GrossExposure, "749.975")
	assertDecimal(t, preview.GrossProfit7d, "9.589041095890411")
	assertDecimal(t, preview.NetProfit7d, "6.089041095890411")
	if preview.MaxSlippagePct.String() != "0.2" {
		t.Fatalf("expected fixed max slippage 0.2%%, got %s", preview.MaxSlippagePct)
	}
	if len(preview.PlannedLegs) != 2 {
		t.Fatalf("expected 2 planned legs, got %d", len(preview.PlannedLegs))
	}
	long, short := preview.PlannedLegs[0], preview.PlannedLegs[1]
	if long.Role != LegRoleLong || long.Side != SideBuy || short.Role != LegRoleShort || short.Side != SideSell {
		t.Fatalf("unexpected planned leg roles/sides: %#v %#v", long, short)
	}
	assertDecimal(t, long.Notional, "250")
	assertDecimal(t, short.Notional, "499.975")
	assertDecimal(t, long.Quantity, "2.500")
	assertDecimal(t, short.Quantity, "2.500")
	assertDecimal(t, preview.InitialLongQuantity, "2.500")
	assertDecimal(t, preview.InitialShortQuantity, "2.500")
}

func TestOpenPreviewPlansCommonQuantityAcrossDifferentPricesAndIncrements(t *testing.T) {
	input := validPreviewInput(time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC))
	input.Long.MarkPrice = decimal.NewFromInt(100)
	input.Short.MarkPrice = decimal.NewFromInt(120)
	input.Long.QuantityIncrement = decimal.RequireFromString("0.003")
	input.Short.QuantityIncrement = decimal.RequireFromString("0.002")
	input.Long.MinQuantity = decimal.RequireFromString("0.006")
	input.Short.MinQuantity = decimal.RequireFromString("0.006")

	preview, err := Preview(input)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}
	if preview.Blocked() {
		t.Fatalf("expected preview to pass, blockers=%v", preview.Blockers)
	}
	long, short := preview.PlannedLegs[0], preview.PlannedLegs[1]
	assertDecimal(t, long.Quantity, "4.164")
	assertDecimal(t, short.Quantity, "4.164")
	assertDecimal(t, long.Notional, "416.4")
	assertDecimal(t, short.Notional, "499.68")
}

func TestClosePreviewUsesCurrentQuantitiesAndIgnoresOpenProfitCapitalAndLiquidationChecks(t *testing.T) {
	input := validPreviewInput(time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC))
	input.Operation = OperationClose
	input.OpportunityID = ""
	input.PositionID = "position-1"
	input.AvailableCapitalUSDT = decimal.Zero
	input.GrossAprPct = decimal.NewFromInt(-100)
	input.LiquidationBufferPct = decimal.Zero
	input.CurrentPositionVerifiable = true
	input.CurrentLongQuantity = decimal.RequireFromString("1.235")
	input.CurrentShortQuantity = decimal.RequireFromString("1.236")
	input.Long.MarkPrice = decimal.NewFromInt(100)
	input.Short.MarkPrice = decimal.NewFromInt(200)

	preview, err := Preview(input)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}
	if preview.Blocked() {
		t.Fatalf("expected close risk-reducing preview to pass, blockers=%v", preview.Blockers)
	}
	assertDecimal(t, preview.PlannedLegs[0].Quantity, "1.235")
	assertDecimal(t, preview.PlannedLegs[1].Quantity, "1.236")
	input.Long.MarkPrice = decimal.NewFromInt(999)
	input.Short.MarkPrice = decimal.NewFromInt(777)
	changedPrice, err := Preview(input)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}
	assertDecimal(t, changedPrice.PlannedLegs[0].Quantity, "1.235")
	assertDecimal(t, changedPrice.PlannedLegs[1].Quantity, "1.236")
}

func TestClosePreviewTreatsUnavailableGatewayRiskLimitsAsWarning(t *testing.T) {
	input := validPreviewInput(time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC))
	input.Operation = OperationClose
	input.OpportunityID = ""
	input.PositionID = "position-1"
	input.CurrentPositionVerifiable = true
	input.CurrentLongQuantity = decimal.RequireFromString("1")
	input.CurrentShortQuantity = decimal.RequireFromString("1")
	input.CapacityNotional = decimal.Zero
	input.RiskVerifiable = false
	input.DataObservedAt = input.PreviewedAt.Add(-4 * time.Second)
	input.Long.MinQuantity = decimal.RequireFromString("2")

	preview, err := Preview(input)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}
	assertFindingCodes(t, preview.Blockers, []ReasonCode{
		ReasonStaleData,
		ReasonCapacityUsage,
		ReasonMinTradeRules,
	})
	assertFindingCodes(t, preview.Warnings, []ReasonCode{ReasonRiskUnverifiable})
}

func TestPreviewRequiresOpenOpportunityAndClosePosition(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)

	openMissingOpportunity := validPreviewInput(now)
	openMissingOpportunity.OpportunityID = ""
	preview, err := Preview(openMissingOpportunity)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}
	assertFindingCodes(t, preview.Blockers, []ReasonCode{ReasonInvalidOpportunityID})

	openWithPosition := validPreviewInput(now)
	openWithPosition.PositionID = "position-1"
	preview, err = Preview(openWithPosition)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}
	assertFindingCodes(t, preview.Blockers, []ReasonCode{ReasonInvalidPositionID})

	closeMissingPosition := validPreviewInput(now)
	closeMissingPosition.Operation = OperationClose
	closeMissingPosition.OpportunityID = ""
	closeMissingPosition.CurrentPositionVerifiable = true
	closeMissingPosition.CurrentLongQuantity = decimal.NewFromInt(5)
	closeMissingPosition.CurrentShortQuantity = decimal.NewFromInt(5)
	preview, err = Preview(closeMissingPosition)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}
	assertFindingCodes(t, preview.Blockers, []ReasonCode{ReasonInvalidPositionID})

	closeInput := closeMissingPosition
	closeInput.PositionID = "position-1"
	preview, err = Preview(closeInput)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}
	if preview.Blocked() {
		t.Fatalf("expected close preview with position to pass, blockers=%v", preview.Blockers)
	}
	if preview.PositionID != "position-1" {
		t.Fatalf("position id = %q, want position-1", preview.PositionID)
	}
}

func TestPreviewPreservesWarningsWhenBlocked(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	input := validPreviewInput(now)
	input.CapacityNotional = decimal.RequireFromString("600")
	input.StabilityPct = decimal.RequireFromString("79")
	input.LiquidationBufferPct = decimal.RequireFromString("45")
	input.RecentReversal = true

	preview, err := Preview(input)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}

	assertFindingCodes(t, preview.Blockers, []ReasonCode{ReasonCapacityUsage})
	assertFindingCodes(t, preview.Warnings, []ReasonCode{
		ReasonLowStability,
		ReasonCapacityUsage,
		ReasonLiquidationBuffer,
		ReasonRecentReversal,
	})
}

func TestPreviewValidationBlocksInvalidCapitalAndLeverage(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	input := validPreviewInput(now)
	input.CapitalUSDT = decimal.RequireFromString("99.99")
	input.Leverage = 3

	preview, err := Preview(input)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}

	assertFindingCodes(t, preview.Blockers, []ReasonCode{
		ReasonInvalidCapital,
		ReasonInvalidLeverage,
	})
}

func TestPreviewBlocksWhenRoundedQuantityCannotSatisfyMinRules(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	input := validPreviewInput(now)
	input.Long.MarkPrice = decimal.RequireFromString("10000")
	input.Long.MinQuantity = decimal.RequireFromString("1")
	input.Long.MinNotional = decimal.RequireFromString("10000")

	preview, err := Preview(input)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}

	assertFindingCodes(t, preview.Blockers, []ReasonCode{ReasonMinTradeRules})
	assertDecimal(t, preview.PlannedLegs[0].Quantity, "0.050")
}

func TestPreviewBlocksInvalidMarketRulesWithoutPanic(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	input := validPreviewInput(now)
	input.Long.MarkPrice = decimal.Zero
	input.Long.QuantityIncrement = decimal.Zero

	preview, err := Preview(input)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}

	assertFindingCodes(t, preview.Blockers, []ReasonCode{ReasonMinTradeRules})
	assertDecimal(t, preview.PlannedLegs[0].Quantity, "0")
}

func TestPreviewWarnsWhenGatewayRiskLimitsAreUnavailable(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	input := validPreviewInput(now)
	input.RiskVerifiable = false

	preview, err := Preview(input)
	if err != nil {
		t.Fatalf("Preview returned error: %v", err)
	}

	if preview.Blocked() || !preview.CanSubmit {
		t.Fatalf("user-controlled risk limits must not block an otherwise valid preview: %+v", preview.Blockers)
	}
	assertFindingCodes(t, preview.Warnings, []ReasonCode{ReasonRiskUnverifiable})
}

func validPreviewInput(now time.Time) PreviewInput {
	return PreviewInput{
		PreviewID:            "preview-1",
		SnapshotID:           "snapshot-1",
		PrincipalID:          "user-1",
		Mode:                 "test",
		Operation:            OperationOpen,
		OpportunityID:        "opp-1",
		AvailableCapitalUSDT: decimal.RequireFromString("1000"),
		CapitalUSDT:          decimal.RequireFromString("250"),
		Leverage:             2,
		GrossAprPct:          decimal.RequireFromString("100"),
		FeesUSDT:             decimal.RequireFromString("2.5"),
		SlippageUSDT:         decimal.RequireFromString("1.0"),
		DataObservedAt:       now.Add(-2 * time.Second),
		PreviewedAt:          now,
		CapacityNotional:     decimal.RequireFromString("5000"),
		LiquidationBufferPct: decimal.RequireFromString("60"),
		StabilityPct:         decimal.RequireFromString("95"),
		RiskVerifiable:       true,
		AccountVerifiable:    true,
		RulesVerifiable:      true,
		Long: PreviewLegInput{
			LegIdentity: LegIdentity{
				AccountID:        "acct-long",
				Venue:            "venue-a",
				InstrumentID:     "btc-perp-a",
				VenueEnvironment: "demo",
			},
			Symbol:              "BTCUSDT",
			MarkPrice:           decimal.RequireFromString("100"),
			QuantityIncrement:   decimal.RequireFromString("0.005"),
			MinQuantity:         decimal.RequireFromString("0.005"),
			MinNotional:         decimal.RequireFromString("5"),
			GatewayInstrumentID: "gw-long",
		},
		Short: PreviewLegInput{
			LegIdentity: LegIdentity{
				AccountID:        "acct-short",
				Venue:            "venue-b",
				InstrumentID:     "btc-perp-b",
				VenueEnvironment: "testnet",
			},
			Symbol:              "BTCUSDT",
			MarkPrice:           decimal.RequireFromString("199.99"),
			QuantityIncrement:   decimal.RequireFromString("0.001"),
			MinQuantity:         decimal.RequireFromString("0.001"),
			MinNotional:         decimal.RequireFromString("5"),
			GatewayInstrumentID: "gw-short",
		},
	}
}

func assertDecimal(t *testing.T, got decimal.Decimal, want string) {
	t.Helper()
	expected := decimal.RequireFromString(want)
	if !got.Equal(expected) {
		t.Fatalf("expected %s, got %s", expected, got)
	}
}

func assertFindingCodes(t *testing.T, findings []Finding, want []ReasonCode) {
	t.Helper()
	got := make(map[ReasonCode]int, len(findings))
	for _, finding := range findings {
		got[finding.Code]++
	}
	for _, code := range want {
		if got[code] == 0 {
			t.Fatalf("expected finding %s in %#v", code, findings)
		}
		got[code]--
	}
	for code, count := range got {
		if count > 0 {
			t.Fatalf("unexpected finding %s in %#v", code, findings)
		}
	}
}
