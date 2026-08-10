package executionservice

import (
	"context"
	"encoding/json"
	"errors"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/shopspring/decimal"
)

var (
	ErrNotReady            = errors.New("execution service is not ready")
	ErrPreviewConflict     = errors.New("execution preview conflict")
	ErrPreviewExpired      = errors.New("execution preview expired")
	ErrPreviewMismatch     = errors.New("execution preview mismatch")
	ErrBlockedPreview      = errors.New("execution preview is blocked")
	ErrIdempotencyConflict = errors.New("idempotency key conflict")
	ErrNotFound            = errors.New("execution not found")
	ErrRecoveryInProgress  = errors.New("execution recovery is in progress")
	ErrServiceUnavailable  = errors.New("execution service unavailable")
	ErrExecutionInProgress = errors.New("execution request is already in progress")
	ErrInvalidScope        = errors.New("execution scope is invalid")
)

type Scope struct {
	PrincipalID string
	Mode        string
}

type PreviewRequest struct {
	PreviewID     string
	Operation     execution.Operation
	OpportunityID string
	PositionID    string
	CapitalUSDT   decimal.Decimal
	Leverage      int
}

type SubmitRequest struct {
	PreviewID     string
	Operation     execution.Operation
	OpportunityID string
	PositionID    string
	CapitalUSDT   decimal.Decimal
	Leverage      int
	ReceivedAt    time.Time
}

type StoredPreview struct {
	Preview     execution.PreviewResult
	RequestHash string
	Expired     bool
}

type StoredExecution struct {
	Execution execution.Execution
	Version   int64
}

type IdempotencyReservation struct {
	Execution    execution.Execution
	Version      int64
	Replayed     bool
	State        string
	ResponseCode int
	ResponseBody json.RawMessage
}

type IdempotencyRecord struct {
	ExecutionID  string
	RequestHash  string
	State        string
	ResponseCode int
	ResponseBody json.RawMessage
}

type PersistTransitionInput struct {
	Scope           Scope
	Execution       execution.Execution
	ExpectedVersion int64
	Kind            string
	RequestID       string
	Payload         json.RawMessage
	OccurredAt      time.Time
}

type CommittedEvent struct {
	ID       string
	Kind     string
	EntityID string
	Payload  json.RawMessage
	Occurred time.Time
}

type Store interface {
	SavePreview(ctx context.Context, preview execution.PreviewResult, requestHash string) error
	GetPreview(ctx context.Context, scope Scope, id string, now time.Time) (StoredPreview, error)
	GetIdempotency(ctx context.Context, scope Scope, method, path, key string) (IdempotencyRecord, error)
	ReserveExecution(ctx context.Context, scope Scope, method, path, key, requestHash string, snapshot execution.Execution) (IdempotencyReservation, error)
	CompleteIdempotency(ctx context.Context, scope Scope, method, path, key string, code int, body json.RawMessage) error
	CompleteLinkedIdempotency(ctx context.Context, scope Scope, executionID string, code int, body json.RawMessage) error
	GetExecution(ctx context.Context, scope Scope, id string) (StoredExecution, error)
	ListNonTerminal(ctx context.Context, scope Scope) ([]StoredExecution, error)
	ListTerminalWithIncompleteIdempotency(ctx context.Context, scope Scope) ([]StoredExecution, error)
	PersistTransition(ctx context.Context, input PersistTransitionInput) ([]CommittedEvent, error)
}

type FreshPreviewSource interface {
	FreshPreview(ctx context.Context, scope Scope, request PreviewRequest) (execution.PreviewInput, error)
}

type Gateway interface {
	PlaceOrder(ctx context.Context, request GatewayOrderRequest) (GatewayOrderResult, error)
	CancelOrder(ctx context.Context, request GatewayCancelRequest) (GatewayOrderResult, error)
	GetOrder(ctx context.Context, accountID, gatewayID string) (OrderObservation, error)
	Fills(ctx context.Context, accountID, instrumentID, gatewayID string) ([]FillObservation, error)
	Positions(ctx context.Context, accountID, instrumentID string) ([]PositionObservation, error)
}

type Publisher interface {
	Publish(ctx context.Context, events []CommittedEvent) error
}

type RecoveryRepairer interface {
	Repair(ctx context.Context, scope Scope) error
}

type Clock interface {
	Now() time.Time
}

type IDGenerator interface {
	NewExecutionID(scope Scope, key string, preview execution.PreviewResult) string
}

type GatewayOutcome string

const (
	GatewayAccepted  GatewayOutcome = "accepted"
	GatewayRejected  GatewayOutcome = "rejected"
	GatewayAmbiguous GatewayOutcome = "ambiguous"
)

type GatewayOrderRequest struct {
	GatewayID      string
	IdempotencyKey string
	AccountID      string
	InstrumentID   string
	Side           execution.Side
	Quantity       decimal.Decimal
	ReduceOnly     bool
	Role           execution.LegRole
}

type GatewayCancelRequest struct {
	AccountID string
	GatewayID string
}

type GatewayOrderResult struct {
	Outcome   GatewayOutcome
	GatewayID string
	OrderID   string
	Error     execution.ErrorInfo
}

type OrderObservation struct {
	Found     bool
	GatewayID string
	OrderID   string
	Status    execution.LegStatus
	Error     execution.ErrorInfo
}

type FillObservation struct {
	FillID        string
	GatewayFillID string
	Quantity      decimal.Decimal
	Price         decimal.Decimal
	Fee           execution.Money
	OccurredAt    time.Time
}

type PositionObservation struct {
	AccountID    string
	InstrumentID string
	Quantity     decimal.Decimal
}

type Result struct {
	Execution     execution.Execution
	Replayed      bool
	Degraded      bool
	Recoverable   bool
	SafeError     execution.ErrorInfo
	ResponseCode  int
	ResponseBody  json.RawMessage
	PublishedIDs  []string
	GatewayWrites []string
}

type Readiness struct {
	Ready      bool
	Recovering bool
	Degraded   bool
	Reason     string
}
