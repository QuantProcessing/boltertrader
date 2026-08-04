package postgres

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/ledger"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/portfolio"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/reservations"
	"github.com/shopspring/decimal"
)

type ReservationStore struct{ db *sql.DB }
type CommandStore struct{ db *sql.DB }
type LedgerStore struct{ db *sql.DB }
type PortfolioStore struct{ db *sql.DB }

func NewReservationStore(db *sql.DB) *ReservationStore {
	if db == nil {
		return nil
	}
	return &ReservationStore{db: db}
}
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

func (store *PortfolioStore) Upsert(ctx context.Context, item portfolio.Position) error {
	_, err := store.db.ExecContext(ctx, `INSERT INTO platform_positions(mode,product_id,lineage_id,account_id,instrument_id,quantity,average_entry_price,mark_price,realized_pnl,unrealized_pnl,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (mode,product_id,lineage_id,account_id,instrument_id) DO UPDATE SET quantity=EXCLUDED.quantity,average_entry_price=EXCLUDED.average_entry_price,mark_price=EXCLUDED.mark_price,realized_pnl=EXCLUDED.realized_pnl,unrealized_pnl=EXCLUDED.unrealized_pnl,updated_at=EXCLUDED.updated_at`, item.Mode, item.ProductID, item.LineageID, item.AccountID, item.InstrumentID, item.Quantity, item.AverageEntryPrice, item.MarkPrice, item.RealizedPnL, item.UnrealizedPnL, item.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert portfolio position")
	}
	return nil
}
func (store *PortfolioStore) List(ctx context.Context, mode portfolio.Mode, accountID, productID string) ([]portfolio.Position, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT mode,product_id,lineage_id,account_id,instrument_id,quantity::text,average_entry_price::text,mark_price::text,realized_pnl::text,unrealized_pnl::text,updated_at FROM platform_positions WHERE mode=$1 AND ($2='' OR account_id=$2) AND ($3='' OR product_id=$3) ORDER BY product_id,lineage_id,account_id,instrument_id`, mode, accountID, productID)
	if err != nil {
		return nil, fmt.Errorf("list portfolio positions")
	}
	defer rows.Close()
	items := make([]portfolio.Position, 0)
	for rows.Next() {
		var item portfolio.Position
		if err := rows.Scan(&item.Mode, &item.ProductID, &item.LineageID, &item.AccountID, &item.InstrumentID, &item.Quantity, &item.AverageEntryPrice, &item.MarkPrice, &item.RealizedPnL, &item.UnrealizedPnL, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan portfolio position")
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

func (store *ReservationStore) Create(ctx context.Context, item reservations.Reservation) (reservations.Reservation, error) {
	scope, err := json.Marshal(item.Scope)
	if err != nil {
		return reservations.Reservation{}, fmt.Errorf("encode reservation scope")
	}
	scopeKey := reservationScopeKey(item)
	result, err := store.db.ExecContext(ctx, `INSERT INTO platform_reservations
		(reservation_id, principal_id, mode, product_id, reservation_kind, netting_scope, scope_key, quantity, notional, currency, status, idempotency_key, payload_hash, expires_at, blocked_reason, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (principal_id, mode, idempotency_key) DO NOTHING`, item.ReservationID, item.PrincipalID, item.Mode, item.ProductID, item.Kind, scope, scopeKey,
		nullString(item.Quantity), nullString(item.Notional), nullString(item.Currency), item.Status, item.IdempotencyKey, item.PayloadHash, item.ExpiresAt, item.BlockedReason, item.CreatedAt, item.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "platform_reservations_held_scope") {
			return reservations.Reservation{}, reservations.ErrScopeOwned
		}
		return reservations.Reservation{}, fmt.Errorf("insert reservation")
	}
	rows, _ := result.RowsAffected()
	if rows == 1 {
		return item, nil
	}
	existing, err := store.loadByKey(ctx, item.PrincipalID, item.Mode, item.IdempotencyKey)
	if err != nil {
		return reservations.Reservation{}, err
	}
	if existing.PayloadHash != item.PayloadHash {
		return existing, reservations.ErrIdempotencyConflict
	}
	return existing, nil
}

func (store *ReservationStore) Get(ctx context.Context, id string) (reservations.Reservation, error) {
	return scanReservation(store.db.QueryRowContext(ctx, reservationProjection+` WHERE reservation_id=$1`, id))
}

func (store *ReservationStore) loadByKey(ctx context.Context, principal string, mode reservations.Mode, key string) (reservations.Reservation, error) {
	return scanReservation(store.db.QueryRowContext(ctx, reservationProjection+` WHERE principal_id=$1 AND mode=$2 AND idempotency_key=$3`, principal, mode, key))
}

func (store *ReservationStore) SetStatus(ctx context.Context, id string, status reservations.Status, now time.Time) (reservations.Reservation, error) {
	return scanReservation(store.db.QueryRowContext(ctx, `UPDATE platform_reservations SET status=$2, updated_at=$3 WHERE reservation_id=$1 RETURNING
		reservation_id,principal_id,mode,product_id,reservation_kind,netting_scope,quantity,notional,currency,status,idempotency_key,payload_hash,expires_at,blocked_reason,created_at,updated_at`, id, status, now))
}

func (store *ReservationStore) List(ctx context.Context, principal string, mode reservations.Mode) ([]reservations.Reservation, error) {
	rows, err := store.db.QueryContext(ctx, reservationProjection+` WHERE principal_id=$1 AND mode=$2 ORDER BY created_at,reservation_id`, principal, mode)
	if err != nil {
		return nil, fmt.Errorf("list reservations")
	}
	defer rows.Close()
	items := make([]reservations.Reservation, 0)
	for rows.Next() {
		item, scanErr := scanReservation(rows)
		if scanErr != nil {
			return nil, scanErr
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

const reservationProjection = `SELECT reservation_id,principal_id,mode,product_id,reservation_kind,netting_scope,quantity,notional,currency,status,idempotency_key,payload_hash,expires_at,blocked_reason,created_at,updated_at FROM platform_reservations`

func scanReservation(row scanner) (reservations.Reservation, error) {
	var item reservations.Reservation
	var scope []byte
	var quantity, notional, currency, blocked sql.NullString
	var expires sql.NullTime
	err := row.Scan(&item.ReservationID, &item.PrincipalID, &item.Mode, &item.ProductID, &item.Kind, &scope, &quantity, &notional, &currency, &item.Status, &item.IdempotencyKey, &item.PayloadHash, &expires, &blocked, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return item, reservations.ErrNotFound
	}
	if err != nil || json.Unmarshal(scope, &item.Scope) != nil {
		return item, fmt.Errorf("scan reservation")
	}
	item.Quantity, item.Notional, item.Currency, item.BlockedReason = stringPointer(quantity), stringPointer(notional), stringPointer(currency), stringPointer(blocked)
	if expires.Valid {
		value := expires.Time
		item.ExpiresAt = &value
	}
	return item, nil
}

func (store *CommandStore) Reserve(ctx context.Context, item orders.Command) (orders.Command, bool, error) {
	blockers, _ := json.Marshal(item.Blockers)
	result, err := store.db.ExecContext(ctx, `INSERT INTO platform_commands
		(command_id,principal_id,account_id,mode,product_id,lineage_id,reservation_id,idempotency_key,method,path,command_kind,state,payload,payload_hash,blockers,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (principal_id,mode,method,path,idempotency_key) DO NOTHING`, item.CommandID, item.PrincipalID, item.AccountID, item.Mode, item.ProductID, item.LineageID, nullString(item.ReservationID), item.IdempotencyKey, item.Method, item.Path, item.CommandKind, item.State, item.Payload, item.PayloadHash, blockers, item.CreatedAt, item.UpdatedAt)
	if err != nil {
		return orders.Command{}, false, fmt.Errorf("insert command")
	}
	rows, _ := result.RowsAffected()
	if rows == 1 {
		return item, false, nil
	}
	existing, err := scanCommand(store.db.QueryRowContext(ctx, commandProjection+` WHERE principal_id=$1 AND mode=$2 AND method=$3 AND path=$4 AND idempotency_key=$5`, item.PrincipalID, item.Mode, item.Method, item.Path, item.IdempotencyKey))
	if err != nil {
		return orders.Command{}, true, err
	}
	if existing.PayloadHash != item.PayloadHash || existing.AccountID != item.AccountID || existing.ProductID != item.ProductID || existing.LineageID != item.LineageID || existing.CommandKind != item.CommandKind || pointerValue(existing.ReservationID) != pointerValue(item.ReservationID) {
		return existing, true, orders.ErrIdempotencyConflict
	}
	return existing, true, nil
}

func (store *CommandStore) Abort(ctx context.Context, id string) error {
	_, err := store.db.ExecContext(ctx, `DELETE FROM platform_commands WHERE command_id=$1 AND state='accepted'`, id)
	if err != nil {
		return fmt.Errorf("abort command")
	}
	return nil
}
func (store *CommandStore) Get(ctx context.Context, id string) (orders.Command, error) {
	return scanCommand(store.db.QueryRowContext(ctx, commandProjection+` WHERE command_id=$1`, id))
}

func (store *CommandStore) Transition(ctx context.Context, request orders.TransitionRequest, now time.Time) (orders.Command, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return orders.Command{}, fmt.Errorf("begin command transition")
	}
	defer tx.Rollback()
	item, err := scanCommand(tx.QueryRowContext(ctx, `UPDATE platform_commands SET state=$2,updated_at=$3 WHERE command_id=$1 RETURNING command_id,principal_id,account_id,mode,product_id,lineage_id,reservation_id,idempotency_key,method,path,command_kind,state,payload,payload_hash,blockers,created_at,updated_at`, request.CommandID, request.State, now))
	if err != nil {
		return orders.Command{}, err
	}
	if request.State == orders.StateUncertain {
		_, err = tx.ExecContext(ctx, `INSERT INTO platform_reconciliation_tasks(command_id,mode,reason,next_attempt_at) VALUES ($1,$2,$3,$4)
			ON CONFLICT (command_id) DO UPDATE SET reason=EXCLUDED.reason,next_attempt_at=EXCLUDED.next_attempt_at,lease_until=NULL`, item.CommandID, item.Mode, request.Reason, request.NextAttemptAt)
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM platform_reconciliation_tasks WHERE command_id=$1`, item.CommandID)
	}
	if err != nil {
		return orders.Command{}, fmt.Errorf("persist reconciliation transition")
	}
	if err := tx.Commit(); err != nil {
		return orders.Command{}, fmt.Errorf("commit command transition")
	}
	return item, nil
}

