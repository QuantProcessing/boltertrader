package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/accounts"
)

type AccountStore struct{ db *sql.DB }

func NewAccountStore(db *sql.DB) *AccountStore { return &AccountStore{db: db} }

func (store *AccountStore) Create(ctx context.Context, account accounts.Account) error {
	products, rotation, err := encodeAccountJSON(account)
	if err != nil {
		return err
	}
	_, err = store.db.ExecContext(ctx, `
		INSERT INTO platform_accounts
		(account_id, venue, venue_environment, display_name, product_ids, secret_ref,
		 desired_state_revision, runtime_generation, credential_version, state,
		 rotation_fence, last_validated_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)`,
		account.ID, account.Venue, account.VenueEnvironment, account.DisplayName, products,
		account.SecretRef, account.DesiredStateRevision, account.RuntimeGeneration,
		account.CredentialVersion, account.State, rotation, account.LastValidatedAt, account.UpdatedAt)
	return err
}

func (store *AccountStore) Get(ctx context.Context, id string) (accounts.Account, error) {
	row := store.db.QueryRowContext(ctx, accountSelect+` WHERE account_id = $1`, id)
	account, err := scanAccount(row)
	if errors.Is(err, sql.ErrNoRows) {
		return accounts.Account{}, accounts.ErrNotFound
	}
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
	return result, rows.Err()
}

func (store *AccountStore) CompareAndSwap(ctx context.Context, id string, expectedRevision int64, account accounts.Account) error {
	products, rotation, err := encodeAccountJSON(account)
	if err != nil {
		return err
	}
	result, err := store.db.ExecContext(ctx, `
		UPDATE platform_accounts SET venue=$3, venue_environment=$4, display_name=$5,
		product_ids=$6, secret_ref=$7, desired_state_revision=$8, runtime_generation=$9,
		credential_version=$10, state=$11, rotation_fence=$12, last_validated_at=$13, updated_at=$14
		WHERE account_id=$1 AND desired_state_revision=$2`,
		id, expectedRevision, account.Venue, account.VenueEnvironment, account.DisplayName,
		products, account.SecretRef, account.DesiredStateRevision, account.RuntimeGeneration,
		account.CredentialVersion, account.State, rotation, account.LastValidatedAt, account.UpdatedAt)
	if err != nil {
		return err
	}
	changed, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if changed == 0 {
		return accounts.ErrRevisionConflict
	}
	return nil
}

const accountSelect = `SELECT account_id, venue, venue_environment, display_name, product_ids,
	secret_ref, desired_state_revision, runtime_generation, credential_version, state,
	rotation_fence, last_validated_at, updated_at FROM platform_accounts`

type scanner interface{ Scan(...any) error }

func scanAccount(row scanner) (accounts.Account, error) {
	var account accounts.Account
	var products, rotation []byte
	var validated sql.NullTime
	err := row.Scan(&account.ID, &account.Venue, &account.VenueEnvironment, &account.DisplayName,
		&products, &account.SecretRef, &account.DesiredStateRevision, &account.RuntimeGeneration,
		&account.CredentialVersion, &account.State, &rotation, &validated, &account.UpdatedAt)
	if err != nil {
		return accounts.Account{}, err
	}
	if err := json.Unmarshal(products, &account.ProductIDs); err != nil {
		return accounts.Account{}, err
	}
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

func encodeAccountJSON(account accounts.Account) ([]byte, []byte, error) {
	products, err := json.Marshal(account.ProductIDs)
	if err != nil {
		return nil, nil, err
	}
	rotation := []byte("null")
	if account.Rotation != nil {
		rotation, err = json.Marshal(account.Rotation)
		if err != nil {
			return nil, nil, err
		}
	}
	return products, rotation, nil
}
