package execution

import (
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/shopspring/decimal"
)

type EventType string

const (
	EventBeginSubmit  EventType = "begin_submit"
	EventLegAccepted  EventType = "leg_accepted"
	EventLegFilled    EventType = "leg_filled"
	EventLegPartial   EventType = "leg_partial"
	EventLegCanceled  EventType = "leg_canceled"
	EventLegRejected  EventType = "leg_rejected"
	EventLegUncertain EventType = "leg_uncertain"
)

type Event struct {
	Type          EventType
	FillID        string
	GatewayID     string
	GatewayFillID string
	Role          LegRole
	OrderID       string
	Quantity      decimal.Decimal
	FillQuantity  decimal.Decimal
	Price         decimal.Decimal
	Fee           Money
	Error         string
	SafeError     ErrorInfo
	At            time.Time
	Compensation  bool
}

type ErrorInfo struct {
	Code        string `json:"code"`
	SafeMessage string `json:"safeMessage"`
	Retryable   bool   `json:"retryable"`
}

type Money struct {
	Amount   decimal.Decimal `json:"amount"`
	Currency string          `json:"currency"`
}

type Leg struct {
	LegIdentity
	LegID             string          `json:"legId"`
	Role              LegRole         `json:"role"`
	Status            LegStatus       `json:"status"`
	State             LegStatus       `json:"state"`
	GatewayID         string          `json:"gatewayId"`
	OrderID           string          `json:"orderId"`
	GatewayOrderID    string          `json:"gatewayOrderId"`
	Side              Side            `json:"side"`
	Quantity          decimal.Decimal `json:"quantity"`
	RequestedQuantity decimal.Decimal `json:"requestedQuantity"`
	Filled            decimal.Decimal `json:"filled"`
	ReduceOnly        bool            `json:"reduceOnly"`
	Error             string          `json:"-"`
	LastError         ErrorInfo       `json:"lastError"`
	UpdatedAt         time.Time       `json:"updatedAt"`
}

type Fill struct {
	LegIdentity
	FillID        string          `json:"fillId"`
	LegID         string          `json:"legId"`
	GatewayID     string          `json:"gatewayId"`
	GatewayFillID string          `json:"gatewayFillId"`
	Role          LegRole         `json:"role"`
	Quantity      decimal.Decimal `json:"quantity"`
	Price         decimal.Decimal `json:"price"`
	Fee           Money           `json:"fee"`
	At            time.Time       `json:"at"`
	OccurredAt    time.Time       `json:"occurredAt"`
}

type CompensationState struct {
	Attempted      bool            `json:"attempted"`
	GatewayID      string          `json:"gatewayId"`
	OrderID        string          `json:"orderId"`
	TargetRole     LegRole         `json:"targetRole"`
	Quantity       decimal.Decimal `json:"quantity"`
	Status         LegStatus       `json:"status"`
	Error          ErrorInfo       `json:"error,omitempty"`
	ManualRequired bool            `json:"manualRequired"`
}

type Execution struct {
	ID                       string             `json:"id"`
	PrincipalID              string             `json:"principalId"`
	Mode                     string             `json:"mode"`
	Operation                Operation          `json:"operation"`
	OpportunityID            string             `json:"opportunityId,omitempty"`
	PositionID               string             `json:"positionId,omitempty"`
	ParentExecutionID        string             `json:"parentExecutionId,omitempty"`
	LineageID                string             `json:"lineageId,omitempty"`
	State                    State              `json:"state"`
	CreatedAt                time.Time          `json:"createdAt"`
	UpdatedAt                time.Time          `json:"updatedAt"`
	CapitalUSDT              decimal.Decimal    `json:"capitalUsdt"`
	Leverage                 int                `json:"leverage"`
	ExpectedNetAprPct        decimal.Decimal    `json:"expectedNetAprPct"`
	InitialLongQuantity      decimal.Decimal    `json:"initialLongQuantity"`
	InitialShortQuantity     decimal.Decimal    `json:"initialShortQuantity"`
	GatewayIDs               []string           `json:"gatewayIds"`
	Compensation             *CompensationState `json:"compensation,omitempty"`
	ReconciliationState      string             `json:"reconciliationState,omitempty"`
	BlockingReason           string             `json:"blockingReason,omitempty"`
	ManualIntervention       bool               `json:"manualIntervention"`
	ManualInterventionReason string             `json:"manualInterventionReason,omitempty"`
	DirectionalExposure      decimal.Decimal    `json:"directionalExposure"`
	Legs                     []Leg              `json:"legs"`
	Fills                    []Fill             `json:"fills"`
	PlannedLegs              []PlannedLeg       `json:"plannedLegs"`
	AppliedEventIDs          []string           `json:"appliedEventIds"`
	applied                  map[string]struct{}
}

