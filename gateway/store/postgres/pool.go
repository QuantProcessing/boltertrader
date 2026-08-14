package postgres

import (
	"context"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/jackc/pgx/v5/pgxpool"
)

type storeOptions struct {
	identity                            *domain.IdentityGenerator
	beforeCommit                        func(context.Context) error
	beforeReserveInsert                 func(context.Context) error
	afterRotationFinalizationReplayMiss func(context.Context) error
}

type Store struct {
	pool                                *pgxpool.Pool
	identity                            *domain.IdentityGenerator
	beforeCommit                        func(context.Context) error
	beforeReserveInsert                 func(context.Context) error
	afterRotationFinalizationReplayMiss func(context.Context) error
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
		pool:                                pool,
		identity:                            identity,
		beforeCommit:                        options.beforeCommit,
		beforeReserveInsert:                 options.beforeReserveInsert,
		afterRotationFinalizationReplayMiss: options.afterRotationFinalizationReplayMiss,
	}
}
