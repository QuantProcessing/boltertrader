package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/domain/execution"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

var (
	ErrExecutionNotFound        = errors.New("execution not found")
	ErrPreviewConflict          = errors.New("execution preview conflicts with an existing immutable preview")
	ErrIdempotencyConflict      = errors.New("idempotency key conflicts with a different request hash")
	ErrExecutionVersionConflict = errors.New("execution version conflict")
	ErrExecutionScopeConflict   = errors.New("execution scope conflicts with request scope")
	ErrExecutionLegConflict     = errors.New("execution leg conflicts with existing immutable leg")
	ErrExecutionFillConflict    = errors.New("execution fill conflicts with existing fill")
	ErrExecutionValidation      = errors.New("execution validation failed")
)

type ExecutionStore struct {
	pool   *pgxpool.Pool
	bootID string
}

func NewExecutionStore(pool *pgxpool.Pool, bootID string) *ExecutionStore {
	return &ExecutionStore{pool: pool, bootID: strings.TrimSpace(bootID)}
}

type StoredPreview struct {
	Preview     execution.PreviewResult
	RequestHash string
	Expired     bool
}

type ExecutionReservation struct {
	Execution    execution.Execution
	Replayed     bool
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
	Scope           readmodel.Scope
	Execution       execution.Execution
	ExpectedVersion int64
	Kind            string
	RequestID       string
	Payload         json.RawMessage
	OccurredAt      time.Time
}

type PersistedTransitionEvents struct {
	Execution readmodel.Event
	Audit     readmodel.Event
}

func (store *ExecutionStore) SavePreview(ctx context.Context, preview execution.PreviewResult, requestHash string) error {
	if err := validateScope(readmodel.Scope{PrincipalID: preview.PrincipalID, Mode: preview.Mode}); err != nil {
		return err
	}
	if strings.TrimSpace(preview.PreviewID) == "" || strings.TrimSpace(preview.SnapshotID) == "" ||
		strings.TrimSpace(requestHash) == "" || preview.ExpiresAt.IsZero() || preview.PreviewedAt.IsZero() {
		return fmt.Errorf("invalid execution preview")
	}
	payload, err := json.Marshal(preview)
	if err != nil {
		return err
	}
	var existingPayload json.RawMessage
	var existingHash string
	err = store.pool.QueryRow(ctx, `
		select payload, request_hash
		from execution_previews
		where principal_id=$1 and mode=$2 and id=$3
	`, preview.PrincipalID, preview.Mode, preview.PreviewID).Scan(&existingPayload, &existingHash)
	if err == nil {
		if existingHash == requestHash && jsonSemanticallyEqual(existingPayload, payload) {
			return nil
		}
		return ErrPreviewConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return err
	}
	_, err = store.pool.Exec(ctx, `
		insert into execution_previews(
			id, snapshot_id, principal_id, mode, opportunity_id, operation,
			payload, request_hash, can_submit, expires_at, created_at
		) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)
	`, preview.PreviewID, preview.SnapshotID, preview.PrincipalID, preview.Mode,
		nullableString(preview.OpportunityID), string(preview.Operation), payload, requestHash,
		preview.CanSubmit, preview.ExpiresAt.UTC(), preview.PreviewedAt.UTC())
	if isUniqueViolation(err) {
		err = store.pool.QueryRow(ctx, `
			select payload, request_hash
			from execution_previews
			where principal_id=$1 and mode=$2 and id=$3
		`, preview.PrincipalID, preview.Mode, preview.PreviewID).Scan(&existingPayload, &existingHash)
		if err == nil && existingHash == requestHash && jsonSemanticallyEqual(existingPayload, payload) {
			return nil
		}
		if err != nil {
			return err
		}
		return ErrPreviewConflict
	}
	return err
}