func NewExecution(id string, preview PreviewResult) (*Execution, error) {
	if id == "" {
		return nil, errors.New("execution id is required")
	}
	if preview.Blocked() {
		return nil, errors.New("blocked preview cannot create execution")
	}
	if preview.Operation != OperationOpen && preview.Operation != OperationClose {
		return nil, errors.New("operation must be open or close")
	}
	lineageID := preview.LineageID
	if lineageID == "" {
		lineageID = id
	}
	positionID := preview.PositionID
	if preview.Operation == OperationOpen && positionID == "" {
		positionID = "position-" + id
	}
	return &Execution{
		ID:                   id,
		PrincipalID:          preview.PrincipalID,
		Mode:                 preview.Mode,
		Operation:            preview.Operation,
		OpportunityID:        preview.OpportunityID,
		PositionID:           positionID,
		ParentExecutionID:    preview.ParentExecutionID,
		LineageID:            lineageID,
		State:                StatePreviewed,
		CreatedAt:            preview.PreviewedAt,
		UpdatedAt:            preview.PreviewedAt,
		CapitalUSDT:          preview.CapitalUSDT,
		Leverage:             preview.Leverage,
		ExpectedNetAprPct:    preview.ExpectedNetAprPct,
		InitialLongQuantity:  preview.InitialLongQuantity,
		InitialShortQuantity: preview.InitialShortQuantity,
		PlannedLegs:          append([]PlannedLeg(nil), preview.PlannedLegs...),
		applied:              make(map[string]struct{}),
	}, nil
}

func (execution *Execution) Apply(event Event) error {
	execution.rehydrate()
	if execution.applied == nil {
		execution.applied = make(map[string]struct{})
	}
	id := eventID(event)
	if _, ok := execution.applied[id]; ok {
		return nil
	}
	if event.Type == EventBeginSubmit {
		if err := execution.applyBeginSubmit(event); err != nil {
			return err
		}
		execution.markApplied(id)
		execution.advanceUpdatedAt(event.At)
		return nil
	}
	if execution.isTerminal() {
		return fmt.Errorf("execution state %s is terminal", execution.State)
	}

	var err error
	switch event.Type {
	case EventLegAccepted:
		err = execution.applyAccepted(event)
	case EventLegFilled, EventLegPartial:
		err = execution.applyFill(event)
	case EventLegCanceled:
		err = execution.applyCanceled(event)
	case EventLegRejected:
		err = execution.applyRejected(event)
	case EventLegUncertain:
		err = execution.applyUncertain(event)
	default:
		err = fmt.Errorf("unsupported event type %s", event.Type)
	}
	if err != nil {
		return err
	}
	execution.markApplied(id)
	execution.recomputeDirectionalExposure()
	execution.advanceUpdatedAt(event.At)
	return nil
}

func (execution *Execution) Snapshot() Execution {
	execution.rehydrate()
	copy := *execution
	copy.applied = nil
	copy.PlannedLegs = append([]PlannedLeg(nil), execution.PlannedLegs...)
	copy.AppliedEventIDs = append([]string(nil), execution.AppliedEventIDs...)
	copy.Legs = append([]Leg(nil), execution.Legs...)
	copy.Fills = append([]Fill(nil), execution.Fills...)
	copy.GatewayIDs = append([]string(nil), execution.GatewayIDs...)
	if execution.Compensation != nil {
		compensation := *execution.Compensation
		copy.Compensation = &compensation
	}
	return copy
}

func RestoreExecution(body []byte) (*Execution, error) {
	var execution Execution
	if err := json.Unmarshal(body, &execution); err != nil {
		return nil, err
	}
	execution.rehydrate()
	return &execution, nil
}

func (execution *Execution) rehydrate() {
	if execution.applied == nil {
		execution.applied = make(map[string]struct{}, len(execution.AppliedEventIDs))
		for _, id := range execution.AppliedEventIDs {
			execution.applied[id] = struct{}{}
		}
	}
	execution.hydrateFillIdentities()
}

func (execution *Execution) hydrateFillIdentities() {
	if len(execution.Fills) == 0 || len(execution.Legs) == 0 {
		return
	}
	byLegID := make(map[string]LegIdentity, len(execution.Legs))
	byGatewayID := make(map[string]LegIdentity, len(execution.Legs))
	byRole := make(map[LegRole]LegIdentity, len(execution.Legs))
	for _, leg := range execution.Legs {
		if leg.LegIdentity.Valid() {
			if leg.LegID != "" {
				byLegID[leg.LegID] = leg.LegIdentity
			}
			if leg.GatewayID != "" {
				byGatewayID[leg.GatewayID] = leg.LegIdentity
			}
			if leg.Role != "" {
				byRole[leg.Role] = leg.LegIdentity
			}
		}
	}
	for index := range execution.Fills {
		if execution.Fills[index].LegIdentity.Valid() {
			continue
		}
		if identity, ok := byLegID[execution.Fills[index].LegID]; ok {
			execution.Fills[index].LegIdentity = identity
			continue
		}
		if identity, ok := byGatewayID[execution.Fills[index].GatewayID]; ok {
			execution.Fills[index].LegIdentity = identity
			continue
		}
		if identity, ok := byRole[execution.Fills[index].Role]; ok {
			execution.Fills[index].LegIdentity = identity
		}
	}
}

