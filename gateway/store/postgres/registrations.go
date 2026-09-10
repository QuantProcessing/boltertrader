package postgres

import (
	"context"
	"errors"

	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/secretstore"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type RegistrationStore struct {
	pool   *pgxpool.Pool
	cipher *secretstore.Cipher
}

func NewRegistrationStore(pool *pgxpool.Pool, cipher *secretstore.Cipher) *RegistrationStore {
	return &RegistrationStore{pool: pool, cipher: cipher}
}

func (store *RegistrationStore) CreateRegistration(ctx context.Context, record account.RegistrationRecord) error {
	sealed, err := store.cipher.Seal(string(record.ID), record.Venue, record.CredentialBundle)
	if err != nil {
		return err
	}
	defer clear(sealed)
	result, err := store.pool.Exec(ctx, `INSERT INTO gateway_accounts(account_id,venue,display_name,credential_ciphertext,created_at,validated_at) VALUES($1,$2,$3,$4,$5,$6) ON CONFLICT(account_id) DO NOTHING`, record.ID, record.Venue, record.DisplayName, sealed, record.CreatedAt, record.ValidatedAt)
	if err != nil {
		return redactErr("save account", err)
	}
	if result.RowsAffected() != 1 {
		return account.ErrRegistrationConflict
	}
	return nil
}

func (store *RegistrationStore) GetRegistration(ctx context.Context, id domain.AccountID) (account.RegistrationRecord, error) {
	var record account.RegistrationRecord
	var sealed []byte
	err := store.pool.QueryRow(ctx, `SELECT account_id,venue,display_name,credential_ciphertext,created_at,validated_at FROM gateway_accounts WHERE account_id=$1`, id).Scan(&record.ID, &record.Venue, &record.DisplayName, &sealed, &record.CreatedAt, &record.ValidatedAt)
	if errors.Is(err, pgx.ErrNoRows) {
		return record, account.ErrRegistrationNotFound
	}
	if err != nil {
		return record, redactErr("read account", err)
	}
	defer clear(sealed)
	record.CredentialBundle, err = store.cipher.Open(string(record.ID), record.Venue, sealed)
	return record, err
}

func (store *RegistrationStore) ListRegistrations(ctx context.Context) ([]account.Registration, error) {
	rows, err := store.pool.Query(ctx, `SELECT account_id,venue,display_name,created_at,validated_at FROM gateway_accounts ORDER BY created_at,account_id`)
	if err != nil {
		return nil, redactErr("list accounts", err)
	}
	defer rows.Close()
	items := []account.Registration{}
	for rows.Next() {
		var item account.Registration
		if err := rows.Scan(&item.ID, &item.Venue, &item.DisplayName, &item.CreatedAt, &item.ValidatedAt); err != nil {
			return nil, redactErr("read account metadata", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return nil, redactErr("list accounts", err)
	}
	return items, nil
}

func (store *RegistrationStore) DeleteRegistration(ctx context.Context, id domain.AccountID) error {
	_, err := store.pool.Exec(ctx, `DELETE FROM gateway_accounts WHERE account_id=$1`, id)
	if err != nil {
		return redactErr("delete account", err)
	}
	return nil
}
