package opportunity

import (
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/risk"
	"github.com/shopspring/decimal"
)

func TestEvaluateBuildsDeterministicNetAPRAndDetailInputs(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	input := validInput(now)

	snapshot := Evaluate(input)

	if snapshot.Blocked() {
		t.Fatalf("snapshot unexpectedly blocked: %#v", snapshot.Blockers)
	}
	if snapshot.RouteID != "acct-a:binance:BTC-PERP:demo|long->acct-b:bybit:BTC-PERP:testnet|short" {
		t.Fatalf("route id = %q", snapshot.RouteID)
	}
	assertDecimal(t, "gross apr", snapshot.GrossAPR, "0.1095")
	expectedFees := input.Notional.
		Mul(input.Fees.LongTakerRate.Add(input.Fees.ShortTakerRate)).
		Mul(decimal.NewFromInt(2))
	expectedSlippage := decimal.RequireFromString("5.997002997002997").Mul(decimal.NewFromInt(2))
	expectedNetAPR := snapshot.GrossAPR.Sub(
		expectedFees.Add(expectedSlippage).
			Div(input.Notional).
			Mul(decimal.NewFromInt(365)).
			Div(decimal.NewFromInt(7)),
	)
	expectedBasis := input.Short.MarkPrice.Sub(input.Long.MarkPrice).
		Div(input.Short.MarkPrice.Add(input.Long.MarkPrice).Div(decimal.NewFromInt(2)))
	expectedCapacity := decimal.RequireFromString("10001").
		Mul(decimal.RequireFromString("6")).
		Mul(decimal.RequireFromString("0.70"))
	assertDecimal(t, "net apr", snapshot.NetAPR, expectedNetAPR.String())
	assertDecimal(t, "round trip fees", snapshot.RoundTripFees, expectedFees.String())
	assertDecimal(t, "slippage", snapshot.Slippage, expectedSlippage.String())
	assertDecimal(t, "basis", snapshot.Basis, expectedBasis.String())
	assertDecimal(t, "capacity", snapshot.CapacityNotional, expectedCapacity.String())
	assertDecimal(t, "capacity usage pct", snapshot.Calculation.CapacityUsagePercent,
		input.Notional.Div(expectedCapacity).Mul(decimal.NewFromInt(100)).String())
	if snapshot.FormulaVersion != FormulaVersion {
		t.Fatalf("formula version = %q", snapshot.FormulaVersion)
	}
	if !snapshot.NextFunding.Equal(now.Add(8 * time.Hour)) {
		t.Fatalf("next funding = %s", snapshot.NextFunding)
	}
	if len(snapshot.Calculation.LongHistoryRates) != 3 || len(snapshot.Calculation.ShortHistoryRates) != 3 {
		t.Fatalf("history rates not retained in calculation detail: %#v", snapshot.Calculation)
	}
	if snapshot.Stability.LessThan(decimal.RequireFromString("99")) {
		t.Fatalf("stability = %s, want high stability", snapshot.Stability)
	}
}

func TestEvaluateKeepsBlockedOpportunitiesVisible(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	input := validInput(now)
	input.Short.Venue = input.Long.Venue
	input.Short.Current.ObservedAt = now.Add(-4 * time.Second)
	input.Short.BBO.BidPrice = decimal.RequireFromString("10010")
	input.Short.BBO.AskPrice = decimal.RequireFromString("10000")
	input.Short.Depth.Bids = nil

	snapshot := Evaluate(input)

	if snapshot.RouteID == "" {
		t.Fatal("blocked opportunity should keep route identity visible")
	}
	assertHasBlocker(t, snapshot, CodeIncompatibleLegs)
	assertHasBlocker(t, snapshot, CodeStaleData)
	assertHasBlocker(t, snapshot, CodeInvalidMarket)
	assertHasBlocker(t, snapshot, CodeNonPositiveCapacity)
	if !snapshot.Blocked() {
		t.Fatal("snapshot should be blocked")
	}
}

func TestEvaluateBlocksScheduleMismatchAndWarnsOnRiskThresholds(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	input := validInput(now)
	input.Short.Current.NextFunding = now.Add(9 * time.Hour)
	input.LiquidationBufferPct = decimal.RequireFromString("49.99")
	input.Long.History[2].Rate = decimal.RequireFromString("0.0004")
	input.Short.History[2].Rate = decimal.RequireFromString("-0.0001")

	snapshot := Evaluate(input)

	assertHasBlocker(t, snapshot, CodeScheduleMismatch)
	assertHasWarning(t, snapshot, risk.CodeLiquidationBuffer)
	assertHasWarning(t, snapshot, risk.CodeRecentReversal)
}