func (execution *Execution) markApplied(id string) {
	execution.applied[id] = struct{}{}
	for _, existing := range execution.AppliedEventIDs {
		if existing == id {
			return
		}
	}
	execution.AppliedEventIDs = append(execution.AppliedEventIDs, id)
}

func (execution *Execution) advanceUpdatedAt(at time.Time) {
	if at.IsZero() || !at.After(execution.UpdatedAt) {
		return
	}
	execution.UpdatedAt = at.UTC()
}

func (execution *Execution) applyBeginSubmit(event Event) error {
	if execution.State != StatePreviewed {
		return fmt.Errorf("cannot begin submit from state %s", execution.State)
	}
	execution.State = StateSubmitting
	return nil
}

func (execution *Execution) applyAccepted(event Event) error {
	if event.Compensation {
		return execution.applyCompensationAccepted(event)
	}
	if execution.State != StateSubmitting && execution.State != StateLeg1Working {
		return fmt.Errorf("cannot accept leg from state %s", execution.State)
	}
	plan, ok := execution.plannedLeg(event.Role)
	if !ok {
		return fmt.Errorf("missing planned leg for role %s", event.Role)
	}
	if len(execution.Legs) == 0 && event.Role != LegRoleLong {
		return errors.New("first leg must be long")
	}
	if len(execution.Legs) == 1 && event.Role != LegRoleShort {
		return errors.New("second leg must be short")
	}
	if len(execution.Legs) >= 2 {
		return errors.New("both planned legs already accepted")
	}
	leg := Leg{
		LegIdentity:       plan.LegIdentity,
		LegID:             legID(event.Role),
		Role:              event.Role,
		Status:            LegStatusWorking,
		State:             LegStatusWorking,
		GatewayID:         event.GatewayID,
		OrderID:           event.OrderID,
		GatewayOrderID:    event.OrderID,
		Side:              plan.Side,
		Quantity:          plan.Quantity,
		RequestedQuantity: plan.Quantity,
		ReduceOnly:        plan.ReduceOnly,
		UpdatedAt:         event.At.UTC(),
	}
	execution.Legs = append(execution.Legs, leg)
	execution.GatewayIDs = append(execution.GatewayIDs, event.GatewayID)
	if len(execution.Legs) == 1 {
		execution.State = StateLeg1Working
	} else {
		execution.State = StateLeg2Working
	}
	return nil
}

func (execution *Execution) applyCompensationAccepted(event Event) error {
	if execution.State != StatePartiallyHedged {
		return errors.New("compensation requires partial hedge")
	}
	if execution.Compensation != nil && execution.Compensation.Attempted {
		return errors.New("automatic compensation was already attempted")
	}
	if !event.Quantity.IsPositive() || event.Quantity.GreaterThan(execution.DirectionalExposure) {
		return errors.New("compensation quantity exceeds directional exposure")
	}
	plan, ok := execution.plannedLeg(event.Role)
	if !ok {
		return fmt.Errorf("missing planned compensation target for role %s", event.Role)
	}
	leg := Leg{
		LegIdentity:       plan.LegIdentity,
		LegID:             legID(LegRoleCompensation),
		Role:              LegRoleCompensation,
		Status:            LegStatusWorking,
		State:             LegStatusWorking,
		GatewayID:         event.GatewayID,
		OrderID:           event.OrderID,
		GatewayOrderID:    event.OrderID,
		Side:              execution.compensationSide(event.Role),
		Quantity:          event.Quantity,
		RequestedQuantity: event.Quantity,
		ReduceOnly:        true,
		UpdatedAt:         event.At.UTC(),
	}
	execution.Legs = append(execution.Legs, leg)
	execution.GatewayIDs = append(execution.GatewayIDs, event.GatewayID)
	execution.Compensation = &CompensationState{
		Attempted:  true,
		GatewayID:  event.GatewayID,
		OrderID:    event.OrderID,
		TargetRole: event.Role,
		Quantity:   event.Quantity,
		Status:     LegStatusWorking,
	}
	execution.State = StateCompensating
	return nil
}

