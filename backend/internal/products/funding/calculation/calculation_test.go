package calculation

import (
	"testing"
	"time"

	fundingpolicy "github.com/QuantProcessing/boltertrader/backend/internal/products/funding/policy"
	"github.com/shopspring/decimal"
)

func TestComputeMatchesFrozenOpportunityAndPreviewEconomics(t *testing.T) {
	input := validInput()
	plan, err := Compute(input)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.CanSubmit || len(plan.Blockers) != 0 {
		t.Fatalf("plan blocked: %+v", plan.Blockers)
	}
	assertDecimal(t, plan.GrossAPR, "1.095")
	assertDecimal(t, plan.RoundTripFees, "0.2")
	assertDecimal(t, plan.Slippage, "0.4")
	assertDecimal(t, plan.NetAPR, "0.7821428571428571")
	assertDecimal(t, plan.CapacityNotional, "700")
	assertDecimal(t, plan.Legs[0].Quantity, "0.828")
	assertDecimal(t, plan.Legs[1].Quantity, "0.828")
	assertDecimal(t, plan.Legs[0].Notional, "82.8")
	assertDecimal(t, plan.Legs[1].Notional, "99.36")
	if plan.Legs[0].Quantity.Cmp(plan.Legs[1].Quantity) != 0 || plan.NormalizedHash == "" {
		t.Fatalf("legs/hash=%+v %q", plan.Legs, plan.NormalizedHash)
	}

	replay, err := Compute(input)
	if err != nil || replay.NormalizedHash != plan.NormalizedHash || replay.PreviewID != plan.PreviewID {
		t.Fatalf("replay=%+v err=%v", replay, err)
	}
}

func TestComputeFailsClosedForStaleScheduleFenceAndMarketFacts(t *testing.T) {
	input := validInput()
	input.Long.AccountFence.RuntimeGeneration = 0
	input.Short.MarketObservedAt = input.ObservedAt.Add(-4 * time.Second)
	input.Short.Current.NextFunding = input.ObservedAt.Add(9 * time.Hour)
	input.Short.BBO.BidPrice = decimal.NewFromInt(121)
	input.Short.BBO.AskPrice = decimal.NewFromInt(120)
	plan, err := Compute(input)
	if err != nil {
		t.Fatal(err)
	}
	assertBlockers(t, plan, CodeAccountFence, CodeStaleData, CodeScheduleMismatch, CodeInvalidMarket)
	if plan.CanSubmit {
		t.Fatal("blocked plan can submit")
	}
}

func TestComputeEnforcesTestCapAndDefaultsToDenyLive(t *testing.T) {
	input := validInput()
	input.RequestedNotionalPerLeg = decimal.RequireFromString("100.01")
	plan, err := Compute(input)
	if err != nil {
		t.Fatal(err)
	}
	assertBlockers(t, plan, CodeTestNotionalLimit)

	input = validInput()
	input.Mode = ModeLive
	plan, err = Compute(input)
	if err != nil {
		t.Fatal(err)
	}
	assertBlockers(t, plan, CodeLiveDisabled)
}

func TestComputeAllowsConfiguredLiveWithinSeparateBound(t *testing.T) {
	admission, err := fundingpolicy.NewAdmission(true, decimal.NewFromInt(200))
	if err != nil {
		t.Fatal(err)
	}
	input := validInput()
	input.Mode = ModeLive
	input.RequestedNotionalPerLeg = decimal.NewFromInt(150)
	plan, err := ComputeWithAdmission(input, admission)
	if err != nil || !plan.CanSubmit {
		t.Fatalf("enabled Live plan=%+v err=%v", plan.Blockers, err)
	}
	input.RequestedNotionalPerLeg = decimal.RequireFromString("200.01")
	plan, err = ComputeWithAdmission(input, admission)
	if err != nil {
		t.Fatal(err)
	}
	assertBlockers(t, plan, CodeTestNotionalLimit)
}

func TestComputeRiskGateIsExplicitAndFailsClosedWithoutAuthority(t *testing.T) {
	plan, err := ComputeWithAdmission(validInput(), fundingpolicy.DefaultAdmission().WithRiskCheckEnabled(true))
	if err != nil {
		t.Fatal(err)
	}
	assertBlockers(t, plan, CodeRiskAuthority)
	if plan.CanSubmit {
		t.Fatal("risk-gated plan submitted without standardized authority")
	}
}

