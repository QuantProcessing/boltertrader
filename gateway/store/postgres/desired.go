package postgres

import (
	"context"
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
	if desired.RequestedState != store.DesiredCreating || desired.RotationFence != nil || desired.PreviousSecretRef != "" || desired.RuntimeID == "" {
		return store.DesiredAccount{}, store.ErrInvalidAccountTransition
	}

	tx, err := postgresStore.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return store.DesiredAccount{}, redactErr("begin desired account create", err)
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx, `
		INSERT INTO gateway_desired_accounts(
			account_id, runtime_id, venue, venue_environment, secret_ref, requested_state
		) VALUES ($1, $2::uuid, $3, $4, $5, $6)
		ON CONFLICT (account_id) DO NOTHING
	`, desired.AccountID, desired.RuntimeID, desired.Venue, desired.VenueEnvironment, desired.SecretRef, desired.RequestedState)
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
	if desired.PreviousSecretRef != "" || desired.RuntimeID == "" {
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
	_ = expectedRevision
	if err := store.ValidateDesiredAccountTransition(current.RequestedState, desired.RequestedState); err != nil {
		return store.DesiredAccount{}, err
	}
	if current.Venue != desired.Venue || current.VenueEnvironment != desired.VenueEnvironment {
		return store.DesiredAccount{}, store.ErrInvalidAccountTransition
	}

	if current.RuntimeID != desired.RuntimeID || current.SecretRef != desired.SecretRef || desired.RotationFence != nil {
		return store.DesiredAccount{}, store.ErrInvalidAccountTransition
	}

	_, err = tx.Exec(ctx, `
		UPDATE gateway_desired_accounts SET
			requested_state = $2,
			updated_at = now()
		WHERE account_id = $1
	`, desired.AccountID, desired.RequestedState)
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
		WHERE requested_state IN ('creating', 'active')
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
	_ = expectedRevision
	tx, err := postgresStore.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return redactErr("begin desired account delete", err)
	}
	defer tx.Rollback(ctx)
	current, err := loadDesiredAccount(ctx, tx, accountID, true)
	if err != nil {
		return err
	}
	if current.RequestedState != store.DesiredRemoved {
		return store.ErrInvalidAccountTransition
	}
	if _, err := tx.Exec(ctx, `DELETE FROM gateway_desired_accounts WHERE account_id = $1`, accountID); err != nil {
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
			  AND runtime_id = $2::uuid
			  AND requested_state = 'active'
		)
	`, fence.AccountID, fence.RuntimeID).Scan(&matches); err != nil {
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
	SELECT account_id, runtime_id::text, venue, venue_environment, COALESCE(secret_ref, ''),
	       requested_state, updated_at
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
	if err := scanner.Scan(
		&desired.AccountID, &desired.RuntimeID, &desired.Venue, &desired.VenueEnvironment, &desired.SecretRef,
		&desired.RequestedState, &desired.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return store.DesiredAccount{}, err
		}
		return store.DesiredAccount{}, redactErr("scan desired account", err)
	}
	return desired, nil
}

func desiredAccountsEqual(left, right store.DesiredAccount) bool {
	left.UpdatedAt = time.Time{}
	right.UpdatedAt = time.Time{}
	return left.AccountID == right.AccountID && left.Venue == right.Venue &&
		left.VenueEnvironment == right.VenueEnvironment && left.SecretRef == right.SecretRef &&
		left.RuntimeID == right.RuntimeID && left.RequestedState == right.RequestedState
}

func (postgresStore *Store) appendDesiredAudit(ctx context.Context, tx pgx.Tx, action string, desired store.DesiredAccount, previousRevision int64) error {
	auditID, err := postgresStore.identity.EventID()
	if err != nil {
		return err
	}
	metadata, err := json.Marshal(map[string]any{
		"account_id":      desired.AccountID,
		"runtime_id":      desired.RuntimeID,
		"requested_state": desired.RequestedState,
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
