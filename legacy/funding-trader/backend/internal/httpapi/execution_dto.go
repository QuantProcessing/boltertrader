package httpapi

import (
	"encoding/json"
	"strings"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionservice"
)

type executionPreviewDTO struct {
	PreviewID            string                `json:"previewId"`
	OpportunityID        string                `json:"opportunityId"`
	SnapshotID           string                `json:"snapshotId"`
	CanSubmit            bool                  `json:"canSubmit"`
	Blockers             []executionFindingDTO `json:"blockers"`
	Warnings             []executionFindingDTO `json:"warnings"`
	PlannedLegs          []plannedLegDTO       `json:"plannedLegs"`
	EstimatedFeesUSD     string                `json:"estimatedFeesUsd"`
	EstimatedSlippageUSD string                `json:"estimatedSlippageUsd"`
	ExpectedNetAprPct    json.Number           `json:"expectedNetAprPct"`
	ExpectedNetPnL7dUSD  string                `json:"expectedNetPnL7dUsd"`
}

type executionFindingDTO struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

type plannedLegDTO struct {
	Role             string `json:"role"`
	AccountID        string `json:"accountId"`
	Venue            string `json:"venue"`
	VenueEnvironment string `json:"venueEnvironment"`
	Symbol           string `json:"symbol"`
	InstrumentID     string `json:"instrumentId"`
	Side             string `json:"side"`
	Quantity         string `json:"quantity"`
	Notional         string `json:"notional"`
	MarkPrice        string `json:"markPrice"`
	ReduceOnly       bool   `json:"reduceOnly"`
}

type executionResponseDTO struct {
	ExecutionID                string                    `json:"executionId"`
	OperationKind              string                    `json:"operationKind"`
	OpportunityID              string                    `json:"opportunityId"`
	State                      string                    `json:"state"`
	LatestEventAt              string                    `json:"latestEventAt"`
	GatewayOrderIDs            []string                  `json:"gatewayOrderIds"`
	CompensationState          *executionCompensationDTO `json:"compensationState"`
	ReconciliationState        string                    `json:"reconciliationState"`
	BlockingReason             string                    `json:"blockingReason"`
	ManualInterventionRequired bool                      `json:"manualInterventionRequired"`
	Legs                       []executionLegDTO         `json:"legs"`
	Fills                      []executionFillDTO        `json:"fills"`
}

type executionLegDTO struct {
	LegID             string             `json:"legId"`
	Role              string             `json:"role"`
	AccountID         string             `json:"accountId"`
	Venue             string             `json:"venue"`
	InstrumentID      string             `json:"instrumentId"`
	VenueEnvironment  string             `json:"venueEnvironment"`
	Side              string             `json:"side"`
	RequestedQuantity string             `json:"requestedQuantity"`
	FilledQuantity    string             `json:"filledQuantity"`
	State             string             `json:"state"`
	GatewayOrderID    string             `json:"gatewayOrderId"`
	LastError         *executionErrorDTO `json:"lastError"`
	UpdatedAt         string             `json:"updatedAt"`
}

type executionFillDTO struct {
	FillID           string            `json:"fillId"`
	LegID            string            `json:"legId"`
	AccountID        string            `json:"accountId"`
	Venue            string            `json:"venue"`
	InstrumentID     string            `json:"instrumentId"`
	VenueEnvironment string            `json:"venueEnvironment"`
	GatewayFillID    string            `json:"gatewayFillId"`
	Quantity         string            `json:"quantity"`
	Price            string            `json:"price"`
	Fee              executionMoneyDTO `json:"fee"`
	OccurredAt       string            `json:"occurredAt"`
}

type executionMoneyDTO struct {
	Amount   string `json:"amount"`
	Currency string `json:"currency"`
}

type executionErrorDTO struct {
	Code      string `json:"code"`
	Message   string `json:"message"`
	Retryable bool   `json:"retryable"`
}

type executionCompensationDTO struct {
	Attempted                  bool               `json:"attempted"`
	TargetRole                 string             `json:"targetRole"`
	RequestedQuantity          string             `json:"requestedQuantity"`
	State                      string             `json:"state"`
	GatewayOrderID             string             `json:"gatewayOrderId"`
	LastError                  *executionErrorDTO `json:"lastError"`
	ManualInterventionRequired bool               `json:"manualInterventionRequired"`
}

