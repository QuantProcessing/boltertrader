package work

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	fundingpolicy "github.com/QuantProcessing/boltertrader/backend/internal/products/funding/policy"
)

type FundingExecutionState string

const (
	FundingExecutionPreviewed       FundingExecutionState = "previewed"
	FundingExecutionSubmitting      FundingExecutionState = "submitting"
	FundingExecutionLeg1Working     FundingExecutionState = "leg_1_working"
	FundingExecutionLeg2Working     FundingExecutionState = "leg_2_working"
	FundingExecutionPartiallyHedged FundingExecutionState = "partially_hedged"
	FundingExecutionCompensating    FundingExecutionState = "compensating"
	FundingExecutionHedged          FundingExecutionState = "hedged"
	FundingExecutionClosed          FundingExecutionState = "closed"
	FundingExecutionUncertain       FundingExecutionState = "uncertain"
	FundingExecutionManual          FundingExecutionState = "manual_intervention_required"
)

type FundingExecutionLeg struct {
	Role              string                   `json:"role"`
	Draft             FundingOrderDraft        `json:"draft"`
	CommandID         string                   `json:"commandId"`
	CommandState      orders.State             `json:"commandState"`
	FilledQuantity    string                   `json:"filledQuantity"`
	ExecutionEvidence orders.ExecutionEvidence `json:"executionEvidence,omitempty"`
}

type FundingExecution struct {
	ExecutionID           string                `json:"executionId"`
	PrincipalID           string                `json:"principalId"`
	Mode                  orders.Mode           `json:"mode"`
	LineageID             string                `json:"lineageId"`
	ParentCommandID       string                `json:"parentCommandId"`
	PlanKey               string                `json:"planKey"`
	PreviewHash           string                `json:"previewHash"`
	IntentKind            string                `json:"intentKind"`
	State                 FundingExecutionState `json:"state"`
	Version               int64                 `json:"version"`
	Legs                  []FundingExecutionLeg `json:"legs"`
	CompensationAttempted bool                  `json:"compensationAttempted"`
	RecoveryCursor        string                `json:"recoveryCursor"`
	CreatedAt             time.Time             `json:"createdAt"`
	UpdatedAt             time.Time             `json:"updatedAt"`
}

type FundingExecutionTransition struct {
	ExecutionID string                `json:"executionId"`
	Version     int64                 `json:"version"`
	From        FundingExecutionState `json:"from"`
	To          FundingExecutionState `json:"to"`
	Kind        string                `json:"kind"`
	OccurredAt  time.Time             `json:"occurredAt"`
}

type FundingExecutionStore interface {
	ReserveFundingExecution(context.Context, FundingExecution) (FundingExecution, bool, error)
	GetFundingExecution(context.Context, string, orders.Mode, string) (FundingExecution, error)
	PersistFundingExecution(context.Context, FundingExecution, int64, string) (FundingExecution, error)
}

type MemoryFundingExecutionStore struct {
	mu          sync.Mutex
	executions  map[string]FundingExecution
	transitions map[string][]FundingExecutionTransition
	admission   fundingpolicy.Admission
}

func (store *MemoryFundingExecutionStore) WithAdmission(admission fundingpolicy.Admission) *MemoryFundingExecutionStore {
	store.admission = admission
	return store
}

func NewMemoryFundingExecutionStore() *MemoryFundingExecutionStore {
	return &MemoryFundingExecutionStore{executions: make(map[string]FundingExecution), transitions: make(map[string][]FundingExecutionTransition)}
}

func (store *MemoryFundingExecutionStore) ReserveFundingExecution(_ context.Context, execution FundingExecution) (FundingExecution, bool, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if err := validateFundingExecutionWithAdmission(execution, store.admission); err != nil {
		return FundingExecution{}, false, err
	}
	key := fundingExecutionKey(execution.PrincipalID, execution.Mode, execution.LineageID)
	if existing, ok := store.executions[key]; ok {
		if existing.ExecutionID != execution.ExecutionID || existing.ParentCommandID != execution.ParentCommandID || existing.PlanKey != execution.PlanKey || existing.PreviewHash != execution.PreviewHash || existing.IntentKind != execution.IntentKind {
			return existing, true, ErrIdempotencyConflict
		}
		return existing, true, nil
	}
	execution.Version = 1
	store.executions[key] = cloneFundingExecution(execution)
	return cloneFundingExecution(execution), false, nil
}

func (store *MemoryFundingExecutionStore) GetFundingExecution(_ context.Context, principal string, mode orders.Mode, lineage string) (FundingExecution, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	execution, ok := store.executions[fundingExecutionKey(principal, mode, lineage)]
	if !ok {
		return FundingExecution{}, ErrWorkRevisionConflict
	}
	return cloneFundingExecution(execution), nil
}

func (store *MemoryFundingExecutionStore) PersistFundingExecution(_ context.Context, execution FundingExecution, expectedVersion int64, kind string) (FundingExecution, error) {
	store.mu.Lock()
	defer store.mu.Unlock()
	if kind == "" || errIfInvalidFundingExecutionWithAdmission(execution, store.admission) != nil {
		return FundingExecution{}, ErrWorkRevisionConflict
	}
	key := fundingExecutionKey(execution.PrincipalID, execution.Mode, execution.LineageID)
	current, ok := store.executions[key]
	if !ok {
		return FundingExecution{}, ErrWorkRevisionConflict
	}
	if current.Version != expectedVersion {
		return current, fmt.Errorf("Funding execution version conflict")
	}
	execution.Version = expectedVersion + 1
	store.executions[key] = cloneFundingExecution(execution)
	store.transitions[key] = append(store.transitions[key], FundingExecutionTransition{ExecutionID: execution.ExecutionID, Version: execution.Version, From: current.State, To: execution.State, Kind: kind, OccurredAt: execution.UpdatedAt})
	return cloneFundingExecution(execution), nil
}

func validateFundingExecution(execution FundingExecution) error {
	return validateFundingExecutionWithAdmission(execution, fundingpolicy.DefaultAdmission())
}

func validateFundingExecutionWithAdmission(execution FundingExecution, admission fundingpolicy.Admission) error {
	return errIfInvalidFundingExecutionWithAdmission(execution, admission)
}

func errIfInvalidFundingExecutionWithAdmission(execution FundingExecution, admission fundingpolicy.Admission) error {
	if execution.ExecutionID == "" || execution.PrincipalID == "" || (execution.Mode != orders.ModeTest && execution.Mode != orders.ModeLive) || (execution.Mode == orders.ModeLive && !admission.LiveEnabled()) || execution.LineageID == "" || execution.ParentCommandID == "" || execution.PlanKey == "" || execution.PreviewHash == "" || execution.IntentKind == "" || execution.State == "" || execution.CreatedAt.IsZero() || execution.UpdatedAt.IsZero() {
		return ErrWorkRevisionConflict
	}
	return nil
}

func fundingExecutionKey(principal string, mode orders.Mode, lineage string) string {
	return principal + "\x00" + string(mode) + "\x00" + lineage
}

func cloneFundingExecution(execution FundingExecution) FundingExecution {
	execution.Legs = append([]FundingExecutionLeg(nil), execution.Legs...)
	return execution
}
