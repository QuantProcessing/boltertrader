package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	storepkg "github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
)

type migrationHead struct {
	accountID domain.AccountID
	active    *storepkg.MigrationPromotionItem
	pending   *storepkg.MigrationPromotionItem
}

func (postgresStore *Store) PromoteMigrationPendingSet(ctx context.Context, request storepkg.MigrationPromotionRequest) (storepkg.MigrationPromotionResult, error) {
	return postgresStore.promoteMigrationPendingSet(ctx, request, nil)
}

func (postgresStore *Store) promoteMigrationPendingSet(ctx context.Context, request storepkg.MigrationPromotionRequest, beforeCommit func(context.Context) error) (storepkg.MigrationPromotionResult, error) {
	operationSetSHA, err := request.OperationSetSHA256()
	if err != nil {
		return storepkg.MigrationPromotionResult{}, err
	}
	expected := make(map[domain.AccountID]storepkg.MigrationPromotionItem, len(request.Items))
	for _, item := range request.Items {
		expected[item.AccountID] = item
	}

	tx, err := postgresStore.pool.BeginTx(ctx, pgx.TxOptions{IsoLevel: pgx.Serializable})
	if err != nil {
		return storepkg.MigrationPromotionResult{}, redactErr("begin migration promotion", err)
	}
	defer tx.Rollback(ctx)

	heads, err := loadMigrationHeads(ctx, tx)
	if err != nil {
		return storepkg.MigrationPromotionResult{}, err
	}
	beforeActive := make([]storepkg.MigrationPromotionItem, 0, len(heads))
	beforePending := make([]storepkg.MigrationPromotionItem, 0, len(request.Items))
	matched := make(map[domain.AccountID]struct{}, len(request.Items))
	pendingCount, replayCount := 0, 0
	for _, head := range heads {
		if head.active != nil {
			beforeActive = append(beforeActive, *head.active)
		}
		item, requested := expected[head.accountID]
		if head.pending != nil {
			if head.active != nil || !requested || !sameMigrationPromotionItem(*head.pending, item) {
				return storepkg.MigrationPromotionResult{}, storepkg.ErrMigrationPromotionConflict
			}
			beforePending = append(beforePending, *head.pending)
			matched[head.accountID] = struct{}{}
			pendingCount++
			continue
		}
		if requested {
			if head.active == nil || !sameMigrationPromotionItem(*head.active, item) {
				return storepkg.MigrationPromotionResult{}, storepkg.ErrMigrationPromotionConflict
			}
			matched[head.accountID] = struct{}{}
			replayCount++
		}
	}
	if len(matched) != len(request.Items) || (pendingCount != 0 && replayCount != 0) {
		return storepkg.MigrationPromotionResult{}, storepkg.ErrMigrationPromotionConflict
	}

	before := storepkg.MigrationPromotionDigest{
		PendingSHA256:      migrationStateSHA256("pending", beforePending),
		ActiveSHA256:       migrationStateSHA256("active", beforeActive),
		OperationSetSHA256: operationSetSHA,
	}
	if pendingCount == 0 {
		if err := tx.Commit(ctx); err != nil {
			return storepkg.MigrationPromotionResult{}, redactErr("commit migration promotion replay", err)
		}
		return storepkg.MigrationPromotionResult{Before: before, After: before, Replayed: true}, nil
	}

	promotedAt := time.Now().UTC()
	for _, item := range request.Items {
		tag, err := tx.Exec(ctx, `
			UPDATE gateway_account_runtime_heads head
			SET active_snapshot_id=head.pending_snapshot_id,pending_snapshot_id=NULL,updated_at=$2
			FROM gateway_runtime_snapshots snapshot
			WHERE head.account_id=$1 AND snapshot.snapshot_id=head.pending_snapshot_id
			  AND snapshot.operation_id=$3 AND snapshot.desired_state_revision=$4
			  AND snapshot.runtime_generation=$5 AND snapshot.credential_version=$6
		`, item.AccountID, promotedAt, item.OperationID, item.DesiredStateRevision, item.RuntimeGeneration, item.CredentialVersion)
		if err != nil {
			return storepkg.MigrationPromotionResult{}, redactErr("promote migration runtime head", err)
		}
		if tag.RowsAffected() != 1 {
			return storepkg.MigrationPromotionResult{}, storepkg.ErrMigrationPromotionConflict
		}
		tag, err = tx.Exec(ctx, `UPDATE gateway_desired_operations SET state='applied',applied_at=$2,updated_at=$2 WHERE operation_id=$1 AND state IN ('prepared','candidate_ready')`, item.OperationID, promotedAt)
		if err != nil {
			return storepkg.MigrationPromotionResult{}, redactErr("apply migration desired operation", err)
		}
		if tag.RowsAffected() != 1 {
			return storepkg.MigrationPromotionResult{}, storepkg.ErrMigrationPromotionConflict
		}
	}

	afterActive := append(append([]storepkg.MigrationPromotionItem(nil), beforeActive...), request.Items...)
	after := storepkg.MigrationPromotionDigest{
		PendingSHA256:      migrationStateSHA256("pending", nil),
		ActiveSHA256:       migrationStateSHA256("active", afterActive),
		OperationSetSHA256: operationSetSHA,
	}
	if beforeCommit != nil {
		if err := beforeCommit(ctx); err != nil {
			return storepkg.MigrationPromotionResult{}, err
		}
	}
	if err := tx.Commit(ctx); err != nil {
		return storepkg.MigrationPromotionResult{}, redactErr("commit migration promotion", err)
	}
	return storepkg.MigrationPromotionResult{Before: before, After: after}, nil
}