func newExecutionPreviewDTO(preview execution.PreviewResult) executionPreviewDTO {
	blockers := make([]executionFindingDTO, 0, len(preview.Blockers))
	for _, finding := range preview.Blockers {
		blockers = append(blockers, executionFindingDTO{
			Code: string(finding.Code), Message: finding.Message,
		})
	}
	warnings := make([]executionFindingDTO, 0, len(preview.Warnings))
	for _, finding := range preview.Warnings {
		warnings = append(warnings, executionFindingDTO{
			Code: string(finding.Code), Message: finding.Message,
		})
	}
	plannedLegs := make([]plannedLegDTO, 0, len(preview.PlannedLegs))
	for _, leg := range preview.PlannedLegs {
		plannedLegs = append(plannedLegs, plannedLegDTO{
			Role:             string(leg.Role),
			AccountID:        leg.AccountID,
			Venue:            leg.Venue,
			VenueEnvironment: leg.VenueEnvironment,
			Symbol:           leg.Symbol,
			InstrumentID:     leg.InstrumentID,
			Side:             string(leg.Side),
			Quantity:         leg.Quantity.String(),
			Notional:         leg.Notional.String(),
			MarkPrice:        leg.MarkPrice.String(),
			ReduceOnly:       leg.ReduceOnly,
		})
	}
	return executionPreviewDTO{
		PreviewID:            preview.PreviewID,
		OpportunityID:        preview.OpportunityID,
		SnapshotID:           preview.SnapshotID,
		CanSubmit:            preview.CanSubmit,
		Blockers:             blockers,
		Warnings:             warnings,
		PlannedLegs:          plannedLegs,
		EstimatedFeesUSD:     preview.FeesUSDT.String(),
		EstimatedSlippageUSD: preview.SlippageUSDT.String(),
		ExpectedNetAprPct:    json.Number(preview.ExpectedNetAprPct.String()),
		ExpectedNetPnL7dUSD:  preview.NetProfit7d.String(),
	}
}

func newExecutionResultDTO(result executionservice.Result) executionResponseDTO {
	snapshot := result.Execution
	if len(result.ResponseBody) > 0 && json.Valid(result.ResponseBody) {
		var first execution.Execution
		if err := json.Unmarshal(result.ResponseBody, &first); err == nil &&
			strings.TrimSpace(first.ID) != "" {
			snapshot = first
		}
	}
	return newExecutionResponseDTO(snapshot)
}

func newExecutionResponseDTO(snapshot execution.Execution) executionResponseDTO {
	legs := make([]executionLegDTO, 0, len(snapshot.Legs))
	orderIDs := make([]string, 0, len(snapshot.Legs))
	seenOrderIDs := make(map[string]struct{}, len(snapshot.Legs))
	for _, leg := range snapshot.Legs {
		orderID := strings.TrimSpace(leg.GatewayOrderID)
		if orderID == "" {
			orderID = strings.TrimSpace(leg.OrderID)
		}
		if orderID != "" {
			if _, seen := seenOrderIDs[orderID]; !seen {
				seenOrderIDs[orderID] = struct{}{}
				orderIDs = append(orderIDs, orderID)
			}
		}
		legs = append(legs, executionLegDTO{
			LegID:             leg.LegID,
			Role:              string(leg.Role),
			AccountID:         leg.AccountID,
			Venue:             leg.Venue,
			InstrumentID:      leg.InstrumentID,
			VenueEnvironment:  leg.VenueEnvironment,
			Side:              string(leg.Side),
			RequestedQuantity: leg.RequestedQuantity.String(),
			FilledQuantity:    leg.Filled.String(),
			State:             executionLegStateDTO(leg.State),
			GatewayOrderID:    orderID,
			LastError:         newExecutionErrorDTO(leg.LastError),
			UpdatedAt:         executionTimeDTO(leg.UpdatedAt),
		})
	}
	fills := make([]executionFillDTO, 0, len(snapshot.Fills))
	for _, fill := range snapshot.Fills {
		occurredAt := fill.OccurredAt
		if occurredAt.IsZero() {
			occurredAt = fill.At
		}
		fills = append(fills, executionFillDTO{
			FillID:           fill.FillID,
			LegID:            fill.LegID,
			AccountID:        fill.AccountID,
			Venue:            fill.Venue,
			InstrumentID:     fill.InstrumentID,
			VenueEnvironment: fill.VenueEnvironment,
			GatewayFillID:    fill.GatewayFillID,
			Quantity:         fill.Quantity.String(),
			Price:            fill.Price.String(),
			Fee: executionMoneyDTO{
				Amount: fill.Fee.Amount.String(), Currency: fill.Fee.Currency,
			},
			OccurredAt: executionTimeDTO(occurredAt),
		})
	}
	blockingReason := strings.TrimSpace(snapshot.BlockingReason)
	if blockingReason == "" && snapshot.ManualIntervention {
		blockingReason = strings.TrimSpace(snapshot.ManualInterventionReason)
	}
	latestEventAt := snapshot.UpdatedAt
	if latestEventAt.IsZero() {
		latestEventAt = snapshot.CreatedAt
	}
	return executionResponseDTO{
		ExecutionID:                snapshot.ID,
		OperationKind:              string(snapshot.Operation),
		OpportunityID:              snapshot.OpportunityID,
		State:                      executionStateDTO(snapshot),
		LatestEventAt:              executionTimeDTO(latestEventAt),
		GatewayOrderIDs:            orderIDs,
		CompensationState:          newExecutionCompensationDTO(snapshot.Compensation),
		ReconciliationState:        snapshot.ReconciliationState,
		BlockingReason:             blockingReason,
		ManualInterventionRequired: snapshot.ManualIntervention,
		Legs:                       legs,
		Fills:                      fills,
	}
}

