package planning

import (
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/calculation"
	"github.com/shopspring/decimal"
)

func TestDeriveOpenUsesAuthoritativeFactAndRejectsChangedFences(t *testing.T) {
	input := validInput()
	planningFact := planningTestFact(input, "reservation-long", "reservation-short")
	input = planningFact.Input
	plan, err := calculation.Compute(input)
	if err != nil || !plan.CanSubmit {
		t.Fatalf("plan=%+v err=%v", plan, err)
	}
	request, _ := json.Marshal(OpenIntent{PreviewID: plan.PreviewID, OpportunityID: plan.OpportunityID, LongAccountID: "account-long", ShortAccountID: "account-short", CanonicalMarketID: plan.RouteID, NotionalPerLeg: "50", MaxSlippageBps: "10", HoldThroughSettlement: true})
	fact, _ := json.Marshal(planningFact)
	facts := []Fact{{Kind: FactKind, Payload: fact}}
	fences := json.RawMessage(`[{"accountId":"account-long","desiredStateRevision":11,"runtimeGeneration":4,"credentialVersion":3},{"accountId":"account-short","desiredStateRevision":13,"runtimeGeneration":7,"credentialVersion":5}]`)
	first, err := DeriveOpen(request, fences, facts)
	if err != nil || first.Plan.NormalizedHash != plan.NormalizedHash || first.LongReservationID != "reservation-long" {
		t.Fatalf("derived=%+v err=%v", first, err)
	}
	second, err := DeriveOpen(request, fences, facts)
	if err != nil || second.Plan.NormalizedHash != first.Plan.NormalizedHash || string(second.IntentPayload) != string(first.IntentPayload) {
		t.Fatalf("replay=%+v err=%v", second, err)
	}
	stale := json.RawMessage(`[{"accountId":"account-long","desiredStateRevision":11,"runtimeGeneration":5,"credentialVersion":3},{"accountId":"account-short","desiredStateRevision":13,"runtimeGeneration":7,"credentialVersion":5}]`)
	if _, err := DeriveOpen(request, stale, facts); !errors.Is(err, ErrStaleFences) {
		t.Fatalf("stale fence err=%v", err)
	}
}

func TestDeriveOpenRejectsBlockedTestPlan(t *testing.T) {
	input := validInput()
	input.RequestedNotionalPerLeg = decimal.NewFromInt(101)
	planningFact := planningTestFact(input, "long", "short")
	input = planningFact.Input
	plan, _ := calculation.Compute(input)
	request, _ := json.Marshal(OpenIntent{PreviewID: plan.PreviewID, OpportunityID: plan.OpportunityID, LongAccountID: "account-long", ShortAccountID: "account-short", CanonicalMarketID: plan.RouteID, NotionalPerLeg: "101", MaxSlippageBps: "10"})
	fact, _ := json.Marshal(planningFact)
	if _, err := DeriveOpen(request, json.RawMessage(`[{"accountId":"account-long","desiredStateRevision":11,"runtimeGeneration":4,"credentialVersion":3},{"accountId":"account-short","desiredStateRevision":13,"runtimeGeneration":7,"credentialVersion":5}]`), []Fact{{Kind: FactKind, Payload: fact}}); !errors.Is(err, ErrInvalidWork) {
		t.Fatalf("oversized plan err=%v", err)
	}
}

func validInput() calculation.Input {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	leg := func(account string, generation int, venue, instrument, rate string) calculation.LegFact {
		price := decimal.NewFromInt(100)
		return calculation.LegFact{AccountFence: calculation.AccountFence{AccountID: account, RuntimeGeneration: generation}, Venue: venue, VenueEnvironment: "test", InstrumentID: instrument, Symbol: "BTCUSDT", Current: calculation.FundingPoint{Rate: decimal.RequireFromString(rate), ObservedAt: now.Add(-time.Second), FundingTime: now, NextFunding: now.Add(8 * time.Hour)}, History: []calculation.FundingPoint{{Rate: decimal.RequireFromString(rate)}}, MarketObservedAt: now.Add(-time.Second), MarkPrice: price, BBO: calculation.Quote{BidPrice: price, BidSize: decimal.NewFromInt(10), AskPrice: price, AskSize: decimal.NewFromInt(10)}, Depth: calculation.Depth{Bids: []calculation.Level{{Price: price, Quantity: decimal.NewFromInt(10)}}, Asks: []calculation.Level{{Price: price, Quantity: decimal.NewFromInt(10)}}}, QuantityIncrement: decimal.RequireFromString("0.001"), MinQuantity: decimal.RequireFromString("0.001"), MinNotional: decimal.NewFromInt(5), TakerFeeRate: decimal.RequireFromString("0.0001")}
	}
	return calculation.Input{Mode: "test", ProductID: "funding", OpportunityID: "opportunity-1", ObservedAt: now, RequestedNotionalPerLeg: decimal.NewFromInt(50), Long: leg("account-long", 4, "venue-a", "btc-a", "0.0001"), Short: leg("account-short", 7, "venue-b", "btc-b", "0.0011")}
}

func planningTestFact(input calculation.Input, longID, shortID string) PlanningFact {
	updated := input.ObservedAt.Add(-time.Minute)
	notional, currency := input.RequestedNotionalPerLeg.String(), "USDT"
	authorities := []ReservationAuthority{
		{ReservationID: longID, Revision: updated, PrincipalID: "platform:funding", Mode: input.Mode, ProductID: "funding", Kind: "open", AccountID: input.Long.AccountFence.AccountID, Venue: input.Long.Venue, VenueEnvironment: input.Long.VenueEnvironment, InstrumentID: input.Long.InstrumentID, Notional: &notional, Currency: &currency, Status: "held"},
		{ReservationID: shortID, Revision: updated, PrincipalID: "platform:funding", Mode: input.Mode, ProductID: "funding", Kind: "open", AccountID: input.Short.AccountFence.AccountID, Venue: input.Short.Venue, VenueEnvironment: input.Short.VenueEnvironment, InstrumentID: input.Short.InstrumentID, Notional: &notional, Currency: &currency, Status: "held"},
	}
	return NewPlanningFact(input, "platform:funding", longID, shortID, authorities, nil)
}
