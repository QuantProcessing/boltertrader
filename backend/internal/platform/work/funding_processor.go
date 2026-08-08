package work

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/ledger"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/portfolio"
	fundingpolicy "github.com/QuantProcessing/boltertrader/backend/internal/products/funding/policy"
	"github.com/shopspring/decimal"
)

type FundingIntent struct {
	Kind       string
	Open       *FundingOpenIntent
	Close      *FundingCloseIntent
	Compensate *FundingCompensateIntent
	Reconcile  *FundingReconcileIntent
}

type FundingOpenIntent struct {
	PreviewID             string `json:"previewId"`
	OpportunityID         string `json:"opportunityId"`
	LongAccountID         string `json:"longAccountId"`
	ShortAccountID        string `json:"shortAccountId"`
	CanonicalMarketID     string `json:"canonicalMarketId"`
	NotionalPerLeg        string `json:"notionalPerLeg"`
	MaxSlippageBps        string `json:"maxSlippageBps"`
	HoldThroughSettlement bool   `json:"holdThroughSettlement"`
}

type FundingCloseIntent struct {
	LineageID   string    `json:"lineageId"`
	Reason      string    `json:"reason"`
	RequestedAt time.Time `json:"requestedAt"`
}

type FundingCompensateIntent struct {
	LineageID               string `json:"lineageId"`
	FailedCommandID         string `json:"failedCommandId"`
	SurvivingCommandID      string `json:"survivingCommandId"`
	MaxCompensationNotional string `json:"maxCompensationNotional"`
	Reason                  string `json:"reason"`
}

type FundingReconcileIntent struct {
	LineageID  string   `json:"lineageId"`
	CommandIDs []string `json:"commandIds"`
	Reason     string   `json:"reason"`
}

type FundingOrderDraft struct {
	LegID                string `json:"legId"`
	AccountID            string `json:"accountId"`
	DesiredStateRevision int64  `json:"desiredStateRevision"`
	RuntimeGeneration    int    `json:"runtimeGeneration"`
	CredentialVersion    int64  `json:"credentialVersion"`
	Venue                string `json:"venue"`
	VenueEnvironment     string `json:"venueEnvironment"`
	ReservationID        string `json:"reservationId"`
	InstrumentID         string `json:"instrumentId"`
	Side                 string `json:"side"`
	Quantity             string `json:"quantity"`
	Notional             string `json:"notional"`
	OrderType            string `json:"orderType"`
	LimitPrice           string `json:"limitPrice,omitempty"`
	TimeInForce          string `json:"timeInForce,omitempty"`
	ReduceOnly           bool   `json:"reduceOnly"`
}

type FundingCommandPlan struct {
	PlanKey       string              `json:"planKey"`
	PreviewHash   string              `json:"previewHash"`
	PrincipalID   string              `json:"principalId"`
	Mode          orders.Mode         `json:"mode"`
	IntentKind    string              `json:"intentKind"`
	IntentPayload json.RawMessage     `json:"intentPayload"`
	LineageID     string              `json:"lineageId"`
	Legs          []FundingOrderDraft `json:"legs"`
	ExpiresAt     time.Time           `json:"expiresAt"`
	CreatedAt     time.Time           `json:"createdAt"`
}

type FundingPlanStore interface {
	ReserveFundingPlan(context.Context, FundingCommandPlan) (FundingCommandPlan, bool, error)
	GetFundingPlan(context.Context, string, orders.Mode, string) (FundingCommandPlan, error)
}

type FundingPayloadProcessor struct {
	commands   *orders.Service
	plans      FundingPlanStore
	executions FundingExecutionStore
	now        func() time.Time
	portfolio  *portfolio.Projection
	ledger     *ledger.Service
	admission  fundingpolicy.Admission
}

func (processor *FundingPayloadProcessor) WithExecutionEvidence(portfolioProjection *portfolio.Projection, ledgerService *ledger.Service) *FundingPayloadProcessor {
	processor.portfolio, processor.ledger = portfolioProjection, ledgerService
	return processor
}

func (processor *FundingPayloadProcessor) WithAdmission(admission fundingpolicy.Admission) *FundingPayloadProcessor {
	processor.admission = admission
	if executions, ok := processor.executions.(*MemoryFundingExecutionStore); ok {
		executions.WithAdmission(admission)
	}
	return processor
}