func (store *CommandStore) ClaimDue(ctx context.Context, mode orders.Mode, now, leaseUntil time.Time, limit int) ([]orders.ReconciliationTask, error) {
	rows, err := store.db.QueryContext(ctx, `WITH due AS (SELECT command_id FROM platform_reconciliation_tasks WHERE mode=$1 AND next_attempt_at<=$2 AND (lease_until IS NULL OR lease_until<=$2) ORDER BY next_attempt_at FOR UPDATE SKIP LOCKED LIMIT $3)
		UPDATE platform_reconciliation_tasks r SET lease_until=$4,attempt_count=r.attempt_count+1 FROM due WHERE r.command_id=due.command_id RETURNING r.command_id,r.mode,r.reason,r.next_attempt_at,r.lease_until,r.attempt_count`, mode, now, limit, leaseUntil)
	if err != nil {
		return nil, fmt.Errorf("claim reconciliation tasks")
	}
	defer rows.Close()
	items := make([]orders.ReconciliationTask, 0)
	for rows.Next() {
		var item orders.ReconciliationTask
		if err := rows.Scan(&item.CommandID, &item.Mode, &item.Reason, &item.NextAttempt, &item.LeaseUntil, &item.AttemptCount); err != nil {
			return nil, fmt.Errorf("scan reconciliation task")
		}
		items = append(items, item)
	}
	return items, rows.Err()
}

