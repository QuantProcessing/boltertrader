package postgres

import (
	"context"
	"errors"
	"sort"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	storepkg "github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/QuantProcessing/boltertrader/secretstore"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

func (postgresStore *Store) PreparePending(ctx context.Context, snapshot storepkg.DesiredSnapshot) (storepkg.PendingSnapshot, error) {
	digest, err := snapshot.CanonicalDigest()
	if err != nil {
		return storepkg.PendingSnapshot{}, err
	}
	if snapshot.RequestDigest != "" && snapshot.RequestDigest != digest {
		return storepkg.PendingSnapshot{}, storepkg.ErrOperationConflict
	}
	snapshot.RequestDigest = digest
	snapshot.Routes = sortedRoutes(snapshot.Routes)

	tx, err := postgresStore.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return storepkg.PendingSnapshot{}, redactErr("begin pending snapshot", err)
	}
	defer tx.Rollback(ctx)

	existing, err := loadSnapshotByOperation(ctx, tx, snapshot.OperationID)
	if err == nil {
		if existing.RequestDigest != digest {
			return storepkg.PendingSnapshot{}, storepkg.ErrOperationConflict
		}
		pending, err := loadPending(ctx, tx, existing)
		if err != nil {
			return storepkg.PendingSnapshot{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return storepkg.PendingSnapshot{}, redactErr("commit pending snapshot replay", err)
		}
		return pending, nil
	}
	if !errors.Is(err, storepkg.ErrNotFound) {
		return storepkg.PendingSnapshot{}, err
	}

	var pendingID *uuid.UUID
	err = tx.QueryRow(ctx, `SELECT pending_snapshot_id FROM gateway_account_runtime_heads WHERE account_id = $1 FOR UPDATE`, snapshot.AccountID).Scan(&pendingID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return storepkg.PendingSnapshot{}, redactErr("lock runtime head", err)
	}
	if pendingID != nil {
		return storepkg.PendingSnapshot{}, storepkg.ErrOperationConflict
	}
	createdAt := snapshot.CreatedAt
	if createdAt.IsZero() {
		createdAt = time.Now().UTC()
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO gateway_runtime_snapshots(
			snapshot_id, account_id, operation_id, request_digest, venue, native_environment,
			secret_ref, desired_state_revision, runtime_generation, credential_version, requested_state, created_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12)
	`, snapshot.SnapshotID, snapshot.AccountID, snapshot.OperationID.String(), digest, snapshot.Venue, snapshot.NativeEnvironment,
		snapshot.SecretRef.RawForTrustedBoundary(), snapshot.Fence.DesiredStateRevision, snapshot.Fence.RuntimeGeneration,
		snapshot.Fence.CredentialVersion, snapshot.RequestedState, createdAt)
	if err != nil {
		return storepkg.PendingSnapshot{}, redactErr("insert pending snapshot", err)
	}
	for _, route := range snapshot.Routes {
		if _, err := tx.Exec(ctx, `
			INSERT INTO gateway_runtime_snapshot_routes(snapshot_id, route_id, product, settlement_asset, route_profile_id, requested_state)
			VALUES ($1,$2,$3,$4,$5,$6)
		`, snapshot.SnapshotID, route.RouteID, route.Product, route.SettlementAsset, route.RouteProfileID, route.RequestedState); err != nil {
			return storepkg.PendingSnapshot{}, redactErr("insert pending snapshot route", err)
		}
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO gateway_desired_operations(operation_id, snapshot_id, request_digest, state)
		VALUES ($1,$2,$3,'prepared')
	`, snapshot.OperationID.String(), snapshot.SnapshotID, digest); err != nil {
		return storepkg.PendingSnapshot{}, redactErr("insert desired operation", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO gateway_account_runtime_heads(account_id, pending_snapshot_id)
		VALUES ($1,$2)
		ON CONFLICT (account_id) DO UPDATE SET pending_snapshot_id = EXCLUDED.pending_snapshot_id, updated_at = now()
	`, snapshot.AccountID, snapshot.SnapshotID); err != nil {
		return storepkg.PendingSnapshot{}, redactErr("set pending runtime head", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return storepkg.PendingSnapshot{}, redactErr("commit pending snapshot", err)
	}
	snapshot.CreatedAt = createdAt
	return storepkg.PendingSnapshot{DesiredSnapshot: snapshot, OperationState: storepkg.OperationPrepared}, nil
}

func (postgresStore *Store) RecordCandidateObservation(ctx context.Context, observation storepkg.CandidateObservation) error {
	if _, err := secretstore.ParseOperationID(observation.OperationID.String()); err != nil {
		return err
	}
	if observation.ObservationRevision == 0 || observation.RouteID.Validate() != nil {
		return storepkg.ErrCandidateNotReady
	}
	if observation.State != storepkg.CandidateBuilding && observation.State != storepkg.CandidateReady && observation.State != storepkg.CandidateFailed {
		return storepkg.ErrCandidateNotReady
	}
	if len(observation.ReasonCode) > 128 {
		return storepkg.ErrCandidateNotReady
	}
	_, err := postgresStore.pool.Exec(ctx, `
		INSERT INTO gateway_candidate_observations(operation_id, observation_revision, route_id, state, reason_code, observed_at)
		VALUES ($1,$2,$3,$4,NULLIF($5,''),COALESCE(NULLIF($6, '0001-01-01 00:00:00+00'::timestamptz), now()))
		ON CONFLICT (operation_id, observation_revision, route_id) DO UPDATE SET
			state = EXCLUDED.state, reason_code = EXCLUDED.reason_code, observed_at = EXCLUDED.observed_at
	`, observation.OperationID.String(), observation.ObservationRevision, observation.RouteID, observation.State, observation.ReasonCode, observation.ObservedAt)
	if err != nil {
		return redactErr("record candidate observation", err)
	}
	return nil
}

func (postgresStore *Store) MarkCandidateReady(ctx context.Context, operationID secretstore.OperationID) error {
	tag, err := postgresStore.pool.Exec(ctx, `
		UPDATE gateway_desired_operations SET state = 'candidate_ready', updated_at = now()
		WHERE operation_id = $1 AND state IN ('prepared','candidate_ready')
		  AND NOT EXISTS (
			SELECT 1 FROM gateway_candidate_observations
			WHERE operation_id = $1 AND state <> 'ready'
		  )
	`, operationID.String())
	if err != nil {
		return redactErr("mark candidate ready", err)
	}
	if tag.RowsAffected() == 0 {
		return storepkg.ErrCandidateNotReady
	}
	return nil
}

func (postgresStore *Store) PromotePending(ctx context.Context, request storepkg.PromotionRequest) (storepkg.PromotionResult, error) {
	if err := request.AccountID.Validate(); err != nil {
		return storepkg.PromotionResult{}, err
	}
	tx, err := postgresStore.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return storepkg.PromotionResult{}, redactErr("begin snapshot promotion", err)
	}
	defer tx.Rollback(ctx)
	var activeID, pendingID *uuid.UUID
	if err := tx.QueryRow(ctx, `SELECT active_snapshot_id, pending_snapshot_id FROM gateway_account_runtime_heads WHERE account_id=$1 FOR UPDATE`, request.AccountID).Scan(&activeID, &pendingID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storepkg.PromotionResult{}, storepkg.ErrNotFound
		}
		return storepkg.PromotionResult{}, redactErr("lock promotion head", err)
	}
	if pendingID == nil {
		return storepkg.PromotionResult{}, storepkg.ErrNotFound
	}
	pending, err := loadSnapshotByID(ctx, tx, *pendingID)
	if err != nil {
		return storepkg.PromotionResult{}, err
	}
	if pending.OperationID != request.OperationID {
		return storepkg.PromotionResult{}, storepkg.ErrOperationConflict
	}
	var operationState storepkg.OperationState
	if err := tx.QueryRow(ctx, `SELECT state FROM gateway_desired_operations WHERE operation_id=$1 FOR UPDATE`, request.OperationID.String()).Scan(&operationState); err != nil {
		return storepkg.PromotionResult{}, redactErr("load promotion operation", err)
	}
	if operationState != storepkg.OperationCandidateReady && operationState != storepkg.OperationApplied {
		return storepkg.PromotionResult{}, storepkg.ErrCandidateNotReady
	}
	var retiring *storepkg.ActiveSnapshot
	if activeID == nil {
		if request.ExpectedActiveFence.AccountID != "" {
			return storepkg.PromotionResult{}, storepkg.ErrPromotionFenceConflict
		}
	} else {
		active, err := loadSnapshotByID(ctx, tx, *activeID)
		if err != nil {
			return storepkg.PromotionResult{}, err
		}
		if active.Fence != request.ExpectedActiveFence {
			return storepkg.PromotionResult{}, storepkg.ErrPromotionFenceConflict
		}
		retiring = &storepkg.ActiveSnapshot{DesiredSnapshot: active}
		if _, err := tx.Exec(ctx, `INSERT INTO gateway_retiring_generations(account_id,snapshot_id,state) VALUES ($1,$2,'reconciling') ON CONFLICT DO NOTHING`, request.AccountID, active.SnapshotID); err != nil {
			return storepkg.PromotionResult{}, redactErr("record retiring generation", err)
		}
	}
	promotedAt := time.Now().UTC()
	if _, err := tx.Exec(ctx, `UPDATE gateway_account_runtime_heads SET active_snapshot_id=$2,pending_snapshot_id=NULL,updated_at=$3 WHERE account_id=$1`, request.AccountID, pending.SnapshotID, promotedAt); err != nil {
		return storepkg.PromotionResult{}, redactErr("swap runtime heads", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE gateway_desired_operations SET state='applied',applied_at=$2,updated_at=$2 WHERE operation_id=$1`, request.OperationID.String(), promotedAt); err != nil {
		return storepkg.PromotionResult{}, redactErr("apply desired operation", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return storepkg.PromotionResult{}, redactErr("commit snapshot promotion", err)
	}
	return storepkg.PromotionResult{Active: storepkg.ActiveSnapshot{DesiredSnapshot: pending, PromotedAt: promotedAt}, Retiring: retiring}, nil
}

func (postgresStore *Store) LoadRuntimeState(ctx context.Context, accountID domain.AccountID) (storepkg.RuntimeState, error) {
	if err := accountID.Validate(); err != nil {
		return storepkg.RuntimeState{}, err
	}
	return loadRuntimeState(ctx, postgresStore.pool, accountID)
}

func (postgresStore *Store) ListRuntimeStateForRecovery(ctx context.Context) ([]storepkg.RuntimeState, error) {
	rows, err := postgresStore.pool.Query(ctx, `SELECT account_id FROM gateway_account_runtime_heads ORDER BY account_id`)
	if err != nil {
		return nil, redactErr("list runtime state", err)
	}
	defer rows.Close()
	var result []storepkg.RuntimeState
	for rows.Next() {
		var accountID domain.AccountID
		if err := rows.Scan(&accountID); err != nil {
			return nil, redactErr("scan runtime state account", err)
		}
		state, err := loadRuntimeState(ctx, postgresStore.pool, accountID)
		if err != nil {
			return nil, err
		}
		result = append(result, state)
	}
	if err := rows.Err(); err != nil {
		return nil, redactErr("scan runtime states", err)
	}
	return result, nil
}

func (postgresStore *Store) TombstoneSecretReference(ctx context.Context, request storepkg.TombstoneRequest) (storepkg.Tombstone, error) {
	if err := request.Scope.Validate(); err != nil {
		return storepkg.Tombstone{}, err
	}
	if err := request.AccountID.Validate(); err != nil || request.TombstoneRevision == 0 {
		return storepkg.Tombstone{}, storepkg.ErrRetiredSecretReference
	}
	createdAt := time.Now().UTC()
	_, err := postgresStore.pool.Exec(ctx, `
		INSERT INTO gateway_retired_secret_refs(mode,namespace,secret_ref,account_id,tombstone_revision,created_at)
		VALUES ($1,$2,$3,$4,$5,$6)
		ON CONFLICT (mode,namespace,secret_ref) DO NOTHING
	`, request.Scope.Mode, request.Scope.Namespace, request.SecretRef.RawForTrustedBoundary(), request.AccountID, request.TombstoneRevision, createdAt)
	if err != nil {
		return storepkg.Tombstone{}, redactErr("tombstone secret reference", err)
	}
	var result storepkg.Tombstone
	result.TombstoneRequest = request
	if err := postgresStore.pool.QueryRow(ctx, `SELECT account_id,tombstone_revision,created_at FROM gateway_retired_secret_refs WHERE mode=$1 AND namespace=$2 AND secret_ref=$3`, request.Scope.Mode, request.Scope.Namespace, request.SecretRef.RawForTrustedBoundary()).Scan(&result.AccountID, &result.TombstoneRevision, &result.CreatedAt); err != nil {
		return storepkg.Tombstone{}, redactErr("load secret tombstone", err)
	}
	if result.AccountID != request.AccountID || result.TombstoneRevision != request.TombstoneRevision {
		return storepkg.Tombstone{}, storepkg.ErrRetiredSecretReference
	}
	return result, nil
}

func (postgresStore *Store) IsSecretReferenceRetired(ctx context.Context, scope secretstore.Scope, reference secretstore.OpaqueRef) (bool, error) {
	if err := scope.Validate(); err != nil {
		return false, err
	}
	var retired bool
	if err := postgresStore.pool.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM gateway_retired_secret_refs WHERE mode=$1 AND namespace=$2 AND secret_ref=$3)`, scope.Mode, scope.Namespace, reference.RawForTrustedBoundary()).Scan(&retired); err != nil {
		return false, redactErr("check retired secret reference", err)
	}
	return retired, nil
}

type snapshotQuerier interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

func loadRuntimeState(ctx context.Context, querier snapshotQuerier, accountID domain.AccountID) (storepkg.RuntimeState, error) {
	var activeID, pendingID *uuid.UUID
	if err := querier.QueryRow(ctx, `SELECT active_snapshot_id,pending_snapshot_id FROM gateway_account_runtime_heads WHERE account_id=$1`, accountID).Scan(&activeID, &pendingID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storepkg.RuntimeState{}, storepkg.ErrNotFound
		}
		return storepkg.RuntimeState{}, redactErr("load runtime head", err)
	}
	result := storepkg.RuntimeState{AccountID: accountID}
	if activeID != nil {
		snapshot, err := loadSnapshotByID(ctx, querier, *activeID)
		if err != nil {
			return storepkg.RuntimeState{}, err
		}
		result.Active = &storepkg.ActiveSnapshot{DesiredSnapshot: snapshot}
	}
	if pendingID != nil {
		snapshot, err := loadSnapshotByID(ctx, querier, *pendingID)
		if err != nil {
			return storepkg.RuntimeState{}, err
		}
		pending, err := loadPending(ctx, querier, snapshot)
		if err != nil {
			return storepkg.RuntimeState{}, err
		}
		result.Pending = &pending
	}
	return result, nil
}

func loadSnapshotByOperation(ctx context.Context, querier snapshotQuerier, operationID secretstore.OperationID) (storepkg.DesiredSnapshot, error) {
	return scanSnapshot(ctx, querier, `WHERE operation_id=$1`, operationID.String())
}

func loadSnapshotByID(ctx context.Context, querier snapshotQuerier, snapshotID uuid.UUID) (storepkg.DesiredSnapshot, error) {
	return scanSnapshot(ctx, querier, `WHERE snapshot_id=$1`, snapshotID)
}

func scanSnapshot(ctx context.Context, querier snapshotQuerier, where string, argument any) (storepkg.DesiredSnapshot, error) {
	var snapshot storepkg.DesiredSnapshot
	var operationID, secretRef string
	err := querier.QueryRow(ctx, `
		SELECT snapshot_id,operation_id,request_digest,account_id,venue,native_environment,secret_ref,
		       desired_state_revision,runtime_generation,credential_version,requested_state,created_at
		FROM gateway_runtime_snapshots `+where, argument).Scan(
		&snapshot.SnapshotID, &operationID, &snapshot.RequestDigest, &snapshot.AccountID, &snapshot.Venue,
		&snapshot.NativeEnvironment, &secretRef, &snapshot.Fence.DesiredStateRevision,
		&snapshot.Fence.RuntimeGeneration, &snapshot.Fence.CredentialVersion, &snapshot.RequestedState, &snapshot.CreatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return storepkg.DesiredSnapshot{}, storepkg.ErrNotFound
	}
	if err != nil {
		return storepkg.DesiredSnapshot{}, redactErr("load runtime snapshot", err)
	}
	parsedOperation, err := secretstore.ParseOperationID(operationID)
	if err != nil {
		return storepkg.DesiredSnapshot{}, storepkg.ErrOperationConflict
	}
	parsedRef, err := secretstore.ParseOpaqueRef(secretRef)
	if err != nil {
		return storepkg.DesiredSnapshot{}, storepkg.ErrOperationConflict
	}
	snapshot.OperationID = parsedOperation
	snapshot.SecretRef = parsedRef
	snapshot.Fence.AccountID = snapshot.AccountID
	rows, err := querier.Query(ctx, `SELECT route_id,product,settlement_asset,route_profile_id,requested_state FROM gateway_runtime_snapshot_routes WHERE snapshot_id=$1 ORDER BY route_id`, snapshot.SnapshotID)
	if err != nil {
		return storepkg.DesiredSnapshot{}, redactErr("load runtime snapshot routes", err)
	}
	defer rows.Close()
	for rows.Next() {
		var route storepkg.RouteDefinition
		if err := rows.Scan(&route.RouteID, &route.Product, &route.SettlementAsset, &route.RouteProfileID, &route.RequestedState); err != nil {
			return storepkg.DesiredSnapshot{}, redactErr("scan runtime snapshot route", err)
		}
		snapshot.Routes = append(snapshot.Routes, route)
	}
	if err := rows.Err(); err != nil {
		return storepkg.DesiredSnapshot{}, redactErr("scan runtime snapshot routes", err)
	}
	return snapshot, nil
}

func loadPending(ctx context.Context, querier snapshotQuerier, snapshot storepkg.DesiredSnapshot) (storepkg.PendingSnapshot, error) {
	pending := storepkg.PendingSnapshot{DesiredSnapshot: snapshot}
	if err := querier.QueryRow(ctx, `SELECT state FROM gateway_desired_operations WHERE operation_id=$1`, snapshot.OperationID.String()).Scan(&pending.OperationState); err != nil {
		return storepkg.PendingSnapshot{}, redactErr("load desired operation", err)
	}
	rows, err := querier.Query(ctx, `SELECT observation_revision,route_id,state,COALESCE(reason_code,''),observed_at FROM gateway_candidate_observations WHERE operation_id=$1 ORDER BY observation_revision,route_id`, snapshot.OperationID.String())
	if err != nil {
		return storepkg.PendingSnapshot{}, redactErr("load candidate observations", err)
	}
	defer rows.Close()
	for rows.Next() {
		observation := storepkg.CandidateObservation{OperationID: snapshot.OperationID}
		if err := rows.Scan(&observation.ObservationRevision, &observation.RouteID, &observation.State, &observation.ReasonCode, &observation.ObservedAt); err != nil {
			return storepkg.PendingSnapshot{}, redactErr("scan candidate observation", err)
		}
		pending.Observations = append(pending.Observations, observation)
	}
	return pending, rows.Err()
}

func sortedRoutes(routes []storepkg.RouteDefinition) []storepkg.RouteDefinition {
	result := append([]storepkg.RouteDefinition(nil), routes...)
	sort.Slice(result, func(i, j int) bool { return result[i].RouteID < result[j].RouteID })
	return result
}
