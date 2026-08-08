package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/work"
)

type FundingExecutionStore struct{ db *sql.DB }

func NewFundingExecutionStore(db *sql.DB) *FundingExecutionStore {
	if db == nil {
		return nil
	}
	return &FundingExecutionStore{db: db}
}

func (store *FundingExecutionStore) ReserveFundingExecution(ctx context.Context, execution work.FundingExecution) (work.FundingExecution, bool, error) {
	execution.Version = 1
	payload, err := json.Marshal(execution)
	if err != nil {
		return work.FundingExecution{}, false, err
	}
	result, err := store.db.ExecContext(ctx, `INSERT INTO platform_funding_executions(execution_id,principal_id,mode,lineage_id,parent_command_id,plan_key,preview_hash,intent_kind,state,version,payload,recovery_cursor,created_at,updated_at)
		VALUES($1,$2,$3,$4,$5,$6,$7,$8,$9,1,$10,$11,$12,$13) ON CONFLICT(principal_id,mode,lineage_id) DO NOTHING`,
		execution.ExecutionID, execution.PrincipalID, execution.Mode, execution.LineageID, execution.ParentCommandID, execution.PlanKey, execution.PreviewHash, execution.IntentKind, execution.State, payload, execution.RecoveryCursor, execution.CreatedAt, execution.UpdatedAt)
	if err != nil {
		return work.FundingExecution{}, false, fmt.Errorf("reserve Funding execution: %w", err)
	}
	rows, _ := result.RowsAffected()
	if rows == 1 {
		return execution, false, nil
	}
	existing, err := store.GetFundingExecution(ctx, execution.PrincipalID, execution.Mode, execution.LineageID)
	if err != nil {
		return work.FundingExecution{}, true, err
	}
	if existing.ExecutionID != execution.ExecutionID || existing.ParentCommandID != execution.ParentCommandID || existing.PlanKey != execution.PlanKey || existing.PreviewHash != execution.PreviewHash || existing.IntentKind != execution.IntentKind {
		return existing, true, work.ErrIdempotencyConflict
	}
	return existing, true, nil
}

func (store *FundingExecutionStore) GetFundingExecution(ctx context.Context, principal string, mode orders.Mode, lineage string) (work.FundingExecution, error) {
	var payload json.RawMessage
	err := store.db.QueryRowContext(ctx, `SELECT payload FROM platform_funding_executions WHERE principal_id=$1 AND mode=$2 AND lineage_id=$3`, principal, mode, lineage).Scan(&payload)
	if errors.Is(err, sql.ErrNoRows) {
		return work.FundingExecution{}, work.ErrWorkRevisionConflict
	}
	if err != nil {
		return work.FundingExecution{}, fmt.Errorf("load Funding execution: %w", err)
	}
	var execution work.FundingExecution
	if err := json.Unmarshal(payload, &execution); err != nil {
		return work.FundingExecution{}, fmt.Errorf("decode Funding execution: %w", err)
	}
	return execution, nil
}

func (store *FundingExecutionStore) PersistFundingExecution(ctx context.Context, execution work.FundingExecution, expectedVersion int64, kind string) (work.FundingExecution, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return work.FundingExecution{}, err
	}
	defer tx.Rollback()
	var currentState work.FundingExecutionState
	var currentVersion int64
	if err := tx.QueryRowContext(ctx, `SELECT state,version FROM platform_funding_executions WHERE principal_id=$1 AND mode=$2 AND lineage_id=$3 FOR UPDATE`, execution.PrincipalID, execution.Mode, execution.LineageID).Scan(&currentState, &currentVersion); err != nil {
		return work.FundingExecution{}, fmt.Errorf("lock Funding execution: %w", err)
	}
	if currentVersion != expectedVersion {
		return work.FundingExecution{}, fmt.Errorf("Funding execution version conflict")
	}
	execution.Version = expectedVersion + 1
	payload, err := json.Marshal(execution)
	if err != nil {
		return work.FundingExecution{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE platform_funding_executions SET state=$4,version=$5,payload=$6,recovery_cursor=$7,updated_at=$8 WHERE principal_id=$1 AND mode=$2 AND lineage_id=$3`, execution.PrincipalID, execution.Mode, execution.LineageID, execution.State, execution.Version, payload, execution.RecoveryCursor, execution.UpdatedAt); err != nil {
		return work.FundingExecution{}, fmt.Errorf("persist Funding execution: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO platform_funding_execution_transitions(execution_id,version,from_state,to_state,kind,payload,occurred_at) VALUES($1,$2,$3,$4,$5,$6,$7)`, execution.ExecutionID, execution.Version, currentState, execution.State, kind, payload, execution.UpdatedAt); err != nil {
		return work.FundingExecution{}, fmt.Errorf("journal Funding execution: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return work.FundingExecution{}, err
	}
	return execution, nil
}
