package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"

	"github.com/QuantProcessing/boltertrader/services/trading/accountpositions"
)

type AccountPositionStore struct{ db *sql.DB }

func NewAccountPositionStore(db *sql.DB) *AccountPositionStore {
	if db == nil {
		return nil
	}
	return &AccountPositionStore{db: db}
}

func (store *AccountPositionStore) LoadPrincipal(ctx context.Context, principal string) ([]accountpositions.AccountState, []accountpositions.Item, error) {
	stateRows, err := store.db.QueryContext(ctx, `SELECT state FROM account_position_states WHERE principal_id=$1 ORDER BY account_id`, principal)
	if err != nil {
		return nil, nil, fmt.Errorf("list account position states: %w", err)
	}
	states := make([]accountpositions.AccountState, 0)
	for stateRows.Next() {
		var encoded []byte
		if err := stateRows.Scan(&encoded); err != nil {
			stateRows.Close()
			return nil, nil, fmt.Errorf("scan account position state: %w", err)
		}
		var state accountpositions.AccountState
		if err := json.Unmarshal(encoded, &state); err != nil {
			stateRows.Close()
			return nil, nil, fmt.Errorf("decode account position state: %w", err)
		}
		states = append(states, state)
	}
	if err := stateRows.Err(); err != nil {
		stateRows.Close()
		return nil, nil, fmt.Errorf("scan account position states: %w", err)
	}
	if err := stateRows.Close(); err != nil {
		return nil, nil, fmt.Errorf("close account position states: %w", err)
	}
	itemRows, err := store.db.QueryContext(ctx, `SELECT item FROM account_position_rows WHERE principal_id=$1 ORDER BY account_id,route_id,instrument_id,position_slot`, principal)
	if err != nil {
		return nil, nil, fmt.Errorf("list account position rows: %w", err)
	}
	defer itemRows.Close()
	items := make([]accountpositions.Item, 0)
	for itemRows.Next() {
		var encoded []byte
		if err := itemRows.Scan(&encoded); err != nil {
			return nil, nil, fmt.Errorf("scan account position row: %w", err)
		}
		var item accountpositions.Item
		if err := json.Unmarshal(encoded, &item); err != nil {
			return nil, nil, fmt.Errorf("decode account position row: %w", err)
		}
		items = append(items, item)
	}
	if err := itemRows.Err(); err != nil {
		return nil, nil, fmt.Errorf("scan account position rows: %w", err)
	}
	return states, items, nil
}

func (store *AccountPositionStore) ReplaceAccount(ctx context.Context, principal string, state accountpositions.AccountState, items []accountpositions.Item) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin account position replacement: %w", err)
	}
	defer tx.Rollback()
	if err := upsertAccountPositionState(ctx, tx, principal, state); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM account_position_rows WHERE principal_id=$1 AND account_id=$2`, principal, state.AccountID); err != nil {
		return fmt.Errorf("clear account position rows: %w", err)
	}
	for _, item := range items {
		if err := upsertAccountPositionItem(ctx, tx, principal, item); err != nil {
			return err
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit account position replacement: %w", err)
	}
	return nil
}

func (store *AccountPositionStore) Apply(ctx context.Context, principal string, state accountpositions.AccountState, operation accountpositions.Operation, item accountpositions.Item) error {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin account position mutation: %w", err)
	}
	defer tx.Rollback()
	if err := upsertAccountPositionState(ctx, tx, principal, state); err != nil {
		return err
	}
	switch operation {
	case accountpositions.OperationUpsert:
		if err := upsertAccountPositionItem(ctx, tx, principal, item); err != nil {
			return err
		}
	case accountpositions.OperationRemove:
		if _, err := tx.ExecContext(ctx, `DELETE FROM account_position_rows WHERE principal_id=$1 AND account_id=$2 AND route_id=$3 AND instrument_id=$4 AND position_slot=$5`, principal, item.AccountID, item.RouteID, item.InstrumentID, item.PositionSlot); err != nil {
			return fmt.Errorf("remove account position row: %w", err)
		}
	default:
		return fmt.Errorf("account position operation is invalid")
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit account position mutation: %w", err)
	}
	return nil
}

func (store *AccountPositionStore) RemoveAccount(ctx context.Context, principal, accountID string) error {
	if _, err := store.db.ExecContext(ctx, `DELETE FROM account_position_states WHERE principal_id=$1 AND account_id=$2`, principal, accountID); err != nil {
		return fmt.Errorf("remove account position state: %w", err)
	}
	return nil
}

func upsertAccountPositionState(ctx context.Context, executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, principal string, state accountpositions.AccountState) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode account position state: %w", err)
	}
	if _, err := executor.ExecContext(ctx, `INSERT INTO account_position_states(principal_id,account_id,state,updated_at)
		VALUES ($1,$2,$3,$4) ON CONFLICT (principal_id,account_id) DO UPDATE SET state=EXCLUDED.state,updated_at=EXCLUDED.updated_at`, principal, state.AccountID, encoded, state.ReceivedAt); err != nil {
		return fmt.Errorf("upsert account position state: %w", err)
	}
	return nil
}

func upsertAccountPositionItem(ctx context.Context, executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, principal string, item accountpositions.Item) error {
	encoded, err := json.Marshal(item)
	if err != nil {
		return fmt.Errorf("encode account position row: %w", err)
	}
	if _, err := executor.ExecContext(ctx, `INSERT INTO account_position_rows(principal_id,account_id,route_id,instrument_id,position_slot,item,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (principal_id,account_id,route_id,instrument_id,position_slot)
		DO UPDATE SET item=EXCLUDED.item,updated_at=EXCLUDED.updated_at`, principal, item.AccountID, item.RouteID, item.InstrumentID, item.PositionSlot, encoded, item.ReceivedAt); err != nil {
		return fmt.Errorf("upsert account position row: %w", err)
	}
	return nil
}
