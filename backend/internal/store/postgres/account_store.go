package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"sort"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/accounts"
	"github.com/QuantProcessing/boltertrader/secretstore"
)

type AccountStore struct{ db *sql.DB }

func NewAccountStore(db *sql.DB) *AccountStore { return &AccountStore{db: db} }

func (store *AccountStore) Create(ctx context.Context, account accounts.Account) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertAccount(ctx, tx, normalizeAccount(account)); err != nil {
		return err
	}
	if err := replaceRoutes(ctx, tx, account.ID, account.Routes); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *AccountStore) CreateDraft(ctx context.Context, account accounts.Account, operation accounts.CredentialOperation) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if err := insertAccount(ctx, tx, normalizeAccount(account)); err != nil {
		return err
	}
	if err := replaceRoutes(ctx, tx, account.ID, account.Routes); err != nil {
		return err
	}
	if err := insertOperation(ctx, tx, operation); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *AccountStore) Get(ctx context.Context, id string) (accounts.Account, error) {
	account, err := scanAccount(store.db.QueryRowContext(ctx, accountSelect+` WHERE account_id = $1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return accounts.Account{}, accounts.ErrNotFound
	}
	if err != nil {
		return accounts.Account{}, err
	}
	account.Routes, err = loadRoutes(ctx, store.db, id)
	return account, err
}

func (store *AccountStore) List(ctx context.Context) ([]accounts.Account, error) {
	rows, err := store.db.QueryContext(ctx, accountSelect+` ORDER BY account_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var result []accounts.Account
	for rows.Next() {
		account, scanErr := scanAccount(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		result = append(result, account)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for index := range result {
		result[index].Routes, err = loadRoutes(ctx, store.db, result[index].ID)
		if err != nil {
			return nil, err
		}
	}
	return result, nil
}

func (store *AccountStore) CompareAndSwap(ctx context.Context, id string, expectedRevision int64, account accounts.Account) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	changed, err := updateAccount(ctx, tx, id, expectedRevision, normalizeAccount(account))
	if err != nil {
		return err
	}
	if !changed {
		return accounts.ErrRevisionConflict
	}
	if err := replaceRoutes(ctx, tx, id, account.Routes); err != nil {
		return err
	}
	return tx.Commit()
}

func (store *AccountStore) GetCredentialOperation(ctx context.Context, id secretstore.OperationID) (accounts.CredentialOperation, error) {
	operation, err := scanOperation(store.db.QueryRowContext(ctx, operationSelect+` WHERE operation_id=$1`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return accounts.CredentialOperation{}, accounts.ErrNotFound
	}
	return operation, err
}

func (store *AccountStore) FindCredentialOperation(ctx context.Context, lookup accounts.OperationLookup) (accounts.CredentialOperation, error) {
	operation, err := scanOperation(store.db.QueryRowContext(ctx, operationSelect+` WHERE mode=$1 AND principal_id=$2 AND kind=$3 AND idempotency_key_digest=$4`, lookup.Mode, lookup.PrincipalID, lookup.Kind, lookup.IdempotencyKeyDigest))
	if errors.Is(err, sql.ErrNoRows) {
		return accounts.CredentialOperation{}, accounts.ErrNotFound
	}
	return operation, err
}

func (store *AccountStore) BindCredentialReference(ctx context.Context, id secretstore.OperationID, reference string) (accounts.Account, accounts.CredentialOperation, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return accounts.Account{}, accounts.CredentialOperation{}, err
	}
	defer tx.Rollback()
	operation, err := scanOperation(tx.QueryRowContext(ctx, operationSelect+` WHERE operation_id=$1 FOR UPDATE`, id))
	if errors.Is(err, sql.ErrNoRows) {
		return accounts.Account{}, accounts.CredentialOperation{}, accounts.ErrNotFound
	}
	if err != nil {
		return accounts.Account{}, accounts.CredentialOperation{}, err
	}
	account, err := scanAccount(tx.QueryRowContext(ctx, accountSelect+` WHERE account_id=$1 FOR UPDATE`, operation.AccountID))
	if err != nil {
		return accounts.Account{}, accounts.CredentialOperation{}, err
	}
	if operation.State == accounts.OperationStored && operation.SecretRef == reference && account.SecretRef == reference {
		account.Routes, err = loadRoutes(ctx, tx, account.ID)
		return account, operation, err
	}
	if operation.State != accounts.OperationAwaitingMaterial || reference == "" {
		return accounts.Account{}, accounts.CredentialOperation{}, accounts.ErrInvalidTransition
	}
	if _, err := tx.ExecContext(ctx, `UPDATE platform_accounts SET secret_ref=$2, updated_at=CURRENT_TIMESTAMP WHERE account_id=$1`, account.ID, reference); err != nil {
		return accounts.Account{}, accounts.CredentialOperation{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE platform_credential_operations SET secret_ref=$2,state='stored',updated_at=CURRENT_TIMESTAMP WHERE operation_id=$1`, id, reference); err != nil {
		return accounts.Account{}, accounts.CredentialOperation{}, err
	}
	account.SecretRef, operation.SecretRef, operation.State = reference, reference, accounts.OperationStored
	account.Routes, err = loadRoutes(ctx, tx, account.ID)
	if err != nil {
		return accounts.Account{}, accounts.CredentialOperation{}, err
	}
	if err := tx.Commit(); err != nil {
		return accounts.Account{}, accounts.CredentialOperation{}, err
	}
	return account, operation, nil
}

func (store *AccountStore) CompareAndSwapAccountAndOperation(ctx context.Context, request accounts.TransitionRequest) (accounts.Account, accounts.CredentialOperation, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return accounts.Account{}, accounts.CredentialOperation{}, err
	}
	defer tx.Rollback()
	var currentState accounts.OperationState
	var accountID string
	if err := tx.QueryRowContext(ctx, `SELECT account_id,state FROM platform_credential_operations WHERE operation_id=$1 FOR UPDATE`, request.OperationID).Scan(&accountID, &currentState); errors.Is(err, sql.ErrNoRows) {
		return accounts.Account{}, accounts.CredentialOperation{}, accounts.ErrNotFound
	} else if err != nil {
		return accounts.Account{}, accounts.CredentialOperation{}, err
	}
	if currentState != request.ExpectedOperationState || accountID != request.Account.ID {
		return accounts.Account{}, accounts.CredentialOperation{}, accounts.ErrRevisionConflict
	}
	changed, err := updateAccount(ctx, tx, accountID, request.ExpectedDesiredStateRevision, normalizeAccount(request.Account))
	if err != nil {
		return accounts.Account{}, accounts.CredentialOperation{}, err
	}
	if !changed {
		return accounts.Account{}, accounts.CredentialOperation{}, accounts.ErrRevisionConflict
	}
	if err := replaceRoutes(ctx, tx, accountID, request.Account.Routes); err != nil {
		return accounts.Account{}, accounts.CredentialOperation{}, err
	}
	result, err := tx.ExecContext(ctx, `UPDATE platform_credential_operations SET state=$3,secret_ref=NULLIF($4,''),stable_error_code=NULLIF($5,''),updated_at=$6 WHERE operation_id=$1 AND state=$2`, request.OperationID, request.ExpectedOperationState, request.Operation.State, request.Operation.SecretRef, request.Operation.StableErrorCode, request.Operation.UpdatedAt)
	if err != nil {
		return accounts.Account{}, accounts.CredentialOperation{}, err
	}
	count, err := result.RowsAffected()
	if err != nil || count != 1 {
		return accounts.Account{}, accounts.CredentialOperation{}, accounts.ErrRevisionConflict
	}
	if err := tx.Commit(); err != nil {
		return accounts.Account{}, accounts.CredentialOperation{}, err
	}
	return request.Account, request.Operation, nil
}

func (store *AccountStore) ListRecoverableCredentialOperations(ctx context.Context, limit int) ([]accounts.CredentialOperation, error) {
	if limit < 1 {
		return nil, nil
	}
	rows, err := store.db.QueryContext(ctx, operationSelect+` WHERE state IN ('awaiting_material','stored','applying','cleanup_pending') ORDER BY created_at,operation_id LIMIT $1`, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]accounts.CredentialOperation, 0)
	for rows.Next() {
		operation, err := scanOperation(rows)
		if err != nil {
			return nil, err
		}
		result = append(result, operation)
	}
	return result, rows.Err()
}

type databaseExecutor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}
type databaseQuerier interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

func insertAccount(ctx context.Context, executor databaseExecutor, account accounts.Account) error {
	products, rotation, err := encodeAccountJSON(account)
	if err != nil {
		return err
	}
	_, err = executor.ExecContext(ctx, `INSERT INTO platform_accounts
		(account_id,venue,venue_environment,mode,native_environment,display_name,product_ids,secret_ref,previous_secret_ref,desired_state_revision,runtime_generation,credential_version,state,rotation_fence,last_validated_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,NULLIF($8,''),NULLIF($9,''),$10,$11,$12,$13,$14,$15,$16)`,
		account.ID, account.Venue, account.VenueEnvironment, account.Mode, account.NativeEnvironment, account.DisplayName, products, account.SecretRef, account.PreviousSecretRef, account.DesiredStateRevision, account.RuntimeGeneration, account.CredentialVersion, account.State, rotation, account.LastValidatedAt, account.UpdatedAt)
	return err
}

func updateAccount(ctx context.Context, executor databaseExecutor, id string, expected int64, account accounts.Account) (bool, error) {
	products, rotation, err := encodeAccountJSON(account)
	if err != nil {
		return false, err
	}
	result, err := executor.ExecContext(ctx, `UPDATE platform_accounts SET venue=$3,venue_environment=$4,mode=$5,native_environment=$6,display_name=$7,product_ids=$8,secret_ref=NULLIF($9,''),previous_secret_ref=NULLIF($10,''),desired_state_revision=$11,runtime_generation=$12,credential_version=$13,state=$14,rotation_fence=$15,last_validated_at=$16,updated_at=$17 WHERE account_id=$1 AND desired_state_revision=$2`, id, expected, account.Venue, account.VenueEnvironment, account.Mode, account.NativeEnvironment, account.DisplayName, products, account.SecretRef, account.PreviousSecretRef, account.DesiredStateRevision, account.RuntimeGeneration, account.CredentialVersion, account.State, rotation, account.LastValidatedAt, account.UpdatedAt)
	if err != nil {
		return false, err
	}
	count, err := result.RowsAffected()
	return count == 1, err
}

func replaceRoutes(ctx context.Context, executor databaseExecutor, accountID string, routes []accounts.AccountRoute) error {
	if _, err := executor.ExecContext(ctx, `DELETE FROM platform_account_routes WHERE account_id=$1`, accountID); err != nil {
		return err
	}
	routes = orderedRoutes(routes)
	for _, route := range routes {
		if _, err := executor.ExecContext(ctx, `INSERT INTO platform_account_routes(account_id,route_id,product,settlement_asset,route_profile_id,requested_state) VALUES($1,$2,$3,$4,$5,$6)`, accountID, route.ID, route.Product, route.SettlementAsset, route.RouteProfileID, route.RequestedState); err != nil {
			return err
		}
	}
	return nil
}

func orderedRoutes(routes []accounts.AccountRoute) []accounts.AccountRoute {
	result := append([]accounts.AccountRoute(nil), routes...)
	sort.Slice(result, func(i, j int) bool { return result[i].ID < result[j].ID })
	return result
}

func loadRoutes(ctx context.Context, query databaseQuerier, accountID string) ([]accounts.AccountRoute, error) {
	rows, err := query.QueryContext(ctx, `SELECT route_id,product,settlement_asset,route_profile_id,requested_state FROM platform_account_routes WHERE account_id=$1 ORDER BY route_id`, accountID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]accounts.AccountRoute, 0)
	for rows.Next() {
		var route accounts.AccountRoute
		if err := rows.Scan(&route.ID, &route.Product, &route.SettlementAsset, &route.RouteProfileID, &route.RequestedState); err != nil {
			return nil, err
		}
		result = append(result, route)
	}
	return result, rows.Err()
}

func insertOperation(ctx context.Context, executor databaseExecutor, operation accounts.CredentialOperation) error {
	_, err := executor.ExecContext(ctx, `INSERT INTO platform_credential_operations(operation_id,principal_id,account_id,mode,kind,state,idempotency_key_digest,request_digest,target_desired_state_revision,target_runtime_generation,target_credential_version,secret_ref,stable_error_code,created_at,updated_at) VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,NULLIF($12,''),NULLIF($13,''),$14,$15)`, operation.ID, operation.PrincipalID, operation.AccountID, operation.Mode, operation.Kind, operation.State, operation.IdempotencyKeyDigest, operation.RequestDigest, operation.TargetDesiredStateRevision, operation.TargetRuntimeGeneration, operation.TargetCredentialVersion, operation.SecretRef, operation.StableErrorCode, operation.CreatedAt, operation.UpdatedAt)
	return err
}

const accountSelect = `SELECT account_id,venue,venue_environment,mode,native_environment,display_name,product_ids,COALESCE(secret_ref,''),COALESCE(previous_secret_ref,''),desired_state_revision,runtime_generation,credential_version,state,rotation_fence,last_validated_at,updated_at FROM platform_accounts`
const operationSelect = `SELECT operation_id,principal_id,account_id,mode,kind,state,idempotency_key_digest,request_digest,target_desired_state_revision,target_runtime_generation,target_credential_version,COALESCE(secret_ref,''),COALESCE(stable_error_code,''),created_at,updated_at FROM platform_credential_operations`

type scanner interface{ Scan(...any) error }

func scanAccount(row scanner) (accounts.Account, error) {
	var account accounts.Account
	var products, rotation []byte
	var validated sql.NullTime
	err := row.Scan(&account.ID, &account.Venue, &account.VenueEnvironment, &account.Mode, &account.NativeEnvironment, &account.DisplayName, &products, &account.SecretRef, &account.PreviousSecretRef, &account.DesiredStateRevision, &account.RuntimeGeneration, &account.CredentialVersion, &account.State, &rotation, &validated, &account.UpdatedAt)
	if err != nil {
		return accounts.Account{}, err
	}
	if err := json.Unmarshal(products, &account.ProductIDs); err != nil {
		return accounts.Account{}, err
	}
	account.ProductPolicy = append([]string(nil), account.ProductIDs...)
	if len(rotation) > 0 && string(rotation) != "null" {
		account.Rotation = &accounts.RotationFence{}
		if err := json.Unmarshal(rotation, account.Rotation); err != nil {
			return accounts.Account{}, err
		}
	}
	if validated.Valid {
		value := validated.Time
		account.LastValidatedAt = &value
	}
	return account, nil
}

func scanOperation(row scanner) (accounts.CredentialOperation, error) {
	var operation accounts.CredentialOperation
	err := row.Scan(&operation.ID, &operation.PrincipalID, &operation.AccountID, &operation.Mode, &operation.Kind, &operation.State, &operation.IdempotencyKeyDigest, &operation.RequestDigest, &operation.TargetDesiredStateRevision, &operation.TargetRuntimeGeneration, &operation.TargetCredentialVersion, &operation.SecretRef, &operation.StableErrorCode, &operation.CreatedAt, &operation.UpdatedAt)
	return operation, err
}

func encodeAccountJSON(account accounts.Account) ([]byte, []byte, error) {
	products := account.ProductPolicy
	if len(products) == 0 {
		products = account.ProductIDs
	}
	encodedProducts, err := json.Marshal(products)
	if err != nil {
		return nil, nil, err
	}
	var rotation []byte
	if account.Rotation != nil {
		rotation, err = json.Marshal(account.Rotation)
		if err != nil {
			return nil, nil, err
		}
	}
	return encodedProducts, rotation, nil
}

func normalizeAccount(account accounts.Account) accounts.Account {
	if account.Mode == "" {
		account.Mode = secretstore.ModeTest
		if account.VenueEnvironment == accounts.EnvironmentLive {
			account.Mode = secretstore.ModeLive
		}
	}
	if account.NativeEnvironment == "" {
		account.NativeEnvironment = account.VenueEnvironment
	}
	if len(account.ProductPolicy) == 0 {
		account.ProductPolicy = append([]string(nil), account.ProductIDs...)
	}
	return account
}
