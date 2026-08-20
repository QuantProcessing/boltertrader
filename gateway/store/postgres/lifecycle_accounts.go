package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"sort"

	"database/sql"

	"github.com/QuantProcessing/boltertrader/gateway/account/lifecycle"
	"github.com/QuantProcessing/boltertrader/secretstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

// AccountLifecycleStore is the durable half of the consolidated account
// authority: gateway_desired_accounts carries both the data-plane fences and
// the control-plane metadata, so a lifecycle transition and the fence it
// publishes are one row and one CAS rather than two tables kept in sync.
type AccountLifecycleStore struct{ pool *pgxpool.Pool }

func NewAccountLifecycleStore(pool *pgxpool.Pool) *AccountLifecycleStore {
	return &AccountLifecycleStore{pool: pool}
}

// lifecycleExecutor is satisfied by both *pgxpool.Pool and pgx.Tx so the row
// helpers work inside and outside a transaction.
type lifecycleExecutor interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	Query(context.Context, string, ...any) (pgx.Rows, error)
	QueryRow(context.Context, string, ...any) pgx.Row
}

const lifecycleAccountSelect = `
	SELECT account_id, runtime_id::text, venue, venue_environment, display_name, product_ids,
	       COALESCE(secret_ref, ''),
	       requested_state, last_validated_at, updated_at
	FROM gateway_desired_accounts`

const lifecycleOperationSelect = `
	SELECT operation_id, principal_id, account_id, kind, state,
	       idempotency_key_digest, request_digest,
	       COALESCE(secret_ref, ''), COALESCE(stable_error_code, ''), created_at, updated_at
	FROM gateway_credential_operations`

func (store *AccountLifecycleStore) Create(ctx context.Context, account lifecycle.Account) error {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return redactErr("begin lifecycle account create", err)
	}
	defer tx.Rollback(ctx)
	if err := insertLifecycleAccount(ctx, tx, normalizeLifecycleAccount(account)); err != nil {
		return err
	}
	if err := replaceLifecycleRoutes(ctx, tx, account.ID, account.Routes); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return redactErr("commit lifecycle account create", err)
	}
	return nil
}

func (store *AccountLifecycleStore) CreateDraft(ctx context.Context, account lifecycle.Account, operation lifecycle.CredentialOperation) error {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return redactErr("begin lifecycle account draft", err)
	}
	defer tx.Rollback(ctx)
	if err := insertLifecycleAccount(ctx, tx, normalizeLifecycleAccount(account)); err != nil {
		return err
	}
	if err := replaceLifecycleRoutes(ctx, tx, account.ID, account.Routes); err != nil {
		return err
	}
	if err := insertLifecycleOperation(ctx, tx, operation); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return redactErr("commit lifecycle account draft", err)
	}
	return nil
}

func (store *AccountLifecycleStore) BeginCredentialOperation(ctx context.Context, expectedRevision int64, account lifecycle.Account, operation lifecycle.CredentialOperation) error {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return redactErr("begin lifecycle credential operation", err)
	}
	defer tx.Rollback(ctx)
	changed, err := updateLifecycleAccount(ctx, tx, account.ID, expectedRevision, normalizeLifecycleAccount(account))
	if err != nil {
		return err
	}
	if !changed {
		return lifecycle.ErrRevisionConflict
	}
	// A second in-flight operation would leave two owners of the same fences,
	// so the account is claimed first and the claim is rejected if one exists.
	var recoverable bool
	if err := tx.QueryRow(ctx, `
		SELECT EXISTS(
			SELECT 1 FROM gateway_credential_operations
			WHERE account_id = $1
			  AND state IN ('awaiting_material', 'stored', 'applying', 'cleanup_pending')
		)`, account.ID).Scan(&recoverable); err != nil {
		return redactErr("probe recoverable credential operations", err)
	}
	if recoverable {
		return lifecycle.ErrRevisionConflict
	}
	if err := replaceLifecycleRoutes(ctx, tx, account.ID, account.Routes); err != nil {
		return err
	}
	if err := insertLifecycleOperation(ctx, tx, operation); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return redactErr("commit lifecycle credential operation", err)
	}
	return nil
}

