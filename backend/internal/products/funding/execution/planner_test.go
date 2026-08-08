package fundingexecution

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/work"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/calculation"
	fundingpolicy "github.com/QuantProcessing/boltertrader/backend/internal/products/funding/policy"
	"github.com/shopspring/decimal"
)

func TestCommandPlanPreservesAuthoritativeCalculationProvenance(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	plan := validPlan(now)
	commandPlan, err := CommandPlan("platform:funding", "lineage-1", plan, PlanReservations{Long: "reservation-long", Short: "reservation-short"}, decimal.NewFromInt(50), 10, true)
	if err != nil {
		t.Fatal(err)
	}
	if commandPlan.PreviewHash != plan.NormalizedHash || commandPlan.Legs[0].RuntimeGeneration != 7 || commandPlan.Legs[1].RuntimeGeneration != 9 {
		t.Fatalf("command plan=%+v", commandPlan)
	}
	var intent work.FundingOpenIntent
	if json.Unmarshal(commandPlan.IntentPayload, &intent) != nil || intent.NotionalPerLeg != "50" {
		t.Fatalf("frozen payload=%s", commandPlan.IntentPayload)
	}
	store := work.NewMemoryFundingPlanStore()
	_, _, _ = store.ReserveFundingPlan(context.Background(), commandPlan)
	changed := commandPlan
	changed.PreviewHash = "changed-business-facts"
	if _, replay, err := store.ReserveFundingPlan(context.Background(), changed); !replay || !errors.Is(err, work.ErrIdempotencyConflict) {
		t.Fatalf("changed hash replay=%v err=%v", replay, err)
	}
}

func TestCommandPlanRejectsBlockedLiveOrOversizedLeg(t *testing.T) {
	now := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	for _, plan := range []calculation.Plan{{Mode: calculation.ModeTest}, {Mode: calculation.ModeLive, CanSubmit: true}, func() calculation.Plan {
		plan := validPlan(now)
		plan.Legs[1].Notional = decimal.NewFromInt(101)
		return plan
	}()} {
		if _, err := CommandPlan("platform:funding", "lineage", plan, PlanReservations{Long: "long", Short: "short"}, decimal.NewFromInt(50), 10, true); err == nil {
			t.Fatalf("unsafe plan accepted: %+v", plan)
		}
	}
}

func TestCommandPlanAllowsLiveOnlyThroughConfiguredAdmission(t *testing.T) {
	plan := validPlan(time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC))
	plan.Mode = calculation.ModeLive
	for index := range plan.Legs {
		plan.Legs[index].VenueEnvironment = "live"
	}
	if _, err := CommandPlan("platform:funding", "lineage", plan, PlanReservations{Long: "long", Short: "short"}, decimal.NewFromInt(50), 10, true); err == nil {
		t.Fatal("default admission accepted Live plan")
	}
	admission, err := fundingpolicy.NewAdmission(true, decimal.NewFromInt(200))
	if err != nil {
		t.Fatal(err)
	}
	command, err := CommandPlanWithAdmission("platform:funding", "lineage", plan, PlanReservations{Long: "long", Short: "short"}, decimal.NewFromInt(150), 10, true, admission)
	if err != nil || command.Mode != "live" {
		t.Fatalf("configured Live command=%+v err=%v", command, err)
	}
}

func validPlan(now time.Time) calculation.Plan {
	return calculation.Plan{
		PreviewID: "preview-1", OpportunityID: "opportunity-1", ProductID: "funding", Mode: calculation.ModeTest,
		RouteID: "venue-a:BTC-USDT|venue-b:BTC-USDT", ObservedAt: now, ExpiresAt: now.Add(30 * time.Second), NormalizedHash: "normalized-hash", CanSubmit: true,
		Legs: []calculation.PlannedLeg{
			{AccountID: "account-long", RuntimeGeneration: 7, Venue: "venue-long", VenueEnvironment: "test", InstrumentID: "BTC-USDT-LONG", Role: "long", Side: "buy", Quantity: decimal.RequireFromString("0.01"), Notional: decimal.NewFromInt(49)},
			{AccountID: "account-short", RuntimeGeneration: 9, Venue: "venue-short", VenueEnvironment: "test", InstrumentID: "BTC-USDT-SHORT", Role: "short", Side: "sell", Quantity: decimal.RequireFromString("0.01"), Notional: decimal.NewFromInt(50)},
		},
	}
}