func NewFundingPayloadProcessor(commands *orders.Service, plans FundingPlanStore, now func() time.Time) (*FundingPayloadProcessor, error) {
	return NewDurableFundingPayloadProcessor(commands, plans, NewMemoryFundingExecutionStore(), now)
}

func NewDurableFundingPayloadProcessor(commands *orders.Service, plans FundingPlanStore, executions FundingExecutionStore, now func() time.Time) (*FundingPayloadProcessor, error) {
	if commands == nil || plans == nil || executions == nil {
		return nil, fmt.Errorf("Funding command plan and execution stores are required")
	}
	if now == nil {
		now = time.Now
	}
	return &FundingPayloadProcessor{commands: commands, plans: plans, executions: executions, now: now}, nil
}

func (processor *FundingPayloadProcessor) Process(ctx context.Context, parent orders.Command) ([]orders.Command, error) {
	intent, err := decodeFundingIntent(parent.CommandKind, parent.Payload)
	if err != nil {
		return nil, err
	}
	if err := validateFundingIntentAdmission(parent.Mode, intent, processor.admission); err != nil {
		return nil, err
	}
	if intent.Reconcile != nil {
		return processor.reconcile(ctx, parent, *intent.Reconcile)
	}
	key := fundingPlanKey(intent)
	plan, err := processor.plans.GetFundingPlan(ctx, parent.PrincipalID, parent.Mode, key)
	if err != nil {
		return nil, fmt.Errorf("load Funding command plan: %w", err)
	}
	if plan.IntentKind != parent.CommandKind || plan.LineageID != parent.LineageID || !EqualJSON(plan.IntentPayload, parent.Payload) || !processor.now().UTC().Before(plan.ExpiresAt.UTC()) {
		return nil, fmt.Errorf("Funding command plan does not match the durable parent intent")
	}
	if err := validateFundingDraftsWithAdmission(parent, intent, plan.Legs, processor.admission); err != nil {
		return nil, err
	}
	children := make([]orders.Command, 0, len(plan.Legs))
	for _, draft := range plan.Legs {
		payload, err := json.Marshal(map[string]any{
			"instrumentId": draft.InstrumentID, "side": draft.Side, "quantity": draft.Quantity,
			"orderType": draft.OrderType, "limitPrice": draft.LimitPrice, "timeInForce": draft.TimeInForce,
			"reduceOnly":           draft.ReduceOnly,
			"desiredStateRevision": draft.DesiredStateRevision, "runtimeGeneration": draft.RuntimeGeneration,
			"credentialVersion": draft.CredentialVersion,
		})
		if err != nil {
			return nil, err
		}
		child, _, err := processor.commands.Create(ctx, orders.CreateRequest{
			PrincipalID: parent.PrincipalID, AccountID: draft.AccountID, ProductID: parent.ProductID,
			LineageID: parent.LineageID, ReservationID: draft.ReservationID, Mode: parent.Mode,
			IdempotencyKey: parent.CommandID + ":" + draft.LegID, Method: "POST", Path: parent.Path + "/children",
			CommandKind: "order.place", Payload: payload,
		})
		if err != nil {
			return nil, fmt.Errorf("create Funding child command: %w", err)
		}
		children = append(children, child)
	}
	execution, err := processor.reserveExecution(ctx, parent, plan, children)
	if err != nil {
		return nil, err
	}
	execution, err = processor.syncExecution(ctx, execution, children, "observe_children")
	if err != nil {
		return nil, err
	}
	return readyFundingCommands(execution, children), nil
}

func validateFundingIntentAdmission(mode orders.Mode, intent FundingIntent, admission fundingpolicy.Admission) error {
	value := ""
	if intent.Open != nil {
		value = intent.Open.NotionalPerLeg
	} else if intent.Compensate != nil {
		value = intent.Compensate.MaxCompensationNotional
	} else {
		return nil
	}
	notional, err := decimal.NewFromString(value)
	if err != nil || !admission.Allows(string(mode), notional) {
		if mode == orders.ModeLive && !admission.LiveEnabled() {
			return ErrLiveWriteDisabled
		}
		return ErrWorkRevisionConflict
	}
	return nil
}