func TestEvaluateFailsClosedWhenRequiredHistoryCannotBePaired(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	input := validInput(now)
	input.RequireHistoryPairing = true
	input.Long.History = nil
	input.Short.History = nil

	snapshot := Evaluate(input)

	assertHasBlocker(t, snapshot, CodeUnverifiableInputs)
}

func TestEvaluateDerivesFundingIntervalFromCurrentNextFundingAndRecentPairedHistory(t *testing.T) {
	now := time.Date(2026, 7, 30, 3, 33, 0, 0, time.UTC)
	input := validInput(now)
	lastSettlement := time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	nextSettlement := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	input.Long.Current.FundingTime = time.Time{}
	input.Short.Current.FundingTime = time.Time{}
	input.Long.Current.NextFunding = nextSettlement
	input.Short.Current.NextFunding = nextSettlement
	input.Long.History = []FundingPoint{
		{Rate: decimal.RequireFromString("0.0001"), FundingTime: lastSettlement.Add(-16 * time.Hour)},
		{Rate: decimal.RequireFromString("0.0001"), FundingTime: lastSettlement.Add(-8 * time.Hour)},
		{Rate: decimal.RequireFromString("0.0001"), FundingTime: lastSettlement},
	}
	input.Short.History = []FundingPoint{
		{Rate: decimal.RequireFromString("0.0002"), FundingTime: lastSettlement.Add(-16 * time.Hour)},
		{Rate: decimal.RequireFromString("0.0002"), FundingTime: lastSettlement.Add(-8 * time.Hour)},
		{Rate: decimal.RequireFromString("0.0002"), FundingTime: lastSettlement},
	}

	snapshot := Evaluate(input)

	if snapshot.Blocked() {
		t.Fatalf("snapshot unexpectedly blocked: %#v", snapshot.Blockers)
	}
	assertDecimal(t, "periods/year", snapshot.Calculation.PeriodsPerYear, "1095")
	assertDecimal(t, "gross apr", snapshot.GrossAPR, "0.1095")
}

func TestEvaluateAcceptsSubsecondFundingTimestampJitter(t *testing.T) {
	now := time.Date(2026, 8, 3, 3, 33, 0, 0, time.UTC)
	input := validInput(now)
	previousSettlement := time.Date(2026, 8, 2, 16, 0, 0, 1_000_000, time.UTC)
	lastSettlement := time.Date(2026, 8, 3, 0, 0, 0, 1_000_000, time.UTC)
	nextSettlement := time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)
	input.Long.Current.FundingTime = time.Time{}
	input.Short.Current.FundingTime = time.Time{}
	input.Long.Current.NextFunding = nextSettlement
	input.Short.Current.NextFunding = nextSettlement
	input.Long.History = []FundingPoint{
		{Rate: decimal.RequireFromString("0.0001"), FundingTime: previousSettlement},
		{Rate: decimal.RequireFromString("0.0001"), FundingTime: lastSettlement},
	}
	input.Short.History = []FundingPoint{
		{Rate: decimal.RequireFromString("0.0002"), FundingTime: previousSettlement.Truncate(time.Second)},
		{Rate: decimal.RequireFromString("0.0002"), FundingTime: lastSettlement.Truncate(time.Second)},
	}

	snapshot := Evaluate(input)

	if snapshot.Blocked() {
		t.Fatalf("snapshot unexpectedly blocked by timestamp precision noise: %#v", snapshot.Blockers)
	}
	assertDecimal(t, "periods/year", snapshot.Calculation.PeriodsPerYear, "1095")
}

func TestEvaluateFailsClosedWhenDerivedFundingIntervalsDifferOrHistoryIsMissing(t *testing.T) {
	now := time.Date(2026, 7, 30, 3, 33, 0, 0, time.UTC)
	nextSettlement := time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)

	t.Run("different recent history intervals", func(t *testing.T) {
		input := validInput(now)
		input.Long.Current.FundingTime = time.Time{}
		input.Short.Current.FundingTime = time.Time{}
		input.Long.Current.NextFunding = nextSettlement
		input.Short.Current.NextFunding = nextSettlement
		input.Long.History = []FundingPoint{{Rate: decimal.RequireFromString("0.0001"), FundingTime: nextSettlement.Add(-8 * time.Hour)}}
		input.Short.History = []FundingPoint{{Rate: decimal.RequireFromString("0.0002"), FundingTime: nextSettlement.Add(-9 * time.Hour)}}

		snapshot := Evaluate(input)

		assertHasBlocker(t, snapshot, CodeUnverifiableInputs)
	})

	t.Run("missing recent history", func(t *testing.T) {
		input := validInput(now)
		input.Long.Current.FundingTime = time.Time{}
		input.Short.Current.FundingTime = time.Time{}
		input.Long.Current.NextFunding = nextSettlement
		input.Short.Current.NextFunding = nextSettlement
		input.Long.History = nil
		input.Short.History = []FundingPoint{{Rate: decimal.RequireFromString("0.0002"), FundingTime: nextSettlement.Add(-8 * time.Hour)}}

		snapshot := Evaluate(input)

		assertHasBlocker(t, snapshot, CodeUnverifiableInputs)
	})
}