func TestComputeHashChangesWithBusinessFactsNotJSONPresentation(t *testing.T) {
	input := validInput()
	first, err := Compute(input)
	if err != nil {
		t.Fatal(err)
	}
	input.Short.Current.Rate = decimal.RequireFromString("0.001100")
	equivalent, err := Compute(input)
	if err != nil {
		t.Fatal(err)
	}
	if equivalent.NormalizedHash != first.NormalizedHash {
		t.Fatal("equivalent decimal presentation changed normalized hash")
	}
	input.Short.Current.Rate = decimal.RequireFromString("0.0012")
	changed, err := Compute(input)
	if err != nil {
		t.Fatal(err)
	}
	if changed.NormalizedHash == first.NormalizedHash {
		t.Fatal("business fact change did not change normalized hash")
	}
}

func validInput() Input {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	long := LegFact{
		AccountFence: AccountFence{AccountID: "account-long", RuntimeGeneration: 4},
		Venue:        "venue-a", VenueEnvironment: "test", InstrumentID: "btc-a", Symbol: "BTCUSDT",
		Current:          FundingPoint{Rate: decimal.RequireFromString("0.0001"), ObservedAt: now.Add(-time.Second), FundingTime: now, NextFunding: now.Add(8 * time.Hour)},
		History:          []FundingPoint{{Rate: decimal.RequireFromString("0.0001")}, {Rate: decimal.RequireFromString("0.0001")}},
		MarketObservedAt: now.Add(-time.Second), MarkPrice: decimal.NewFromInt(100),
		BBO:               Quote{BidPrice: decimal.RequireFromString("99.9"), BidSize: decimal.NewFromInt(10), AskPrice: decimal.RequireFromString("100.1"), AskSize: decimal.NewFromInt(10)},
		Depth:             Depth{Asks: []Level{{Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(10)}}, Bids: []Level{{Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(10)}}},
		QuantityIncrement: decimal.RequireFromString("0.003"), MinQuantity: decimal.RequireFromString("0.006"), MinNotional: decimal.NewFromInt(5), TakerFeeRate: decimal.RequireFromString("0.0004"),
	}
	short := LegFact{
		AccountFence: AccountFence{AccountID: "account-short", RuntimeGeneration: 7},
		Venue:        "venue-b", VenueEnvironment: "test", InstrumentID: "btc-b", Symbol: "BTCUSDT",
		Current:          FundingPoint{Rate: decimal.RequireFromString("0.0011"), ObservedAt: now.Add(-time.Second), FundingTime: now, NextFunding: now.Add(8 * time.Hour)},
		History:          []FundingPoint{{Rate: decimal.RequireFromString("0.0011")}, {Rate: decimal.RequireFromString("0.0011")}},
		MarketObservedAt: now.Add(-time.Second), MarkPrice: decimal.NewFromInt(120),
		BBO:               Quote{BidPrice: decimal.RequireFromString("119.88"), BidSize: decimal.NewFromInt(10), AskPrice: decimal.RequireFromString("120.12"), AskSize: decimal.NewFromInt(10)},
		Depth:             Depth{Asks: []Level{{Price: decimal.NewFromInt(120), Quantity: decimal.NewFromInt(10)}}, Bids: []Level{{Price: decimal.NewFromInt(120), Quantity: decimal.NewFromInt(10)}}},
		QuantityIncrement: decimal.RequireFromString("0.002"), MinQuantity: decimal.RequireFromString("0.006"), MinNotional: decimal.NewFromInt(5), TakerFeeRate: decimal.RequireFromString("0.0006"),
	}
	return Input{
		Mode: ModeTest, ProductID: "funding", OpportunityID: "opportunity-1", ObservedAt: now,
		RequestedNotionalPerLeg: decimal.NewFromInt(100),
		Long:                    long, Short: short,
	}
}

func assertDecimal(t *testing.T, got decimal.Decimal, want string) {
	t.Helper()
	if !got.Equal(decimal.RequireFromString(want)) {
		t.Fatalf("got %s want %s", got, want)
	}
}

func assertBlockers(t *testing.T, plan Plan, codes ...Code) {
	t.Helper()
	got := make(map[Code]bool, len(plan.Blockers))
	for _, blocker := range plan.Blockers {
		got[blocker.Code] = true
	}
	for _, code := range codes {
		if !got[code] {
			t.Fatalf("missing blocker %s in %+v", code, plan.Blockers)
		}
	}
}