func newExecutionCompensationDTO(
	compensation *execution.CompensationState,
) *executionCompensationDTO {
	if compensation == nil {
		return nil
	}
	return &executionCompensationDTO{
		Attempted:                  compensation.Attempted,
		TargetRole:                 string(compensation.TargetRole),
		RequestedQuantity:          compensation.Quantity.String(),
		State:                      executionLegStateDTO(compensation.Status),
		GatewayOrderID:             compensation.OrderID,
		LastError:                  newExecutionErrorDTO(compensation.Error),
		ManualInterventionRequired: compensation.ManualRequired,
	}
}

func newExecutionErrorDTO(info execution.ErrorInfo) *executionErrorDTO {
	if strings.TrimSpace(info.Code) == "" &&
		strings.TrimSpace(info.SafeMessage) == "" &&
		!info.Retryable {
		return nil
	}
	return &executionErrorDTO{
		Code: info.Code, Message: info.SafeMessage, Retryable: info.Retryable,
	}
}

func executionStateDTO(snapshot execution.Execution) string {
	switch snapshot.State {
	case execution.StatePreviewed, execution.StateSubmitting,
		execution.StateLeg1Working, execution.StateLeg2Working:
		if len(snapshot.Fills) > 0 {
			return "partiallyFilled"
		}
		for _, leg := range snapshot.Legs {
			if leg.Filled.IsPositive() {
				return "partiallyFilled"
			}
		}
		return "open"
	case execution.StateHedged:
		return "open"
	case execution.StateClosed:
		return "completed"
	case execution.StatePartiallyHedged:
		return "partiallyHedged"
	case execution.StateCompensating:
		return "compensating"
	case execution.StateFailed:
		return "rejected"
	case execution.StateUncertain:
		return "uncertain"
	case execution.StateManualInterventionRequired:
		return "manualInterventionRequired"
	default:
		return "uncertain"
	}
}

func executionLegStateDTO(state execution.LegStatus) string {
	switch state {
	case execution.LegStatusAccepted, execution.LegStatusWorking:
		return "open"
	case execution.LegStatusPartial:
		return "partiallyFilled"
	case execution.LegStatusFilled:
		return "completed"
	case execution.LegStatusCanceled:
		return "canceled"
	case execution.LegStatusRejected:
		return "rejected"
	case execution.LegStatusUncertain:
		return "uncertain"
	default:
		return "uncertain"
	}
}

func executionTimeDTO(value time.Time) string {
	if value.IsZero() {
		return ""
	}
	return value.UTC().Format(time.RFC3339Nano)
}
