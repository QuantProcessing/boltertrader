package executionprojection

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/shopspring/decimal"
)

type Store interface {
	ListExecutions(context.Context, readmodel.Scope) ([]execution.Execution, error)
	ApplyExecutionProjection(
		context.Context,
		string,
		readmodel.PositionProjection,
		readmodel.ActivityProjection,
	) ([]readmodel.Event, error)
}

type Projector struct {
	store  Store
	bootID string
}

func NewProjector(store Store, bootID string) *Projector {
	return &Projector{store: store, bootID: strings.TrimSpace(bootID)}
}

func (projector *Projector) Rebuild(ctx context.Context, scope readmodel.Scope) error {
	_, err := projector.RebuildEvents(ctx, scope)
	return err
}

func (projector *Projector) RebuildEvents(ctx context.Context, scope readmodel.Scope) ([]readmodel.Event, error) {
	if projector == nil || projector.store == nil {
		return nil, errors.New("execution projector store is required")
	}
	executions, err := projector.store.ListExecutions(ctx, scope)
	if err != nil {
		return nil, err
	}
	sort.SliceStable(executions, func(i, j int) bool {
		left := executions[i]
		right := executions[j]
		leftAt := orderTime(left)
		rightAt := orderTime(right)
		if !leftAt.Equal(rightAt) {
			return leftAt.Before(rightAt)
		}
		return left.ID < right.ID
	})
	var events []readmodel.Event
	for _, item := range executions {
		committed, err := projector.ProjectEvents(ctx, scope, item)
		if err != nil {
			return nil, err
		}
		events = append(events, committed...)
	}
	return events, nil
}

func (projector *Projector) Project(ctx context.Context, scope readmodel.Scope, item execution.Execution) error {
	_, err := projector.ProjectEvents(ctx, scope, item)
	return err
}

func (projector *Projector) ProjectEvents(
	ctx context.Context,
	scope readmodel.Scope,
	item execution.Execution,
) ([]readmodel.Event, error) {
	if projector == nil || projector.store == nil {
		return nil, errors.New("execution projector store is required")
	}
	if strings.TrimSpace(scope.PrincipalID) == "" || !isProductMode(scope.Mode) {
		return nil, errors.New("projection scope is required")
	}
	if strings.TrimSpace(item.ID) == "" {
		return nil, errors.New("execution id is required")
	}
	if item.PrincipalID != scope.PrincipalID || item.Mode != scope.Mode {
		return nil, fmt.Errorf("execution scope mismatch")
	}
	if !isProductMode(item.Mode) {
		return nil, fmt.Errorf("execution mode is invalid")
	}
	if item.Operation != execution.OperationOpen && item.Operation != execution.OperationClose {
		return nil, fmt.Errorf("unsupported execution operation %q", item.Operation)
	}
	if item.State == execution.StatePreviewed {
		return nil, nil
	}
	position, activity, err := projector.build(scope, item)
	if err != nil {
		return nil, err
	}
	if activity.ID == "" {
		return nil, errors.New("execution activity projection is required")
	}
	events, err := projector.store.ApplyExecutionProjection(
		ctx,
		projector.bootID,
		position,
		activity,
	)
	if err != nil {
		return nil, err
	}
	return events, nil
}