func (execution *Execution) applyFill(event Event) error {
	index := execution.legIndex(event.GatewayID)
	if index < 0 {
		return fmt.Errorf("unknown leg gateway id %s", event.GatewayID)
	}
	correctingRejectedOrUncertain := execution.Legs[index].Status == LegStatusRejected || execution.Legs[index].Status == LegStatusUncertain
	if correctingRejectedOrUncertain && !hasAuthoritativeFillEvidence(event, execution.Legs[index]) {
		return errors.New("cannot fill rejected or uncertain leg")
	}
	if !event.Quantity.IsPositive() {
		return errors.New("fill quantity must be positive")
	}
	if event.Quantity.GreaterThan(execution.Legs[index].RequestedQuantity) {
		return errors.New("fill quantity cannot exceed requested quantity")
	}
	if event.Quantity.Equal(execution.Legs[index].Filled) {
		if event.FillQuantity.IsPositive() {
			return errors.New("fill delta requires cumulative progress")
		}
		if event.Type == EventLegFilled && execution.Legs[index].Status == LegStatusPartial {
			execution.Legs[index].Status = LegStatusFilled
			execution.Legs[index].State = LegStatusFilled
			if execution.Legs[index].Role == LegRoleCompensation && execution.Compensation != nil {
				execution.Compensation.Status = LegStatusFilled
				execution.recomputeDirectionalExposure()
				execution.State = StateFailed
				return nil
			}
			if execution.plannedLegsFilled() {
				execution.finishIfFlat()
			}
		}
		return nil
	}
	if event.Quantity.LessThan(execution.Legs[index].Filled) {
		return errors.New("fill quantity is cumulative and cannot decrease")
	}
	fillQuantity := event.FillQuantity
	if fillQuantity.IsPositive() {
		if event.FillID == "" {
			return errors.New("detailed fill id is required")
		}
		increase := event.Quantity.Sub(execution.Legs[index].Filled)
		if !fillQuantity.Equal(increase) {
			return errors.New("fill delta must equal cumulative progress")
		}
	} else {
		fillQuantity = event.Quantity
	}
	if correctingRejectedOrUncertain {
		execution.applyLegOrderIdentity(index, event.OrderID)
		execution.Legs[index].Error = ""
		execution.Legs[index].LastError = ErrorInfo{}
	}
	execution.Legs[index].Filled = event.Quantity
	execution.Legs[index].UpdatedAt = event.At.UTC()
	execution.Fills = append(execution.Fills, Fill{
		LegIdentity:   execution.Legs[index].LegIdentity,
		FillID:        event.FillID,
		LegID:         execution.Legs[index].LegID,
		GatewayID:     event.GatewayID,
		GatewayFillID: event.GatewayFillID,
		Role:          execution.Legs[index].Role,
		Quantity:      fillQuantity,
		Price:         event.Price,
		Fee:           event.Fee,
		At:            event.At.UTC(),
		OccurredAt:    event.At.UTC(),
	})
	if event.Type == EventLegPartial {
		execution.Legs[index].Status = LegStatusPartial
		execution.Legs[index].State = LegStatusPartial
		if execution.unresolvedCompensationUncertain() {
			execution.State = StateUncertain
			return nil
		}
		if index == 0 && len(execution.Legs) == 1 {
			execution.State = StateLeg1Working
		} else {
			execution.State = StatePartiallyHedged
		}
		return nil
	}
	execution.Legs[index].Status = LegStatusFilled
	execution.Legs[index].State = LegStatusFilled
	if correctingRejectedOrUncertain {
		execution.clearResolvedManualIntervention()
	}
	if execution.Legs[index].Role == LegRoleCompensation && execution.Compensation != nil {
		execution.Compensation.Status = LegStatusFilled
		execution.recomputeDirectionalExposure()
		execution.State = StateFailed
		return nil
	}
	if execution.plannedLegsFilled() {
		execution.finishIfFlat()
	}
	return nil
}

func hasAuthoritativeFillEvidence(event Event, leg Leg) bool {
	if event.OrderID == "" || event.FillID == "" || event.GatewayFillID == "" ||
		!event.FillQuantity.IsPositive() || !event.Price.IsPositive() {
		return false
	}
	if leg.OrderID != "" && leg.OrderID != event.OrderID {
		return false
	}
	return leg.GatewayOrderID == "" || leg.GatewayOrderID == event.OrderID
}

func (execution *Execution) clearResolvedManualIntervention() {
	if execution.Compensation != nil && execution.Compensation.ManualRequired {
		return
	}
	for _, leg := range execution.Legs {
		if leg.Status == LegStatusUncertain {
			return
		}
	}
	execution.ManualIntervention = false
	execution.ManualInterventionReason = ""
}

