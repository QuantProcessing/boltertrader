package store

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
)

var ErrNotFound = errors.New("gateway store not found")

type ReserveCommandRequest struct {
	PrincipalID    domain.PrincipalID
	AccountID      domain.AccountID
	InstrumentID   domain.InstrumentID
	IdempotencyKey domain.IdempotencyKey
	CorrelationID  domain.EventID
	PayloadHash    string
	Payload        json.RawMessage
}

func (request ReserveCommandRequest) Validate() error {
	if err := request.PrincipalID.Validate(); err != nil {
		return err
	}
	if err := request.AccountID.Validate(); err != nil {
		return err
	}
	if err := request.InstrumentID.Validate(); err != nil {
		return err
	}
	if err := request.IdempotencyKey.Validate(); err != nil {
		return err
	}
	if err := request.CorrelationID.Validate(); err != nil {
		return err
	}
	if request.PayloadHash == "" {
		return domainErr("payload hash is required")
	}
	if len(request.Payload) == 0 {
		return domainErr("durable payload is required")
	}
	canonicalHash, err := domain.CanonicalPayloadHash(request.Payload)
	if err != nil {
		return domainErr("durable payload must be valid JSON")
	}
	if canonicalHash != request.PayloadHash {
		return domainErr("durable payload hash mismatch")
	}
	return nil
}

type ReservedCommand struct {
	CommandID       domain.CommandID
	GatewayOrderID  domain.GatewayOrderID
	ClientOrderID   string
	State           domain.OrderState
	ResourceVersion domain.ResourceVersion
	VenueOrderID    string
	New             bool
	Conflict        IdempotencyConflict
}

type CommandStore interface {
	ReserveCommand(context.Context, ReserveCommandRequest) (ReservedCommand, error)
	MarkDispatching(context.Context, MarkDispatchingRequest) (StoredCommand, error)
	RecordVenueResult(context.Context, RecordVenueResultRequest) (StoredCommand, error)
	LoadOrder(context.Context, LoadOrderRequest) (StoredCommand, error)
	LoadOrderByVenueIdentity(context.Context, LoadOrderByVenueIdentityRequest) (StoredCommand, error)
	ListOrders(context.Context, ListOrdersRequest) ([]StoredCommand, error)
	LoadRecoverableCommands(context.Context, LoadRecoverableCommandsRequest) ([]StoredCommand, error)
	RescheduleReconciliation(context.Context, RescheduleReconciliationRequest) (ReconciliationTask, error)
	ClaimDueReconciliationTasks(context.Context, ClaimDueReconciliationTasksRequest) ([]ReconciliationTask, error)
}

type IdempotencyConflict string

const (
	ConflictNone       IdempotencyConflict = ""
	ConflictPayload    IdempotencyConflict = "payload_mismatch"
	ConflictAccount    IdempotencyConflict = "account_mismatch"
	ConflictInstrument IdempotencyConflict = "instrument_mismatch"
)

type StoredCommand struct {
	CommandID       domain.CommandID
	GatewayOrderID  domain.GatewayOrderID
	PrincipalID     domain.PrincipalID
	AccountID       domain.AccountID
	InstrumentID    domain.InstrumentID
	ClientOrderID   string
	State           domain.OrderState
	ResourceVersion domain.ResourceVersion
	VenueOrderID    string
	EventID         domain.EventID
	Payload         json.RawMessage
}

type LoadOrderRequest struct {
	PrincipalID    domain.PrincipalID
	GatewayOrderID domain.GatewayOrderID
}

type LoadOrderByVenueIdentityRequest struct {
	AccountID     domain.AccountID
	Product       string
	ClientOrderID string
	VenueOrderID  string
}

func (request LoadOrderByVenueIdentityRequest) Validate() error {
	if err := request.AccountID.Validate(); err != nil {
		return err
	}
	if request.Product != "spot" && request.Product != "perp" {
		return domainErr("order product is invalid")
	}
	if request.ClientOrderID == "" && request.VenueOrderID == "" {
		return domainErr("venue order identity is required")
	}
	if request.ClientOrderID != "" {
		if _, err := domain.ParseClientOrderID(request.ClientOrderID); err != nil {
			return err
		}
	}
	if request.VenueOrderID != strings.TrimSpace(request.VenueOrderID) ||
		strings.ContainsAny(request.VenueOrderID, "\n\r\t") {
		return domainErr("venue order id is invalid")
	}
	return nil
}