func (projector *Projector) build(scope readmodel.Scope, item execution.Execution) (readmodel.PositionProjection, readmodel.ActivityProjection, error) {
	positionID := stablePositionID(item)
	if positionID == "" {
		return readmodel.PositionProjection{}, readmodel.ActivityProjection{}, errors.New("position id is required")
	}
	longPlan, shortPlan, err := plannedLegPair(item.PlannedLegs)
	if err != nil {
		return readmodel.PositionProjection{}, readmodel.ActivityProjection{}, err
	}
	longFilled := netRoleFill(item, execution.LegRoleLong)
	shortFilled := netRoleFill(item, execution.LegRoleShort)
	longCurrent := longFilled
	shortCurrent := shortFilled
	if item.Operation == execution.OperationClose {
		longCurrent = nonNegative(item.InitialLongQuantity.Sub(longFilled))
		shortCurrent = nonNegative(item.InitialShortQuantity.Sub(shortFilled))
	}
	status := positionStatus(item)
	if item.Operation == execution.OperationOpen &&
		item.State == execution.StateFailed &&
		longCurrent.IsZero() &&
		shortCurrent.IsZero() {
		status = "closed"
	}
	openedBy := item.ID
	if item.Operation == execution.OperationClose {
		openedBy = strings.TrimSpace(item.ParentExecutionID)
		if openedBy == "" {
			openedBy = strings.TrimSpace(item.LineageID)
		}
	}
	updatedAt := item.UpdatedAt.UTC()
	if updatedAt.IsZero() {
		updatedAt = item.CreatedAt.UTC()
	}
	payload := projectedPositionPayload{
		PositionID:            positionID,
		OpeningExecutionID:    openedBy,
		ClosingExecutionID:    closeExecutionID(item),
		ParentExecutionID:     item.ParentExecutionID,
		LineageID:             lineageID(item),
		OpportunityID:         item.OpportunityID,
		Symbol:                longPlan.Symbol,
		Status:                status,
		State:                 string(item.State),
		RequiresAttention:     status == "attention",
		CapitalUSDT:           item.CapitalUSDT,
		SingleLegNotional:     item.CapitalUSDT.Mul(decimal.NewFromInt(int64(item.Leverage))),
		Leverage:              item.Leverage,
		ExpectedNetAprPct:     item.ExpectedNetAprPct,
		LongLeg:               legPayloadFrom(longPlan, item.InitialLongQuantity, longCurrent),
		ShortLeg:              legPayloadFrom(shortPlan, item.InitialShortQuantity, shortCurrent),
		UpdatedAt:             updatedAt,
		ManualIntervention:    item.ManualIntervention,
		ManualInterventionWhy: item.ManualInterventionReason,
	}
	body, err := json.Marshal(payload)
	if err != nil {
		return readmodel.PositionProjection{}, readmodel.ActivityProjection{}, err
	}
	position := readmodel.PositionProjection{
		ID:        positionID,
		Scope:     scope,
		Symbol:    longPlan.Symbol,
		Status:    status,
		UpdatedAt: updatedAt,
		Payload:   body,
	}
	activityPayload := projectedActivityPayload{
		PositionID:  positionID,
		ExecutionID: item.ID,
		Kind:        activityKind(status),
		Status:      status,
		State:       string(item.State),
		Operation:   string(item.Operation),
		OccurredAt:  updatedAt,
	}
	activityIdentityBody, err := json.Marshal(activityPayload)
	if err != nil {
		return readmodel.PositionProjection{}, readmodel.ActivityProjection{}, err
	}
	activityID := stableActivityID(
		activityPayload.Kind,
		positionID,
		item.ID,
		activityIdentityBody,
	)
	activityPayload.ActivityID = activityID
	activityBody, err := json.Marshal(activityPayload)
	if err != nil {
		return readmodel.PositionProjection{}, readmodel.ActivityProjection{}, err
	}
	activity := readmodel.ActivityProjection{
		ID:          activityID,
		Scope:       scope,
		Kind:        activityPayload.Kind,
		PositionID:  positionID,
		ExecutionID: item.ID,
		OccurredAt:  updatedAt,
		Payload:     activityBody,
	}
	return position, activity, nil
}

type projectedPositionPayload struct {
	PositionID            string          `json:"positionId"`
	OpeningExecutionID    string          `json:"openingExecutionId"`
	ClosingExecutionID    string          `json:"closingExecutionId,omitempty"`
	ParentExecutionID     string          `json:"parentExecutionId,omitempty"`
	LineageID             string          `json:"lineageId"`
	OpportunityID         string          `json:"opportunityId,omitempty"`
	Symbol                string          `json:"symbol"`
	Status                string          `json:"status"`
	State                 string          `json:"state"`
	RequiresAttention     bool            `json:"requiresAttention"`
	CapitalUSDT           decimal.Decimal `json:"capitalUsdt"`
	SingleLegNotional     decimal.Decimal `json:"singleLegNotional"`
	Leverage              int             `json:"leverage"`
	ExpectedNetAprPct     decimal.Decimal `json:"expectedNetAprPct"`
	LongLeg               projectedLeg    `json:"longLeg"`
	ShortLeg              projectedLeg    `json:"shortLeg"`
	UpdatedAt             time.Time       `json:"updatedAt"`
	ManualIntervention    bool            `json:"manualIntervention,omitempty"`
	ManualInterventionWhy string          `json:"manualInterventionReason,omitempty"`
}

type projectedLeg struct {
	AccountID           string          `json:"accountId"`
	Venue               string          `json:"venue"`
	Symbol              string          `json:"symbol"`
	InstrumentID        string          `json:"instrumentId"`
	VenueEnvironment    string          `json:"venueEnvironment"`
	GatewayInstrumentID string          `json:"gatewayInstrumentId"`
	InitialQuantity     decimal.Decimal `json:"initialQuantity"`
	CurrentQuantity     decimal.Decimal `json:"currentQuantity"`
}