func (execution *Execution) applyCanceled(event Event) error {
	index := execution.legIndex(event.GatewayID)
	if index < 0 {
		return fmt.Errorf("unknown leg gateway id %s", event.GatewayID)
	}
	leg := &execution.Legs[index]
	switch leg.Status {
	case LegStatusWorking, LegStatusPartial, LegStatusUncertain:
	default:
		return fmt.Errorf("cannot cancel leg in status %s", leg.Status)
	}
	leg.Status = LegStatusCanceled
	leg.State = LegStatusCanceled
	leg.Error = event.Error
	leg.LastError = safeError(event)
	leg.UpdatedAt = event.At.UTC()
	execution.recomputeDirectionalExposure()
	if leg.Role == LegRoleCompensation && execution.Compensation != nil {
		execution.Compensation.Status = LegStatusCanceled
		execution.Compensation.Error = safeError(event)
		execution.Compensation.ManualRequired = true
		execution.requireManual("compensation canceled: " + safeError(event).SafeMessage)
		return nil
	}
	if execution.DirectionalExposure.IsPositive() {
		execution.State = StatePartiallyHedged
		return nil
	}
	execution.State = StateFailed
	return nil
}

func (execution *Execution) applyRejected(event Event) error {
	index := execution.legIndex(event.GatewayID)
	if index < 0 {
		if event.Compensation {
			return execution.applyCompensationRejectedBeforeAcceptance(event)
		}
		return execution.applyRejectedBeforeAcceptance(event)
	}
	execution.Legs[index].Status = LegStatusRejected
	execution.Legs[index].State = LegStatusRejected
	execution.Legs[index].Error = event.Error
	execution.Legs[index].LastError = safeError(event)
	execution.Legs[index].UpdatedAt = event.At.UTC()
	if execution.Legs[index].Role == LegRoleCompensation && execution.Compensation != nil {
		execution.Compensation.Status = LegStatusRejected
		execution.Compensation.Error = safeError(event)
		execution.Compensation.ManualRequired = true
		execution.requireManual("compensation rejected: " + safeError(event).SafeMessage)
		return nil
	}
	execution.recomputeDirectionalExposure()
	if execution.DirectionalExposure.IsPositive() {
		execution.State = StatePartiallyHedged
		return nil
	}
	execution.State = StateFailed
	return nil
}

func (execution *Execution) applyCompensationRejectedBeforeAcceptance(event Event) error {
	if execution.State != StatePartiallyHedged {
		return errors.New("compensation rejection requires partial hedge")
	}
	if execution.Compensation != nil && execution.Compensation.Attempted {
		return errors.New("automatic compensation was already attempted")
	}
	if event.GatewayID == "" || !event.Quantity.IsPositive() ||
		event.Quantity.GreaterThan(execution.DirectionalExposure) {
		return errors.New("rejected compensation identity or quantity is invalid")
	}
	plan, ok := execution.plannedLeg(event.Role)
	if !ok {
		return fmt.Errorf("missing planned compensation target for role %s", event.Role)
	}
	info := safeError(event)
	execution.Legs = append(execution.Legs, Leg{
		LegIdentity:       plan.LegIdentity,
		LegID:             legID(LegRoleCompensation),
		Role:              LegRoleCompensation,
		Status:            LegStatusRejected,
		State:             LegStatusRejected,
		GatewayID:         event.GatewayID,
		OrderID:           event.OrderID,
		GatewayOrderID:    event.OrderID,
		Side:              oppositeSide(plan.Side),
		Quantity:          event.Quantity,
		RequestedQuantity: event.Quantity,
		ReduceOnly:        true,
		Error:             event.Error,
		LastError:         info,
		UpdatedAt:         event.At.UTC(),
	})
	execution.GatewayIDs = append(execution.GatewayIDs, event.GatewayID)
	execution.Compensation = &CompensationState{
		Attempted:      true,
		GatewayID:      event.GatewayID,
		OrderID:        event.OrderID,
		TargetRole:     event.Role,
		Quantity:       event.Quantity,
		Status:         LegStatusRejected,
		Error:          info,
		ManualRequired: true,
	}
	execution.requireManual("compensation rejected: " + info.SafeMessage)
	return nil
}

func (execution *Execution) applyRejectedBeforeAcceptance(event Event) error {
	if event.GatewayID == "" {
		return errors.New("rejected leg gateway identity is required")
	}
	expectedRole := LegRoleLong
	switch len(execution.Legs) {
	case 0:
		if execution.State != StateSubmitting {
			return fmt.Errorf("cannot reject first leg from state %s", execution.State)
		}
	case 1:
		if execution.State != StateLeg1Working ||
			execution.Legs[0].Role != LegRoleLong ||
			execution.Legs[0].Status != LegStatusFilled {
			return errors.New("second leg rejection requires a filled first leg")
		}
		expectedRole = LegRoleShort
	default:
		return errors.New("both planned legs were already attempted")
	}
	if event.Role != expectedRole {
		return fmt.Errorf("expected rejected role %s, got %s", expectedRole, event.Role)
	}
	plan, ok := execution.plannedLeg(event.Role)
	if !ok {
		return fmt.Errorf("missing planned leg for role %s", event.Role)
	}
	execution.Legs = append(execution.Legs, Leg{
		LegIdentity:       plan.LegIdentity,
		LegID:             legID(event.Role),
		Role:              event.Role,
		Status:            LegStatusRejected,
		State:             LegStatusRejected,
		GatewayID:         event.GatewayID,
		OrderID:           event.OrderID,
		GatewayOrderID:    event.OrderID,
		Side:              plan.Side,
		Quantity:          plan.Quantity,
		RequestedQuantity: plan.Quantity,
		ReduceOnly:        plan.ReduceOnly,
		Error:             event.Error,
		LastError:         safeError(event),
		UpdatedAt:         event.At.UTC(),
	})
	execution.GatewayIDs = append(execution.GatewayIDs, event.GatewayID)
	execution.recomputeDirectionalExposure()
	if execution.DirectionalExposure.IsPositive() {
		execution.State = StatePartiallyHedged
	} else {
		execution.State = StateFailed
	}
	return nil
}