func (processor *FundingPayloadProcessor) RecordHandoff(ctx context.Context, child orders.Command, handoff orders.HandoffResult) (orders.Command, error) {
	request := orders.TransitionRequest{CommandID: child.CommandID, Evidence: handoff.Evidence}
	switch handoff.Outcome {
	case orders.HandoffAccepted:
		request.State = commandStateFromGateway(handoff.Evidence)
	case orders.HandoffOutcomeUnknown:
		request.State = orders.StateUncertain
		request.Reason = orders.ReconciliationAmbiguousHandoff
		request.NextAttemptAt = processor.now().UTC().Add(5 * time.Second)
	case orders.HandoffNotAttempted, orders.HandoffRejected:
		request.State = orders.StateFailed
	default:
		return child, fmt.Errorf("Gateway handoff outcome is invalid")
	}
	updated, err := processor.commands.Transition(ctx, request)
	if err != nil {
		return updated, err
	}
	if err := processor.ingestFills(ctx, updated, handoff.Evidence.Fills); err != nil {
		return updated, err
	}
	execution, err := processor.executions.GetFundingExecution(ctx, child.PrincipalID, child.Mode, child.LineageID)
	if err != nil {
		return updated, err
	}
	_, err = processor.syncExecution(ctx, execution, []orders.Command{updated}, "child_handoff")
	return updated, err
}

func (processor *FundingPayloadProcessor) ingestFills(ctx context.Context, command orders.Command, fills []orders.FillEvidence) error {
	if len(fills) == 0 {
		return nil
	}
	if processor.portfolio == nil || processor.ledger == nil {
		return fmt.Errorf("Platform execution evidence projections are unavailable")
	}
	for _, fill := range fills {
		accountID := fill.AccountID
		if accountID == "" {
			accountID = command.AccountID
		}
		_, result, err := processor.portfolio.ApplyFill(ctx, portfolio.Fill{
			FillID: fill.FillID, CommandID: command.CommandID, LineageID: command.LineageID, ProductID: command.ProductID,
			Mode: portfolio.Mode(command.Mode), AccountID: accountID, InstrumentID: fill.InstrumentID, Side: portfolio.Side(fill.Side),
			Quantity: fill.Quantity, Price: fill.Price, Fee: fill.Fee, FeeCurrency: fill.FeeCurrency, SettlementCurrency: fill.SettlementCurrency, OccurredAt: fill.OccurredAt,
		})
		if err != nil {
			return fmt.Errorf("apply authoritative Gateway fill %q: %w", fill.FillID, err)
		}
		if err := processor.ledger.Record(ctx, ledger.Entry{
			EntryID: fill.FillID + ":fee", Mode: ledger.Mode(command.Mode), LineageID: command.LineageID, ProductID: command.ProductID,
			AccountID: accountID, InstrumentID: fill.InstrumentID, Kind: ledger.KindFee, Amount: result.Fee,
			Currency: fill.FeeCurrency, OccurredAt: fill.OccurredAt,
		}); err != nil {
			return fmt.Errorf("record authoritative Gateway fill fee %q: %w", fill.FillID, err)
		}
		if result.RealizedPnL != "0" {
			if err := processor.ledger.Record(ctx, ledger.Entry{
				EntryID: fill.FillID + ":realized_pnl", Mode: ledger.Mode(command.Mode), LineageID: command.LineageID, ProductID: command.ProductID,
				AccountID: accountID, InstrumentID: fill.InstrumentID, Kind: ledger.KindRealizedPnL, Amount: result.RealizedPnL,
				Currency: fill.SettlementCurrency, OccurredAt: fill.OccurredAt,
			}); err != nil {
				return fmt.Errorf("record authoritative Gateway fill PnL %q: %w", fill.FillID, err)
			}
		}
	}
	return nil
}

func commandStateFromGateway(evidence orders.ExecutionEvidence) orders.State {
	switch strings.ToLower(evidence.State) {
	case "filled":
		if len(evidence.Fills) > 0 {
			return orders.StateFilled
		}
		return orders.StateSubmitted
	case "canceled", "cancelled":
		return orders.StateCanceled
	case "rejected", "failed":
		return orders.StateFailed
	default:
		return orders.StateSubmitted
	}
}