const commandProjection = `SELECT command_id,principal_id,account_id,mode,product_id,lineage_id,reservation_id,idempotency_key,method,path,command_kind,state,payload,payload_hash,blockers,created_at,updated_at FROM platform_commands`

func scanCommand(row scanner) (orders.Command, error) {
	var item orders.Command
	var reservation sql.NullString
	var blockers []byte
	err := row.Scan(&item.CommandID, &item.PrincipalID, &item.AccountID, &item.Mode, &item.ProductID, &item.LineageID, &reservation, &item.IdempotencyKey, &item.Method, &item.Path, &item.CommandKind, &item.State, &item.Payload, &item.PayloadHash, &blockers, &item.CreatedAt, &item.UpdatedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return item, orders.ErrNotFound
	}
	if err != nil || json.Unmarshal(blockers, &item.Blockers) != nil {
		return item, fmt.Errorf("scan command")
	}
	item.ReservationID = stringPointer(reservation)
	return item, nil
}

func (store *LedgerStore) Append(ctx context.Context, item ledger.Entry) error {
	result, err := store.db.ExecContext(ctx, `INSERT INTO platform_ledger_entries(entry_id,mode,product_id,lineage_id,account_id,instrument_id,kind,amount,currency,occurred_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10) ON CONFLICT (entry_id) DO NOTHING`, item.EntryID, item.Mode, item.ProductID, item.LineageID, item.AccountID, item.InstrumentID, item.Kind, item.Amount, item.Currency, item.OccurredAt)
	if err != nil {
		return fmt.Errorf("append ledger entry")
	}
	rows, _ := result.RowsAffected()
	if rows == 1 {
		return nil
	}
	var existing ledger.Entry
	err = store.db.QueryRowContext(ctx, `SELECT entry_id,mode,product_id,lineage_id,account_id,instrument_id,kind,amount::text,currency,occurred_at FROM platform_ledger_entries WHERE entry_id=$1`, item.EntryID).Scan(&existing.EntryID, &existing.Mode, &existing.ProductID, &existing.LineageID, &existing.AccountID, &existing.InstrumentID, &existing.Kind, &existing.Amount, &existing.Currency, &existing.OccurredAt)
	if err != nil {
		return fmt.Errorf("load ledger replay")
	}
	if existing != item {
		return ledger.ErrEntryConflict
	}
	return nil
}
func (store *LedgerStore) Entries(ctx context.Context, q ledger.Query) ([]ledger.Entry, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT entry_id,mode,product_id,lineage_id,account_id,instrument_id,kind,amount::text,currency,occurred_at FROM platform_ledger_entries WHERE mode=$1 AND ($2='' OR product_id=$2) AND ($3='' OR lineage_id=$3) AND ($4='' OR account_id=$4) AND ($5='' OR instrument_id=$5) ORDER BY occurred_at,entry_id`, q.Mode, q.ProductID, q.LineageID, q.AccountID, q.InstrumentID)
	if err != nil {
		return nil, fmt.Errorf("list ledger entries")
	}
	defer rows.Close()
	items := make([]ledger.Entry, 0)
	for rows.Next() {
		var item ledger.Entry
		if err := rows.Scan(&item.EntryID, &item.Mode, &item.ProductID, &item.LineageID, &item.AccountID, &item.InstrumentID, &item.Kind, &item.Amount, &item.Currency, &item.OccurredAt); err != nil {
			return nil, fmt.Errorf("scan ledger entry")
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (store *LedgerStore) SetUnrealized(ctx context.Context, v ledger.Unrealized) error {
	_, err := store.db.ExecContext(ctx, `INSERT INTO platform_unrealized_pnl(mode,product_id,lineage_id,account_id,instrument_id,amount,as_of) VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (mode,product_id,lineage_id,account_id,instrument_id) DO UPDATE SET amount=EXCLUDED.amount,as_of=EXCLUDED.as_of`, v.Mode, v.ProductID, v.LineageID, v.AccountID, v.InstrumentID, v.Amount, v.AsOf)
	if err != nil {
		return fmt.Errorf("set unrealized pnl")
	}
	return nil
}
func (store *LedgerStore) Unrealized(ctx context.Context, q ledger.Query) (decimal.Decimal, time.Time, error) {
	var amount string
	var asOf time.Time
	err := store.db.QueryRowContext(ctx, `SELECT amount::text,as_of FROM platform_unrealized_pnl WHERE mode=$1 AND product_id=$2 AND lineage_id=$3 AND account_id=$4 AND instrument_id=$5`, q.Mode, q.ProductID, q.LineageID, q.AccountID, q.InstrumentID).Scan(&amount, &asOf)
	if errors.Is(err, sql.ErrNoRows) {
		return decimal.Zero, time.Time{}, nil
	}
	if err != nil {
		return decimal.Zero, time.Time{}, fmt.Errorf("load unrealized pnl")
	}
	value, err := decimal.NewFromString(amount)
	if err != nil {
		return decimal.Zero, time.Time{}, fmt.Errorf("decode unrealized pnl")
	}
	return value, asOf, nil
}

type scanner interface{ Scan(...any) error }

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
func reservationScopeKey(item reservations.Reservation) string {
	scope, _ := json.Marshal(item.Scope)
	return string(item.Mode) + "\x00" + string(scope)
}