func TestEvaluateFailsClosedWhenDerivedFundingIntervalLacksDirectPreviousSettlement(t *testing.T) {
	now := time.Date(2026, 7, 30, 3, 33, 0, 0, time.UTC)
	nextSettlement := time.Date(2026, 7, 30, 16, 0, 0, 0, time.UTC)
	input := validInput(now)
	input.Long.Current.FundingTime = time.Time{}
	input.Short.Current.FundingTime = time.Time{}
	input.Long.Current.NextFunding = nextSettlement
	input.Short.Current.NextFunding = nextSettlement
	input.Long.History = []FundingPoint{
		{Rate: decimal.RequireFromString("0.0001"), FundingTime: time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)},
		{Rate: decimal.RequireFromString("0.0001"), FundingTime: time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)},
	}
	input.Short.History = []FundingPoint{
		{Rate: decimal.RequireFromString("0.0002"), FundingTime: time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)},
		{Rate: decimal.RequireFromString("0.0002"), FundingTime: time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)},
	}

	snapshot := Evaluate(input)

	assertHasBlocker(t, snapshot, CodeUnverifiableInputs)
}

func TestEvaluateFailsClosedWhenDerivedFundingHistoryCadenceIsIrregular(t *testing.T) {
	now := time.Date(2026, 7, 30, 3, 33, 0, 0, time.UTC)
	nextSettlement := time.Date(2026, 7, 30, 16, 0, 0, 0, time.UTC)
	input := validInput(now)
	input.Long.Current.FundingTime = time.Time{}
	input.Short.Current.FundingTime = time.Time{}
	input.Long.Current.NextFunding = nextSettlement
	input.Short.Current.NextFunding = nextSettlement
	input.Long.History = []FundingPoint{
		{Rate: decimal.RequireFromString("0.0001"), FundingTime: time.Date(2026, 7, 29, 23, 0, 0, 0, time.UTC)},
		{Rate: decimal.RequireFromString("0.0001"), FundingTime: time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)},
	}
	input.Short.History = []FundingPoint{
		{Rate: decimal.RequireFromString("0.0002"), FundingTime: time.Date(2026, 7, 29, 23, 0, 0, 0, time.UTC)},
		{Rate: decimal.RequireFromString("0.0002"), FundingTime: time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)},
	}

	snapshot := Evaluate(input)

	assertHasBlocker(t, snapshot, CodeUnverifiableInputs)
}

func TestEvaluateUsesExplicitCurrentFundingTimeBeforeHistoryCadence(t *testing.T) {
	now := time.Date(2026, 7, 30, 3, 33, 0, 0, time.UTC)
	input := validInput(now)
	input.Long.Current.FundingTime = time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	input.Short.Current.FundingTime = time.Date(2026, 7, 30, 0, 0, 0, 0, time.UTC)
	input.Long.Current.NextFunding = time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	input.Short.Current.NextFunding = time.Date(2026, 7, 30, 8, 0, 0, 0, time.UTC)
	input.Long.History = nil
	input.Short.History = nil

	snapshot := Evaluate(input)

	if snapshot.Blocked() {
		t.Fatalf("snapshot unexpectedly blocked: %#v", snapshot.Blockers)
	}
	assertDecimal(t, "periods/year", snapshot.Calculation.PeriodsPerYear, "1095")
}

func TestEvaluateAcceptsSubsecondCurrentFundingTimestampJitter(t *testing.T) {
	now := time.Date(2026, 8, 3, 3, 33, 0, 0, time.UTC)
	input := validInput(now)
	lastSettlement := time.Date(2026, 8, 3, 0, 0, 0, 0, time.UTC)
	nextSettlement := time.Date(2026, 8, 3, 8, 0, 0, 0, time.UTC)
	input.Long.Current.FundingTime = lastSettlement.Add(time.Millisecond)
	input.Short.Current.FundingTime = lastSettlement
	input.Long.Current.NextFunding = nextSettlement
	input.Short.Current.NextFunding = nextSettlement

	snapshot := Evaluate(input)

	if snapshot.Blocked() {
		t.Fatalf("snapshot unexpectedly blocked by current timestamp precision noise: %#v", snapshot.Blockers)
	}
	assertDecimal(t, "periods/year", snapshot.Calculation.PeriodsPerYear, "1095")
}