func (processor *FundingPayloadProcessor) reserveExecution(ctx context.Context, parent orders.Command, plan FundingCommandPlan, children []orders.Command) (FundingExecution, error) {
	now := processor.now().UTC()
	legs := make([]FundingExecutionLeg, 0, len(children))
	for index, child := range children {
		legs = append(legs, FundingExecutionLeg{Role: plan.Legs[index].LegID, Draft: plan.Legs[index], CommandID: child.CommandID, CommandState: child.State})
	}
	execution := FundingExecution{
		ExecutionID: fundingExecutionID(parent.PrincipalID, parent.Mode, parent.LineageID), PrincipalID: parent.PrincipalID, Mode: parent.Mode,
		LineageID: parent.LineageID, ParentCommandID: parent.CommandID, PlanKey: plan.PlanKey,
		PreviewHash: plan.PreviewHash, IntentKind: parent.CommandKind, State: FundingExecutionPreviewed,
		Legs: legs, CreatedAt: now, UpdatedAt: now,
	}
	stored, _, err := processor.executions.ReserveFundingExecution(ctx, execution)
	return stored, err
}

func fundingExecutionID(principal string, mode orders.Mode, lineage string) string {
	hash := CanonicalRequestHash(struct {
		Principal string
		Mode      orders.Mode
		Lineage   string
	}{Principal: principal, Mode: mode, Lineage: lineage})
	return "funding:" + hash[:24]
}

func (processor *FundingPayloadProcessor) RecordFundingPartial(ctx context.Context, principal string, mode orders.Mode, lineage, commandID, filledQuantity, recoveryCursor string) (orders.Command, error) {
	execution, err := processor.executions.GetFundingExecution(ctx, principal, mode, lineage)
	if err != nil {
		return orders.Command{}, err
	}
	var leg *FundingExecutionLeg
	for index := range execution.Legs {
		if execution.Legs[index].CommandID == commandID {
			leg = &execution.Legs[index]
			break
		}
	}
	if leg == nil || leg.Role == "compensation" || leg.FilledQuantity != "" {
		return orders.Command{}, ErrWorkRevisionConflict
	}
	filled, err := decimal.NewFromString(filledQuantity)
	requested, requestedErr := decimal.NewFromString(leg.Draft.Quantity)
	if err != nil || requestedErr != nil || !filled.IsPositive() || !filled.LessThan(requested) {
		return orders.Command{}, ErrWorkRevisionConflict
	}
	payload, _ := json.Marshal(map[string]string{"commandId": commandID})
	cancel, _, err := processor.commands.Create(ctx, orders.CreateRequest{
		PrincipalID: principal, AccountID: leg.Draft.AccountID, ProductID: "funding", LineageID: lineage,
		Mode: mode, IdempotencyKey: execution.ExecutionID + ":" + leg.Role + ":cancel", Method: "POST",
		Path: "/internal/v1/funding-executions/" + execution.ExecutionID + "/children", CommandKind: "order.cancel", Payload: payload,
	})
	if err != nil {
		return orders.Command{}, err
	}
	leg.FilledQuantity = filled.String()
	execution.Legs = append(execution.Legs, FundingExecutionLeg{Role: leg.Role + "_cancel", CommandID: cancel.CommandID, CommandState: cancel.State})
	execution.State = FundingExecutionPartiallyHedged
	execution.RecoveryCursor = recoveryCursor
	execution.UpdatedAt = processor.now().UTC()
	if _, err := processor.executions.PersistFundingExecution(ctx, execution, execution.Version, "partial_cancel_requested"); err != nil {
		return orders.Command{}, err
	}
	return cancel, nil
}

