package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/jackc/pgx/v5"
)

func (postgresStore *Store) CreateDesiredAccount(ctx context.Context, desired store.DesiredAccount) (store.DesiredAccount, error) {
	if err := desired.Validate(); err != nil {
		return store.DesiredAccount{}, err
	}
	if desired.DesiredStateRevision != 1 || desired.RuntimeGeneration != 1 || desired.CredentialVersion != 1 ||
		desired.RequestedState != store.DesiredCreating || desired.RotationFence != nil || desired.PreviousSecretRef != "" {
		return store.DesiredAccount{}, store.ErrInvalidAccountTransition
	}

	tx, err := postgresStore.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return store.DesiredAccount{}, redactErr("begin desired account create", err)
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		INSERT INTO gateway_desired_accounts(
			account_id, venue, venue_environment, secret_ref,
			desired_state_revision, runtime_generation, credential_version, requested_state
		) VALUES ($1, $2, $3, $4, $5, $6, $7, $8)
		ON CONFLICT (account_id) DO NOTHING
	`, desired.AccountID, desired.Venue, desired.VenueEnvironment, desired.SecretRef,
		desired.DesiredStateRevision, desired.RuntimeGeneration, desired.CredentialVersion, desired.RequestedState)
	if err != nil {
		return store.DesiredAccount{}, redactErr("create desired account", err)
	}
	if tag.RowsAffected() == 0 {
		existing, err := loadDesiredAccount(ctx, tx, desired.AccountID, false)
		if err != nil {
			return store.DesiredAccount{}, err
		}
		if desiredAccountsEqual(existing, desired) {
			return existing, nil
		}
		return store.DesiredAccount{}, store.ErrDesiredStateRevisionConflict
	}
	if err := postgresStore.appendDesiredAudit(ctx, tx, "account.desired.created", desired, 0); err != nil {
		return store.DesiredAccount{}, err
	}
	created, err := loadDesiredAccount(ctx, tx, desired.AccountID, false)
	if err != nil {
		return store.DesiredAccount{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.DesiredAccount{}, redactErr("commit desired account create", err)
	}
	return created, nil
}

func (postgresStore *Store) ApplyDesiredAccount(ctx context.Context, expectedRevision int64, desired store.DesiredAccount) (store.DesiredAccount, error) {
	if err := desired.Validate(); err != nil {
		return store.DesiredAccount{}, err
	}
	if expectedRevision <= 0 || desired.PreviousSecretRef != "" {
		return store.DesiredAccount{}, store.ErrDesiredStateRevisionConflict
	}

	tx, err := postgresStore.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return store.DesiredAccount{}, redactErr("begin desired account update", err)
	}
	defer tx.Rollback(ctx)

	current, err := loadDesiredAccount(ctx, tx, desired.AccountID, true)
	if err != nil {
		return store.DesiredAccount{}, err
	}
	if desired.DesiredStateRevision == current.DesiredStateRevision {
		if expectedRevision != current.DesiredStateRevision-1 {
			return store.DesiredAccount{}, store.ErrDesiredStateRevisionConflict
		}
		if desiredAccountsEqual(current, desired) {
			return current, nil
		}
		return store.DesiredAccount{}, store.ErrDesiredStateRevisionConflict
	}
	if current.DesiredStateRevision != expectedRevision || desired.DesiredStateRevision != expectedRevision+1 {
		return store.DesiredAccount{}, store.ErrDesiredStateRevisionConflict
	}
	if err := store.ValidateDesiredAccountTransition(current.RequestedState, desired.RequestedState); err != nil {
		return store.DesiredAccount{}, err
	}
	if current.Venue != desired.Venue || current.VenueEnvironment != desired.VenueEnvironment {
		return store.DesiredAccount{}, store.ErrInvalidAccountTransition
	}

	previousSecretRef, err := validateDesiredFenceTransition(current, desired)
	if err != nil {
		return store.DesiredAccount{}, err
	}
	desired.PreviousSecretRef = previousSecretRef

	_, err = tx.Exec(ctx, `
		UPDATE gateway_desired_accounts SET
			secret_ref = $3,
			previous_secret_ref = NULLIF($4, ''),
			desired_state_revision = $5,
			runtime_generation = $6,
			credential_version = $7,
			requested_state = $8,
			rotation_fence_id = $9,
			rotation_fence_state = $10,
			rotation_from_credential_version = $11,
			rotation_to_credential_version = $12,
			rotation_from_runtime_generation = $13,
			rotation_to_runtime_generation = $14,
			rotation_armed_at = $15,
			rotation_released_at = $16,
			rotation_blocked_reason = $17,
			updated_at = now()
		WHERE account_id = $1 AND desired_state_revision = $2
	`, desired.AccountID, expectedRevision, desired.SecretRef, desired.PreviousSecretRef,
		desired.DesiredStateRevision, desired.RuntimeGeneration, desired.CredentialVersion, desired.RequestedState,
		fenceID(desired.RotationFence), fenceState(desired.RotationFence), fenceFromCredential(desired.RotationFence),
		fenceToCredential(desired.RotationFence), fenceFromRuntime(desired.RotationFence), fenceToRuntime(desired.RotationFence),
		fenceArmedAt(desired.RotationFence), fenceReleasedAt(desired.RotationFence), fenceBlockedReason(desired.RotationFence))
	if err != nil {
		return store.DesiredAccount{}, redactErr("update desired account", err)
	}
	if err := postgresStore.appendDesiredAudit(ctx, tx, "account.desired.updated", desired, expectedRevision); err != nil {
		return store.DesiredAccount{}, err
	}
	updated, err := loadDesiredAccount(ctx, tx, desired.AccountID, false)
	if err != nil {
		return store.DesiredAccount{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return store.DesiredAccount{}, redactErr("commit desired account update", err)
	}
	return updated, nil
}

func (postgresStore *Store) LoadDesiredAccount(ctx context.Context, accountID domain.AccountID) (store.DesiredAccount, error) {
	if err := accountID.Validate(); err != nil {
		return store.DesiredAccount{}, err
	}
	return loadDesiredAccount(ctx, postgresStore.pool, accountID, false)
}

func (postgresStore *Store) ListRecoverableDesiredAccounts(ctx context.Context) ([]store.DesiredAccount, error) {
	return postgresStore.listDesiredAccounts(ctx, `
		WHERE requested_state IN ('creating', 'active', 'rotating', 'draining')
		ORDER BY account_id
	`)
}

func (postgresStore *Store) ListDesiredAccounts(ctx context.Context) ([]store.DesiredAccount, error) {
	return postgresStore.listDesiredAccounts(ctx, ` ORDER BY account_id`)
}

func (postgresStore *Store) listDesiredAccounts(ctx context.Context, suffix string) ([]store.DesiredAccount, error) {
	rows, err := postgresStore.pool.Query(ctx, desiredAccountSelect+suffix)
	if err != nil {
		return nil, redactErr("list desired accounts", err)
	}
	defer rows.Close()

	var desired []store.DesiredAccount
	for rows.Next() {
		account, err := scanDesiredAccount(rows)
		if err != nil {
			return nil, err
		}
		desired = append(desired, account)
	}
	if err := rows.Err(); err != nil {
		return nil, redactErr("list desired accounts", err)
	}
	return desired, nil
}

func (postgresStore *Store) DeleteDesiredAccount(ctx context.Context, accountID domain.AccountID, expectedRevision int64) error {
	if err := accountID.Validate(); err != nil {
		return err
	}
	if expectedRevision <= 0 {
		return store.ErrDesiredStateRevisionConflict
	}
	tx, err := postgresStore.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return redactErr("begin desired account delete", err)
	}
	defer tx.Rollback(ctx)
	current, err := loadDesiredAccount(ctx, tx, accountID, true)
	if err != nil {
		return err
	}
	if current.DesiredStateRevision != expectedRevision {
		return store.ErrDesiredStateRevisionConflict
	}
	if current.RequestedState != store.DesiredRemoved {
		return store.ErrInvalidAccountTransition
	}
	if _, err := tx.Exec(ctx, `DELETE FROM gateway_desired_accounts WHERE account_id = $1 AND desired_state_revision = $2`, accountID, expectedRevision); err != nil {
		return redactErr("delete desired account", err)
	}
	if err := postgresStore.appendDesiredAudit(ctx, tx, "account.desired.deleted", current, expectedRevision); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return redactErr("commit desired account delete", err)
	}
	return nil
}

func (postgresStore *Store) ValidateRuntimeFence(ctx context.Context, fence store.RuntimeFence) error {
	if err := fence.Validate(); err != nil {
		return err
	}
	var matches bool
	if err := postgresStore.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM gateway_desired_accounts
			WHERE account_id = $1
			  AND desired_state_revision = $2
			  AND runtime_generation = $3
			  AND credential_version = $4
			  AND requested_state IN ('active', 'rotating')
		)
	`, fence.AccountID, fence.DesiredStateRevision, fence.RuntimeGeneration, fence.CredentialVersion).Scan(&matches); err != nil {
		return redactErr("validate runtime fence", err)
	}
	if !matches {
		return store.ErrStaleRuntimeGeneration
	}
	return nil
}

type desiredQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

const desiredAccountSelect = `
	SELECT account_id, venue, venue_environment, secret_ref, COALESCE(previous_secret_ref, ''),
	       desired_state_revision, runtime_generation, credential_version, requested_state,
	       rotation_fence_id, rotation_fence_state,
	       rotation_from_credential_version, rotation_to_credential_version,
	       rotation_from_runtime_generation, rotation_to_runtime_generation,
	       rotation_armed_at, rotation_released_at, rotation_blocked_reason, updated_at
	FROM gateway_desired_accounts
`

func loadDesiredAccount(ctx context.Context, querier desiredQuerier, accountID domain.AccountID, forUpdate bool) (store.DesiredAccount, error) {
	query := desiredAccountSelect + ` WHERE account_id = $1`
	if forUpdate {
		query += ` FOR UPDATE`
	}
	desired, err := scanDesiredAccount(querier.QueryRow(ctx, query, accountID))
	if errors.Is(err, pgx.ErrNoRows) {
		return store.DesiredAccount{}, store.ErrNotFound
	}
	if err != nil {
		return store.DesiredAccount{}, err
	}
	return desired, nil
}

type desiredScanner interface {
	Scan(...any) error
}

func scanDesiredAccount(scanner desiredScanner) (store.DesiredAccount, error) {
	var desired store.DesiredAccount
	var fenceID, fenceState, blockedReason sql.NullString
	var fromCredential, toCredential, fromRuntime, toRuntime sql.NullInt64
	var armedAt, releasedAt sql.NullTime
	if err := scanner.Scan(
		&desired.AccountID, &desired.Venue, &desired.VenueEnvironment, &desired.SecretRef, &desired.PreviousSecretRef,
		&desired.DesiredStateRevision, &desired.RuntimeGeneration, &desired.CredentialVersion, &desired.RequestedState,
		&fenceID, &fenceState, &fromCredential, &toCredential, &fromRuntime, &toRuntime,
		&armedAt, &releasedAt, &blockedReason, &desired.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.DesiredAccount{}, err
		}
		return store.DesiredAccount{}, redactErr("scan desired account", err)
	}
	if fenceID.Valid {
		desired.RotationFence = &store.RotationFence{
			ID:                    fenceID.String,
			State:                 store.RotationFenceState(fenceState.String),
			FromCredentialVersion: fromCredential.Int64,
			ToCredentialVersion:   toCredential.Int64,
			FromRuntimeGeneration: fromRuntime.Int64,
			ToRuntimeGeneration:   toRuntime.Int64,
			ArmedAt:               armedAt.Time,
			BlockedReason:         blockedReason.String,
		}
		if releasedAt.Valid {
			released := releasedAt.Time
			desired.RotationFence.ReleasedAt = &released
		}
	}
	return desired, nil
}

