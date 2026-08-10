package executionruntime

import (
	"context"
	"encoding/json"
	"errors"
	"strconv"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/executionservice"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/QuantProcessing/funding-trader/backend/internal/store/postgres"
)

type postgresExecutionStore interface {
	SavePreview(context.Context, execution.PreviewResult, string) error
	GetPreview(context.Context, readmodel.Scope, string, time.Time) (postgres.StoredPreview, error)
	GetIdempotency(context.Context, readmodel.Scope, string, string, string) (postgres.IdempotencyRecord, error)
	ReserveExecution(context.Context, readmodel.Scope, string, string, string, string, execution.Execution) (postgres.ExecutionReservation, error)
	CompleteIdempotency(context.Context, readmodel.Scope, string, string, string, int, json.RawMessage) error
	CompleteLinkedIdempotency(context.Context, readmodel.Scope, string, int, json.RawMessage) error
	GetExecution(context.Context, readmodel.Scope, string) (postgres.StoredExecution, error)
	ListNonTerminal(context.Context, readmodel.Scope) ([]postgres.StoredExecution, error)
	ListTerminalWithIncompleteIdempotency(context.Context, readmodel.Scope) ([]postgres.StoredExecution, error)
	PersistTransition(context.Context, postgres.PersistTransitionInput) (postgres.PersistedTransitionEvents, error)
}

type StoreAdapter struct {
	store postgresExecutionStore
}

func NewStoreAdapter(store postgresExecutionStore) *StoreAdapter {
	return &StoreAdapter{store: store}
}

func (adapter *StoreAdapter) SavePreview(ctx context.Context, preview execution.PreviewResult, requestHash string) error {
	return mapPostgresError(adapter.store.SavePreview(ctx, preview, requestHash))
}

func (adapter *StoreAdapter) GetPreview(ctx context.Context, scope executionservice.Scope, id string, now time.Time) (executionservice.StoredPreview, error) {
	stored, err := adapter.store.GetPreview(ctx, toReadScope(scope), id, now)
	if err != nil {
		return executionservice.StoredPreview{}, mapPostgresError(err)
	}
	return executionservice.StoredPreview{
		Preview: stored.Preview, RequestHash: stored.RequestHash, Expired: stored.Expired,
	}, nil
}

func (adapter *StoreAdapter) GetIdempotency(ctx context.Context, scope executionservice.Scope, method, path, key string) (executionservice.IdempotencyRecord, error) {
	record, err := adapter.store.GetIdempotency(ctx, toReadScope(scope), method, path, key)
	if err != nil {
		return executionservice.IdempotencyRecord{}, mapPostgresError(err)
	}
	return executionservice.IdempotencyRecord{
		ExecutionID: record.ExecutionID, RequestHash: record.RequestHash, State: record.State,
		ResponseCode: record.ResponseCode, ResponseBody: cloneJSON(record.ResponseBody),
	}, nil
}

func (adapter *StoreAdapter) ReserveExecution(ctx context.Context, scope executionservice.Scope, method, path, key, requestHash string, snapshot execution.Execution) (executionservice.IdempotencyReservation, error) {
	reservation, err := adapter.store.ReserveExecution(ctx, toReadScope(scope), method, path, key, requestHash, snapshot)
	if err != nil {
		return executionservice.IdempotencyReservation{}, mapPostgresError(err)
	}
	stored, err := adapter.store.GetExecution(ctx, toReadScope(scope), reservation.Execution.ID)
	if err != nil {
		return executionservice.IdempotencyReservation{}, mapPostgresError(err)
	}
	result := executionservice.IdempotencyReservation{
		Execution: reservation.Execution.Snapshot(), Version: stored.Version, Replayed: reservation.Replayed,
		ResponseCode: reservation.ResponseCode, ResponseBody: cloneJSON(reservation.ResponseBody),
	}
	if reservation.Replayed {
		record, err := adapter.store.GetIdempotency(ctx, toReadScope(scope), method, path, key)
		if err != nil {
			return executionservice.IdempotencyReservation{}, mapPostgresError(err)
		}
		result.State = record.State
		if result.ResponseCode == 0 {
			result.ResponseCode = record.ResponseCode
		}
		if len(result.ResponseBody) == 0 {
			result.ResponseBody = cloneJSON(record.ResponseBody)
		}
	} else {
		result.State = "in_progress"
	}
	return result, nil
}

