package fundingexecution

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/work"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/accounting"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/planning"
	fundingpolicy "github.com/QuantProcessing/boltertrader/backend/internal/products/funding/policy"
	"github.com/shopspring/decimal"
)

type PlanProducer struct {
	plans      work.FundingPlanStore
	executions work.FundingExecutionStore
	accounting *accounting.PlatformAdapter
	admission  fundingpolicy.Admission
}

func (producer *PlanProducer) WithAdmission(admission fundingpolicy.Admission) *PlanProducer {
	producer.admission = admission
	return producer
}

func (producer *PlanProducer) WithAccounting(adapter *accounting.PlatformAdapter) *PlanProducer {
	producer.accounting = adapter
	return producer
}

func NewPlanProducer(plans work.FundingPlanStore, executions work.FundingExecutionStore) (*PlanProducer, error) {
	if plans == nil || executions == nil {
		return nil, fmt.Errorf("Funding plan and execution stores are required")
	}
	return &PlanProducer{plans: plans, executions: executions}, nil
}

func (producer *PlanProducer) Prepare(ctx context.Context, record work.IntentRecord) error {
	if err := producer.projectAccounting(ctx, record.Facts); err != nil {
		return err
	}
	if record.Command.CommandKind == "funding.reconcile" {
		return nil
	}
	var plan work.FundingCommandPlan
	var err error
	if record.Command.CommandKind != "funding.open" {
		plan, err = producer.unwindPlan(ctx, record)
		if err != nil {
			return err
		}
		return producer.reserve(ctx, plan)
	}
	facts := make([]planning.Fact, 0, len(record.Facts))
	for _, fact := range record.Facts {
		facts = append(facts, planning.Fact{Kind: fact.Kind, Payload: fact.Payload})
	}
	derived, err := planning.DeriveOpenWithAdmission(record.WorkPayload, record.AccountFences, facts, producer.admission)
	if err != nil {
		return err
	}
	requestedNotional, err := decimal.NewFromString(derived.Intent.NotionalPerLeg)
	if err != nil {
		return work.ErrWorkRevisionConflict
	}
	plan, err = CommandPlanWithAdmission(record.Command.PrincipalID, record.Command.LineageID, derived.Plan, PlanReservations{Long: derived.LongReservationID, Short: derived.ShortReservationID}, requestedNotional, derived.MaxSlippageBps, derived.Intent.HoldThroughSettlement, producer.admission)
	if err != nil {
		return err
	}
	for index := range plan.Legs {
		fence, ok := derived.AccountFences[plan.Legs[index].AccountID]
		if !ok {
			return work.ErrWorkRevisionConflict
		}
		plan.Legs[index].DesiredStateRevision = fence.DesiredStateRevision
		plan.Legs[index].RuntimeGeneration = fence.RuntimeGeneration
		plan.Legs[index].CredentialVersion = fence.CredentialVersion
	}
	if !work.EqualJSON(plan.IntentPayload, record.Command.Payload) {
		return fmt.Errorf("worker intent does not match authoritative Funding plan")
	}
	return producer.reserve(ctx, plan)
}

func (producer *PlanProducer) projectAccounting(ctx context.Context, source []work.Fact) error {
	if producer.accounting == nil {
		return nil
	}
	groups := make(map[string][]accounting.Fact)
	for _, sourceFact := range source {
		if !strings.HasPrefix(sourceFact.Kind, "funding.accounting.") {
			continue
		}
		var fact accounting.Fact
		if json.Unmarshal(sourceFact.Payload, &fact) != nil {
			return work.ErrWorkRevisionConflict
		}
		key := strings.Join([]string{fact.ProductID, fact.Mode, fact.LineageID, fact.PositionID}, "\x00")
		groups[key] = append(groups[key], fact)
	}
	keys := make([]string, 0, len(groups))
	for key := range groups {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		facts := groups[key]
		sort.Slice(facts, func(i, j int) bool {
			if facts[i].EffectiveAt.Equal(facts[j].EffectiveAt) {
				return facts[i].FactID < facts[j].FactID
			}
			return facts[i].EffectiveAt.Before(facts[j].EffectiveAt)
		})
		groups[key] = facts
		for _, fact := range facts {
			if fact.SupersedesFactID != "" {
				return fmt.Errorf("Funding accounting supersession requires atomic projection replacement")
			}
		}
		if _, err := accounting.Rebuild(facts); err != nil {
			return err
		}
	}
	for _, key := range keys {
		facts := groups[key]
		for _, fact := range facts {
			if err := producer.accounting.Apply(ctx, fact); err != nil {
				return err
			}
		}
	}
	return nil
}

func (producer *PlanProducer) reserve(ctx context.Context, plan work.FundingCommandPlan) error {
	stored, _, err := producer.plans.ReserveFundingPlan(ctx, plan)
	if err != nil {
		return err
	}
	if stored.PreviewHash != plan.PreviewHash || !work.EqualJSON(stored.IntentPayload, plan.IntentPayload) {
		return work.ErrIdempotencyConflict
	}
	return nil
}