func validateDesiredFenceTransition(current, desired store.DesiredAccount) (string, error) {
	if desired.RuntimeGeneration < current.RuntimeGeneration || desired.CredentialVersion < current.CredentialVersion {
		return "", store.ErrStaleRuntimeGeneration
	}
	if desired.RequestedState == store.DesiredRotating {
		fence := desired.RotationFence
		if fence == nil || desired.RuntimeGeneration != current.RuntimeGeneration ||
			desired.CredentialVersion != current.CredentialVersion ||
			fence.FromRuntimeGeneration != current.RuntimeGeneration || fence.FromCredentialVersion != current.CredentialVersion {
			return "", store.ErrInvalidAccountTransition
		}
		switch current.RequestedState {
		case store.DesiredActive:
			return current.SecretRef, nil
		case store.DesiredRotating:
			if current.RotationFence == nil || fence.ID != current.RotationFence.ID ||
				fence.ToRuntimeGeneration != current.RotationFence.ToRuntimeGeneration ||
				fence.ToCredentialVersion != current.RotationFence.ToCredentialVersion ||
				desired.SecretRef != current.SecretRef {
				return "", store.ErrInvalidAccountTransition
			}
			return current.PreviousSecretRef, nil
		default:
			return "", store.ErrInvalidAccountTransition
		}
	}
	if current.RequestedState == store.DesiredRotating && desired.RequestedState == store.DesiredActive {
		if current.RotationFence == nil || desired.RotationFence == nil ||
			desired.RotationFence.ID != current.RotationFence.ID || desired.RotationFence.State != store.RotationReleased ||
			desired.RuntimeGeneration != current.RotationFence.ToRuntimeGeneration ||
			desired.CredentialVersion != current.RotationFence.ToCredentialVersion {
			return "", store.ErrInvalidAccountTransition
		}
		return "", nil
	}
	if desired.RuntimeGeneration != current.RuntimeGeneration || desired.CredentialVersion != current.CredentialVersion ||
		desired.SecretRef != current.SecretRef {
		return "", store.ErrStaleRuntimeGeneration
	}
	return current.PreviousSecretRef, nil
}