func (adapter *StoreAdapter) CompleteIdempotency(ctx context.Context, scope executionservice.Scope, method, path, key string, code int, body json.RawMessage) error {
	return mapPostgresError(adapter.store.CompleteIdempotency(ctx, toReadScope(scope), method, path, key, code, body))
}

func (adapter *StoreAdapter) CompleteLinkedIdempotency(ctx context.Context, scope executionservice.Scope, executionID string, code int, body json.RawMessage) error {
	return mapPostgresError(adapter.store.CompleteLinkedIdempotency(ctx, toReadScope(scope), executionID, code, body))
}

func (adapter *StoreAdapter) GetExecution(ctx context.Context, scope executionservice.Scope, id string) (executionservice.StoredExecution, error) {
	stored, err := adapter.store.GetExecution(ctx, toReadScope(scope), id)
	if err != nil {
		return executionservice.StoredExecution{}, mapPostgresError(err)
	}
	return executionservice.StoredExecution{Execution: stored.Execution.Snapshot(), Version: stored.Version}, nil
}

func (adapter *StoreAdapter) ListNonTerminal(ctx context.Context, scope executionservice.Scope) ([]executionservice.StoredExecution, error) {
	stored, err := adapter.store.ListNonTerminal(ctx, toReadScope(scope))
	if err != nil {
		return nil, mapPostgresError(err)
	}
	result := make([]executionservice.StoredExecution, 0, len(stored))
	for _, item := range stored {
		result = append(result, executionservice.StoredExecution{Execution: item.Execution.Snapshot(), Version: item.Version})
	}
	return result, nil
}

func (adapter *StoreAdapter) ListTerminalWithIncompleteIdempotency(ctx context.Context, scope executionservice.Scope) ([]executionservice.StoredExecution, error) {
	stored, err := adapter.store.ListTerminalWithIncompleteIdempotency(ctx, toReadScope(scope))
	if err != nil {
		return nil, mapPostgresError(err)
	}
	result := make([]executionservice.StoredExecution, 0, len(stored))
	for _, item := range stored {
		result = append(result, executionservice.StoredExecution{Execution: item.Execution.Snapshot(), Version: item.Version})
	}
	return result, nil
}

func (adapter *StoreAdapter) PersistTransition(ctx context.Context, input executionservice.PersistTransitionInput) ([]executionservice.CommittedEvent, error) {
	events, err := adapter.store.PersistTransition(ctx, postgres.PersistTransitionInput{
		Scope:           toReadScope(input.Scope),
		Execution:       input.Execution.Snapshot(),
		ExpectedVersion: input.ExpectedVersion,
		Kind:            input.Kind,
		RequestID:       input.RequestID,
		Payload:         cloneJSON(input.Payload),
		OccurredAt:      input.OccurredAt,
	})
	if err != nil {
		return nil, mapPostgresError(err)
	}
	return committedEvents(events), nil
}

func toReadScope(scope executionservice.Scope) readmodel.Scope {
	return readmodel.Scope{PrincipalID: scope.PrincipalID, Mode: scope.Mode}
}

func committedEvents(events postgres.PersistedTransitionEvents) []executionservice.CommittedEvent {
	var result []executionservice.CommittedEvent
	for _, event := range []readmodel.Event{events.Execution, events.Audit} {
		if event.ID == 0 {
			continue
		}
		result = append(result, executionservice.CommittedEvent{
			ID: strconv.FormatInt(event.ID, 10), Kind: string(event.Kind), EntityID: event.EntityID,
			Payload: cloneJSON(event.Payload), Occurred: event.OccurredAt,
		})
	}
	return result
}

func mapPostgresError(err error) error {
	if err == nil {
		return nil
	}
	switch {
	case errors.Is(err, postgres.ErrExecutionNotFound):
		return executionservice.ErrNotFound
	case errors.Is(err, postgres.ErrPreviewConflict):
		return executionservice.ErrPreviewConflict
	case errors.Is(err, postgres.ErrIdempotencyConflict):
		return executionservice.ErrIdempotencyConflict
	case errors.Is(err, postgres.ErrExecutionVersionConflict):
		return executionservice.ErrExecutionInProgress
	case errors.Is(err, postgres.ErrExecutionScopeConflict):
		return executionservice.ErrPreviewMismatch
	default:
		return err
	}
}

func cloneJSON(body json.RawMessage) json.RawMessage {
	if body == nil {
		return nil
	}
	return append(json.RawMessage(nil), body...)
}