func (producer *PlanProducer) unwindPlan(ctx context.Context, record work.IntentRecord) (work.FundingCommandPlan, error) {
	execution, err := producer.executions.GetFundingExecution(ctx, record.Command.PrincipalID, record.Command.Mode, record.Command.LineageID)
	if err != nil {
		return work.FundingCommandPlan{}, err
	}
	reservations := make(map[string]planning.ReservationAuthority)
	effectiveAt := make(map[string]time.Time)
	for _, fact := range record.Facts {
		if fact.Kind != "funding.unwind-reservation" {
			continue
		}
		var value planning.ReservationAuthority
		if json.Unmarshal(fact.Payload, &value) != nil || value.ReservationID == "" || fact.AccountID == "" {
			return work.FundingCommandPlan{}, work.ErrWorkRevisionConflict
		}
		if _, duplicate := reservations[fact.AccountID]; duplicate {
			return work.FundingCommandPlan{}, work.ErrWorkRevisionConflict
		}
		reservations[fact.AccountID] = value
		effectiveAt[fact.AccountID] = fact.EffectiveAt
	}
	legs := make([]work.FundingOrderDraft, 0, 2)
	planKey := ""
	switch record.Command.CommandKind {
	case "funding.close":
		var intent work.FundingCloseIntent
		if json.Unmarshal(record.Command.Payload, &intent) != nil || intent.LineageID != execution.LineageID || intent.RequestedAt.IsZero() || !work.EqualJSON(record.Command.Payload, record.WorkPayload) || len(reservations) != 2 {
			return work.FundingCommandPlan{}, work.ErrWorkRevisionConflict
		}
		planKey = "close:" + intent.LineageID
		for _, leg := range execution.Legs {
			if leg.Draft.AccountID == "" || leg.Draft.Quantity == "" || len(legs) == 2 {
				continue
			}
			reservation, ok := reservations[leg.Draft.AccountID]
			if !ok || !effectiveAt[leg.Draft.AccountID].Equal(intent.RequestedAt) || !validUnwindAuthority(reservation, record, leg.Draft, intent.RequestedAt) {
				return work.FundingCommandPlan{}, work.ErrWorkRevisionConflict
			}
			draft := reverseDraft(leg.Draft, reservation.ReservationID, "close_"+leg.Role)
			legs = append(legs, draft)
		}
	case "funding.compensate":
		var intent work.FundingCompensateIntent
		if json.Unmarshal(record.Command.Payload, &intent) != nil || intent.LineageID != execution.LineageID {
			return work.FundingCommandPlan{}, work.ErrWorkRevisionConflict
		}
		planKey = strings.Join([]string{"compensate", intent.LineageID, intent.FailedCommandID, intent.SurvivingCommandID}, ":")
		for _, leg := range execution.Legs {
			if leg.CommandID != intent.SurvivingCommandID {
				continue
			}
			reservation := reservations[leg.Draft.AccountID]
			draft := reverseDraft(leg.Draft, reservation.ReservationID, "compensation")
			if leg.FilledQuantity != "" {
				draft.Quantity = leg.FilledQuantity
			}
			draft.Notional = intent.MaxCompensationNotional
			legs = append(legs, draft)
		}
	default:
		return work.FundingCommandPlan{}, work.ErrWorkRevisionConflict
	}
	if len(legs) == 0 {
		return work.FundingCommandPlan{}, work.ErrWorkRevisionConflict
	}
	sum := sha256.Sum256(append(append([]byte(nil), record.Command.Payload...), record.WorkPayload...))
	created := record.Command.CreatedAt.UTC()
	if created.IsZero() {
		created = time.Now().UTC()
	}
	return work.FundingCommandPlan{PlanKey: planKey, PreviewHash: hex.EncodeToString(sum[:]), PrincipalID: record.Command.PrincipalID, Mode: record.Command.Mode, IntentKind: record.Command.CommandKind, IntentPayload: record.Command.Payload, LineageID: record.Command.LineageID, Legs: legs, CreatedAt: created, ExpiresAt: created.Add(30 * time.Second)}, nil
}

func validUnwindAuthority(authority planning.ReservationAuthority, record work.IntentRecord, draft work.FundingOrderDraft, requestedAt time.Time) bool {
	if authority.Revision.IsZero() || authority.ProductID != "funding" || authority.Mode != string(record.Command.Mode) || authority.Kind != "close" || authority.Status != "held" || authority.AccountID != draft.AccountID || authority.Venue != draft.Venue || authority.VenueEnvironment != draft.VenueEnvironment || authority.InstrumentID != draft.InstrumentID || authority.PrincipalID == "" {
		return false
	}
	if authority.ExpiresAt != nil && !requestedAt.Before(authority.ExpiresAt.UTC()) {
		return false
	}
	wantQuantity, err := decimal.NewFromString(draft.Quantity)
	if err != nil || !wantQuantity.IsPositive() {
		return false
	}
	if authority.Quantity != nil {
		value, parseErr := decimal.NewFromString(*authority.Quantity)
		return parseErr == nil && value.GreaterThanOrEqual(wantQuantity)
	}
	if authority.Notional == nil || authority.Currency == nil || *authority.Currency == "" {
		return false
	}
	wantNotional, err := decimal.NewFromString(draft.Notional)
	value, parseErr := decimal.NewFromString(*authority.Notional)
	return err == nil && parseErr == nil && value.GreaterThanOrEqual(wantNotional)
}

func reverseDraft(source work.FundingOrderDraft, reservationID, legID string) work.FundingOrderDraft {
	side := "buy"
	if source.Side == "buy" {
		side = "sell"
	}
	return work.FundingOrderDraft{LegID: legID, AccountID: source.AccountID, DesiredStateRevision: source.DesiredStateRevision, RuntimeGeneration: source.RuntimeGeneration, CredentialVersion: source.CredentialVersion, Venue: source.Venue, VenueEnvironment: source.VenueEnvironment, ReservationID: reservationID, InstrumentID: source.InstrumentID, Side: side, Quantity: source.Quantity, Notional: source.Notional, OrderType: "market", ReduceOnly: true}
}