func (store *ExecutionStore) GetPreview(ctx context.Context, scope readmodel.Scope, id string, now time.Time) (StoredPreview, error) {
	if err := validateScope(scope); err != nil {
		return StoredPreview{}, err
	}
	var payload json.RawMessage
	var requestHash string
	var expiresAt time.Time
	err := store.pool.QueryRow(ctx, `
		select payload, request_hash, expires_at
		from execution_previews
		where principal_id=$1 and mode=$2 and id=$3
	`, scope.PrincipalID, scope.Mode, id).Scan(&payload, &requestHash, &expiresAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredPreview{}, ErrExecutionNotFound
	}
	if err != nil {
		return StoredPreview{}, err
	}
	var preview execution.PreviewResult
	if err := json.Unmarshal(payload, &preview); err != nil {
		return StoredPreview{}, err
	}
	return StoredPreview{
		Preview: preview, RequestHash: requestHash,
		Expired: !now.IsZero() && !now.UTC().Before(expiresAt.UTC()),
	}, nil
}

func (store *ExecutionStore) ReserveExecution(
	ctx context.Context,
	scope readmodel.Scope,
	method, path, key, requestHash string,
	snapshot execution.Execution,
) (ExecutionReservation, error) {
	if err := validateIdempotencyIdentity(scope, method, path, key, requestHash); err != nil {
		return ExecutionReservation{}, err
	}
	if err := validateExecutionScope(scope, snapshot); err != nil {
		return ExecutionReservation{}, err
	}
	if err := validateExecutionPersistence(snapshot); err != nil {
		return ExecutionReservation{}, err
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return ExecutionReservation{}, err
	}
	defer tx.Rollback(ctx)

	id := strings.Join([]string{scope.PrincipalID, scope.Mode, method, path, key}, "\x1f")
	tag, err := tx.Exec(ctx, `
		insert into idempotency_records(
			id, principal_id, mode, method, path, key, request_hash, execution_id, state, created_at, updated_at
		) values($1,$2,$3,$4,$5,$6,$7,$8,'in_progress',now(),now())
		on conflict(principal_id, mode, method, path, key) do nothing
	`, id, scope.PrincipalID, scope.Mode, method, path, key, requestHash, snapshot.ID)
	if err != nil {
		return ExecutionReservation{}, err
	}
	if tag.RowsAffected() == 1 {
		if err := insertExecutionSnapshot(ctx, tx, scope, key, requestHash, snapshot, 1); err != nil {
			return ExecutionReservation{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return ExecutionReservation{}, err
		}
		return ExecutionReservation{Execution: snapshot}, nil
	}
	record, err := scanIdempotency(ctx, tx, scope, method, path, key)
	if err != nil {
		return ExecutionReservation{}, err
	}
	if record.RequestHash != requestHash {
		return ExecutionReservation{}, ErrIdempotencyConflict
	}
	stored, err := getExecutionTx(ctx, tx, scope, record.ExecutionID)
	if err != nil {
		return ExecutionReservation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return ExecutionReservation{}, err
	}
	return ExecutionReservation{
		Execution: stored.Execution, Replayed: true,
		ResponseCode: record.ResponseCode, ResponseBody: record.ResponseBody,
	}, nil
}

func (store *ExecutionStore) CompleteIdempotency(ctx context.Context, scope readmodel.Scope, method, path, key string, code int, body json.RawMessage) error {
	if err := validateScope(scope); err != nil {
		return err
	}
	if !json.Valid(body) || code < 100 {
		return fmt.Errorf("invalid idempotency response")
	}
	tag, err := store.pool.Exec(ctx, `
		update idempotency_records
		set state='completed', response_code=$6, response_body=$7, updated_at=now()
		where principal_id=$1 and mode=$2 and method=$3 and path=$4 and key=$5
		  and state <> 'completed'
	`, scope.PrincipalID, scope.Mode, method, path, key, code, body)
	if err != nil {
		return err
	}
	if tag.RowsAffected() == 1 {
		return nil
	}
	var exists bool
	if err := store.pool.QueryRow(ctx, `
		select exists(select 1 from idempotency_records where principal_id=$1 and mode=$2 and method=$3 and path=$4 and key=$5)
	`, scope.PrincipalID, scope.Mode, method, path, key).Scan(&exists); err != nil {
		return err
	}
	if !exists {
		return ErrExecutionNotFound
	}
	return nil
}

func (store *ExecutionStore) CompleteLinkedIdempotency(ctx context.Context, scope readmodel.Scope, executionID string, code int, body json.RawMessage) error {
	if err := validateScope(scope); err != nil {
		return err
	}
	if strings.TrimSpace(executionID) == "" || !json.Valid(body) || code < 100 {
		return fmt.Errorf("invalid idempotency response")
	}
	_, err := store.pool.Exec(ctx, `
		update idempotency_records
		set state='completed', response_code=$4, response_body=$5, updated_at=now()
		where principal_id=$1 and mode=$2 and execution_id=$3
		  and state <> 'completed'
	`, scope.PrincipalID, scope.Mode, executionID, code, body)
	return err
}

func (store *ExecutionStore) GetIdempotency(ctx context.Context, scope readmodel.Scope, method, path, key string) (IdempotencyRecord, error) {
	if err := validateScope(scope); err != nil {
		return IdempotencyRecord{}, err
	}
	return scanIdempotency(ctx, store.pool, scope, method, path, key)
}

func (store *ExecutionStore) PersistTransition(ctx context.Context, input PersistTransitionInput) (PersistedTransitionEvents, error) {
	if err := validateScope(input.Scope); err != nil {
		return PersistedTransitionEvents{}, err
	}
	if strings.TrimSpace(input.Execution.ID) == "" || strings.TrimSpace(input.Kind) == "" ||
		strings.TrimSpace(input.RequestID) == "" || input.ExpectedVersion < 1 ||
		(input.Payload != nil && !json.Valid(input.Payload)) {
		return PersistedTransitionEvents{}, fmt.Errorf("invalid execution transition")
	}
	if err := validateExecutionScope(input.Scope, input.Execution); err != nil {
		return PersistedTransitionEvents{}, err
	}
	if err := validateExecutionPersistence(input.Execution); err != nil {
		return PersistedTransitionEvents{}, err
	}
	if input.Payload == nil {
		input.Payload = json.RawMessage(`{}`)
	}
	enrichedPayload, err := enrichExecutionPayload(input.Payload, input.Scope, input.Execution)
	if err != nil {
		return PersistedTransitionEvents{}, err
	}
	if input.OccurredAt.IsZero() {
		input.OccurredAt = input.Execution.UpdatedAt
	}
	if input.OccurredAt.IsZero() {
		input.OccurredAt = time.Now()
	}
	input.OccurredAt = input.OccurredAt.UTC()

	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return PersistedTransitionEvents{}, err
	}
	defer tx.Rollback(ctx)

	stored, err := getExecutionForUpdate(ctx, tx, input.Scope, input.Execution.ID)
	if err != nil {
		return PersistedTransitionEvents{}, err
	}
	if stored.Version != input.ExpectedVersion {
		return PersistedTransitionEvents{}, ErrExecutionVersionConflict
	}
	payload, err := json.Marshal(input.Execution)
	if err != nil {
		return PersistedTransitionEvents{}, err
	}
	completedAt := nullableTime(time.Time{})
	if isTerminalExecution(input.Execution.State, input.Execution.ManualIntervention) {
		completedAt = input.OccurredAt
	}
	_, err = tx.Exec(ctx, `
		update executions set
			status=$4, payload=$5, operation_kind=$6, opportunity_id=$7, position_id=$8,
			parent_execution_id=$9, lineage_id=$10, reconciliation_state=$11,
			manual_intervention=$12, version=version+1, updated_at=$13, completed_at=$14
		where principal_id=$1 and mode=$2 and id=$3
	`, input.Scope.PrincipalID, input.Scope.Mode, input.Execution.ID,
		string(input.Execution.State), payload, string(input.Execution.Operation),
		nullableString(input.Execution.OpportunityID), nullableString(input.Execution.PositionID),
		nullableString(input.Execution.ParentExecutionID), nullableString(input.Execution.LineageID),
		nullableStringOr(input.Execution.ReconciliationState, "pending"), input.Execution.ManualIntervention,
		input.OccurredAt, completedAt)
	if err != nil {
		return PersistedTransitionEvents{}, err
	}
	if err := upsertExecutionLegs(ctx, tx, input.Execution); err != nil {
		return PersistedTransitionEvents{}, err
	}
	if err := upsertExecutionFills(ctx, tx, input.Execution); err != nil {
		return PersistedTransitionEvents{}, err
	}
	if _, err := tx.Exec(ctx, `
		insert into execution_transitions(
			execution_id, principal_id, mode, from_status, to_status, kind, request_id, payload, occurred_at
		 ) values($1,$2,$3,$4,$5,$6,$7,$8,$9)
	`, input.Execution.ID, input.Scope.PrincipalID, input.Scope.Mode,
		stored.Status, string(input.Execution.State), input.Kind, input.RequestID, enrichedPayload, input.OccurredAt); err != nil {
		return PersistedTransitionEvents{}, err
	}
	var auditID int64
	if err := tx.QueryRow(ctx, `
		insert into audit_events(request_id, principal_id, mode, action, outcome, execution_id, target_id, payload, created_at)
		values($1,$2,$3,$4,$5,$6,$6,$7,$8)
		returning id
	`, input.RequestID, input.Scope.PrincipalID, input.Scope.Mode,
		"execution."+input.Kind, string(input.Execution.State), input.Execution.ID, enrichedPayload, input.OccurredAt).Scan(&auditID); err != nil {
		return PersistedTransitionEvents{}, err
	}
	var executionEvent, auditEvent readmodel.Event
	if store.bootID != "" {
		executionEvent, err = appendEventTx(ctx, tx, readmodel.Event{
			BootID: store.bootID, PrincipalID: input.Scope.PrincipalID, Mode: input.Scope.Mode,
			Kind: readmodel.ExecutionUpsert, Scope: []string{"executions"}, EntityID: input.Execution.ID,
			Payload: payload, OccurredAt: input.OccurredAt,
		})
		if err != nil {
			return PersistedTransitionEvents{}, err
		}
		auditPayload, marshalErr := json.Marshal(map[string]any{
			"auditId":     fmt.Sprintf("%d", auditID),
			"time":        input.OccurredAt.Format(time.RFC3339Nano),
			"principalId": input.Scope.PrincipalID,
			"kind":        "execution." + input.Kind,
			"targetId":    input.Execution.ID,
			"outcome":     string(input.Execution.State),
			"requestId":   input.RequestID,
			"executionId": input.Execution.ID,
			"mode":        input.Scope.Mode,
			"legs":        executionRouteLegs(input.Execution),
		})
		if marshalErr != nil {
			return PersistedTransitionEvents{}, marshalErr
		}
		auditEvent, err = appendEventTx(ctx, tx, readmodel.Event{
			BootID: store.bootID, PrincipalID: input.Scope.PrincipalID, Mode: input.Scope.Mode,
			Kind: readmodel.AuditAppend, Scope: []string{"audit", "executions"}, EntityID: input.Execution.ID,
			Payload: auditPayload, OccurredAt: input.OccurredAt,
		})
		if err != nil {
			return PersistedTransitionEvents{}, err
		}
	}
	if err := pruneReadModelEventsTx(ctx, tx); err != nil {
		return PersistedTransitionEvents{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return PersistedTransitionEvents{}, err
	}
	return PersistedTransitionEvents{Execution: executionEvent, Audit: auditEvent}, nil
}

type StoredExecution struct {
	Execution execution.Execution
	Payload   json.RawMessage
	Version   int64
	Status    string
	UpdatedAt time.Time
}

func (store *ExecutionStore) GetExecution(ctx context.Context, scope readmodel.Scope, id string) (StoredExecution, error) {
	if err := validateScope(scope); err != nil {
		return StoredExecution{}, err
	}
	return getExecutionTx(ctx, store.pool, scope, id)
}

func (store *ExecutionStore) ListNonTerminal(ctx context.Context, scope readmodel.Scope) ([]StoredExecution, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	rows, err := store.pool.Query(ctx, `
		select id, status, payload, version, updated_at
		from executions
		where principal_id=$1 and mode=$2
		  and (manual_intervention or status = any($3))
		order by updated_at asc, id asc
	`, scope.PrincipalID, scope.Mode, []string{
		string(execution.StateSubmitting),
		string(execution.StateLeg1Working),
		string(execution.StateLeg2Working),
		string(execution.StatePartiallyHedged),
		string(execution.StateCompensating),
		string(execution.StateUncertain),
		string(execution.StateManualInterventionRequired),
	})
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []StoredExecution
	for rows.Next() {
		stored, err := scanStoredExecution(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, stored)
	}
	return result, rows.Err()
}

func (store *ExecutionStore) ListTerminalWithIncompleteIdempotency(ctx context.Context, scope readmodel.Scope) ([]StoredExecution, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	rows, err := store.pool.Query(ctx, `
		select distinct on (execution.id) execution.id, execution.status, execution.payload, execution.version, execution.updated_at
		from executions execution
		join idempotency_records record
		  on record.principal_id=execution.principal_id
		 and record.mode=execution.mode
		 and record.execution_id=execution.id
		 and record.state='in_progress'
		where execution.principal_id=$1 and execution.mode=$2
		  and not execution.manual_intervention
		  and execution.status = any($3)
		order by execution.id, execution.updated_at asc
	`, scope.PrincipalID, scope.Mode, []string{
		string(execution.StateHedged),
		string(execution.StateClosed),
		string(execution.StateFailed),
	})
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []StoredExecution
	for rows.Next() {
		stored, err := scanStoredExecution(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, stored)
	}
	return result, rows.Err()
}

func (store *ExecutionStore) ListExecutions(
	ctx context.Context,
	scope readmodel.Scope,
) ([]execution.Execution, error) {
	if err := validateScope(scope); err != nil {
		return nil, err
	}
	rows, err := store.pool.Query(ctx, `
		select id, status, payload, version, updated_at
		from executions
		where principal_id=$1 and mode=$2
		order by updated_at asc, id asc
	`, scope.PrincipalID, scope.Mode)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []execution.Execution
	for rows.Next() {
		stored, scanErr := scanStoredExecution(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, stored.Execution.Snapshot())
	}
	return result, rows.Err()
}

type executionScanner interface {
	Scan(...any) error
}

func scanStoredExecution(row executionScanner) (StoredExecution, error) {
	var stored StoredExecution
	if err := row.Scan(&stored.Execution.ID, &stored.Status, &stored.Payload, &stored.Version, &stored.UpdatedAt); err != nil {
		return StoredExecution{}, err
	}
	restored, err := execution.RestoreExecution(stored.Payload)
	if err != nil {
		return StoredExecution{}, err
	}
	stored.Execution = restored.Snapshot()
	if stored.UpdatedAt.After(stored.Execution.UpdatedAt) {
		stored.Execution.UpdatedAt = stored.UpdatedAt.UTC()
	}
	return stored, nil
}

type executionQueryer interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func getExecutionTx(ctx context.Context, q executionQueryer, scope readmodel.Scope, id string) (StoredExecution, error) {
	row := q.QueryRow(ctx, `
		select id, status, payload, version, updated_at
		from executions
		where principal_id=$1 and mode=$2 and id=$3
	`, scope.PrincipalID, scope.Mode, id)
	stored, err := scanStoredExecution(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredExecution{}, ErrExecutionNotFound
	}
	return stored, err
}

func getExecutionForUpdate(ctx context.Context, q executionQueryer, scope readmodel.Scope, id string) (StoredExecution, error) {
	row := q.QueryRow(ctx, `
		select id, status, payload, version, updated_at
		from executions
		where principal_id=$1 and mode=$2 and id=$3
		for update
	`, scope.PrincipalID, scope.Mode, id)
	stored, err := scanStoredExecution(row)
	if errors.Is(err, pgx.ErrNoRows) {
		return StoredExecution{}, ErrExecutionNotFound
	}
	return stored, err
}

func insertExecutionSnapshot(ctx context.Context, tx pgx.Tx, scope readmodel.Scope, key, requestHash string, snapshot execution.Execution, version int64) error {
	payload, err := json.Marshal(snapshot)
	if err != nil {
		return err
	}
	_, err = tx.Exec(ctx, `
		insert into executions(
			id, principal_id, mode, operation_kind, opportunity_id, position_id,
			parent_execution_id, lineage_id, idempotency_key, request_hash, status,
			reconciliation_state, manual_intervention, payload, version, created_at,
			updated_at, completed_at
		) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18)
	`, snapshot.ID, scope.PrincipalID, scope.Mode, string(snapshot.Operation),
		nullableString(snapshot.OpportunityID), nullableString(snapshot.PositionID),
		nullableString(snapshot.ParentExecutionID), nullableString(snapshot.LineageID),
		key, requestHash, string(snapshot.State), nullableStringOr(snapshot.ReconciliationState, "pending"),
		snapshot.ManualIntervention, payload, version, snapshot.CreatedAt.UTC(), snapshot.UpdatedAt.UTC(),
		nullableTime(time.Time{}))
	return err
}

func upsertExecutionLegs(ctx context.Context, tx pgx.Tx, snapshot execution.Execution) error {
	for _, leg := range snapshot.Legs {
		safeError, err := json.Marshal(leg.LastError)
		if err != nil {
			return err
		}
		tag, err := tx.Exec(ctx, `
			insert into execution_legs(
				leg_id, execution_id, role, account_id, venue, instrument_id, venue_environment, side,
				requested_quantity, filled_quantity, status, gateway_id, command_id,
				gateway_order_id, reduce_only, safe_error, updated_at
			) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
			on conflict(leg_id) do update set
				status=excluded.status,
				filled_quantity=excluded.filled_quantity,
				gateway_id=excluded.gateway_id,
				command_id=excluded.command_id,
				gateway_order_id=excluded.gateway_order_id,
				safe_error=excluded.safe_error,
				updated_at=excluded.updated_at
			where execution_legs.execution_id=excluded.execution_id
			  and execution_legs.role=excluded.role
			  and execution_legs.account_id is not distinct from excluded.account_id
			  and execution_legs.venue=excluded.venue
			  and execution_legs.instrument_id is not distinct from excluded.instrument_id
			  and execution_legs.venue_environment=excluded.venue_environment
			  and execution_legs.side=excluded.side
			  and execution_legs.requested_quantity=excluded.requested_quantity
			  and execution_legs.reduce_only=excluded.reduce_only
		`, dbLegID(snapshot.ID, leg.LegID), snapshot.ID, string(leg.Role),
			leg.AccountID, leg.Venue, leg.InstrumentID, leg.VenueEnvironment, string(leg.Side),
			leg.RequestedQuantity, leg.Filled, string(leg.Status), nullableString(leg.GatewayID),
			nullableString(leg.OrderID), nullableString(leg.GatewayOrderID), leg.ReduceOnly,
			safeError, leg.UpdatedAt.UTC())
		if err != nil {
			if isUniqueViolation(err) {
				return ErrExecutionLegConflict
			}
			return err
		}
		if tag.RowsAffected() != 1 {
			return ErrExecutionLegConflict
		}
	}
	return nil
}

func upsertExecutionFills(ctx context.Context, tx pgx.Tx, snapshot execution.Execution) error {
	for _, fill := range snapshot.Fills {
		body, err := fillPayloadWithRoute(snapshot, fill)
		if err != nil {
			return err
		}
		if err := verifyExistingFillReplay(ctx, tx, snapshot.ID, fill, body); err != nil {
			return err
		}
		_, err = tx.Exec(ctx, `
			insert into execution_fills(
				fill_id, execution_id, leg_id, gateway_fill_id, quantity, price,
				fee, fee_currency, occurred_at, payload
			) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
			on conflict do nothing
		`, dbFillID(snapshot.ID, fill.FillID), snapshot.ID, dbLegID(snapshot.ID, fill.LegID),
			nullableString(fill.GatewayFillID), fill.Quantity, fill.Price,
			fill.Fee.Amount, nullableString(fill.Fee.Currency), fill.OccurredAt.UTC(), body)
		if err != nil {
			if isUniqueViolation(err) {
				return ErrExecutionFillConflict
			}
			return err
		}
	}
	return nil
}

func dbLegID(executionID, legID string) string {
	return executionID + ":" + legID
}

func dbFillID(executionID, fillID string) string {
	return executionID + ":" + fillID
}

func enrichExecutionPayload(payload json.RawMessage, scope readmodel.Scope, snapshot execution.Execution) (json.RawMessage, error) {
	var object map[string]any
	if len(payload) == 0 {
		object = map[string]any{}
	} else if err := json.Unmarshal(payload, &object); err != nil {
		return nil, err
	}
	if object == nil {
		return nil, ErrExecutionValidation
	}
	object["mode"] = scope.Mode
	object["legs"] = executionRouteLegs(snapshot)
	return json.Marshal(object)
}

func fillPayloadWithRoute(snapshot execution.Execution, fill execution.Fill) (json.RawMessage, error) {
	var object map[string]any
	body, err := json.Marshal(fill)
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(body, &object); err != nil {
		return nil, err
	}
	route, ok := executionRouteForLeg(snapshot, fill.LegID)
	if !ok {
		return nil, ErrExecutionValidation
	}
	object["route"] = route
	return json.Marshal(object)
}

func executionRouteForLeg(snapshot execution.Execution, legID string) (map[string]string, bool) {
	for _, leg := range snapshot.Legs {
		if leg.LegID == legID {
			return map[string]string{
				"role":             string(leg.Role),
				"accountId":        leg.AccountID,
				"venue":            leg.Venue,
				"instrumentId":     leg.InstrumentID,
				"venueEnvironment": leg.VenueEnvironment,
			}, true
		}
	}
	return nil, false
}

func executionRouteLegs(snapshot execution.Execution) []map[string]string {
	byRole := make(map[execution.LegRole]map[string]string, len(snapshot.PlannedLegs)+len(snapshot.Legs))
	roles := make([]execution.LegRole, 0, len(snapshot.PlannedLegs)+len(snapshot.Legs))
	addRole := func(role execution.LegRole) {
		for _, existing := range roles {
			if existing == role {
				return
			}
		}
		roles = append(roles, role)
	}
	for _, leg := range snapshot.PlannedLegs {
		byRole[leg.Role] = map[string]string{
			"role":             string(leg.Role),
			"accountId":        leg.AccountID,
			"venue":            leg.Venue,
			"instrumentId":     leg.InstrumentID,
			"venueEnvironment": leg.VenueEnvironment,
		}
		addRole(leg.Role)
	}
	for _, leg := range snapshot.Legs {
		byRole[leg.Role] = map[string]string{
			"role":             string(leg.Role),
			"accountId":        leg.AccountID,
			"venue":            leg.Venue,
			"instrumentId":     leg.InstrumentID,
			"venueEnvironment": leg.VenueEnvironment,
		}
		addRole(leg.Role)
	}
	sort.SliceStable(roles, func(i, j int) bool {
		return executionLegRoleRank(roles[i]) < executionLegRoleRank(roles[j])
	})
	routes := make([]map[string]string, 0, len(roles))
	for _, role := range roles {
		routes = append(routes, byRole[role])
	}
	return routes
}

func executionLegRoleRank(role execution.LegRole) int {
	switch role {
	case execution.LegRoleLong:
		return 0
	case execution.LegRoleShort:
		return 1
	default:
		return 2
	}
}

func scanIdempotency(ctx context.Context, q executionQueryer, scope readmodel.Scope, method, path, key string) (IdempotencyRecord, error) {
	var record IdempotencyRecord
	var responseCode *int
	var responseBody []byte
	err := q.QueryRow(ctx, `
		select coalesce(execution_id, ''), request_hash, state, response_code, response_body
		from idempotency_records
		where principal_id=$1 and mode=$2 and method=$3 and path=$4 and key=$5
	`, scope.PrincipalID, scope.Mode, method, path, key).
		Scan(&record.ExecutionID, &record.RequestHash, &record.State, &responseCode, &responseBody)
	if errors.Is(err, pgx.ErrNoRows) {
		return IdempotencyRecord{}, ErrExecutionNotFound
	}
	if err != nil {
		return IdempotencyRecord{}, err
	}
	if responseCode != nil {
		record.ResponseCode = *responseCode
	}
	if responseBody != nil {
		record.ResponseBody = append(json.RawMessage(nil), responseBody...)
	}
	return record, nil
}

func verifyExistingFillReplay(ctx context.Context, tx pgx.Tx, executionID string, fill execution.Fill, payload json.RawMessage) error {
	rows, err := tx.Query(ctx, `
		select fill_id, execution_id, leg_id, coalesce(gateway_fill_id, ''), quantity::text,
			price::text, fee::text, coalesce(fee_currency, ''), occurred_at, payload
		from execution_fills
		where execution_id=$1
		  and (fill_id=$2 or (gateway_fill_id is not null and gateway_fill_id <> '' and gateway_fill_id=$3))
	`, executionID, dbFillID(executionID, fill.FillID), fill.GatewayFillID)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var existing struct {
			FillID        string
			ExecutionID   string
			LegID         string
			GatewayFillID string
			Quantity      string
			Price         string
			Fee           string
			FeeCurrency   string
			OccurredAt    time.Time
			Payload       json.RawMessage
		}
		if err := rows.Scan(
			&existing.FillID, &existing.ExecutionID, &existing.LegID, &existing.GatewayFillID,
			&existing.Quantity, &existing.Price, &existing.Fee, &existing.FeeCurrency,
			&existing.OccurredAt, &existing.Payload,
		); err != nil {
			return err
		}
		if existing.FillID != dbFillID(executionID, fill.FillID) ||
			existing.ExecutionID != executionID ||
			existing.LegID != dbLegID(executionID, fill.LegID) ||
			existing.GatewayFillID != fill.GatewayFillID ||
			existing.Quantity != fill.Quantity.String() ||
			existing.Price != fill.Price.String() ||
			existing.Fee != fill.Fee.Amount.String() ||
			existing.FeeCurrency != fill.Fee.Currency ||
			!postgresTimeEqual(existing.OccurredAt, fill.OccurredAt) ||
			!jsonSemanticallyEqual(existing.Payload, payload) {
			return ErrExecutionFillConflict
		}
	}
	return rows.Err()
}