func (request LoadOrderRequest) Validate() error {
	if err := request.PrincipalID.Validate(); err != nil {
		return err
	}
	return request.GatewayOrderID.Validate()
}

type ListOrdersRequest struct {
	PrincipalID  domain.PrincipalID
	AccountID    domain.AccountID
	InstrumentID domain.InstrumentID
	OpenOnly     bool
	Limit        int
	Cursor       domain.GatewayOrderID
}

func (request ListOrdersRequest) Validate() error {
	if err := request.PrincipalID.Validate(); err != nil {
		return err
	}
	if request.AccountID != "" {
		if err := request.AccountID.Validate(); err != nil {
			return err
		}
	}
	if request.InstrumentID != "" {
		if err := request.InstrumentID.Validate(); err != nil {
			return err
		}
	}
	if request.Cursor != "" {
		if err := request.Cursor.Validate(); err != nil {
			return err
		}
	}
	if request.Limit <= 0 || request.Limit > 500 {
		return domainErr("order list limit is out of range")
	}
	return nil
}

type MarkDispatchingRequest struct {
	CommandID domain.CommandID
	EventID   domain.EventID
}

type RecordVenueResultRequest struct {
	CommandID       domain.CommandID
	VenueOrderID    string
	NextState       domain.OrderState
	EventID         domain.EventID
	ResourceVersion domain.ResourceVersion
	Reconciliation  *ReconciliationPlan
}

type LoadRecoverableCommandsRequest struct {
	AccountID domain.AccountID
	Limit     int
}

type ReconciliationReason string

const (
	ReconciliationAmbiguousHandoff    ReconciliationReason = "ambiguous_handoff"
	ReconciliationLookupFailed        ReconciliationReason = "lookup_failed"
	ReconciliationEvidenceInvalid     ReconciliationReason = "evidence_invalid"
	ReconciliationEvidenceUnconfirmed ReconciliationReason = "evidence_unconfirmed"
)

func (reason ReconciliationReason) Validate() error {
	switch reason {
	case ReconciliationAmbiguousHandoff, ReconciliationLookupFailed,
		ReconciliationEvidenceInvalid, ReconciliationEvidenceUnconfirmed:
		return nil
	default:
		return domainErr("reconciliation reason is invalid")
	}
}

type ReconciliationPlan struct {
	TaskID        domain.EventID
	Reason        ReconciliationReason
	NextAttemptAt time.Time
}

func (plan ReconciliationPlan) Validate() error {
	if err := plan.TaskID.Validate(); err != nil {
		return err
	}
	if err := plan.Reason.Validate(); err != nil {
		return err
	}
	if plan.NextAttemptAt.IsZero() {
		return domainErr("reconciliation next attempt is required")
	}
	return nil
}

type ReconciliationTask struct {
	TaskID        domain.EventID
	Command       StoredCommand
	Reason        ReconciliationReason
	NextAttemptAt time.Time
	Attempts      int
}

type RescheduleReconciliationRequest struct {
	GatewayOrderID domain.GatewayOrderID
	Plan           ReconciliationPlan
}

func (request RescheduleReconciliationRequest) Validate() error {
	if err := request.GatewayOrderID.Validate(); err != nil {
		return err
	}
	return request.Plan.Validate()
}

type ClaimDueReconciliationTasksRequest struct {
	AccountID   domain.AccountID
	Now         time.Time
	LeaseUntil  time.Time
	Limit       int
	MaxAttempts int
}

func (request ClaimDueReconciliationTasksRequest) Validate() error {
	if err := request.AccountID.Validate(); err != nil {
		return err
	}
	if request.Now.IsZero() {
		return domainErr("reconciliation due time is required")
	}
	if !request.LeaseUntil.After(request.Now) {
		return domainErr("reconciliation lease must end after due time")
	}
	if request.Limit <= 0 || request.Limit > 1000 {
		return domainErr("reconciliation task limit is out of range")
	}
	if request.MaxAttempts <= 0 || request.MaxAttempts > 1000 {
		return domainErr("reconciliation max attempts is out of range")
	}
	return nil
}

func domainErr(message string) error {
	return &contractError{message: message}
}

type contractError struct{ message string }

func (err *contractError) Error() string { return err.message }