func (store *AccountLifecycleStore) Get(ctx context.Context, id string) (lifecycle.Account, error) {
	account, err := scanLifecycleAccount(store.pool.QueryRow(ctx, lifecycleAccountSelect+` WHERE account_id = $1`, id))
	if errors.Is(err, pgx.ErrNoRows) {
		return lifecycle.Account{}, lifecycle.ErrNotFound
	}
	if err != nil {
		return lifecycle.Account{}, err
	}
	account.Routes, err = loadLifecycleRoutes(ctx, store.pool, id)
	return account, err
}

func (store *AccountLifecycleStore) List(ctx context.Context) ([]lifecycle.Account, error) {
	rows, err := store.pool.Query(ctx, lifecycleAccountSelect+` ORDER BY account_id`)
	if err != nil {
		return nil, redactErr("list lifecycle accounts", err)
	}
	defer rows.Close()
	var result []lifecycle.Account
	for rows.Next() {
		account, scanErr := scanLifecycleAccount(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, account)
	}
	if err := rows.Err(); err != nil {
		return nil, redactErr("list lifecycle accounts", err)
	}
	for index := range result {
		result[index].Routes, err = loadLifecycleRoutes(ctx, store.pool, result[index].ID)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (store *AccountLifecycleStore) CompareAndSwap(ctx context.Context, id string, expectedRevision int64, account lifecycle.Account) error {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return redactErr("begin lifecycle account swap", err)
	}
	defer tx.Rollback(ctx)
	changed, err := updateLifecycleAccount(ctx, tx, id, expectedRevision, normalizeLifecycleAccount(account))
	if err != nil {
		return err
	}
	if !changed {
		return lifecycle.ErrRevisionConflict
	}
	if err := replaceLifecycleRoutes(ctx, tx, id, account.Routes); err != nil {
		return err
	}
	if err := tx.Commit(ctx); err != nil {
		return redactErr("commit lifecycle account swap", err)
	}
	return nil
}

func (store *AccountLifecycleStore) GetCredentialOperation(ctx context.Context, id secretstore.OperationID) (lifecycle.CredentialOperation, error) {
	operation, err := scanLifecycleOperation(store.pool.QueryRow(ctx, lifecycleOperationSelect+` WHERE operation_id = $1`, string(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return lifecycle.CredentialOperation{}, lifecycle.ErrNotFound
	}
	return operation, err
}

func (store *AccountLifecycleStore) FindCredentialOperation(ctx context.Context, lookup lifecycle.OperationLookup) (lifecycle.CredentialOperation, error) {
	operation, err := scanLifecycleOperation(store.pool.QueryRow(ctx, lifecycleOperationSelect+`
		WHERE principal_id = $1 AND kind = $2 AND idempotency_key_digest = $3`,
		lookup.PrincipalID, lookup.Kind, lookup.IdempotencyKeyDigest))
	if errors.Is(err, pgx.ErrNoRows) {
		return lifecycle.CredentialOperation{}, lifecycle.ErrNotFound
	}
	return operation, err
}

func (store *AccountLifecycleStore) BindCredentialReference(ctx context.Context, id secretstore.OperationID, reference string) (lifecycle.Account, lifecycle.CredentialOperation, error) {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return lifecycle.Account{}, lifecycle.CredentialOperation{}, redactErr("begin credential reference bind", err)
	}
	defer tx.Rollback(ctx)
	operation, err := scanLifecycleOperation(tx.QueryRow(ctx, lifecycleOperationSelect+` WHERE operation_id = $1 FOR UPDATE`, string(id)))
	if errors.Is(err, pgx.ErrNoRows) {
		return lifecycle.Account{}, lifecycle.CredentialOperation{}, lifecycle.ErrNotFound
	}
	if err != nil {
		return lifecycle.Account{}, lifecycle.CredentialOperation{}, err
	}
	account, err := scanLifecycleAccount(tx.QueryRow(ctx, lifecycleAccountSelect+` WHERE account_id = $1 FOR UPDATE`, operation.AccountID))
	if errors.Is(err, pgx.ErrNoRows) {
		return lifecycle.Account{}, lifecycle.CredentialOperation{}, lifecycle.ErrNotFound
	}
	if err != nil {
		return lifecycle.Account{}, lifecycle.CredentialOperation{}, err
	}
	// Replaying the bind after a crash between the secret write and the commit
	// must observe the same reference rather than fail the operation.
	if operation.State == lifecycle.OperationStored && operation.SecretRef == reference && account.SecretRef == reference {
		account.Routes, err = loadLifecycleRoutes(ctx, tx, account.ID)
		return account, operation, err
	}
	if operation.State != lifecycle.OperationAwaitingMaterial || reference == "" {
		return lifecycle.Account{}, lifecycle.CredentialOperation{}, lifecycle.ErrInvalidTransition
	}
	if _, err := tx.Exec(ctx, `UPDATE gateway_desired_accounts SET secret_ref = $2, updated_at = CURRENT_TIMESTAMP WHERE account_id = $1`, account.ID, reference); err != nil {
		return lifecycle.Account{}, lifecycle.CredentialOperation{}, redactErr("bind account secret reference", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE gateway_credential_operations SET secret_ref = $2, state = 'stored', updated_at = CURRENT_TIMESTAMP WHERE operation_id = $1`, string(id), reference); err != nil {
		return lifecycle.Account{}, lifecycle.CredentialOperation{}, redactErr("bind operation secret reference", err)
	}
	account.SecretRef, operation.SecretRef, operation.State = reference, reference, lifecycle.OperationStored
	account.Routes, err = loadLifecycleRoutes(ctx, tx, account.ID)
	if err != nil {
		return lifecycle.Account{}, lifecycle.CredentialOperation{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return lifecycle.Account{}, lifecycle.CredentialOperation{}, redactErr("commit credential reference bind", err)
	}
	return account, operation, nil
}

func (store *AccountLifecycleStore) CompareAndSwapAccountAndOperation(ctx context.Context, request lifecycle.TransitionRequest) (lifecycle.Account, lifecycle.CredentialOperation, error) {
	tx, err := store.pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return lifecycle.Account{}, lifecycle.CredentialOperation{}, redactErr("begin lifecycle transition", err)
	}
	defer tx.Rollback(ctx)
	var currentState lifecycle.OperationState
	var accountID string
	err = tx.QueryRow(ctx, `SELECT account_id, state FROM gateway_credential_operations WHERE operation_id = $1 FOR UPDATE`, string(request.OperationID)).Scan(&accountID, &currentState)
	if errors.Is(err, pgx.ErrNoRows) {
		return lifecycle.Account{}, lifecycle.CredentialOperation{}, lifecycle.ErrNotFound
	}
	if err != nil {
		return lifecycle.Account{}, lifecycle.CredentialOperation{}, redactErr("lock credential operation", err)
	}
	if currentState != request.ExpectedOperationState || accountID != request.Account.ID {
		return lifecycle.Account{}, lifecycle.CredentialOperation{}, lifecycle.ErrRevisionConflict
	}
	changed, err := updateLifecycleAccount(ctx, tx, accountID, request.ExpectedDesiredStateRevision, normalizeLifecycleAccount(request.Account))
	if err != nil {
		return lifecycle.Account{}, lifecycle.CredentialOperation{}, err
	}
	if !changed {
		return lifecycle.Account{}, lifecycle.CredentialOperation{}, lifecycle.ErrRevisionConflict
	}
	if err := replaceLifecycleRoutes(ctx, tx, accountID, request.Account.Routes); err != nil {
		return lifecycle.Account{}, lifecycle.CredentialOperation{}, err
	}
	tag, err := tx.Exec(ctx, `
		UPDATE gateway_credential_operations
		SET state = $3, secret_ref = NULLIF($4, ''), stable_error_code = NULLIF($5, ''),
		    updated_at = $6
		WHERE operation_id = $1 AND state = $2`,
		string(request.OperationID), request.ExpectedOperationState, request.Operation.State,
		request.Operation.SecretRef, request.Operation.StableErrorCode, request.Operation.UpdatedAt)
	if err != nil {
		return lifecycle.Account{}, lifecycle.CredentialOperation{}, redactErr("swap credential operation", err)
	}
	if tag.RowsAffected() != 1 {
		return lifecycle.Account{}, lifecycle.CredentialOperation{}, lifecycle.ErrRevisionConflict
	}
	if err := tx.Commit(ctx); err != nil {
		return lifecycle.Account{}, lifecycle.CredentialOperation{}, redactErr("commit lifecycle transition", err)
	}
	return request.Account, request.Operation, nil
}

func (store *AccountLifecycleStore) RecordRotationCleanupProof(ctx context.Context, proof lifecycle.RotationCleanupProof) (lifecycle.CredentialOperation, error) {
	return lifecycle.CredentialOperation{}, lifecycle.ErrInvalidTransition
}

func (store *AccountLifecycleStore) CompleteRotationCleanup(ctx context.Context, completion lifecycle.RotationCleanupCompletion) (lifecycle.Account, lifecycle.CredentialOperation, error) {
	return lifecycle.Account{}, lifecycle.CredentialOperation{}, lifecycle.ErrInvalidTransition
}

func (store *AccountLifecycleStore) ListRecoverableCredentialOperations(ctx context.Context, limit int) ([]lifecycle.CredentialOperation, error) {
	if limit < 1 {
		return nil, nil
	}
	rows, err := store.pool.Query(ctx, lifecycleOperationSelect+`
		WHERE state IN ('awaiting_material', 'stored', 'applying')
		ORDER BY created_at, operation_id LIMIT $1`, limit)
	if err != nil {
		return nil, redactErr("list recoverable credential operations", err)
	}
	defer rows.Close()
	result := make([]lifecycle.CredentialOperation, 0)
	for rows.Next() {
		operation, err := scanLifecycleOperation(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, operation)
	}
	if err := rows.Err(); err != nil {
		return nil, redactErr("list recoverable credential operations", err)
	}
	return result, nil
}

func insertLifecycleAccount(ctx context.Context, executor lifecycleExecutor, account lifecycle.Account) error {
	products, err := encodeLifecycleProductIDs(account)
	if err != nil {
		return err
	}
	args := []any{
		account.ID, account.RuntimeID, account.Venue, account.VenueEnvironment, account.DisplayName, products,
		account.SecretRef, account.State,
	}
	args = append(args, account.LastValidatedAt, account.UpdatedAt)
	if _, err := executor.Exec(ctx, `
		INSERT INTO gateway_desired_accounts
			(account_id, runtime_id, venue, venue_environment, display_name, product_ids,
			 secret_ref,
			 requested_state, last_validated_at, updated_at)
		VALUES ($1, $2::uuid, $3, $4, $5, $6, NULLIF($7, ''),
		        $8, $9, $10)`, args...); err != nil {
		return redactErr("insert lifecycle account", err)
	}
	return nil
}

func updateLifecycleAccount(ctx context.Context, executor lifecycleExecutor, id string, expected int64, account lifecycle.Account) (bool, error) {
	products, err := encodeLifecycleProductIDs(account)
	if err != nil {
		return false, err
	}
	args := []any{
		id, account.RuntimeID,
		account.Venue, account.VenueEnvironment, account.DisplayName, products,
		account.SecretRef, account.State,
	}
	args = append(args, account.LastValidatedAt, account.UpdatedAt)
	_ = expected // kept in the compatibility interface; account versions no longer exist.
	tag, err := executor.Exec(ctx, `
		UPDATE gateway_desired_accounts
		SET runtime_id = $2::uuid, venue = $3, venue_environment = $4, display_name = $5, product_ids = $6,
		    secret_ref = NULLIF($7, ''), requested_state = $8, last_validated_at = $9, updated_at = $10
		WHERE account_id = $1`, args...)
	if err != nil {
		return false, redactErr("update lifecycle account", err)
	}
	return tag.RowsAffected() == 1, nil
}

func replaceLifecycleRoutes(ctx context.Context, executor lifecycleExecutor, accountID string, routes []lifecycle.AccountRoute) error {
	if _, err := executor.Exec(ctx, `DELETE FROM gateway_account_routes WHERE account_id = $1`, accountID); err != nil {
		return redactErr("clear lifecycle account routes", err)
	}
	for _, route := range orderedLifecycleRoutes(routes) {
		if _, err := executor.Exec(ctx, `
			INSERT INTO gateway_account_routes
				(account_id, route_id, product, settlement_asset, route_profile_id, requested_state)
			VALUES ($1, $2, $3, $4, $5, $6)`,
			accountID, route.ID, route.Product, route.SettlementAsset, route.RouteProfileID, route.RequestedState); err != nil {
			return redactErr("insert lifecycle account route", err)
		}
	}
	return nil
}

func orderedLifecycleRoutes(routes []lifecycle.AccountRoute) []lifecycle.AccountRoute {
	result := append([]lifecycle.AccountRoute(nil), routes...)
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func loadLifecycleRoutes(ctx context.Context, executor lifecycleExecutor, accountID string) ([]lifecycle.AccountRoute, error) {
	rows, err := executor.Query(ctx, `
		SELECT route_id, product, settlement_asset, route_profile_id, requested_state
		FROM gateway_account_routes WHERE account_id = $1 ORDER BY route_id`, accountID)
	if err != nil {
		return nil, redactErr("load lifecycle account routes", err)
	}
	defer rows.Close()
	result := make([]lifecycle.AccountRoute, 0)
	for rows.Next() {
		var route lifecycle.AccountRoute
		if err := rows.Scan(&route.ID, &route.Product, &route.SettlementAsset, &route.RouteProfileID, &route.RequestedState); err != nil {
			return nil, redactErr("scan lifecycle account route", err)
		}
		result = append(result, route)
	}
	if err := rows.Err(); err != nil {
		return nil, redactErr("load lifecycle account routes", err)
	}
	return result, nil
}

func insertLifecycleOperation(ctx context.Context, executor lifecycleExecutor, operation lifecycle.CredentialOperation) error {
	if _, err := executor.Exec(ctx, `
		INSERT INTO gateway_credential_operations
			(operation_id, principal_id, account_id, kind, state,
			 idempotency_key_digest, request_digest, secret_ref, stable_error_code, created_at, updated_at)
		VALUES ($1, $2, $3, $4, $5, $6, $7, NULLIF($8, ''), NULLIF($9, ''), $10, $11)`,
		string(operation.ID), operation.PrincipalID, operation.AccountID, operation.Kind, operation.State,
		operation.IdempotencyKeyDigest, operation.RequestDigest, operation.SecretRef,
		operation.StableErrorCode, operation.CreatedAt, operation.UpdatedAt); err != nil {
		return redactErr("insert credential operation", err)
	}
	return nil
}

func scanLifecycleAccount(row interface{ Scan(...any) error }) (lifecycle.Account, error) {
	var account lifecycle.Account
	var products []byte
	var validatedAt sql.NullTime
	if err := row.Scan(
		&account.ID, &account.RuntimeID, &account.Venue, &account.VenueEnvironment, &account.DisplayName, &products,
		&account.SecretRef,
		&account.State, &validatedAt, &account.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return lifecycle.Account{}, err
		}
		return lifecycle.Account{}, redactErr("scan lifecycle account", err)
	}
	if len(products) > 0 && string(products) != "null" {
		if err := json.Unmarshal(products, &account.ProductIDs); err != nil {
			return lifecycle.Account{}, redactErr("decode lifecycle product ids", err)
		}
	}
	account.ProductPolicy = append([]string(nil), account.ProductIDs...)
	// The consolidated row carries one environment column; the venue environment
	// and the native environment were provably equal at every construction site.
	account.NativeEnvironment = account.VenueEnvironment
	if validatedAt.Valid {
		value := validatedAt.Time.UTC()
		account.LastValidatedAt = &value
	}
	return account, nil
}

func scanLifecycleOperation(row interface{ Scan(...any) error }) (lifecycle.CredentialOperation, error) {
	var operation lifecycle.CredentialOperation
	if err := row.Scan(
		&operation.ID, &operation.PrincipalID, &operation.AccountID, &operation.Kind, &operation.State,
		&operation.IdempotencyKeyDigest, &operation.RequestDigest,
		&operation.SecretRef, &operation.StableErrorCode, &operation.CreatedAt, &operation.UpdatedAt,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return lifecycle.CredentialOperation{}, err
		}
		return lifecycle.CredentialOperation{}, redactErr("scan credential operation", err)
	}
	return operation, nil
}

func encodeLifecycleProductIDs(account lifecycle.Account) ([]byte, error) {
	products := account.ProductPolicy
	if len(products) == 0 {
		products = account.ProductIDs
	}
	if products == nil {
		products = []string{}
	}
	encoded, err := json.Marshal(products)
	if err != nil {
		return nil, redactErr("encode lifecycle product ids", err)
	}
	return encoded, nil
}

func normalizeLifecycleAccount(account lifecycle.Account) lifecycle.Account {
	if account.NativeEnvironment == "" {
		account.NativeEnvironment = account.VenueEnvironment
	}
	if len(account.ProductPolicy) == 0 {
		account.ProductPolicy = append([]string(nil), account.ProductIDs...)
	}
	return account
}

var _ lifecycle.Store = (*AccountLifecycleStore)(nil)