func validateIdempotencyIdentity(scope readmodel.Scope, method, path, key, requestHash string) error {
	if err := validateScope(scope); err != nil {
		return err
	}
	for name, value := range map[string]string{
		"method": method, "path": path, "key": key, "request hash": requestHash,
	} {
		if strings.TrimSpace(value) == "" {
			return fmt.Errorf("invalid idempotency %s", name)
		}
	}
	return nil
}

func validateScope(scope readmodel.Scope) error {
	if strings.TrimSpace(scope.PrincipalID) == "" || !isProductMode(scope.Mode) {
		return fmt.Errorf("invalid scope")
	}
	return nil
}

func validateExecutionScope(scope readmodel.Scope, snapshot execution.Execution) error {
	if strings.TrimSpace(snapshot.PrincipalID) != scope.PrincipalID ||
		strings.TrimSpace(snapshot.Mode) != scope.Mode {
		return ErrExecutionScopeConflict
	}
	return nil
}

func validateExecutionPersistence(snapshot execution.Execution) error {
	for _, leg := range snapshot.PlannedLegs {
		if strings.TrimSpace(leg.AccountID) == "" ||
			strings.TrimSpace(leg.Venue) == "" ||
			strings.TrimSpace(leg.InstrumentID) == "" ||
			!validVenueEnvironmentForMode(snapshot.Mode, leg.VenueEnvironment) {
			return ErrExecutionValidation
		}
	}
	for _, leg := range snapshot.Legs {
		if strings.TrimSpace(leg.LegID) == "" ||
			strings.TrimSpace(leg.AccountID) == "" ||
			strings.TrimSpace(leg.Venue) == "" ||
			strings.TrimSpace(leg.InstrumentID) == "" ||
			!validVenueEnvironmentForMode(snapshot.Mode, leg.VenueEnvironment) {
			return ErrExecutionValidation
		}
	}
	for _, fill := range snapshot.Fills {
		if strings.TrimSpace(fill.FillID) == "" {
			return ErrExecutionValidation
		}
		if _, ok := executionRouteForLeg(snapshot, fill.LegID); !ok {
			return ErrExecutionValidation
		}
	}
	return nil
}

func nullableStringOr(value string, fallback string) string {
	if strings.TrimSpace(value) == "" {
		return fallback
	}
	return value
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

func isTerminalExecution(state execution.State, manual bool) bool {
	if manual {
		return false
	}
	return state == execution.StateHedged ||
		state == execution.StateClosed ||
		state == execution.StateFailed
}

func jsonSemanticallyEqual(left, right []byte) bool {
	var leftValue any
	var rightValue any
	if err := json.Unmarshal(left, &leftValue); err != nil {
		return false
	}
	if err := json.Unmarshal(right, &rightValue); err != nil {
		return false
	}
	return reflect.DeepEqual(leftValue, rightValue)
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func postgresTimeEqual(left, right time.Time) bool {
	return left.UTC().Equal(right.UTC().Round(time.Microsecond))
}