func desiredAccountsEqual(left, right store.DesiredAccount) bool {
	left.UpdatedAt = time.Time{}
	right.UpdatedAt = time.Time{}
	previousSecretMatches := left.PreviousSecretRef == right.PreviousSecretRef ||
		left.PreviousSecretRef == "" || right.PreviousSecretRef == ""
	return left.AccountID == right.AccountID && left.Venue == right.Venue &&
		left.VenueEnvironment == right.VenueEnvironment && left.SecretRef == right.SecretRef &&
		previousSecretMatches && left.DesiredStateRevision == right.DesiredStateRevision &&
		left.RuntimeGeneration == right.RuntimeGeneration && left.CredentialVersion == right.CredentialVersion &&
		left.RequestedState == right.RequestedState && rotationFencesEqual(left.RotationFence, right.RotationFence)
}

func rotationFencesEqual(left, right *store.RotationFence) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.ID == right.ID && left.State == right.State &&
		left.FromCredentialVersion == right.FromCredentialVersion && left.ToCredentialVersion == right.ToCredentialVersion &&
		left.FromRuntimeGeneration == right.FromRuntimeGeneration && left.ToRuntimeGeneration == right.ToRuntimeGeneration &&
		left.ArmedAt.Equal(right.ArmedAt) && equalTimes(left.ReleasedAt, right.ReleasedAt) &&
		left.BlockedReason == right.BlockedReason
}

func equalTimes(left, right *time.Time) bool {
	if left == nil || right == nil {
		return left == right
	}
	return left.Equal(*right)
}

func (postgresStore *Store) appendDesiredAudit(ctx context.Context, tx pgx.Tx, action string, desired store.DesiredAccount, previousRevision int64) error {
	auditID, err := postgresStore.identity.EventID()
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(map[string]any{
		"account_id":             desired.AccountID,
		"desired_state_revision": desired.DesiredStateRevision,
		"previous_revision":      previousRevision,
		"runtime_generation":     desired.RuntimeGeneration,
		"credential_version":     desired.CredentialVersion,
		"requested_state":        desired.RequestedState,
		"rotation_fence_id":      fenceID(desired.RotationFence),
	})
	if err != nil {
		return err
	}
	if _, err := tx.Exec(ctx, `
		INSERT INTO audit_records(audit_id, principal_id, action, resource_id, outcome, metadata)
		VALUES ($1, 'platform-gateway-adapter', $2, $3, 'prepared', $4)
	`, auditID, action, desired.AccountID, metadata); err != nil {
		return redactErr("append desired account audit", err)
	}
	return nil
}

func fenceID(fence *store.RotationFence) any {
	if fence == nil {
		return nil
	}
	return fence.ID
}
func fenceState(fence *store.RotationFence) any {
	if fence == nil {
		return nil
	}
	return fence.State
}
func fenceFromCredential(fence *store.RotationFence) any {
	if fence == nil {
		return nil
	}
	return fence.FromCredentialVersion
}
func fenceToCredential(fence *store.RotationFence) any {
	if fence == nil {
		return nil
	}
	return fence.ToCredentialVersion
}
func fenceFromRuntime(fence *store.RotationFence) any {
	if fence == nil {
		return nil
	}
	return fence.FromRuntimeGeneration
}
func fenceToRuntime(fence *store.RotationFence) any {
	if fence == nil {
		return nil
	}
	return fence.ToRuntimeGeneration
}
func fenceArmedAt(fence *store.RotationFence) any {
	if fence == nil {
		return nil
	}
	return fence.ArmedAt
}
func fenceReleasedAt(fence *store.RotationFence) any {
	if fence == nil || fence.ReleasedAt == nil {
		return nil
	}
	return *fence.ReleasedAt
}
func fenceBlockedReason(fence *store.RotationFence) any {
	if fence == nil || fence.BlockedReason == "" {
		return nil
	}
	return fence.BlockedReason
}
