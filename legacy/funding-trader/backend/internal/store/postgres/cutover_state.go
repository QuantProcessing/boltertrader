package postgres

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/cutover"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
)

type CutoverStateStore struct {
	pool *pgxpool.Pool
}

func NewCutoverStateStore(pool *pgxpool.Pool) *CutoverStateStore {
	return &CutoverStateStore{pool: pool}
}

func (store *CutoverStateStore) State(ctx context.Context) (cutover.State, error) {
	if store == nil || store.pool == nil {
		return cutover.State{}, fmt.Errorf("%w: cutover state store is unavailable", cutover.ErrInvalidTransition)
	}
	var state cutover.State
	if err := store.pool.QueryRow(ctx, `
		select
		  phase,
		  contract_version,
		  backup_id,
		  backup_verified_at,
		  normal_traffic_reopened,
		  new_contract_write_accepted
		from cutover_control
		where singleton
	`).Scan(
		&state.Phase,
		&state.SchemaVersion,
		&state.BackupID,
		&state.BackupVerifiedAt,
		&state.NormalTrafficReopened,
		&state.NewContractWriteAccepted,
	); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return cutover.State{}, cutover.ErrInvalidTransition
		}
		return cutover.State{}, err
	}
	state.BackupVerifiedAt = state.BackupVerifiedAt.UTC()
	return state, nil
}

func (store *CutoverStateStore) MarkTrafficReopened(ctx context.Context) error {
	tag, err := store.pool.Exec(ctx, `
		update cutover_control
		set
		  phase='reopened',
		  normal_traffic_reopened=true,
		  updated_at=now()
		where singleton
		  and phase in ('validated','maintenance_smoke')
		  and not normal_traffic_reopened
		  and not new_contract_write_accepted
	`)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return cutover.ErrInvalidTransition
	}
	return nil
}

func (store *CutoverStateStore) MarkWriteAccepted(ctx context.Context) error {
	tag, err := store.pool.Exec(ctx, `
		update cutover_control
		set
		  phase='write_accepted',
		  new_contract_write_accepted=true,
		  updated_at=now()
		where singleton
		  and phase in ('validated','maintenance_smoke','reopened')
		  and not new_contract_write_accepted
	`)
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return cutover.ErrInvalidTransition
	}
	return nil
}

func (store *CutoverStateStore) AuthorizeRestore(ctx context.Context, backupID string) error {
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)

	var storedBackup, phase string
	var reopened, writeAccepted bool
	if err := tx.QueryRow(ctx, `
		select backup_id, phase, normal_traffic_reopened, new_contract_write_accepted
		from cutover_control
		where singleton
		for update
	`).Scan(&storedBackup, &phase, &reopened, &writeAccepted); err != nil {
		if errors.Is(err, pgx.ErrNoRows) {
			return cutover.ErrInvalidTransition
		}
		return err
	}
	if backupID != storedBackup {
		return cutover.ErrBackupMismatch
	}
	if (phase != "validated" && phase != "maintenance_smoke") ||
		reopened || writeAccepted {
		return cutover.ErrRestoreForbidden
	}
	if _, err := tx.Exec(ctx, `
		update cutover_control
		set phase='restore_authorized', updated_at=$1
		where singleton
	`, time.Now().UTC()); err != nil {
		return err
	}
	return tx.Commit(ctx)
}
