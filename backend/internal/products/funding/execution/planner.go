package fundingexecution

import (
	"encoding/json"
	"fmt"
	"strconv"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/work"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/calculation"
	fundingpolicy "github.com/QuantProcessing/boltertrader/backend/internal/products/funding/policy"
	"github.com/shopspring/decimal"
)

type PlanReservations struct {
	Long  string
	Short string
}

func CommandPlan(principalID, lineageID string, plan calculation.Plan, reservations PlanReservations, requestedNotional decimal.Decimal, maxSlippageBps int, holdThroughSettlement bool) (work.FundingCommandPlan, error) {
	return CommandPlanWithAdmission(principalID, lineageID, plan, reservations, requestedNotional, maxSlippageBps, holdThroughSettlement, fundingpolicy.DefaultAdmission())
}

func CommandPlanWithAdmission(principalID, lineageID string, plan calculation.Plan, reservations PlanReservations, requestedNotional decimal.Decimal, maxSlippageBps int, holdThroughSettlement bool, admission fundingpolicy.Admission) (work.FundingCommandPlan, error) {
	if principalID == "" || lineageID == "" || (plan.Mode != calculation.ModeTest && plan.Mode != calculation.ModeLive) || !admission.Allows(plan.Mode, requestedNotional) || !plan.CanSubmit || plan.NormalizedHash == "" || plan.PreviewID == "" || plan.OpportunityID == "" || plan.RouteID == "" || len(plan.Legs) != 2 || reservations.Long == "" || reservations.Short == "" || maxSlippageBps < 0 {
		return work.FundingCommandPlan{}, work.ErrWorkRevisionConflict
	}
	byRole := make(map[string]calculation.PlannedLeg, len(plan.Legs))
	for _, leg := range plan.Legs {
		byRole[leg.Role] = leg
	}
	long, longOK := byRole["long"]
	short, shortOK := byRole["short"]
	if !longOK || !shortOK || long.AccountID == short.AccountID || !long.Quantity.Equal(short.Quantity) || long.ReduceOnly || short.ReduceOnly ||
		long.RuntimeGeneration < 1 || short.RuntimeGeneration < 1 || long.Venue == "" || short.Venue == "" || long.VenueEnvironment == "" || short.VenueEnvironment == "" || long.InstrumentID == "" || short.InstrumentID == "" {
		return work.FundingCommandPlan{}, work.ErrWorkRevisionConflict
	}
	if !long.Notional.IsPositive() || !short.Notional.IsPositive() || long.Notional.GreaterThan(requestedNotional) || short.Notional.GreaterThan(requestedNotional) {
		return work.FundingCommandPlan{}, work.ErrWorkRevisionConflict
	}
	intent := work.FundingOpenIntent{
		PreviewID: plan.PreviewID, OpportunityID: plan.OpportunityID, LongAccountID: long.AccountID,
		ShortAccountID: short.AccountID, CanonicalMarketID: plan.RouteID,
		NotionalPerLeg: requestedNotional.String(), MaxSlippageBps: strconv.Itoa(maxSlippageBps),
		HoldThroughSettlement: holdThroughSettlement,
	}
	payload, err := json.Marshal(intent)
	if err != nil {
		return work.FundingCommandPlan{}, fmt.Errorf("encode Funding open intent: %w", err)
	}
	return work.FundingCommandPlan{
		PlanKey: "open:" + plan.PreviewID, PreviewHash: plan.NormalizedHash, PrincipalID: principalID, Mode: orders.Mode(plan.Mode),
		IntentKind: "funding.open", IntentPayload: payload, LineageID: lineageID,
		Legs: []work.FundingOrderDraft{
			{LegID: "long", AccountID: long.AccountID, RuntimeGeneration: long.RuntimeGeneration, Venue: long.Venue, VenueEnvironment: long.VenueEnvironment, ReservationID: reservations.Long, InstrumentID: long.InstrumentID, Side: "buy", Quantity: long.Quantity.String(), Notional: long.Notional.String(), OrderType: "market"},
			{LegID: "short", AccountID: short.AccountID, RuntimeGeneration: short.RuntimeGeneration, Venue: short.Venue, VenueEnvironment: short.VenueEnvironment, ReservationID: reservations.Short, InstrumentID: short.InstrumentID, Side: "sell", Quantity: short.Quantity.String(), Notional: short.Notional.String(), OrderType: "market"},
		},
		ExpiresAt: plan.ExpiresAt.UTC(), CreatedAt: plan.ObservedAt.UTC(),
	}, nil
}