func (execution *Execution) applyUncertain(event Event) error {
	if event.Compensation {
		if execution.Compensation != nil && execution.Compensation.Attempted && !sameCompensationIdentity(execution.Compensation, event) {
			return errors.New("automatic compensation was already attempted")
		}
		if execution.Compensation == nil {
			execution.Compensation = &CompensationState{
				Attempted:  true,
				GatewayID:  event.GatewayID,
				OrderID:    event.OrderID,
				TargetRole: event.Role,
				Quantity:   event.Quantity,
			}
		}
		execution.Compensation.Status = LegStatusUncertain
		execution.Compensation.Error = safeError(event)
		execution.Compensation.ManualRequired = true
		if execution.Compensation.GatewayID == "" {
			execution.Compensation.GatewayID = event.GatewayID
		}
		if execution.Compensation.OrderID == "" {
			execution.Compensation.OrderID = event.OrderID
		}
		if execution.Compensation.Quantity.IsZero() {
			execution.Compensation.Quantity = event.Quantity
		}
		index := execution.legIndex(event.GatewayID)
		if index >= 0 {
			execution.Legs[index].Status = LegStatusUncertain
			execution.Legs[index].State = LegStatusUncertain
			execution.applyLegOrderIdentity(index, event.OrderID)
			execution.Legs[index].Error = event.Error
			execution.Legs[index].LastError = safeError(event)
			execution.Legs[index].UpdatedAt = event.At.UTC()
		} else {
			plan, ok := execution.plannedLeg(event.Role)
			if !ok {
				return fmt.Errorf("missing planned compensation target for role %s", event.Role)
			}
			execution.Legs = append(execution.Legs, Leg{
				LegIdentity:       plan.LegIdentity,
				LegID:             legID(LegRoleCompensation),
				Role:              LegRoleCompensation,
				Status:            LegStatusUncertain,
				State:             LegStatusUncertain,
				GatewayID:         event.GatewayID,
				OrderID:           event.OrderID,
				GatewayOrderID:    event.OrderID,
				Side:              execution.compensationSide(event.Role),
				Quantity:          event.Quantity,
				RequestedQuantity: event.Quantity,
				ReduceOnly:        true,
				Error:             event.Error,
				LastError:         safeError(event),
				UpdatedAt:         event.At.UTC(),
			})
		}
		execution.ManualIntervention = true
		execution.ManualInterventionReason = "compensation uncertain: " + safeError(event).SafeMessage
	} else if index := execution.legIndex(event.GatewayID); index >= 0 {
		execution.Legs[index].Status = LegStatusUncertain
		execution.Legs[index].State = LegStatusUncertain
		execution.applyLegOrderIdentity(index, event.OrderID)
		execution.Legs[index].Error = event.Error
		execution.Legs[index].LastError = safeError(event)
		execution.Legs[index].UpdatedAt = event.At.UTC()
	} else {
		plan, ok := execution.plannedLeg(event.Role)
		if !ok {
			return fmt.Errorf("missing planned leg for role %s", event.Role)
		}
		execution.Legs = append(execution.Legs, Leg{
			LegIdentity:       plan.LegIdentity,
			LegID:             legID(event.Role),
			Role:              event.Role,
			Status:            LegStatusUncertain,
			State:             LegStatusUncertain,
			GatewayID:         event.GatewayID,
			OrderID:           event.OrderID,
			GatewayOrderID:    event.OrderID,
			Side:              plan.Side,
			Quantity:          plan.Quantity,
			RequestedQuantity: plan.Quantity,
			ReduceOnly:        plan.ReduceOnly,
			Error:             event.Error,
			LastError:         safeError(event),
			UpdatedAt:         event.At.UTC(),
		})
		execution.GatewayIDs = append(execution.GatewayIDs, event.GatewayID)
	}
	execution.State = StateUncertain
	return nil
}

