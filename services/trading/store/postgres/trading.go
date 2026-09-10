package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
	"github.com/shopspring/decimal"
)

type CommandStore struct{ db *sql.DB }
type LedgerStore struct{ db *sql.DB }
type PortfolioStore struct{ db *sql.DB }

func NewCommandStore(db *sql.DB) *CommandStore {
	if db == nil {
		return nil
	}
	return &CommandStore{db: db}
}
func NewLedgerStore(db *sql.DB) *LedgerStore {
	if db == nil {
		return nil
	}
	return &LedgerStore{db: db}
}
func NewPortfolioStore(db *sql.DB) *PortfolioStore {
	if db == nil {
		return nil
	}
	return &PortfolioStore{db: db}
}

func (store *PortfolioStore) ApplyFill(ctx context.Context, fill portfolio.Fill, reduce func(portfolio.Position) (portfolio.Position, portfolio.FillResult, error)) (portfolio.Position, portfolio.FillResult, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return portfolio.Position{}, portfolio.FillResult{}, fmt.Errorf("begin portfolio fill: %w", err)
	}
	defer tx.Rollback()

	fingerprint := portfolio.FillFingerprint(fill)
	insert, err := tx.ExecContext(ctx, `INSERT INTO portfolio_fills
		(principal_id,fill_id,fill_hash,command_id,product_id,lineage_id,account_id,instrument_id,side,quantity,price,fee,fee_currency,settlement_currency,realized_pnl,occurred_at,instrument_product,base_asset)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,0,$15,$16,$17) ON CONFLICT (principal_id,account_id,instrument_id,fill_id) DO NOTHING`,
		fill.PrincipalID, fill.FillID, fingerprint, fill.CommandID, fill.ProductID, fill.LineageID, fill.AccountID, fill.InstrumentID,
		fill.Side, fill.Quantity, fill.Price, fill.Fee, fill.FeeCurrency, fill.SettlementCurrency, fill.OccurredAt, fill.InstrumentProduct, fill.BaseAsset)
	if err != nil {
		return portfolio.Position{}, portfolio.FillResult{}, fmt.Errorf("claim portfolio fill: %w", err)
	}
	inserted, err := insert.RowsAffected()
	if err != nil {
		return portfolio.Position{}, portfolio.FillResult{}, fmt.Errorf("inspect portfolio fill claim: %w", err)
	}
	if inserted == 0 {
		var recordedHash string
		var result portfolio.FillResult
		if err := tx.QueryRowContext(ctx, `SELECT fill_hash,realized_pnl::text,fee::text,settlement_currency FROM portfolio_fills WHERE principal_id=$1 AND fill_id=$2 AND account_id=$3 AND instrument_id=$4`, fill.PrincipalID, fill.FillID, fill.AccountID, fill.InstrumentID).Scan(&recordedHash, &result.RealizedPnL, &result.Fee, &result.SettlementCurrency); err != nil {
			return portfolio.Position{}, portfolio.FillResult{}, fmt.Errorf("load portfolio fill replay: %w", err)
		}
		if !portfolio.ReplayCompatible(recordedHash, fill) {
			return portfolio.Position{}, portfolio.FillResult{}, fmt.Errorf("%w: %s", portfolio.ErrFillConflict, fill.FillID)
		}
		position, err := loadPortfolioPosition(ctx, tx, fill, false)
		if err != nil {
			return portfolio.Position{}, portfolio.FillResult{}, fmt.Errorf("load portfolio fill replay position: %w", err)
		}
		if err := tx.Commit(); err != nil {
			return portfolio.Position{}, portfolio.FillResult{}, fmt.Errorf("commit portfolio fill replay: %w", err)
		}
		return position, result, nil
	}

	if _, err := tx.ExecContext(ctx, `INSERT INTO positions
		(principal_id,product_id,lineage_id,account_id,instrument_id,settlement_currency,quantity,average_entry_price,mark_price,realized_pnl,unrealized_pnl,mark_observed_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,0,0,0,0,0,$7,$8) ON CONFLICT (principal_id,product_id,lineage_id,account_id,instrument_id) DO NOTHING`,
		fill.PrincipalID, fill.ProductID, fill.LineageID, fill.AccountID, fill.InstrumentID, fill.SettlementCurrency, time.Time{}, fill.OccurredAt); err != nil {
		return portfolio.Position{}, portfolio.FillResult{}, fmt.Errorf("initialize portfolio position: %w", err)
	}
	current, err := loadPortfolioPosition(ctx, tx, fill, true)
	if err != nil {
		return portfolio.Position{}, portfolio.FillResult{}, fmt.Errorf("hydrate portfolio position: %w", err)
	}
	position, result, err := reduce(current)
	if err != nil {
		return portfolio.Position{}, portfolio.FillResult{}, err
	}
	if err := upsertPortfolioPosition(ctx, tx, position); err != nil {
		return portfolio.Position{}, portfolio.FillResult{}, err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE portfolio_fills SET realized_pnl=$3,fee=$4 WHERE principal_id=$1 AND fill_id=$2 AND account_id=$5 AND instrument_id=$6`, fill.PrincipalID, fill.FillID, result.RealizedPnL, result.Fee, fill.AccountID, fill.InstrumentID); err != nil {
		return portfolio.Position{}, portfolio.FillResult{}, fmt.Errorf("persist portfolio fill result: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return portfolio.Position{}, portfolio.FillResult{}, fmt.Errorf("commit portfolio fill: %w", err)
	}
	return position, result, nil
}

func (store *PortfolioStore) ApplyMark(ctx context.Context, principalID string, accountID, productID, lineageID, instrumentID string, reduce func(portfolio.Position) (portfolio.Position, error)) (portfolio.Position, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return portfolio.Position{}, fmt.Errorf("begin portfolio mark: %w", err)
	}
	defer tx.Rollback()
	identity := portfolio.Fill{
		PrincipalID: principalID, AccountID: accountID, ProductID: productID, LineageID: lineageID, InstrumentID: instrumentID,
	}
	current, err := loadPortfolioPosition(ctx, tx, identity, true)
	if errors.Is(err, sql.ErrNoRows) {
		return portfolio.Position{}, fmt.Errorf("position not found")
	}
	if err != nil {
		return portfolio.Position{}, fmt.Errorf("hydrate portfolio mark position: %w", err)
	}
	position, err := reduce(current)
	if err != nil {
		return portfolio.Position{}, err
	}
	if err := upsertPortfolioPosition(ctx, tx, position); err != nil {
		return portfolio.Position{}, err
	}
	if err := tx.Commit(); err != nil {
		return portfolio.Position{}, fmt.Errorf("commit portfolio mark: %w", err)
	}
	return position, nil
}

func loadPortfolioPosition(ctx context.Context, queryer interface {
	QueryRowContext(context.Context, string, ...any) *sql.Row
}, fill portfolio.Fill, forUpdate bool) (portfolio.Position, error) {
	query := `SELECT principal_id,product_id,lineage_id,account_id,instrument_id,settlement_currency,quantity::text,average_entry_price::text,mark_price::text,realized_pnl::text,unrealized_pnl::text,mark_observed_at,updated_at,accounting_status
		FROM positions WHERE principal_id=$1 AND product_id=$2 AND lineage_id=$3 AND account_id=$4 AND instrument_id=$5`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var item portfolio.Position
	err := queryer.QueryRowContext(ctx, query, fill.PrincipalID, fill.ProductID, fill.LineageID, fill.AccountID, fill.InstrumentID).Scan(
		&item.PrincipalID, &item.ProductID, &item.LineageID, &item.AccountID, &item.InstrumentID, &item.SettlementCurrency, &item.Quantity,
		&item.AverageEntryPrice, &item.MarkPrice, &item.RealizedPnL, &item.UnrealizedPnL, &item.MarkObservedAt, &item.UpdatedAt, &item.AccountingStatus,
	)
	return item, err
}

func upsertPortfolioPosition(ctx context.Context, executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, item portfolio.Position) error {
	_, err := executor.ExecContext(ctx, `INSERT INTO positions(principal_id,product_id,lineage_id,account_id,instrument_id,settlement_currency,quantity,average_entry_price,mark_price,realized_pnl,unrealized_pnl,mark_observed_at,updated_at,accounting_status)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT (principal_id,product_id,lineage_id,account_id,instrument_id) DO UPDATE SET settlement_currency=EXCLUDED.settlement_currency,quantity=EXCLUDED.quantity,average_entry_price=EXCLUDED.average_entry_price,mark_price=EXCLUDED.mark_price,realized_pnl=EXCLUDED.realized_pnl,unrealized_pnl=EXCLUDED.unrealized_pnl,mark_observed_at=EXCLUDED.mark_observed_at,updated_at=EXCLUDED.updated_at,accounting_status=EXCLUDED.accounting_status`, item.PrincipalID, item.ProductID, item.LineageID, item.AccountID, item.InstrumentID, item.SettlementCurrency, item.Quantity, item.AverageEntryPrice, item.MarkPrice, item.RealizedPnL, item.UnrealizedPnL, item.MarkObservedAt, item.UpdatedAt, item.AccountingStatus)
	if err != nil {
		return fmt.Errorf("upsert portfolio position: %w", err)
	}
	return nil
}

func (store *PortfolioStore) Upsert(ctx context.Context, item portfolio.Position) error {
	return upsertPortfolioPosition(ctx, store.db, item)
}
func (store *PortfolioStore) List(ctx context.Context, principalID string, accountID, productID string) ([]portfolio.Position, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT principal_id,product_id,lineage_id,account_id,instrument_id,settlement_currency,quantity::text,average_entry_price::text,mark_price::text,realized_pnl::text,unrealized_pnl::text,mark_observed_at,updated_at,accounting_status FROM positions WHERE principal_id=$1 AND ($2='' OR account_id=$2) AND ($3='' OR product_id=$3) ORDER BY product_id,lineage_id,account_id,instrument_id`, principalID, accountID, productID)
	if err != nil {
		return nil, fmt.Errorf("list portfolio positions: %w", err)
	}
	defer rows.Close()
	items := make([]portfolio.Position, 0)
	for rows.Next() {
		var item portfolio.Position
		if err := rows.Scan(&item.PrincipalID, &item.ProductID, &item.LineageID, &item.AccountID, &item.InstrumentID, &item.SettlementCurrency, &item.Quantity, &item.AverageEntryPrice, &item.MarkPrice, &item.RealizedPnL, &item.UnrealizedPnL, &item.MarkObservedAt, &item.UpdatedAt, &item.AccountingStatus); err != nil {
			return nil, fmt.Errorf("scan portfolio position: %w", err)
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

// Create persists command intent and its initial recovery task atomically.
func (store *CommandStore) Create(ctx context.Context, item orders.Command) (orders.Command, bool, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return orders.Command{}, false, fmt.Errorf("begin command")
	}
	defer tx.Rollback()
	blockers, _ := json.Marshal(item.Blockers)
	result, err := tx.ExecContext(ctx, `INSERT INTO commands
		(command_id,principal_id,account_id,product_id,lineage_id,reservation_id,idempotency_key,method,path,command_kind,state,payload,payload_hash,blockers,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		ON CONFLICT (principal_id,method,path,idempotency_key) DO NOTHING`, item.CommandID, item.PrincipalID, item.AccountID, item.ProductID, item.LineageID, nullString(item.ReservationID), item.IdempotencyKey, item.Method, item.Path, item.CommandKind, item.State, item.Payload, item.PayloadHash, blockers, item.CreatedAt, item.UpdatedAt)
	if err != nil {
		return orders.Command{}, false, fmt.Errorf("insert command")
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		existing, loadErr := scanCommand(tx.QueryRowContext(ctx, commandProjection+` WHERE principal_id=$1 AND method=$2 AND path=$3 AND idempotency_key=$4`, item.PrincipalID, item.Method, item.Path, item.IdempotencyKey))
		if loadErr != nil {
			return orders.Command{}, true, loadErr
		}
		if !sameCommandAuthority(existing, item) {
			return existing, true, orders.ErrIdempotencyConflict
		}
		return existing, true, nil
	}
	if item.State == orders.StateAccepted {
		if _, err := tx.ExecContext(ctx, `INSERT INTO reconciliation_tasks(command_id,reason,next_attempt_at) VALUES ($1,$2,$3)`, item.CommandID, orders.ReconciliationAwaitingHandoff, item.CreatedAt.Add(orders.HandoffStartWindow)); err != nil {
			return orders.Command{}, false, fmt.Errorf("persist initial reconciliation task: %w", err)
		}
	}
	item, err = scanCommand(tx.QueryRowContext(ctx, commandProjection+` WHERE command_id=$1`, item.CommandID))
	if err != nil {
		return orders.Command{}, false, err
	}
	if err := tx.Commit(); err != nil {
		return orders.Command{}, false, fmt.Errorf("commit command")
	}
	return item, false, nil
}

func sameCommandAuthority(existing, incoming orders.Command) bool {
	return existing.PayloadHash == incoming.PayloadHash && existing.AccountID == incoming.AccountID &&
		existing.ProductID == incoming.ProductID && existing.LineageID == incoming.LineageID &&
		existing.CommandKind == incoming.CommandKind
}

func (store *CommandStore) Get(ctx context.Context, id string) (orders.Command, error) {
	return scanCommand(store.db.QueryRowContext(ctx, commandProjection+` WHERE command_id=$1`, id))
}

func (store *CommandStore) ListOwned(ctx context.Context, principal string, productID string) ([]orders.Command, error) {
	rows, err := store.db.QueryContext(ctx, commandProjection+` WHERE principal_id=$1 AND product_id=$2 ORDER BY created_at,command_id`, principal, productID)
	if err != nil {
		return nil, fmt.Errorf("list owned commands")
	}
	defer rows.Close()
	items := make([]orders.Command, 0)
	for rows.Next() {
		item, scanErr := scanCommand(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (store *CommandStore) Transition(ctx context.Context, request orders.TransitionRequest, now time.Time) (orders.Command, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return orders.Command{}, fmt.Errorf("begin command transition")
	}
	defer tx.Rollback()
	current, err := scanCommand(tx.QueryRowContext(ctx, commandProjection+` WHERE command_id=$1 FOR UPDATE`, request.CommandID))
	if err != nil {
		return orders.Command{}, err
	}
	item, err := orders.ReduceTransition(current, request, now)
	if err != nil {
		return current, err
	}
	evidence, err := json.Marshal(item.ExecutionEvidence)
	if err != nil {
		return current, fmt.Errorf("encode command evidence")
	}
	blockers, _ := json.Marshal(item.Blockers)
	if _, err := tx.ExecContext(ctx, `UPDATE commands SET state=$2,updated_at=$3,execution_evidence=$4::jsonb,blockers=$5::jsonb WHERE command_id=$1`, item.CommandID, item.State, item.UpdatedAt, evidence, blockers); err != nil {
		return current, fmt.Errorf("persist command transition")
	}
	if request.State == orders.StateUncertain || request.State == orders.StateSubmitted {
		_, err = tx.ExecContext(ctx, `INSERT INTO reconciliation_tasks(command_id,reason,next_attempt_at) VALUES ($1,$2,$3)
			ON CONFLICT (command_id) DO UPDATE SET reason=EXCLUDED.reason,next_attempt_at=EXCLUDED.next_attempt_at,lease_until=NULL`, item.CommandID, request.Reason, request.NextAttemptAt)
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM reconciliation_tasks WHERE command_id=$1`, item.CommandID)
	}
	if err != nil {
		return orders.Command{}, fmt.Errorf("persist reconciliation transition")
	}
	item, err = scanCommand(tx.QueryRowContext(ctx, commandProjection+` WHERE command_id=$1`, item.CommandID))
	if err != nil {
		return orders.Command{}, err
	}
	if err := tx.Commit(); err != nil {
		return orders.Command{}, fmt.Errorf("commit command transition")
	}
	return item, nil
}

func (store *CommandStore) ClaimDue(ctx context.Context, now, leaseUntil time.Time, limit int) ([]orders.ReconciliationTask, error) {
	rows, err := store.db.QueryContext(ctx, `WITH due AS (SELECT command_id FROM reconciliation_tasks WHERE next_attempt_at<=$1 AND (lease_until IS NULL OR lease_until<=$1) ORDER BY next_attempt_at FOR UPDATE SKIP LOCKED LIMIT $2)
		UPDATE reconciliation_tasks r SET lease_until=$3,attempt_count=r.attempt_count+1 FROM due WHERE r.command_id=due.command_id RETURNING r.command_id,r.reason,r.next_attempt_at,r.lease_until,r.attempt_count`, now, limit, leaseUntil)
	if err != nil {
		return nil, fmt.Errorf("claim reconciliation tasks")
	}
	defer rows.Close()
	items := make([]orders.ReconciliationTask, 0)
	for rows.Next() {
		var item orders.ReconciliationTask
		if err := rows.Scan(&item.CommandID, &item.Reason, &item.NextAttempt, &item.LeaseUntil, &item.AttemptCount); err != nil {
			return nil, fmt.Errorf("scan reconciliation task")
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

const commandProjection = `SELECT command_id,principal_id,account_id,product_id,lineage_id,reservation_id,idempotency_key,method,path,command_kind,state,payload,payload_hash,blockers,created_at,updated_at,execution_evidence,
 (SELECT jsonb_build_object('reason',r.reason,'nextAttemptAt',r.next_attempt_at,'attemptCount',r.attempt_count) FROM reconciliation_tasks r WHERE r.command_id=commands.command_id) FROM commands`

func scanCommand(row scanner) (orders.Command, error) {
	var item orders.Command
	var reservation sql.NullString
	var blockers []byte
	var evidence []byte
	var reconciliation []byte
	err := row.Scan(&item.CommandID, &item.PrincipalID, &item.AccountID, &item.ProductID, &item.LineageID, &reservation, &item.IdempotencyKey, &item.Method, &item.Path, &item.CommandKind, &item.State, &item.Payload, &item.PayloadHash, &blockers, &item.CreatedAt, &item.UpdatedAt, &evidence, &reconciliation)
	if errors.Is(err, sql.ErrNoRows) {
		return item, orders.ErrNotFound
	}
	if err != nil || json.Unmarshal(blockers, &item.Blockers) != nil || json.Unmarshal(evidence, &item.ExecutionEvidence) != nil {
		return item, fmt.Errorf("scan command")
	}
	if len(reconciliation) > 0 && json.Unmarshal(reconciliation, &item.Reconciliation) != nil {
		return item, fmt.Errorf("scan reconciliation status")
	}
	item.ReservationID = stringPointer(reservation)
	return item, nil
}

func (store *LedgerStore) Append(ctx context.Context, item ledger.Entry) error {
	item.OccurredAt = postgresTimestamp(item.OccurredAt)
	result, err := store.db.ExecContext(ctx, `INSERT INTO ledger_entries(principal_id,entry_id,product_id,lineage_id,account_id,instrument_id,kind,amount,currency,occurred_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (principal_id,account_id,instrument_id,entry_id) DO NOTHING`, item.PrincipalID, item.EntryID, item.ProductID, item.LineageID, item.AccountID, item.InstrumentID, item.Kind, item.Amount, item.Currency, item.OccurredAt)
	if err != nil {
		return fmt.Errorf("append ledger entry")
	}
	rows, _ := result.RowsAffected()
	if rows == 1 {
		return nil
	}
	var existing ledger.Entry
	err = store.db.QueryRowContext(ctx, `SELECT principal_id,entry_id,product_id,lineage_id,account_id,instrument_id,kind,amount::text,currency,occurred_at FROM ledger_entries WHERE principal_id=$1 AND entry_id=$2 AND account_id=$3 AND instrument_id=$4`, item.PrincipalID, item.EntryID, item.AccountID, item.InstrumentID).Scan(&existing.PrincipalID, &existing.EntryID, &existing.ProductID, &existing.LineageID, &existing.AccountID, &existing.InstrumentID, &existing.Kind, &existing.Amount, &existing.Currency, &existing.OccurredAt)
	if err != nil {
		return fmt.Errorf("load ledger replay")
	}
	if !sameLedgerEntry(existing, item) {
		return ledger.ErrEntryConflict
	}
	return nil
}
func (store *LedgerStore) SupersedeFunding(ctx context.Context, priorEntryID string, replacement ledger.Entry) error {
	replacement.OccurredAt = postgresTimestamp(replacement.OccurredAt)
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin funding supersession: %w", err)
	}
	defer tx.Rollback()
	var existing ledger.Entry
	err = tx.QueryRowContext(ctx, `SELECT principal_id,entry_id,product_id,lineage_id,account_id,instrument_id,kind,amount::text,currency,occurred_at FROM ledger_entries WHERE principal_id=$1 AND entry_id=$2 AND account_id=$3 AND instrument_id=$4 FOR UPDATE`, replacement.PrincipalID, replacement.EntryID, replacement.AccountID, replacement.InstrumentID).Scan(&existing.PrincipalID, &existing.EntryID, &existing.ProductID, &existing.LineageID, &existing.AccountID, &existing.InstrumentID, &existing.Kind, &existing.Amount, &existing.Currency, &existing.OccurredAt)
	if err == nil {
		if !sameLedgerEntry(existing, replacement) {
			return ledger.ErrEntryConflict
		}
		var prior ledger.Entry
		err = tx.QueryRowContext(ctx, `SELECT principal_id,entry_id,product_id,lineage_id,account_id,instrument_id,kind,amount::text,currency,occurred_at FROM ledger_entries WHERE principal_id=$1 AND entry_id=$2 AND account_id=$3 AND instrument_id=$4 FOR UPDATE`, replacement.PrincipalID, priorEntryID, replacement.AccountID, replacement.InstrumentID).Scan(&prior.PrincipalID, &prior.EntryID, &prior.ProductID, &prior.LineageID, &prior.AccountID, &prior.InstrumentID, &prior.Kind, &prior.Amount, &prior.Currency, &prior.OccurredAt)
		if err == nil {
			if !sameLedgerFundingScope(prior, replacement) {
				return ledger.ErrEntryConflict
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM ledger_entries WHERE principal_id=$1 AND entry_id=$2 AND account_id=$3 AND instrument_id=$4`, replacement.PrincipalID, priorEntryID, replacement.AccountID, replacement.InstrumentID); err != nil {
				return fmt.Errorf("remove replayed superseded funding entry: %w", err)
			}
		} else if !errors.Is(err, sql.ErrNoRows) {
			return fmt.Errorf("load replayed superseded funding entry: %w", err)
		}
		return tx.Commit()
	}
	if !errors.Is(err, sql.ErrNoRows) {
		return fmt.Errorf("load funding supersession replay: %w", err)
	}
	var prior ledger.Entry
	err = tx.QueryRowContext(ctx, `SELECT principal_id,entry_id,product_id,lineage_id,account_id,instrument_id,kind,amount::text,currency,occurred_at FROM ledger_entries WHERE principal_id=$1 AND entry_id=$2 AND account_id=$3 AND instrument_id=$4 FOR UPDATE`, replacement.PrincipalID, priorEntryID, replacement.AccountID, replacement.InstrumentID).Scan(&prior.PrincipalID, &prior.EntryID, &prior.ProductID, &prior.LineageID, &prior.AccountID, &prior.InstrumentID, &prior.Kind, &prior.Amount, &prior.Currency, &prior.OccurredAt)
	if err != nil || !sameLedgerFundingScope(prior, replacement) {
		return ledger.ErrEntryConflict
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM ledger_entries WHERE principal_id=$1 AND entry_id=$2 AND account_id=$3 AND instrument_id=$4`, replacement.PrincipalID, priorEntryID, replacement.AccountID, replacement.InstrumentID); err != nil {
		return fmt.Errorf("remove superseded funding entry: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ledger_entries(principal_id,entry_id,product_id,lineage_id,account_id,instrument_id,kind,amount,currency,occurred_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)`, replacement.PrincipalID, replacement.EntryID, replacement.ProductID, replacement.LineageID, replacement.AccountID, replacement.InstrumentID, replacement.Kind, replacement.Amount, replacement.Currency, replacement.OccurredAt); err != nil {
		return fmt.Errorf("insert funding replacement: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit funding supersession: %w", err)
	}
	return nil
}
func (store *LedgerStore) Entries(ctx context.Context, q ledger.Query) ([]ledger.Entry, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT principal_id,entry_id,product_id,lineage_id,account_id,instrument_id,kind,amount::text,currency,occurred_at FROM ledger_entries WHERE principal_id=$1 AND ($2='' OR product_id=$2) AND ($3='' OR lineage_id=$3) AND ($4='' OR account_id=$4) AND ($5='' OR instrument_id=$5) ORDER BY occurred_at,entry_id`, q.PrincipalID, q.ProductID, q.LineageID, q.AccountID, q.InstrumentID)
	if err != nil {
		return nil, fmt.Errorf("list ledger entries")
	}
	defer rows.Close()
	items := make([]ledger.Entry, 0)
	for rows.Next() {
		var item ledger.Entry
		if err := rows.Scan(&item.PrincipalID, &item.EntryID, &item.ProductID, &item.LineageID, &item.AccountID, &item.InstrumentID, &item.Kind, &item.Amount, &item.Currency, &item.OccurredAt); err != nil {
			return nil, fmt.Errorf("scan ledger entry")
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (store *LedgerStore) SetUnrealized(ctx context.Context, v ledger.Unrealized) error {
	_, err := store.db.ExecContext(ctx, `INSERT INTO unrealized_pnl(principal_id,product_id,lineage_id,account_id,instrument_id,amount,currency,as_of) VALUES ($1,$2,$3,$4,$5,$6,$7,$8) ON CONFLICT (principal_id,product_id,lineage_id,account_id,instrument_id) DO UPDATE SET amount=EXCLUDED.amount,currency=EXCLUDED.currency,as_of=EXCLUDED.as_of WHERE unrealized_pnl.as_of < EXCLUDED.as_of`, v.PrincipalID, v.ProductID, v.LineageID, v.AccountID, v.InstrumentID, v.Amount, v.Currency, v.AsOf)
	if err != nil {
		return fmt.Errorf("set unrealized pnl")
	}
	return nil
}

func sameLedgerFundingScope(left, right ledger.Entry) bool {
	return left.Kind == ledger.KindFunding && right.Kind == ledger.KindFunding && left.PrincipalID == right.PrincipalID && left.ProductID == right.ProductID && left.LineageID == right.LineageID && left.AccountID == right.AccountID && left.InstrumentID == right.InstrumentID && left.Currency == right.Currency
}

func sameLedgerEntry(left, right ledger.Entry) bool {
	leftAmount, leftErr := decimal.NewFromString(left.Amount)
	rightAmount, rightErr := decimal.NewFromString(right.Amount)
	return leftErr == nil && rightErr == nil && leftAmount.Equal(rightAmount) && postgresTimestamp(left.OccurredAt).Equal(postgresTimestamp(right.OccurredAt)) &&
		left.PrincipalID == right.PrincipalID && left.EntryID == right.EntryID && left.ProductID == right.ProductID && left.LineageID == right.LineageID &&
		left.AccountID == right.AccountID && left.InstrumentID == right.InstrumentID && left.Kind == right.Kind && left.Currency == right.Currency
}

func postgresTimestamp(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

func (store *LedgerStore) Unrealized(ctx context.Context, q ledger.Query) (decimal.Decimal, string, time.Time, error) {
	var amount, currency string
	var asOf time.Time
	err := store.db.QueryRowContext(ctx, `SELECT amount::text,currency,as_of FROM unrealized_pnl WHERE principal_id=$1 AND product_id=$2 AND lineage_id=$3 AND account_id=$4 AND instrument_id=$5`, q.PrincipalID, q.ProductID, q.LineageID, q.AccountID, q.InstrumentID).Scan(&amount, &currency, &asOf)
	if errors.Is(err, sql.ErrNoRows) {
		return decimal.Zero, "", time.Time{}, nil
	}
	if err != nil {
		return decimal.Zero, "", time.Time{}, fmt.Errorf("load unrealized pnl")
	}
	if currency == "" {
		return decimal.Zero, "", time.Time{}, fmt.Errorf("persisted unrealized currency is missing")
	}
	value, err := decimal.NewFromString(amount)
	if err != nil {
		return decimal.Zero, "", time.Time{}, fmt.Errorf("decode unrealized pnl")
	}
	return value, currency, asOf, nil
}

func nullString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}
func stringPointer(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	result := value.String
	return &result
}
func pointerValue(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}

func (store *CommandStore) Find(ctx context.Context, identity orders.IdempotencyIdentity) (orders.Command, error) {
	return scanCommand(store.db.QueryRowContext(ctx, commandProjection+` WHERE principal_id=$1 AND method=$2 AND path=$3 AND idempotency_key=$4`, identity.PrincipalID, identity.Method, identity.Path, identity.Key))
}

type scanner interface{ Scan(...any) error }