func (processor *FundingPayloadProcessor) RecordFundingCancellation(ctx context.Context, principal string, mode orders.Mode, lineage, cancelCommandID, maxCompensationNotional string) (orders.Command, error) {
	execution, err := processor.executions.GetFundingExecution(ctx, principal, mode, lineage)
	if err != nil {
		return orders.Command{}, err
	}
	cancel, err := processor.commands.GetOwned(ctx, principal, mode, cancelCommandID)
	if err != nil || cancel.LineageID != lineage || cancel.CommandKind != "order.cancel" || cancel.State != orders.StateCanceled {
		return orders.Command{}, ErrWorkRevisionConflict
	}
	if execution.CompensationAttempted {
		for _, leg := range execution.Legs {
			if leg.Role == "compensation" {
				return processor.commands.Get(ctx, leg.CommandID)
			}
		}
		return orders.Command{}, ErrWorkRevisionConflict
	}
	var partial *FundingExecutionLeg
	for index := range execution.Legs {
		if execution.Legs[index].FilledQuantity != "" && execution.Legs[index].Role != "compensation" {
			partial = &execution.Legs[index]
		}
		if execution.Legs[index].CommandID == cancelCommandID {
			execution.Legs[index].CommandState = orders.StateCanceled
		}
	}
	if partial == nil {
		return orders.Command{}, ErrWorkRevisionConflict
	}
	filled, filledErr := decimal.NewFromString(partial.FilledQuantity)
	requested, requestedErr := decimal.NewFromString(partial.Draft.Quantity)
	legNotional, notionalErr := decimal.NewFromString(partial.Draft.Notional)
	maximum, maximumErr := decimal.NewFromString(maxCompensationNotional)
	if filledErr != nil || requestedErr != nil || notionalErr != nil || maximumErr != nil || !filled.IsPositive() || !requested.IsPositive() || !legNotional.IsPositive() || !maximum.IsPositive() || !filled.LessThan(requested) {
		return orders.Command{}, ErrWorkRevisionConflict
	}
	compensationNotional := filled.Div(requested).Mul(legNotional)
	if compensationNotional.GreaterThan(maximum) || !processor.admission.Allows(string(mode), compensationNotional) {
		return orders.Command{}, ErrWorkRevisionConflict
	}
	side := "sell"
	if partial.Draft.Side == "sell" {
		side = "buy"
	}
	payload, _ := json.Marshal(map[string]any{
		"instrumentId": partial.Draft.InstrumentID, "side": side, "quantity": partial.FilledQuantity,
		"orderType": "market", "limitPrice": "", "timeInForce": "", "reduceOnly": true,
	})
	compensation, _, err := processor.commands.Create(ctx, orders.CreateRequest{
		PrincipalID: principal, AccountID: partial.Draft.AccountID, ProductID: "funding", LineageID: lineage,
		Mode: mode, IdempotencyKey: execution.ExecutionID + ":compensation", Method: "POST",
		Path: "/internal/v1/funding-executions/" + execution.ExecutionID + "/children", CommandKind: "order.place", Payload: payload,
	})
	if err != nil {
		return orders.Command{}, err
	}
	execution.CompensationAttempted = true
	execution.State = FundingExecutionCompensating
	execution.Legs = append(execution.Legs, FundingExecutionLeg{Role: "compensation", Draft: FundingOrderDraft{LegID: "compensation", AccountID: partial.Draft.AccountID, DesiredStateRevision: partial.Draft.DesiredStateRevision, RuntimeGeneration: partial.Draft.RuntimeGeneration, CredentialVersion: partial.Draft.CredentialVersion, InstrumentID: partial.Draft.InstrumentID, Side: side, Quantity: partial.FilledQuantity, OrderType: "market", ReduceOnly: true}, CommandID: compensation.CommandID, CommandState: compensation.State})
	execution.UpdatedAt = processor.now().UTC()
	if _, err := processor.executions.PersistFundingExecution(ctx, execution, execution.Version, "compensation_requested"); err != nil {
		return orders.Command{}, err
	}
	return compensation, nil
}