func (execution *Execution) applyLegOrderIdentity(index int, orderID string) {
	if orderID == "" || index < 0 || index >= len(execution.Legs) {
		return
	}
	if execution.Legs[index].OrderID == "" {
		execution.Legs[index].OrderID = orderID
	}
	if execution.Legs[index].GatewayOrderID == "" {
		execution.Legs[index].GatewayOrderID = orderID
	}
}

func (execution *Execution) requireManual(reason string) {
	execution.State = StateManualInterventionRequired
	execution.ManualIntervention = true
	execution.ManualInterventionReason = reason
}

func (execution *Execution) unresolvedCompensationUncertain() bool {
	return execution.ManualIntervention &&
		execution.Compensation != nil &&
		execution.Compensation.ManualRequired &&
		execution.Compensation.Status == LegStatusUncertain
}

func (execution *Execution) finishIfFlat() {
	execution.recomputeDirectionalExposure()
	if execution.unresolvedCompensationUncertain() {
		execution.State = StateUncertain
		return
	}
	if !execution.DirectionalExposure.IsZero() {
		execution.State = StatePartiallyHedged
		return
	}
	if execution.Operation == OperationClose {
		execution.State = StateClosed
	} else {
		execution.State = StateHedged
	}
}

func (execution *Execution) recomputeDirectionalExposure() {
	longFilled := decimal.Zero
	shortFilled := decimal.Zero
	for _, leg := range execution.Legs {
		if !leg.Filled.IsPositive() {
			continue
		}
		switch leg.Role {
		case LegRoleLong:
			longFilled = longFilled.Add(leg.Filled)
		case LegRoleShort:
			shortFilled = shortFilled.Add(leg.Filled)
		case LegRoleCompensation:
			if execution.Compensation == nil {
				continue
			}
			switch execution.Compensation.TargetRole {
			case LegRoleLong:
				longFilled = longFilled.Sub(leg.Filled)
			case LegRoleShort:
				shortFilled = shortFilled.Sub(leg.Filled)
			}
		}
	}
	if execution.Operation == OperationClose {
		remainingLong := execution.InitialLongQuantity.Sub(longFilled)
		remainingShort := execution.InitialShortQuantity.Sub(shortFilled)
		execution.DirectionalExposure = remainingLong.Sub(remainingShort).Abs()
		return
	}
	execution.DirectionalExposure = longFilled.Sub(shortFilled).Abs()
}

func (execution *Execution) plannedLeg(role LegRole) (PlannedLeg, bool) {
	for _, leg := range execution.PlannedLegs {
		if leg.Role == role {
			return leg, true
		}
	}
	return PlannedLeg{}, false
}

func (execution *Execution) legIndex(gatewayID string) int {
	for i := range execution.Legs {
		if execution.Legs[i].GatewayID == gatewayID {
			return i
		}
	}
	return -1
}

func (execution *Execution) plannedLegsFilled() bool {
	if len(execution.Legs) < 2 {
		return false
	}
	return execution.Legs[0].Status == LegStatusFilled && execution.Legs[1].Status == LegStatusFilled
}

func (execution *Execution) isTerminal() bool {
	switch execution.State {
	case StateHedged, StateClosed, StateFailed, StateManualInterventionRequired:
		return true
	default:
		return false
	}
}

func (execution *Execution) compensationSide(target LegRole) Side {
	for _, leg := range execution.Legs {
		if leg.Role == target {
			return oppositeSide(leg.Side)
		}
	}
	for _, leg := range execution.PlannedLegs {
		if leg.Role == target {
			return oppositeSide(leg.Side)
		}
	}
	return SideSell
}

func oppositeSide(side Side) Side {
	if side == SideSell {
		return SideBuy
	}
	return SideSell
}

func legID(role LegRole) string {
	return string(role) + "-leg"
}

func safeError(event Event) ErrorInfo {
	if event.SafeError.Code != "" || event.SafeError.SafeMessage != "" {
		return event.SafeError
	}
	return ErrorInfo{Code: "GATEWAY_ERROR", SafeMessage: "gateway error"}
}

func sameCompensationIdentity(compensation *CompensationState, event Event) bool {
	if compensation == nil {
		return false
	}
	if compensation.GatewayID != "" && event.GatewayID != "" && compensation.GatewayID == event.GatewayID {
		return true
	}
	return compensation.OrderID != "" && event.OrderID != "" && compensation.OrderID == event.OrderID
}

func eventID(event Event) string {
	err := safeError(event)
	return fmt.Sprintf("%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%s|%t|%t",
		event.Type,
		event.FillID,
		event.GatewayID,
		event.GatewayFillID,
		event.Role,
		event.OrderID,
		event.Quantity.String(),
		event.FillQuantity.String(),
		event.Price.String(),
		event.Fee.Amount.String(),
		event.Fee.Currency,
		err.Code,
		err.SafeMessage,
		err.Retryable,
		event.Compensation,
	)
}

func BeginSubmit(at time.Time) Event {
	return Event{Type: EventBeginSubmit, At: at}
}

