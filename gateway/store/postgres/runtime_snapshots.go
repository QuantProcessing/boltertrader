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

	secretRef := snapshot.SecretRef.RawForTrustedBoundary()
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, secretRef); err != nil {
		return storepkg.PendingSnapshot{}, redactErr("lock pending secret reference", err)
	}
	var tombstoned bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM gateway_retired_secret_refs WHERE secret_ref=$1
		)
	`, secretRef).Scan(&tombstoned); err != nil {
		return storepkg.PendingSnapshot{}, redactErr("check pending secret reference", err)
	}
	if tombstoned {
		return storepkg.PendingSnapshot{}, storepkg.ErrRetiredSecretReference
	}

	var activeID, pendingID *uuid.UUID
	err = tx.QueryRow(ctx, `SELECT active_snapshot_id, pending_snapshot_id FROM gateway_account_runtime_heads WHERE account_id = $1 FOR UPDATE`, snapshot.AccountID).Scan(&activeID, &pendingID)
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return storepkg.PendingSnapshot{}, redactErr("lock runtime head", err)
	}
	retired, err := accountRetired(ctx, tx, snapshot.AccountID)
	if err != nil {
		return storepkg.PendingSnapshot{}, redactErr("check pending account retirement", err)
	}
	if retired {
		return storepkg.PendingSnapshot{}, storepkg.ErrOperationConflict
	}
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
	if pendingID != nil {
		return storepkg.PendingSnapshot{}, storepkg.ErrOperationConflict
	}
	if activeID != nil {
		active, err := loadSnapshotByID(ctx, tx, *activeID)
		if err != nil {
			return storepkg.PendingSnapshot{}, err
		}
		if snapshot.Fence.DesiredStateRevision <= active.Fence.DesiredStateRevision ||
			snapshot.Fence.RuntimeGeneration <= active.Fence.RuntimeGeneration ||
			snapshot.Fence.CredentialVersion < active.Fence.CredentialVersion {
			return storepkg.PendingSnapshot{}, storepkg.ErrStaleRuntimeGeneration
		}
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
		secretRef, snapshot.Fence.DesiredStateRevision, snapshot.Fence.RuntimeGeneration,
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
	tx, err := postgresStore.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return redactErr("begin candidate observation", err)
	}
	defer tx.Rollback(ctx)
	var known bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM gateway_desired_operations operation
			JOIN gateway_runtime_snapshot_routes route ON route.snapshot_id = operation.snapshot_id
			WHERE operation.operation_id=$1 AND route.route_id=$2 AND route.requested_state='active'
		)
	`, observation.OperationID.String(), observation.RouteID).Scan(&known); err != nil {
		return redactErr("validate candidate observation route", err)
	}
	if !known {
		return storepkg.ErrCandidateNotReady
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO gateway_candidate_observations(operation_id, observation_revision, route_id, state, reason_code, observed_at)
		VALUES ($1,$2,$3,$4,NULLIF($5,''),COALESCE(NULLIF($6, '0001-01-01 00:00:00+00'::timestamptz), now()))
		ON CONFLICT (operation_id, observation_revision, route_id) DO UPDATE SET
			state = EXCLUDED.state, reason_code = EXCLUDED.reason_code, observed_at = EXCLUDED.observed_at
	`, observation.OperationID.String(), observation.ObservationRevision, observation.RouteID, observation.State, observation.ReasonCode, observation.ObservedAt)
	if err != nil {
		return redactErr("record candidate observation", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return redactErr("commit candidate observation", err)
	}
	return nil
}

func (postgresStore *Store) MarkCandidateReady(ctx context.Context, operationID secretstore.OperationID) error {
	tag, err := postgresStore.pool.Exec(ctx, `
		UPDATE gateway_desired_operations operation SET state = 'candidate_ready', updated_at = now()
		WHERE operation.operation_id = $1 AND operation.state IN ('prepared','candidate_ready','candidate_failed')
		  AND EXISTS (
			SELECT 1 FROM gateway_runtime_snapshot_routes route
			WHERE route.snapshot_id=operation.snapshot_id AND route.requested_state='active'
		  )
		  AND NOT EXISTS (
			SELECT 1 FROM gateway_runtime_snapshot_routes route
			WHERE route.snapshot_id=operation.snapshot_id AND route.requested_state='active'
			  AND COALESCE((
				SELECT observation.state FROM gateway_candidate_observations observation
				WHERE observation.operation_id=operation.operation_id AND observation.route_id=route.route_id
				ORDER BY observation.observation_revision DESC LIMIT 1
			  ), '') <> 'ready'
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

func (postgresStore *Store) MarkCandidateFailed(ctx context.Context, operationID secretstore.OperationID) error {
	tag, err := postgresStore.pool.Exec(ctx, `
		UPDATE gateway_desired_operations operation SET state = 'candidate_failed', updated_at = now()
		WHERE operation.operation_id = $1 AND operation.state IN ('prepared','candidate_ready','candidate_failed')
		  AND EXISTS (
			SELECT 1 FROM gateway_candidate_observations observation
			WHERE observation.operation_id=operation.operation_id AND observation.state='failed'
		  )
	`, operationID.String())
	if err != nil {
		return redactErr("mark candidate failed", err)
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
	retired, err := accountRetired(ctx, tx, request.AccountID)
	if err != nil {
		return storepkg.PromotionResult{}, redactErr("check promotion account retirement", err)
	}
	if retired {
		return storepkg.PromotionResult{}, storepkg.ErrOperationConflict
	}
	if pendingID == nil {
		if activeID == nil {
			return storepkg.PromotionResult{}, storepkg.ErrNotFound
		}
		active, err := loadSnapshotByID(ctx, tx, *activeID)
		if err != nil {
			return storepkg.PromotionResult{}, err
		}
		if active.OperationID != request.OperationID {
			return storepkg.PromotionResult{}, storepkg.ErrNotFound
		}
		promotedAt, err := loadAppliedAt(ctx, tx, request.OperationID)
		if err != nil {
			return storepkg.PromotionResult{}, err
		}
		if err := tx.Commit(ctx); err != nil {
			return storepkg.PromotionResult{}, redactErr("commit promotion replay", err)
		}
		return storepkg.PromotionResult{Active: storepkg.ActiveSnapshot{DesiredSnapshot: active, PromotedAt: promotedAt}}, nil
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
		var unresolved int
		if err := tx.QueryRow(ctx, `
			SELECT count(*) FROM gateway_order_handoffs
			WHERE account_id=$1 AND desired_state_revision=$2 AND runtime_generation=$3
			  AND credential_version=$4 AND state='started'
		`, request.AccountID, active.Fence.DesiredStateRevision, active.Fence.RuntimeGeneration, active.Fence.CredentialVersion).Scan(&unresolved); err != nil {
			return storepkg.PromotionResult{}, redactErr("check promotion handoff drain", err)
		}
		if unresolved != 0 {
			return storepkg.PromotionResult{}, storepkg.ErrHandoffDrainBlocked
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

func accountRetired(ctx context.Context, querier snapshotQuerier, accountID domain.AccountID) (bool, error) {
	var retired bool
	err := querier.QueryRow(ctx, `SELECT EXISTS(SELECT 1 FROM gateway_account_retirements WHERE account_id=$1 AND state='tombstoned')`, accountID).Scan(&retired)
	return retired, err
}

func (postgresStore *Store) LookupRotationFinalization(ctx context.Context, request storepkg.RotationFinalizationRequest) (storepkg.RotationFinalizationResult, error) {
	digest, err := request.CanonicalDigest()
	if err != nil {
		return storepkg.RotationFinalizationResult{}, err
	}
	result, err := loadRotationFinalization(ctx, postgresStore.pool, request, digest)
	if err != nil {
		return storepkg.RotationFinalizationResult{}, err
	}
	return result, nil
}

func (postgresStore *Store) FinalizeRotation(ctx context.Context, request storepkg.RotationFinalizationRequest) (storepkg.RotationFinalizationResult, error) {
	digest, err := request.CanonicalDigest()
	if err != nil {
		return storepkg.RotationFinalizationResult{}, err
	}
	tx, err := postgresStore.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return storepkg.RotationFinalizationResult{}, redactErr("begin rotation finalization", err)
	}
	defer tx.Rollback(ctx)

	replay, err := loadRotationFinalization(ctx, tx, request, digest)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return storepkg.RotationFinalizationResult{}, redactErr("commit rotation finalization replay", err)
		}
		return replay, nil
	}
	if !errors.Is(err, storepkg.ErrNotFound) {
		return storepkg.RotationFinalizationResult{}, err
	}
	if postgresStore.afterRotationFinalizationReplayMiss != nil {
		if err := postgresStore.afterRotationFinalizationReplayMiss(ctx); err != nil {
			return storepkg.RotationFinalizationResult{}, err
		}
	}

	var retiringID uuid.UUID
	var retiringSecretRefRaw string
	err = tx.QueryRow(ctx, `
		SELECT retiring.snapshot_id,snapshot.secret_ref
		FROM gateway_retiring_generations retiring
		JOIN gateway_runtime_snapshots snapshot ON snapshot.snapshot_id=retiring.snapshot_id
		WHERE retiring.account_id=$1 AND retiring.state <> 'retired'
		  AND snapshot.account_id=$1
		  AND snapshot.desired_state_revision=$2
		  AND snapshot.runtime_generation=$3
		  AND snapshot.credential_version=$4
	`, request.AccountID, request.ExpectedRetiringFence.DesiredStateRevision,
		request.ExpectedRetiringFence.RuntimeGeneration, request.ExpectedRetiringFence.CredentialVersion).Scan(&retiringID, &retiringSecretRefRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		replay, replayErr := loadRotationFinalization(ctx, tx, request, digest)
		if replayErr == nil {
			if err := tx.Commit(ctx); err != nil {
				return storepkg.RotationFinalizationResult{}, redactErr("commit rotation finalization post-retirement replay", err)
			}
			return replay, nil
		}
		if !errors.Is(replayErr, storepkg.ErrNotFound) {
			return storepkg.RotationFinalizationResult{}, replayErr
		}
		return storepkg.RotationFinalizationResult{}, storepkg.ErrPromotionFenceConflict
	}
	if err != nil {
		return storepkg.RotationFinalizationResult{}, redactErr("load retiring rotation reference", err)
	}
	retiringSecretRef, err := secretstore.ParseOpaqueRef(retiringSecretRefRaw)
	if err != nil {
		return storepkg.RotationFinalizationResult{}, redactErr("parse retiring rotation reference", err)
	}
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1,0))`, retiringSecretRefRaw); err != nil {
		return storepkg.RotationFinalizationResult{}, redactErr("lock retiring rotation reference", err)
	}
	replay, err = loadRotationFinalization(ctx, tx, request, digest)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return storepkg.RotationFinalizationResult{}, redactErr("commit rotation finalization reference-locked replay", err)
		}
		return replay, nil
	}
	if !errors.Is(err, storepkg.ErrNotFound) {
		return storepkg.RotationFinalizationResult{}, err
	}

	var activeID, pendingID *uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT active_snapshot_id,pending_snapshot_id
		FROM gateway_account_runtime_heads WHERE account_id=$1 FOR UPDATE
	`, request.AccountID).Scan(&activeID, &pendingID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storepkg.RotationFinalizationResult{}, storepkg.ErrNotFound
		}
		return storepkg.RotationFinalizationResult{}, redactErr("lock rotation finalization head", err)
	}
	replay, err = loadRotationFinalization(ctx, tx, request, digest)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return storepkg.RotationFinalizationResult{}, redactErr("commit rotation finalization locked replay", err)
		}
		return replay, nil
	}
	if !errors.Is(err, storepkg.ErrNotFound) {
		return storepkg.RotationFinalizationResult{}, err
	}
	if pendingID != nil {
		return storepkg.RotationFinalizationResult{}, storepkg.ErrOperationConflict
	}
	if activeID == nil {
		return storepkg.RotationFinalizationResult{}, storepkg.ErrPromotionFenceConflict
	}
	active, err := loadSnapshotByID(ctx, tx, *activeID)
	if err != nil {
		return storepkg.RotationFinalizationResult{}, err
	}
	if active.Fence != request.ExpectedActiveFence {
		return storepkg.RotationFinalizationResult{}, storepkg.ErrPromotionFenceConflict
	}
	if active.OperationID != request.OperationID {
		return storepkg.RotationFinalizationResult{}, storepkg.ErrOperationConflict
	}
	var operationState storepkg.OperationState
	if err := tx.QueryRow(ctx, `SELECT state FROM gateway_desired_operations WHERE operation_id=$1 FOR UPDATE`, request.OperationID.String()).Scan(&operationState); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storepkg.RotationFinalizationResult{}, storepkg.ErrNotFound
		}
		return storepkg.RotationFinalizationResult{}, redactErr("load rotation finalization operation", err)
	}
	if operationState != storepkg.OperationApplied {
		return storepkg.RotationFinalizationResult{}, storepkg.ErrOperationConflict
	}

	var lockedRetiringID uuid.UUID
	var lockedRetiringSecretRefRaw string
	err = tx.QueryRow(ctx, `
		SELECT retiring.snapshot_id,snapshot.secret_ref
		FROM gateway_retiring_generations retiring
		JOIN gateway_runtime_snapshots snapshot ON snapshot.snapshot_id=retiring.snapshot_id
		WHERE retiring.account_id=$1 AND retiring.state <> 'retired'
		  AND snapshot.account_id=$1
		  AND snapshot.desired_state_revision=$2
		  AND snapshot.runtime_generation=$3
		  AND snapshot.credential_version=$4
		FOR UPDATE OF retiring
	`, request.AccountID, request.ExpectedRetiringFence.DesiredStateRevision,
		request.ExpectedRetiringFence.RuntimeGeneration, request.ExpectedRetiringFence.CredentialVersion).Scan(&lockedRetiringID, &lockedRetiringSecretRefRaw)
	if errors.Is(err, pgx.ErrNoRows) {
		return storepkg.RotationFinalizationResult{}, storepkg.ErrPromotionFenceConflict
	}
	if err != nil {
		return storepkg.RotationFinalizationResult{}, redactErr("lock retiring rotation generation", err)
	}
	if lockedRetiringID != retiringID || lockedRetiringSecretRefRaw != retiringSecretRefRaw {
		return storepkg.RotationFinalizationResult{}, storepkg.ErrPromotionFenceConflict
	}
	retiring, err := loadSnapshotByID(ctx, tx, retiringID)
	if err != nil {
		return storepkg.RotationFinalizationResult{}, err
	}
	if retiring.Fence != request.ExpectedRetiringFence {
		return storepkg.RotationFinalizationResult{}, storepkg.ErrPromotionFenceConflict
	}
	if retiring.SecretRef != retiringSecretRef {
		return storepkg.RotationFinalizationResult{}, storepkg.ErrOperationConflict
	}
	if active.SecretRef == retiring.SecretRef {
		return storepkg.RotationFinalizationResult{}, storepkg.ErrRetiredSecretReference
	}

	headReferenceRows, err := tx.Query(ctx, `
		SELECT snapshot.snapshot_id
		FROM gateway_account_runtime_heads head
		JOIN gateway_runtime_snapshots snapshot
		  ON snapshot.snapshot_id=head.active_snapshot_id OR snapshot.snapshot_id=head.pending_snapshot_id
		WHERE snapshot.secret_ref=$1
		FOR KEY SHARE OF snapshot
	`, retiringSecretRefRaw)
	if err != nil {
		return storepkg.RotationFinalizationResult{}, redactErr("lock rotation head reference users", err)
	}
	if headReferenceRows.Next() {
		headReferenceRows.Close()
		return storepkg.RotationFinalizationResult{}, storepkg.ErrRetiredSecretReference
	}
	if err := headReferenceRows.Err(); err != nil {
		headReferenceRows.Close()
		return storepkg.RotationFinalizationResult{}, redactErr("scan rotation head reference users", err)
	}
	headReferenceRows.Close()

	retiringReferenceRows, err := tx.Query(ctx, `
		SELECT generation.snapshot_id
		FROM gateway_retiring_generations generation
		JOIN gateway_runtime_snapshots snapshot ON snapshot.snapshot_id=generation.snapshot_id
		WHERE generation.state <> 'retired' AND snapshot.secret_ref=$1
		ORDER BY generation.account_id,generation.snapshot_id
		FOR UPDATE OF generation, snapshot
	`, retiringSecretRefRaw)
	if err != nil {
		return storepkg.RotationFinalizationResult{}, redactErr("lock retiring rotation reference users", err)
	}
	foundRetiring := false
	for retiringReferenceRows.Next() {
		var snapshotID uuid.UUID
		if err := retiringReferenceRows.Scan(&snapshotID); err != nil {
			retiringReferenceRows.Close()
			return storepkg.RotationFinalizationResult{}, redactErr("scan retiring rotation reference user", err)
		}
		if snapshotID != retiringID {
			retiringReferenceRows.Close()
			return storepkg.RotationFinalizationResult{}, storepkg.ErrRetiredSecretReference
		}
		foundRetiring = true
	}
	if err := retiringReferenceRows.Err(); err != nil {
		retiringReferenceRows.Close()
		return storepkg.RotationFinalizationResult{}, redactErr("scan retiring rotation reference users", err)
	}
	retiringReferenceRows.Close()
	if !foundRetiring {
		return storepkg.RotationFinalizationResult{}, storepkg.ErrOperationConflict
	}

	var tombstoned bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM gateway_retired_secret_refs
			WHERE mode=$1 AND namespace=$2 AND secret_ref=$3
		)
	`, request.Scope.Mode, request.Scope.Namespace, retiring.SecretRef.RawForTrustedBoundary()).Scan(&tombstoned); err != nil {
		return storepkg.RotationFinalizationResult{}, redactErr("check retiring rotation reference", err)
	}
	if tombstoned {
		return storepkg.RotationFinalizationResult{}, storepkg.ErrRetiredSecretReference
	}

	var unresolved int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM gateway_order_handoffs
		WHERE account_id=$1 AND desired_state_revision=$2 AND runtime_generation=$3
		  AND credential_version=$4 AND state='started'
	`, request.AccountID, request.ExpectedRetiringFence.DesiredStateRevision,
		request.ExpectedRetiringFence.RuntimeGeneration, request.ExpectedRetiringFence.CredentialVersion).Scan(&unresolved); err != nil {
		return storepkg.RotationFinalizationResult{}, redactErr("check rotation finalization handoff drain", err)
	}
	if unresolved != 0 {
		return storepkg.RotationFinalizationResult{}, storepkg.ErrHandoffDrainBlocked
	}

	completedAt := time.Now().UTC()
	tombstoneRevision := request.ExpectedActiveFence.DesiredStateRevision
	tag, err := tx.Exec(ctx, `
		INSERT INTO gateway_retired_secret_refs(mode,namespace,secret_ref,account_id,tombstone_revision,created_at)
		VALUES ($1,$2,$3,$4,$5,$6) ON CONFLICT DO NOTHING
	`, request.Scope.Mode, request.Scope.Namespace, retiring.SecretRef.RawForTrustedBoundary(), request.AccountID, tombstoneRevision, completedAt)
	if err != nil {
		return storepkg.RotationFinalizationResult{}, redactErr("tombstone retiring rotation reference", err)
	}
	if tag.RowsAffected() != 1 {
		return storepkg.RotationFinalizationResult{}, storepkg.ErrRetiredSecretReference
	}
	tag, err = tx.Exec(ctx, `
		UPDATE gateway_retiring_generations SET state='retired',updated_at=$3
		WHERE account_id=$1 AND snapshot_id=$2 AND state <> 'retired'
	`, request.AccountID, retiringID, completedAt)
	if err != nil {
		return storepkg.RotationFinalizationResult{}, redactErr("retire rotation generation", err)
	}
	if tag.RowsAffected() != 1 {
		return storepkg.RotationFinalizationResult{}, storepkg.ErrOperationConflict
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO gateway_rotation_finalizations(
			operation_id,account_id,request_digest,active_snapshot_id,retiring_snapshot_id,
			expected_active_desired_state_revision,expected_active_runtime_generation,expected_active_credential_version,
			expected_retiring_desired_state_revision,expected_retiring_runtime_generation,expected_retiring_credential_version,
			retired_secret_ref,tombstone_revision,mode,namespace,key_version,
			retiring_state,secret_reference_state,completed_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,'retired','tombstoned',$17)
	`, request.OperationID.String(), request.AccountID, digest, active.SnapshotID, retiring.SnapshotID,
		request.ExpectedActiveFence.DesiredStateRevision, request.ExpectedActiveFence.RuntimeGeneration, request.ExpectedActiveFence.CredentialVersion,
		request.ExpectedRetiringFence.DesiredStateRevision, request.ExpectedRetiringFence.RuntimeGeneration, request.ExpectedRetiringFence.CredentialVersion,
		retiring.SecretRef.RawForTrustedBoundary(), tombstoneRevision, request.Scope.Mode, request.Scope.Namespace, request.Scope.KeyVersion, completedAt); err != nil {
		return storepkg.RotationFinalizationResult{}, redactErr("record rotation finalization", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return storepkg.RotationFinalizationResult{}, redactErr("commit rotation finalization", err)
	}
	return storepkg.RotationFinalizationResult{
		OperationID: request.OperationID, AccountID: request.AccountID,
		ActiveFence: request.ExpectedActiveFence, RetiringFence: request.ExpectedRetiringFence,
		TombstoneRevision: tombstoneRevision, RetiringState: "retired", SecretReferenceState: "tombstoned", CompletedAt: completedAt,
	}, nil
}

func loadRotationFinalization(ctx context.Context, querier snapshotQuerier, request storepkg.RotationFinalizationRequest, digest string) (storepkg.RotationFinalizationResult, error) {
	var result storepkg.RotationFinalizationResult
	var operationID, storedDigest string
	var mode secretstore.Mode
	var namespace, keyVersion string
	err := querier.QueryRow(ctx, `
		SELECT operation_id::text,account_id,request_digest,
		       expected_active_desired_state_revision,expected_active_runtime_generation,expected_active_credential_version,
		       expected_retiring_desired_state_revision,expected_retiring_runtime_generation,expected_retiring_credential_version,
		       tombstone_revision,mode,namespace,key_version,retiring_state,secret_reference_state,completed_at
		FROM gateway_rotation_finalizations WHERE operation_id=$1
	`, request.OperationID.String()).Scan(
		&operationID, &result.AccountID, &storedDigest,
		&result.ActiveFence.DesiredStateRevision, &result.ActiveFence.RuntimeGeneration, &result.ActiveFence.CredentialVersion,
		&result.RetiringFence.DesiredStateRevision, &result.RetiringFence.RuntimeGeneration, &result.RetiringFence.CredentialVersion,
		&result.TombstoneRevision, &mode, &namespace, &keyVersion, &result.RetiringState, &result.SecretReferenceState, &result.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return storepkg.RotationFinalizationResult{}, storepkg.ErrNotFound
	}
	if err != nil {
		return storepkg.RotationFinalizationResult{}, redactErr("load rotation finalization", err)
	}
	parsedOperationID, err := secretstore.ParseOperationID(operationID)
	if err != nil {
		return storepkg.RotationFinalizationResult{}, storepkg.ErrOperationConflict
	}
	result.OperationID = parsedOperationID
	result.ActiveFence.AccountID = result.AccountID
	result.RetiringFence.AccountID = result.AccountID
	result.CompletedAt = result.CompletedAt.UTC()
	if storedDigest != digest || result.OperationID != request.OperationID || result.AccountID != request.AccountID ||
		result.ActiveFence != request.ExpectedActiveFence || result.RetiringFence != request.ExpectedRetiringFence ||
		result.TombstoneRevision != request.ExpectedActiveFence.DesiredStateRevision || mode != request.Scope.Mode ||
		namespace != request.Scope.Namespace || keyVersion != request.Scope.KeyVersion ||
		result.RetiringState != "retired" || result.SecretReferenceState != "tombstoned" {
		return storepkg.RotationFinalizationResult{}, storepkg.ErrOperationConflict
	}
	return result, nil
}

func loadAccountRetirement(ctx context.Context, querier snapshotQuerier, request storepkg.RetirementRequest, digest string) (storepkg.RetirementResult, error) {
	var replay storepkg.RetirementResult
	var replayOperationID, replayDigest string
	err := querier.QueryRow(ctx, `
			SELECT operation_id::text,account_id,request_digest,tombstone_revision,completed_at
			FROM gateway_account_retirements WHERE operation_id=$1
		`, request.OperationID.String()).Scan(&replayOperationID, &replay.AccountID, &replayDigest, &replay.TombstoneRevision, &replay.CompletedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return storepkg.RetirementResult{}, storepkg.ErrNotFound
	}
	if err != nil {
		return storepkg.RetirementResult{}, redactErr("load account retirement replay", err)
	}
	parsedOperationID, parseErr := secretstore.ParseOperationID(replayOperationID)
	if parseErr != nil || replayDigest != digest || replay.AccountID != request.AccountID || replay.TombstoneRevision != request.TombstoneRevision {
		return storepkg.RetirementResult{}, storepkg.ErrOperationConflict
	}
	replay.OperationID = parsedOperationID
	replay.Tombstoned = true
	replay.CompletedAt = replay.CompletedAt.UTC()
	return replay, nil
}

func (postgresStore *Store) RetireAccount(ctx context.Context, request storepkg.RetirementRequest) (storepkg.RetirementResult, error) {
	digest, err := request.CanonicalDigest()
	if err != nil {
		return storepkg.RetirementResult{}, err
	}
	tx, err := postgresStore.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return storepkg.RetirementResult{}, redactErr("begin account retirement", err)
	}
	defer tx.Rollback(ctx)

	replay, err := loadAccountRetirement(ctx, tx, request, digest)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return storepkg.RetirementResult{}, redactErr("commit account retirement replay", err)
		}
		return replay, nil
	}
	if !errors.Is(err, storepkg.ErrNotFound) {
		return storepkg.RetirementResult{}, err
	}

	var activeID, pendingID *uuid.UUID
	if err := tx.QueryRow(ctx, `
		SELECT active_snapshot_id,pending_snapshot_id
		FROM gateway_account_runtime_heads WHERE account_id=$1 FOR UPDATE
	`, request.AccountID).Scan(&activeID, &pendingID); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return storepkg.RetirementResult{}, storepkg.ErrNotFound
		}
		return storepkg.RetirementResult{}, redactErr("lock account retirement head", err)
	}
	replay, err = loadAccountRetirement(ctx, tx, request, digest)
	if err == nil {
		if err := tx.Commit(ctx); err != nil {
			return storepkg.RetirementResult{}, redactErr("commit account retirement locked replay", err)
		}
		return replay, nil
	}
	if !errors.Is(err, storepkg.ErrNotFound) {
		return storepkg.RetirementResult{}, err
	}
	var existingOperationID string
	err = tx.QueryRow(ctx, `SELECT operation_id::text FROM gateway_account_retirements WHERE account_id=$1`, request.AccountID).Scan(&existingOperationID)
	if err == nil {
		return storepkg.RetirementResult{}, storepkg.ErrOperationConflict
	}
	if !errors.Is(err, pgx.ErrNoRows) {
		return storepkg.RetirementResult{}, redactErr("load account retirement", err)
	}
	if activeID == nil {
		return storepkg.RetirementResult{}, storepkg.ErrPromotionFenceConflict
	}
	active, err := loadSnapshotByID(ctx, tx, *activeID)
	if err != nil {
		return storepkg.RetirementResult{}, err
	}
	if active.Fence != request.ExpectedActiveFence {
		return storepkg.RetirementResult{}, storepkg.ErrPromotionFenceConflict
	}

	var unresolved int
	if err := tx.QueryRow(ctx, `
		SELECT count(*) FROM gateway_order_handoffs WHERE account_id=$1 AND state='started'
	`, request.AccountID).Scan(&unresolved); err != nil {
		return storepkg.RetirementResult{}, redactErr("check account retirement handoff drain", err)
	}
	if unresolved != 0 {
		return storepkg.RetirementResult{}, storepkg.ErrHandoffDrainBlocked
	}

	var referenceCount int64
	if err := tx.QueryRow(ctx, `
		SELECT count(DISTINCT secret_ref) FROM gateway_runtime_snapshots WHERE account_id=$1
	`, request.AccountID).Scan(&referenceCount); err != nil {
		return storepkg.RetirementResult{}, redactErr("count account retirement references", err)
	}
	if referenceCount == 0 {
		return storepkg.RetirementResult{}, storepkg.ErrNotFound
	}

	completedAt := time.Now().UTC()
	if _, err := tx.Exec(ctx, `
		INSERT INTO gateway_retiring_generations(account_id,snapshot_id,state,created_at,updated_at)
		SELECT account_id,snapshot_id,'retired',$2,$2 FROM gateway_runtime_snapshots WHERE account_id=$1
		ON CONFLICT (account_id,snapshot_id) DO UPDATE SET state='retired',updated_at=EXCLUDED.updated_at
	`, request.AccountID, completedAt); err != nil {
		return storepkg.RetirementResult{}, redactErr("retire account generations", err)
	}
	tag, err := tx.Exec(ctx, `
		INSERT INTO gateway_retired_secret_refs(mode,namespace,secret_ref,account_id,tombstone_revision,created_at)
		SELECT $1,$2,secret_ref,$3,$4,$5
		FROM (SELECT DISTINCT secret_ref FROM gateway_runtime_snapshots WHERE account_id=$3) retired_refs
		ON CONFLICT (mode,namespace,secret_ref) DO UPDATE SET
			tombstone_revision=GREATEST(gateway_retired_secret_refs.tombstone_revision,EXCLUDED.tombstone_revision)
		WHERE gateway_retired_secret_refs.account_id=EXCLUDED.account_id
	`, request.Scope.Mode, request.Scope.Namespace, request.AccountID, request.TombstoneRevision, completedAt)
	if err != nil {
		return storepkg.RetirementResult{}, redactErr("tombstone account references", err)
	}
	if tag.RowsAffected() != referenceCount {
		return storepkg.RetirementResult{}, storepkg.ErrRetiredSecretReference
	}
	if _, err := tx.Exec(ctx, `
		UPDATE gateway_account_runtime_heads
		SET active_snapshot_id=NULL,pending_snapshot_id=NULL,updated_at=$2 WHERE account_id=$1
	`, request.AccountID, completedAt); err != nil {
		return storepkg.RetirementResult{}, redactErr("clear retired account heads", err)
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO gateway_account_retirements(
			operation_id,account_id,request_digest,expected_desired_state_revision,
			expected_runtime_generation,expected_credential_version,tombstone_revision,
			mode,namespace,state,completed_at
		) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,'tombstoned',$10)
	`, request.OperationID.String(), request.AccountID, digest, request.ExpectedActiveFence.DesiredStateRevision,
		request.ExpectedActiveFence.RuntimeGeneration, request.ExpectedActiveFence.CredentialVersion,
		request.TombstoneRevision, request.Scope.Mode, request.Scope.Namespace, completedAt); err != nil {
		return storepkg.RetirementResult{}, redactErr("record account retirement", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return storepkg.RetirementResult{}, redactErr("commit account retirement", err)
	}
	return storepkg.RetirementResult{
		OperationID: request.OperationID, AccountID: request.AccountID, TombstoneRevision: request.TombstoneRevision,
		Tombstoned: true, CompletedAt: completedAt,
	}, nil
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
	var retirementState string
	if err := querier.QueryRow(ctx, `
		SELECT state,tombstone_revision FROM gateway_account_retirements WHERE account_id=$1
	`, accountID).Scan(&retirementState, &result.TombstoneRevision); err == nil {
		if retirementState != "tombstoned" || activeID != nil || pendingID != nil {
			return storepkg.RuntimeState{}, storepkg.ErrOperationConflict
		}
		result.Tombstoned = true
	} else if !errors.Is(err, pgx.ErrNoRows) {
		return storepkg.RuntimeState{}, redactErr("load account retirement state", err)
	}
	if activeID != nil {
		snapshot, err := loadSnapshotByID(ctx, querier, *activeID)
		if err != nil {
			return storepkg.RuntimeState{}, err
		}
		promotedAt, err := loadAppliedAt(ctx, querier, snapshot.OperationID)
		if err != nil {
			return storepkg.RuntimeState{}, err
		}
		result.Active = &storepkg.ActiveSnapshot{DesiredSnapshot: snapshot, PromotedAt: promotedAt}
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
	rows, err := querier.Query(ctx, `
		SELECT snapshot_id FROM gateway_retiring_generations
		WHERE account_id=$1 AND state <> 'retired' ORDER BY snapshot_id
	`, accountID)
	if err != nil {
		return storepkg.RuntimeState{}, redactErr("load retiring generations", err)
	}
	var retiringIDs []uuid.UUID
	for rows.Next() {
		var snapshotID uuid.UUID
		if err := rows.Scan(&snapshotID); err != nil {
			rows.Close()
			return storepkg.RuntimeState{}, redactErr("scan retiring generation", err)
		}
		retiringIDs = append(retiringIDs, snapshotID)
	}
	if err := rows.Err(); err != nil {
		rows.Close()
		return storepkg.RuntimeState{}, redactErr("scan retiring generations", err)
	}
	rows.Close()
	for _, snapshotID := range retiringIDs {
		snapshot, err := loadSnapshotByID(ctx, querier, snapshotID)
		if err != nil {
			return storepkg.RuntimeState{}, err
		}
		promotedAt, err := loadAppliedAt(ctx, querier, snapshot.OperationID)
		if err != nil {
			return storepkg.RuntimeState{}, err
		}
		result.Retiring = append(result.Retiring, storepkg.ActiveSnapshot{DesiredSnapshot: snapshot, PromotedAt: promotedAt})
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

func loadAppliedAt(ctx context.Context, querier snapshotQuerier, operationID secretstore.OperationID) (time.Time, error) {
	var appliedAt *time.Time
	if err := querier.QueryRow(ctx, `SELECT applied_at FROM gateway_desired_operations WHERE operation_id=$1 AND state='applied'`, operationID.String()).Scan(&appliedAt); err != nil {
		return time.Time{}, redactErr("load runtime promotion time", err)
	}
	if appliedAt == nil || appliedAt.IsZero() {
		return time.Time{}, storepkg.ErrOperationConflict
	}
	return appliedAt.UTC(), nil
}

func sortedRoutes(routes []storepkg.RouteDefinition) []storepkg.RouteDefinition {
	result := append([]storepkg.RouteDefinition(nil), routes...)
	sort.Slice(result, func(i, j int) bool { return result[i].RouteID < result[j].RouteID })
	return result
}