func (processor *FundingPayloadProcessor) syncExecution(ctx context.Context, execution FundingExecution, children []orders.Command, kind string) (FundingExecution, error) {
	changed := false
	for _, child := range children {
		for index := range execution.Legs {
			if execution.Legs[index].CommandID == child.CommandID && execution.Legs[index].CommandState != child.State {
				execution.Legs[index].CommandState = child.State
				execution.Legs[index].ExecutionEvidence = child.ExecutionEvidence
				changed = true
			} else if execution.Legs[index].CommandID == child.CommandID && execution.Legs[index].ExecutionEvidence.ResourceVersion < child.ExecutionEvidence.ResourceVersion {
				execution.Legs[index].ExecutionEvidence = child.ExecutionEvidence
				changed = true
			}
		}
	}
	state := deriveFundingExecutionState(execution)
	if state != execution.State {
		execution.State = state
		changed = true
	}
	if !changed {
		return execution, nil
	}
	execution.UpdatedAt = processor.now().UTC()
	return processor.executions.PersistFundingExecution(ctx, execution, execution.Version, kind)
}

func deriveFundingExecutionState(execution FundingExecution) FundingExecutionState {
	if len(execution.Legs) == 0 {
		return FundingExecutionPreviewed
	}
	for _, leg := range execution.Legs {
		if leg.CommandState == orders.StateUncertain {
			return FundingExecutionUncertain
		}
	}
	if allFundingLegs(execution.Legs, orders.StateFilled) {
		if execution.IntentKind == "funding.close" || execution.IntentKind == "funding.compensate" {
			return FundingExecutionClosed
		}
		return FundingExecutionHedged
	}
	if len(execution.Legs) > 1 && execution.Legs[1].CommandState != orders.StateAccepted {
		return FundingExecutionLeg2Working
	}
	if execution.Legs[0].CommandState != orders.StateAccepted {
		return FundingExecutionLeg1Working
	}
	return FundingExecutionSubmitting
}

func allFundingLegs(legs []FundingExecutionLeg, state orders.State) bool {
	for _, leg := range legs {
		if leg.CommandState != state {
			return false
		}
	}
	return len(legs) > 0
}

func readyFundingCommands(execution FundingExecution, children []orders.Command) []orders.Command {
	if len(children) == 0 {
		return nil
	}
	byID := make(map[string]orders.Command, len(children))
	for _, child := range children {
		byID[child.CommandID] = child
	}
	first := byID[execution.Legs[0].CommandID]
	if first.State == orders.StateAccepted || first.State == orders.StateSubmitted || first.State == orders.StateUncertain {
		return []orders.Command{first}
	}
	if first.State != orders.StateFilled || len(execution.Legs) < 2 {
		return nil
	}
	second := byID[execution.Legs[1].CommandID]
	if second.State == orders.StateAccepted {
		return []orders.Command{second}
	}
	return nil
}

func fundingPlanKey(intent FundingIntent) string {
	switch {
	case intent.Open != nil:
		return "open:" + intent.Open.PreviewID
	case intent.Close != nil:
		return "close:" + intent.Close.LineageID
	case intent.Compensate != nil:
		return strings.Join([]string{"compensate", intent.Compensate.LineageID, intent.Compensate.FailedCommandID, intent.Compensate.SurvivingCommandID}, ":")
	default:
		return ""
	}
}

func (processor *FundingPayloadProcessor) reconcile(ctx context.Context, parent orders.Command, intent FundingReconcileIntent) ([]orders.Command, error) {
	children := make([]orders.Command, 0, len(intent.CommandIDs))
	for _, commandID := range intent.CommandIDs {
		child, err := processor.commands.Get(ctx, commandID)
		if err != nil {
			return nil, fmt.Errorf("load Funding reconciliation command: %w", err)
		}
		if child.ProductID != parent.ProductID || child.LineageID != parent.LineageID || child.Mode != parent.Mode {
			return nil, fmt.Errorf("Funding reconciliation command ownership mismatch")
		}
		children = append(children, child)
	}
	execution, err := processor.executions.GetFundingExecution(ctx, parent.PrincipalID, parent.Mode, parent.LineageID)
	if err != nil {
		return nil, err
	}
	execution, err = processor.syncExecution(ctx, execution, children, "reconcile_children")
	if err != nil {
		return nil, err
	}
	return readyFundingCommands(execution, children), nil
}

