package postgres

import (
	"context"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

type storeOptions struct {
	identity            *domain.IdentityGenerator
	beforeCommit        func(context.Context) error
	beforeReserveInsert func(context.Context) error
}

type Store struct {
	pool                *pgxpool.Pool
	identity            *domain.IdentityGenerator
	beforeCommit        func(context.Context) error
	beforeReserveInsert func(context.Context) error
}

func NewStore(pool *pgxpool.Pool) *Store {
	return newStore(pool, storeOptions{})
}

func newStoreForTest(pool *pgxpool.Pool, options storeOptions) *Store {
	return newStore(pool, options)
}

func newStore(pool *pgxpool.Pool, options storeOptions) *Store {
	identity := options.identity
	if identity == nil {
		identity = domain.NewIdentityGenerator(domain.IdentityGeneratorOptions{})
	}
	return &Store{
		pool:                pool,
		identity:            identity,
		beforeCommit:        options.beforeCommit,
		beforeReserveInsert: options.beforeReserveInsert,
	}
}

type AccountRegistration struct {
	AccountID   domain.AccountID
	Venue       string
	Product     string
	Environment string
	Settlement  string
	ConfigHash  string
	Status      string
}

func (store *Store) RegisterAccount(ctx context.Context, registration AccountRegistration) error {
	if err := registration.AccountID.Validate(); err != nil {
		return err
	}
	_, err := store.pool.Exec(ctx, `
		INSERT INTO gateway_accounts(account_id, venue, product, environment, settlement, config_hash, status)
		VALUES ($1, $2, $3, $4, $5, $6, $7)
		ON CONFLICT (account_id) DO UPDATE SET
			venue = EXCLUDED.venue,
			product = EXCLUDED.product,
			environment = EXCLUDED.environment,
			settlement = EXCLUDED.settlement,
			config_hash = EXCLUDED.config_hash,
			status = EXCLUDED.status,
			updated_at = now()
	`, registration.AccountID, registration.Venue, registration.Product, registration.Environment, registration.Settlement, registration.ConfigHash, registration.Status)
	if err != nil {
		return redactErr("register account", err)
	}
	return nil
}