func LegAccepted(gatewayID string, role LegRole, orderID string, at time.Time) Event {
	return Event{Type: EventLegAccepted, GatewayID: gatewayID, Role: role, OrderID: orderID, At: at}
}

func CompensationAccepted(gatewayID string, orderID string, target LegRole, quantity decimal.Decimal, at time.Time) Event {
	return Event{Type: EventLegAccepted, GatewayID: gatewayID, Role: target, OrderID: orderID, Quantity: quantity, At: at, Compensation: true}
}

func LegFilled(gatewayID string, quantity decimal.Decimal, at time.Time) Event {
	return Event{Type: EventLegFilled, GatewayID: gatewayID, Quantity: quantity, At: at}
}

func LegFilledDetail(fillID string, gatewayID string, gatewayFillID string, quantity decimal.Decimal, price decimal.Decimal, fee Money, at time.Time) Event {
	return Event{Type: EventLegFilled, FillID: fillID, GatewayID: gatewayID, GatewayFillID: gatewayFillID, Quantity: quantity, Price: price, Fee: fee, At: at}
}

func LegPartialFillDetail(fillID string, gatewayID string, gatewayFillID string, cumulativeQuantity decimal.Decimal, fillQuantity decimal.Decimal, price decimal.Decimal, fee Money, at time.Time) Event {
	return Event{
		Type: EventLegPartial, FillID: fillID, GatewayID: gatewayID,
		GatewayFillID: gatewayFillID, Quantity: cumulativeQuantity,
		FillQuantity: fillQuantity, Price: price, Fee: fee, At: at,
	}
}

func LegFilledDeltaDetail(fillID string, gatewayID string, gatewayFillID string, cumulativeQuantity decimal.Decimal, fillQuantity decimal.Decimal, price decimal.Decimal, fee Money, at time.Time) Event {
	return Event{
		Type: EventLegFilled, FillID: fillID, GatewayID: gatewayID,
		GatewayFillID: gatewayFillID, Quantity: cumulativeQuantity,
		FillQuantity: fillQuantity, Price: price, Fee: fee, At: at,
	}
}

func LegPartial(gatewayID string, quantity decimal.Decimal, at time.Time) Event {
	return Event{Type: EventLegPartial, GatewayID: gatewayID, Quantity: quantity, At: at}
}

func LegRejected(gatewayID string, message string, at time.Time) Event {
	return Event{Type: EventLegRejected, GatewayID: gatewayID, Error: message, At: at}
}

func LegRejectedCode(gatewayID string, info ErrorInfo, at time.Time) Event {
	return Event{Type: EventLegRejected, GatewayID: gatewayID, Error: info.SafeMessage, SafeError: info, At: at}
}

func LegRejectedBeforeAcceptance(gatewayID string, role LegRole, info ErrorInfo, at time.Time) Event {
	return Event{
		Type: EventLegRejected, GatewayID: gatewayID, Role: role,
		Error: info.SafeMessage, SafeError: info, At: at,
	}
}

func LegCanceled(gatewayID string, at time.Time) Event {
	return LegCanceledCode(
		gatewayID,
		ErrorInfo{Code: "CANCELED", SafeMessage: "order canceled"},
		at,
	)
}

func LegCanceledCode(gatewayID string, info ErrorInfo, at time.Time) Event {
	return Event{
		Type: EventLegCanceled, GatewayID: gatewayID,
		Error: info.SafeMessage, SafeError: info, At: at,
	}
}

func LegUncertain(gatewayID string, role LegRole, message string, at time.Time) Event {
	return Event{Type: EventLegUncertain, GatewayID: gatewayID, Role: role, Error: message, At: at}
}

func CompensationUncertain(gatewayID string, message string, target LegRole, quantity decimal.Decimal, at time.Time) Event {
	return Event{
		Type:         EventLegUncertain,
		GatewayID:    gatewayID,
		Role:         target,
		Quantity:     quantity,
		Error:        message,
		At:           at,
		Compensation: true,
	}
}

func CompensationUncertainCode(gatewayID string, info ErrorInfo, target LegRole, quantity decimal.Decimal, at time.Time) Event {
	return Event{
		Type:         EventLegUncertain,
		GatewayID:    gatewayID,
		Role:         target,
		Quantity:     quantity,
		Error:        info.SafeMessage,
		SafeError:    info,
		At:           at,
		Compensation: true,
	}
}

func CompensationRejectedBeforeAcceptance(gatewayID string, orderID string, target LegRole, quantity decimal.Decimal, info ErrorInfo, at time.Time) Event {
	return Event{
		Type:         EventLegRejected,
		GatewayID:    gatewayID,
		Role:         target,
		OrderID:      orderID,
		Quantity:     quantity,
		Error:        info.SafeMessage,
		SafeError:    info,
		At:           at,
		Compensation: true,
	}
}