func TestEvaluateBlocksStaleMarketDataEvenWhenFundingIsFresh(t *testing.T) {
	now := time.Date(2026, 7, 30, 12, 0, 0, 0, time.UTC)
	input := validInput(now)
	input.Long.Current.ObservedAt = now.Add(-time.Second)
	input.Short.Current.ObservedAt = now.Add(-time.Second)
	input.Short.MarketObservedAt = now.Add(-4 * time.Second)

	snapshot := Evaluate(input)

	assertHasBlocker(t, snapshot, CodeStaleData)
	if snapshot.DataAge != 4*time.Second {
		t.Fatalf("data age=%s want=4s", snapshot.DataAge)
	}
}

func validInput(now time.Time) Input {
	return Input{
		ObservedAt: now,
		Notional:   decimal.RequireFromString("30000"),
		Long: Leg{
			AccountID:        "acct-a",
			InstrumentID:     "BTC-PERP",
			Venue:            "binance",
			VenueEnvironment: "demo",
			Symbol:           "BTCUSDT",
			Current: FundingPoint{
				Rate:        decimal.RequireFromString("0.0001"),
				ObservedAt:  now.Add(-2 * time.Second),
				FundingTime: now,
				NextFunding: now.Add(8 * time.Hour),
			},
			History: []FundingPoint{
				{Rate: decimal.RequireFromString("0.0001")},
				{Rate: decimal.RequireFromString("0.0001")},
				{Rate: decimal.RequireFromString("0.0001")},
			},
			MarketObservedAt: now.Add(-time.Second),
			MarkPrice:        decimal.RequireFromString("10000"),
			BBO: Quote{
				BidPrice: decimal.RequireFromString("9999"),
				BidSize:  decimal.RequireFromString("3"),
				AskPrice: decimal.RequireFromString("10001"),
				AskSize:  decimal.RequireFromString("3"),
			},
			Depth: Depth{
				Asks: []Level{
					{Price: decimal.RequireFromString("10001"), Quantity: decimal.RequireFromString("6")},
					{Price: decimal.RequireFromString("11000"), Quantity: decimal.RequireFromString("1000")},
				},
			},
		},
		Short: Leg{
			AccountID:        "acct-b",
			InstrumentID:     "BTC-PERP",
			Venue:            "bybit",
			VenueEnvironment: "testnet",
			Symbol:           "BTCUSDT",
			Current: FundingPoint{
				Rate:        decimal.RequireFromString("0.0002"),
				ObservedAt:  now.Add(-1 * time.Second),
				FundingTime: now,
				NextFunding: now.Add(8 * time.Hour),
			},
			History: []FundingPoint{
				{Rate: decimal.RequireFromString("0.0002")},
				{Rate: decimal.RequireFromString("0.0002")},
				{Rate: decimal.RequireFromString("0.0002")},
			},
			MarketObservedAt: now.Add(-time.Second),
			MarkPrice:        decimal.RequireFromString("10010"),
			BBO: Quote{
				BidPrice: decimal.RequireFromString("10009"),
				BidSize:  decimal.RequireFromString("3"),
				AskPrice: decimal.RequireFromString("10011"),
				AskSize:  decimal.RequireFromString("3"),
			},
			Depth: Depth{
				Bids: []Level{
					{Price: decimal.RequireFromString("10009"), Quantity: decimal.RequireFromString("6")},
					{Price: decimal.RequireFromString("9000"), Quantity: decimal.RequireFromString("1000")},
				},
			},
		},
		Fees: Fees{
			LongTakerRate:  decimal.RequireFromString("0.0003"),
			ShortTakerRate: decimal.RequireFromString("0.0003"),
		},
		LiquidationBufferPct: decimal.RequireFromString("55"),
	}
}

func assertDecimal(t *testing.T, label string, got decimal.Decimal, want string) {
	t.Helper()
	expected := decimal.RequireFromString(want)
	if !got.Equal(expected) {
		t.Fatalf("%s = %s, want %s", label, got, expected)
	}
}

func assertHasBlocker(t *testing.T, snapshot Snapshot, code Code) {
	t.Helper()
	for _, blocker := range snapshot.Blockers {
		if blocker.Code == code {
			return
		}
	}
	t.Fatalf("missing blocker %s in %#v", code, snapshot.Blockers)
}

func assertHasWarning(t *testing.T, snapshot Snapshot, code any) {
	t.Helper()
	want := stringCode(code)
	for _, warning := range snapshot.Warnings {
		if stringCode(warning.Code) == want {
			return
		}
	}
	t.Fatalf("missing warning %s in %#v", want, snapshot.Warnings)
}

func stringCode(code any) string {
	switch typed := code.(type) {
	case Code:
		return string(typed)
	case risk.Code:
		return string(typed)
	default:
		return ""
	}
}