func loadMigrationHeads(ctx context.Context, tx pgx.Tx) ([]migrationHead, error) {
	rows, err := tx.Query(ctx, `
		SELECT head.account_id,
		       active.snapshot_id,active.operation_id,active.request_digest,active.desired_state_revision,active.runtime_generation,active.credential_version,active_operation.state,
		       pending.snapshot_id,pending.operation_id,pending.request_digest,pending.desired_state_revision,pending.runtime_generation,pending.credential_version,pending_operation.state
		FROM gateway_account_runtime_heads head
		LEFT JOIN gateway_runtime_snapshots active ON active.snapshot_id=head.active_snapshot_id
		LEFT JOIN gateway_runtime_snapshots pending ON pending.snapshot_id=head.pending_snapshot_id
		LEFT JOIN gateway_desired_operations active_operation ON active_operation.operation_id=active.operation_id
		LEFT JOIN gateway_desired_operations pending_operation ON pending_operation.operation_id=pending.operation_id
		ORDER BY head.account_id
		FOR UPDATE OF head
	`)
	if err != nil {
		return nil, redactErr("lock migration runtime heads", err)
	}
	defer rows.Close()
	type rawHead struct {
		accountID                                                                                         domain.AccountID
		activeSnapshot, activeOperation, pendingSnapshot, pendingOperation                                *uuid.UUID
		activeInput, pendingInput                                                                         *string
		activeState, pendingState                                                                         *storepkg.OperationState
		activeDesired, activeRuntime, activeCredential, pendingDesired, pendingRuntime, pendingCredential *int64
	}
	var rawHeads []rawHead
	for rows.Next() {
		var raw rawHead
		if err := rows.Scan(&raw.accountID,
			&raw.activeSnapshot, &raw.activeOperation, &raw.activeInput, &raw.activeDesired, &raw.activeRuntime, &raw.activeCredential, &raw.activeState,
			&raw.pendingSnapshot, &raw.pendingOperation, &raw.pendingInput, &raw.pendingDesired, &raw.pendingRuntime, &raw.pendingCredential, &raw.pendingState); err != nil {
			return nil, redactErr("scan migration runtime head", err)
		}
		rawHeads = append(rawHeads, raw)
	}
	if err := rows.Err(); err != nil {
		return nil, redactErr("iterate migration runtime heads", err)
	}
	rows.Close()

	heads := make([]migrationHead, 0, len(rawHeads))
	for _, raw := range rawHeads {
		head := migrationHead{accountID: raw.accountID}
		if raw.activeSnapshot != nil {
			if raw.activeState == nil || *raw.activeState != storepkg.OperationApplied {
				return nil, storepkg.ErrMigrationPromotionConflict
			}
			routeSHA, err := loadMigrationRouteSetSHA256(ctx, tx, *raw.activeSnapshot)
			if err != nil {
				return nil, err
			}
			head.active = &storepkg.MigrationPromotionItem{AccountID: raw.accountID, OperationID: raw.activeOperation.String(), DesiredStateRevision: *raw.activeDesired, RuntimeGeneration: *raw.activeRuntime, CredentialVersion: *raw.activeCredential, RouteSetSHA256: routeSHA, InputSHA256: *raw.activeInput}
		}
		if raw.pendingSnapshot != nil {
			if raw.pendingState == nil || (*raw.pendingState != storepkg.OperationPrepared && *raw.pendingState != storepkg.OperationCandidateReady) {
				return nil, storepkg.ErrMigrationPromotionConflict
			}
			routeSHA, err := loadMigrationRouteSetSHA256(ctx, tx, *raw.pendingSnapshot)
			if err != nil {
				return nil, err
			}
			head.pending = &storepkg.MigrationPromotionItem{AccountID: raw.accountID, OperationID: raw.pendingOperation.String(), DesiredStateRevision: *raw.pendingDesired, RuntimeGeneration: *raw.pendingRuntime, CredentialVersion: *raw.pendingCredential, RouteSetSHA256: routeSHA, InputSHA256: *raw.pendingInput}
		}
		heads = append(heads, head)
	}
	return heads, nil
}

func loadMigrationRouteSetSHA256(ctx context.Context, tx pgx.Tx, snapshotID uuid.UUID) (string, error) {
	rows, err := tx.Query(ctx, `SELECT route_id,product,settlement_asset,route_profile_id,requested_state FROM gateway_runtime_snapshot_routes WHERE snapshot_id=$1 ORDER BY route_id`, snapshotID)
	if err != nil {
		return "", redactErr("load migration route set", err)
	}
	defer rows.Close()
	var routes []storepkg.RouteDefinition
	for rows.Next() {
		var route storepkg.RouteDefinition
		if err := rows.Scan(&route.RouteID, &route.Product, &route.SettlementAsset, &route.RouteProfileID, &route.RequestedState); err != nil {
			return "", redactErr("scan migration route set", err)
		}
		routes = append(routes, route)
	}
	if err := rows.Err(); err != nil {
		return "", redactErr("iterate migration route set", err)
	}
	digest, err := storepkg.MigrationRouteSetSHA256(routes)
	if err != nil {
		return "", storepkg.ErrMigrationPromotionConflict
	}
	return digest, nil
}

func sameMigrationPromotionItem(left, right storepkg.MigrationPromotionItem) bool {
	return left == right
}

func migrationStateSHA256(state string, items []storepkg.MigrationPromotionItem) string {
	items = append([]storepkg.MigrationPromotionItem(nil), items...)
	sort.Slice(items, func(i, j int) bool { return items[i].AccountID < items[j].AccountID })
	encoded, _ := json.Marshal(struct {
		State string                            `json:"state"`
		Items []storepkg.MigrationPromotionItem `json:"items"`
	}{state, items})
	digest := sha256.Sum256(encoded)
	return hex.EncodeToString(digest[:])
}