func decodeFundingIntent(kind string, payload json.RawMessage) (FundingIntent, error) {
	intent := FundingIntent{Kind: kind}
	switch kind {
	case "funding.open":
		var value FundingOpenIntent
		if err := decodeExact(payload, &value); err != nil || value.PreviewID == "" || value.OpportunityID == "" || value.LongAccountID == "" || value.ShortAccountID == "" || value.LongAccountID == value.ShortAccountID || value.CanonicalMarketID == "" {
			return FundingIntent{}, ErrWorkRevisionConflict
		}
		if err := validatePositiveNotional(value.NotionalPerLeg); err != nil {
			return FundingIntent{}, err
		}
		if slippage, err := strconv.Atoi(value.MaxSlippageBps); err != nil || slippage < 0 {
			return FundingIntent{}, ErrWorkRevisionConflict
		}
		intent.Open = &value
	case "funding.close":
		var value FundingCloseIntent
		if err := decodeExact(payload, &value); err != nil || value.LineageID == "" || value.Reason == "" {
			return FundingIntent{}, ErrWorkRevisionConflict
		}
		intent.Close = &value
	case "funding.compensate":
		var value FundingCompensateIntent
		if err := decodeExact(payload, &value); err != nil || value.LineageID == "" || value.FailedCommandID == "" || value.SurvivingCommandID == "" || value.Reason == "" {
			return FundingIntent{}, ErrWorkRevisionConflict
		}
		if err := validatePositiveNotional(value.MaxCompensationNotional); err != nil {
			return FundingIntent{}, err
		}
		intent.Compensate = &value
	case "funding.reconcile":
		var value FundingReconcileIntent
		if err := decodeExact(payload, &value); err != nil || value.LineageID == "" || value.Reason == "" || len(value.CommandIDs) == 0 || hasDuplicate(value.CommandIDs) {
			return FundingIntent{}, ErrWorkRevisionConflict
		}
		intent.Reconcile = &value
	default:
		return FundingIntent{}, ErrWorkRevisionConflict
	}
	return intent, nil
}

func validateFundingDrafts(parent orders.Command, intent FundingIntent, drafts []FundingOrderDraft) error {
	return validateFundingDraftsWithAdmission(parent, intent, drafts, fundingpolicy.DefaultAdmission())
}

func validateFundingDraftsWithAdmission(parent orders.Command, intent FundingIntent, drafts []FundingOrderDraft, admission fundingpolicy.Admission) error {
	if len(drafts) == 0 {
		return fmt.Errorf("Funding intent produced no child commands")
	}
	legIDs := make(map[string]struct{}, len(drafts))
	accounts := make(map[string]struct{}, len(drafts))
	for _, draft := range drafts {
		if draft.LegID == "" || draft.AccountID == "" || draft.RuntimeGeneration < 1 || draft.Venue == "" || draft.VenueEnvironment == "" || draft.ReservationID == "" || draft.InstrumentID == "" || draft.Quantity == "" || draft.Notional == "" || (draft.Side != "buy" && draft.Side != "sell") || draft.OrderType == "" {
			return fmt.Errorf("Funding intent produced an incomplete child command")
		}
		if _, exists := legIDs[draft.LegID]; exists {
			return fmt.Errorf("Funding intent produced duplicate leg identity")
		}
		legIDs[draft.LegID] = struct{}{}
		accounts[draft.AccountID] = struct{}{}
		quantity, err := decimal.NewFromString(draft.Quantity)
		if err != nil || !quantity.IsPositive() {
			return fmt.Errorf("Funding intent produced invalid quantity")
		}
		notional, err := decimal.NewFromString(draft.Notional)
		if err != nil || !admission.Allows(string(parent.Mode), notional) {
			return fmt.Errorf("Funding intent produced invalid leg notional")
		}
		if intent.Open == nil && !draft.ReduceOnly {
			return fmt.Errorf("Funding close and compensation commands must be reduce-only")
		}
	}
	if intent.Open != nil {
		if len(drafts) != 2 {
			return fmt.Errorf("Funding open requires exactly two child commands")
		}
		if _, ok := accounts[intent.Open.LongAccountID]; !ok {
			return fmt.Errorf("Funding long account was not resolved")
		}
		if _, ok := accounts[intent.Open.ShortAccountID]; !ok {
			return fmt.Errorf("Funding short account was not resolved")
		}
		for _, draft := range drafts {
			if draft.ReduceOnly || (draft.AccountID == intent.Open.LongAccountID && draft.Side != "buy") || (draft.AccountID == intent.Open.ShortAccountID && draft.Side != "sell") {
				return fmt.Errorf("Funding open leg direction is invalid")
			}
		}
	}
	if parent.Mode == orders.ModeLive && !admission.LiveEnabled() {
		return ErrLiveWriteDisabled
	}
	if parent.Mode != orders.ModeTest && parent.Mode != orders.ModeLive {
		return ErrWorkRevisionConflict
	}
	return nil
}