type projectedActivityPayload struct {
	ActivityID  string    `json:"activityId"`
	PositionID  string    `json:"positionId"`
	ExecutionID string    `json:"executionId"`
	Kind        string    `json:"kind"`
	Status      string    `json:"status"`
	State       string    `json:"state"`
	Operation   string    `json:"operation"`
	OccurredAt  time.Time `json:"occurredAt"`
}

func plannedLegPair(legs []execution.PlannedLeg) (execution.PlannedLeg, execution.PlannedLeg, error) {
	var long execution.PlannedLeg
	var short execution.PlannedLeg
	for _, leg := range legs {
		switch leg.Role {
		case execution.LegRoleLong:
			long = leg
		case execution.LegRoleShort:
			short = leg
		}
	}
	if strings.TrimSpace(long.Symbol) == "" || strings.TrimSpace(short.Symbol) == "" {
		return execution.PlannedLeg{}, execution.PlannedLeg{}, errors.New("long and short planned legs are required")
	}
	if long.Symbol != short.Symbol {
		return execution.PlannedLeg{}, execution.PlannedLeg{}, errors.New("planned leg symbol mismatch")
	}
	return long, short, nil
}

func legPayloadFrom(plan execution.PlannedLeg, initial, current decimal.Decimal) projectedLeg {
	if initial.IsZero() {
		initial = plan.Quantity
	}
	return projectedLeg{
		AccountID:           plan.AccountID,
		Venue:               plan.Venue,
		Symbol:              plan.Symbol,
		InstrumentID:        plan.InstrumentID,
		VenueEnvironment:    plan.VenueEnvironment,
		GatewayInstrumentID: plan.GatewayInstrumentID,
		InitialQuantity:     initial,
		CurrentQuantity:     current,
	}
}

func filledQuantity(legs []execution.Leg, role execution.LegRole) decimal.Decimal {
	total := decimal.Zero
	for _, leg := range legs {
		if leg.Role == role {
			total = total.Add(leg.Filled)
		}
	}
	return total
}

func netRoleFill(item execution.Execution, role execution.LegRole) decimal.Decimal {
	normal := filledQuantity(item.Legs, role)
	if item.Compensation == nil || item.Compensation.TargetRole != role {
		return normal
	}
	compensation := filledQuantity(item.Legs, execution.LegRoleCompensation)
	if item.Operation == execution.OperationClose {
		return nonNegative(normal.Sub(compensation))
	}
	return nonNegative(normal.Sub(compensation))
}

func stablePositionID(item execution.Execution) string {
	if strings.TrimSpace(item.PositionID) != "" {
		return strings.TrimSpace(item.PositionID)
	}
	if strings.TrimSpace(item.ID) == "" {
		return ""
	}
	return "position-" + item.ID
}

func lineageID(item execution.Execution) string {
	if strings.TrimSpace(item.LineageID) != "" {
		return strings.TrimSpace(item.LineageID)
	}
	return strings.TrimSpace(item.ID)
}

func closeExecutionID(item execution.Execution) string {
	if item.Operation == execution.OperationClose {
		return item.ID
	}
	return ""
}

func positionStatus(item execution.Execution) string {
	if item.Operation == execution.OperationClose && item.State == execution.StateClosed {
		return "closed"
	}
	if item.Operation == execution.OperationOpen && item.State == execution.StateHedged {
		return "open"
	}
	switch item.State {
	case execution.StatePartiallyHedged,
		execution.StateCompensating,
		execution.StateUncertain,
		execution.StateManualInterventionRequired,
		execution.StateFailed,
		execution.StateSubmitting,
		execution.StateLeg1Working,
		execution.StateLeg2Working:
		return "attention"
	default:
		return "attention"
	}
}

func activityKind(status string) string {
	switch status {
	case "open":
		return "position.opened"
	case "closed":
		return "position.closed"
	case "attention":
		return "position.attention_required"
	default:
		return "position.updated"
	}
}

func stableActivityID(kind, positionID, executionID string, payload json.RawMessage) string {
	sum := sha256.Sum256([]byte(kind + "\x00" + positionID + "\x00" + executionID + "\x00" + string(payload)))
	return "activity-" + hex.EncodeToString(sum[:16])
}

func nonNegative(value decimal.Decimal) decimal.Decimal {
	if value.IsNegative() {
		return decimal.Zero
	}
	return value
}

func isProductMode(mode string) bool {
	switch strings.TrimSpace(mode) {
	case "test", "live":
		return true
	default:
		return false
	}
}

func orderTime(item execution.Execution) time.Time {
	if !item.UpdatedAt.IsZero() {
		return item.UpdatedAt.UTC()
	}
	return item.CreatedAt.UTC()
}