type MemoryFundingPlanStore struct {
	mu        sync.Mutex
	plans     map[string]FundingCommandPlan
	admission fundingpolicy.Admission
}

func (store *MemoryFundingPlanStore) WithAdmission(admission fundingpolicy.Admission) *MemoryFundingPlanStore {
	store.admission = admission
	return store
}

func NewMemoryFundingPlanStore() *MemoryFundingPlanStore {
	return &MemoryFundingPlanStore{plans: make(map[string]FundingCommandPlan)}
}

func (store *MemoryFundingPlanStore) ReserveFundingPlan(_ context.Context, plan FundingCommandPlan) (FundingCommandPlan, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := validateFundingPlanWithAdmission(plan, store.admission); err != nil {
		return FundingCommandPlan{}, false, err
	}
	key := fundingStoredPlanKey(plan.PrincipalID, plan.Mode, plan.PlanKey)
	if existing, ok := store.plans[key]; ok {
		if existing.PreviewHash != plan.PreviewHash || existing.IntentKind != plan.IntentKind || existing.LineageID != plan.LineageID || !EqualJSON(existing.IntentPayload, plan.IntentPayload) || !EqualJSON(mustJSON(existing.Legs), mustJSON(plan.Legs)) || !existing.ExpiresAt.Equal(plan.ExpiresAt) {
			return existing, true, ErrIdempotencyConflict
		}
		return existing, true, nil
	}
	store.plans[key] = plan
	return plan, false, nil
}

func (store *MemoryFundingPlanStore) GetFundingPlan(_ context.Context, principal string, mode orders.Mode, key string) (FundingCommandPlan, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	plan, ok := store.plans[fundingStoredPlanKey(principal, mode, key)]
	if !ok {
		return FundingCommandPlan{}, ErrWorkRevisionConflict
	}
	return plan, nil
}

func validateFundingPlan(plan FundingCommandPlan) error {
	return validateFundingPlanWithAdmission(plan, fundingpolicy.DefaultAdmission())
}

func validateFundingPlanWithAdmission(plan FundingCommandPlan, admission fundingpolicy.Admission) error {
	if plan.PlanKey == "" || plan.PreviewHash == "" || plan.PrincipalID == "" || (plan.Mode != orders.ModeTest && plan.Mode != orders.ModeLive) || (plan.Mode == orders.ModeLive && !admission.LiveEnabled()) || plan.IntentKind == "" || len(plan.IntentPayload) == 0 || plan.LineageID == "" || len(plan.Legs) == 0 || plan.ExpiresAt.IsZero() || plan.CreatedAt.IsZero() || !plan.ExpiresAt.After(plan.CreatedAt) {
		return ErrWorkRevisionConflict
	}
	return nil
}

func validatePositiveNotional(value string) error {
	notional, err := decimal.NewFromString(value)
	if err != nil || !notional.IsPositive() {
		return ErrWorkRevisionConflict
	}
	return nil
}

func fundingStoredPlanKey(principal string, mode orders.Mode, key string) string {
	return strings.Join([]string{principal, string(mode), key}, "\x00")
}

func mustJSON(value any) json.RawMessage {
	encoded, _ := json.Marshal(value)
	return encoded
}

func decodeExact(payload json.RawMessage, target any) error {
	decoder := json.NewDecoder(bytes.NewReader(payload))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := decoder.Decode(&extra); err != io.EOF {
		if err == nil {
			return fmt.Errorf("multiple JSON values")
		}
		return err
	}
	return nil
}

func hasDuplicate(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if strings.TrimSpace(value) == "" {
			return true
		}
		if _, ok := seen[value]; ok {
			return true
		}
		seen[value] = struct{}{}
	}
	return false
}
