package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
	"github.com/QuantProcessing/boltertrader/services/trading/reservations"
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

func (store *PortfolioStore) ApplyFill(ctx context.Context, fill portfolio.Fill, reduce func(portfolio.Position) (portfolio.Position, portfolio.FillResult, error)) (portfolio.Position, portfolio.FillResult, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return portfolio.Position{}, portfolio.FillResult{}, fmt.Errorf("begin portfolio fill: %w", err)
	}
	defer tx.Rollback()

	fingerprint := portfolio.FillFingerprint(fill)
	insert, err := tx.ExecContext(ctx, `INSERT INTO portfolio_fills
		(principal_id,fill_id,fill_hash,command_id,mode,product_id,lineage_id,account_id,instrument_id,side,quantity,price,fee,fee_currency,settlement_currency,realized_pnl,occurred_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,0,$16) ON CONFLICT (principal_id,fill_id) DO NOTHING`,
		fill.PrincipalID, fill.FillID, fingerprint, fill.CommandID, fill.Mode, fill.ProductID, fill.LineageID, fill.AccountID, fill.InstrumentID,
		fill.Side, fill.Quantity, fill.Price, fill.Fee, fill.FeeCurrency, fill.SettlementCurrency, fill.OccurredAt)
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
		if err := tx.QueryRowContext(ctx, `SELECT fill_hash,realized_pnl::text,fee::text,settlement_currency FROM portfolio_fills WHERE principal_id=$1 AND fill_id=$2`, fill.PrincipalID, fill.FillID).Scan(&recordedHash, &result.RealizedPnL, &result.Fee, &result.SettlementCurrency); err != nil {
			return portfolio.Position{}, portfolio.FillResult{}, fmt.Errorf("load portfolio fill replay: %w", err)
		}
		if recordedHash != fingerprint {
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
		(principal_id,mode,product_id,lineage_id,account_id,instrument_id,settlement_currency,quantity,average_entry_price,mark_price,realized_pnl,unrealized_pnl,mark_observed_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,0,0,0,0,0,$8,$9) ON CONFLICT (principal_id,mode,product_id,lineage_id,account_id,instrument_id) DO NOTHING`,
		fill.PrincipalID, fill.Mode, fill.ProductID, fill.LineageID, fill.AccountID, fill.InstrumentID, fill.SettlementCurrency, time.Time{}, fill.OccurredAt); err != nil {
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
	if _, err := tx.ExecContext(ctx, `UPDATE portfolio_fills SET realized_pnl=$3,fee=$4 WHERE principal_id=$1 AND fill_id=$2`, fill.PrincipalID, fill.FillID, result.RealizedPnL, result.Fee); err != nil {
		return portfolio.Position{}, portfolio.FillResult{}, fmt.Errorf("persist portfolio fill result: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return portfolio.Position{}, portfolio.FillResult{}, fmt.Errorf("commit portfolio fill: %w", err)
	}
	return position, result, nil
}

func (store *PortfolioStore) ApplyMark(ctx context.Context, principalID string, mode portfolio.Mode, accountID, productID, lineageID, instrumentID string, reduce func(portfolio.Position) (portfolio.Position, error)) (portfolio.Position, error) {
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return portfolio.Position{}, fmt.Errorf("begin portfolio mark: %w", err)
	}
	defer tx.Rollback()
	identity := portfolio.Fill{
		PrincipalID: principalID, Mode: mode, AccountID: accountID, ProductID: productID, LineageID: lineageID, InstrumentID: instrumentID,
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
	query := `SELECT principal_id,mode,product_id,lineage_id,account_id,instrument_id,settlement_currency,quantity::text,average_entry_price::text,mark_price::text,realized_pnl::text,unrealized_pnl::text,mark_observed_at,updated_at
		FROM positions WHERE principal_id=$1 AND mode=$2 AND product_id=$3 AND lineage_id=$4 AND account_id=$5 AND instrument_id=$6`
	if forUpdate {
		query += " FOR UPDATE"
	}
	var item portfolio.Position
	err := queryer.QueryRowContext(ctx, query, fill.PrincipalID, fill.Mode, fill.ProductID, fill.LineageID, fill.AccountID, fill.InstrumentID).Scan(
		&item.PrincipalID, &item.Mode, &item.ProductID, &item.LineageID, &item.AccountID, &item.InstrumentID, &item.SettlementCurrency, &item.Quantity,
		&item.AverageEntryPrice, &item.MarkPrice, &item.RealizedPnL, &item.UnrealizedPnL, &item.MarkObservedAt, &item.UpdatedAt,
	)
	return item, err
}

func upsertPortfolioPosition(ctx context.Context, executor interface {
	ExecContext(context.Context, string, ...any) (sql.Result, error)
}, item portfolio.Position) error {
	_, err := executor.ExecContext(ctx, `INSERT INTO positions(principal_id,mode,product_id,lineage_id,account_id,instrument_id,settlement_currency,quantity,average_entry_price,mark_price,realized_pnl,unrealized_pnl,mark_observed_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14) ON CONFLICT (principal_id,mode,product_id,lineage_id,account_id,instrument_id) DO UPDATE SET settlement_currency=EXCLUDED.settlement_currency,quantity=EXCLUDED.quantity,average_entry_price=EXCLUDED.average_entry_price,mark_price=EXCLUDED.mark_price,realized_pnl=EXCLUDED.realized_pnl,unrealized_pnl=EXCLUDED.unrealized_pnl,mark_observed_at=EXCLUDED.mark_observed_at,updated_at=EXCLUDED.updated_at`, item.PrincipalID, item.Mode, item.ProductID, item.LineageID, item.AccountID, item.InstrumentID, item.SettlementCurrency, item.Quantity, item.AverageEntryPrice, item.MarkPrice, item.RealizedPnL, item.UnrealizedPnL, item.MarkObservedAt, item.UpdatedAt)
	if err != nil {
		return fmt.Errorf("upsert portfolio position: %w", err)
	}
	return nil
}

func (store *PortfolioStore) Upsert(ctx context.Context, item portfolio.Position) error {
	return upsertPortfolioPosition(ctx, store.db, item)
}
func (store *PortfolioStore) List(ctx context.Context, principalID string, mode portfolio.Mode, accountID, productID string) ([]portfolio.Position, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT principal_id,mode,product_id,lineage_id,account_id,instrument_id,settlement_currency,quantity::text,average_entry_price::text,mark_price::text,realized_pnl::text,unrealized_pnl::text,mark_observed_at,updated_at FROM positions WHERE principal_id=$1 AND mode=$2 AND ($3='' OR account_id=$3) AND ($4='' OR product_id=$4) ORDER BY product_id,lineage_id,account_id,instrument_id`, principalID, mode, accountID, productID)
	if err != nil {
		return nil, fmt.Errorf("list portfolio positions: %w", err)
	}
	defer rows.Close()
	items := make([]portfolio.Position, 0)
	for rows.Next() {
		var item portfolio.Position
		if err := rows.Scan(&item.PrincipalID, &item.Mode, &item.ProductID, &item.LineageID, &item.AccountID, &item.InstrumentID, &item.SettlementCurrency, &item.Quantity, &item.AverageEntryPrice, &item.MarkPrice, &item.RealizedPnL, &item.UnrealizedPnL, &item.MarkObservedAt, &item.UpdatedAt); err != nil {
			return nil, fmt.Errorf("scan portfolio position: %w", err)
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
	result, err := store.db.ExecContext(ctx, `INSERT INTO reservations
		(reservation_id, principal_id, mode, product_id, reservation_kind, netting_scope, scope_key, quantity, notional, currency, status, idempotency_key, payload_hash, expires_at, blocked_reason, created_at, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (principal_id, mode, idempotency_key) DO NOTHING`, item.ReservationID, item.PrincipalID, item.Mode, item.ProductID, item.Kind, scope, scopeKey,
		nullString(item.Quantity), nullString(item.Notional), nullString(item.Currency), item.Status, item.IdempotencyKey, item.PayloadHash, item.ExpiresAt, item.BlockedReason, item.CreatedAt, item.UpdatedAt)
	if err != nil {
		if strings.Contains(err.Error(), "reservations_held_scope") {
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
	return scanReservation(store.db.QueryRowContext(ctx, `UPDATE reservations SET status=$2, updated_at=$3 WHERE reservation_id=$1 RETURNING
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

// scanner is the shape both *sql.Row and *sql.Rows satisfy, so a projection can
// be decoded once for the single-row and the iterated read alike.
type scanner interface{ Scan(...any) error }

const reservationProjection = `SELECT reservation_id,principal_id,mode,product_id,reservation_kind,netting_scope,quantity,notional,currency,status,idempotency_key,payload_hash,expires_at,blocked_reason,created_at,updated_at FROM reservations`

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
	result, err := store.db.ExecContext(ctx, `INSERT INTO commands
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
	if !sameCommandAuthority(existing, item) {
		return existing, true, orders.ErrIdempotencyConflict
	}
	return existing, true, nil
}

func (store *CommandStore) ReserveWithReservation(ctx context.Context, item orders.Command, claim orders.ReservationClaim) (orders.Command, bool, error) {
	if item.ReservationID == nil || *item.ReservationID != claim.ReservationID || item.PrincipalID != claim.PrincipalID || item.Mode != claim.Mode || item.ProductID != claim.ProductID {
		return orders.Command{}, false, reservations.ErrOwnership
	}
	tx, err := store.db.BeginTx(ctx, nil)
	if err != nil {
		return orders.Command{}, false, fmt.Errorf("begin reservation command")
	}
	defer tx.Rollback()
	blockers, _ := json.Marshal(item.Blockers)
	result, err := tx.ExecContext(ctx, `INSERT INTO commands
		(command_id,principal_id,account_id,mode,product_id,lineage_id,reservation_id,idempotency_key,method,path,command_kind,state,payload,payload_hash,blockers,created_at,updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		ON CONFLICT (principal_id,mode,method,path,idempotency_key) DO NOTHING`, item.CommandID, item.PrincipalID, item.AccountID, item.Mode, item.ProductID, item.LineageID, nullString(item.ReservationID), item.IdempotencyKey, item.Method, item.Path, item.CommandKind, item.State, item.Payload, item.PayloadHash, blockers, item.CreatedAt, item.UpdatedAt)
	if err != nil {
		return orders.Command{}, false, fmt.Errorf("insert reservation command")
	}
	rows, _ := result.RowsAffected()
	if rows == 0 {
		existing, loadErr := scanCommand(tx.QueryRowContext(ctx, commandProjection+` WHERE principal_id=$1 AND mode=$2 AND method=$3 AND path=$4 AND idempotency_key=$5`, item.PrincipalID, item.Mode, item.Method, item.Path, item.IdempotencyKey))
		if loadErr != nil {
			return orders.Command{}, true, loadErr
		}
		if !sameCommandAuthority(existing, item) {
			return existing, true, orders.ErrIdempotencyConflict
		}
		return existing, true, nil
	}
	var reservationID string
	err = tx.QueryRowContext(ctx, `UPDATE reservations SET status='consumed',updated_at=$5
		WHERE reservation_id=$1 AND principal_id=$2 AND mode=$3 AND product_id=$4 AND status='held'
		RETURNING reservation_id`, claim.ReservationID, claim.PrincipalID, claim.Mode, claim.ProductID, item.UpdatedAt).Scan(&reservationID)
	if errors.Is(err, sql.ErrNoRows) {
		reservation, loadErr := scanReservation(tx.QueryRowContext(ctx, reservationProjection+` WHERE reservation_id=$1`, claim.ReservationID))
		if loadErr != nil {
			return orders.Command{}, false, loadErr
		}
		if reservation.PrincipalID != claim.PrincipalID || reservations.Mode(claim.Mode) != reservation.Mode || reservation.ProductID != claim.ProductID {
			return orders.Command{}, false, reservations.ErrOwnership
		}
		return orders.Command{}, false, reservations.ErrNotHeld
	}
	if err != nil {
		return orders.Command{}, false, fmt.Errorf("consume reservation command authority")
	}
	if reservationID != claim.ReservationID {
		return orders.Command{}, false, reservations.ErrOwnership
	}
	if err := tx.Commit(); err != nil {
		return orders.Command{}, false, fmt.Errorf("commit reservation command")
	}
	return item, false, nil
}

func sameCommandAuthority(existing, incoming orders.Command) bool {
	return existing.PayloadHash == incoming.PayloadHash && existing.AccountID == incoming.AccountID &&
		existing.ProductID == incoming.ProductID && existing.LineageID == incoming.LineageID &&
		existing.CommandKind == incoming.CommandKind && pointerValue(existing.ReservationID) == pointerValue(incoming.ReservationID)
}

func (store *CommandStore) Abort(ctx context.Context, id string) error {
	_, err := store.db.ExecContext(ctx, `DELETE FROM commands WHERE command_id=$1 AND state='accepted'`, id)
	if err != nil {
		return fmt.Errorf("abort command")
	}
	return nil
}
func (store *CommandStore) Get(ctx context.Context, id string) (orders.Command, error) {
	return scanCommand(store.db.QueryRowContext(ctx, commandProjection+` WHERE command_id=$1`, id))
}

func (store *CommandStore) ListOwned(ctx context.Context, principal string, mode orders.Mode, productID string) ([]orders.Command, error) {
	rows, err := store.db.QueryContext(ctx, commandProjection+` WHERE principal_id=$1 AND mode=$2 AND product_id=$3 ORDER BY created_at,command_id`, principal, mode, productID)
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
	evidence, _ := json.Marshal(request.Evidence)
	item, err := scanCommand(tx.QueryRowContext(ctx, `UPDATE commands SET state=$2,updated_at=$3,execution_evidence=CASE WHEN $4::jsonb='{}'::jsonb THEN execution_evidence ELSE $4::jsonb END WHERE command_id=$1 RETURNING command_id,principal_id,account_id,mode,product_id,lineage_id,reservation_id,idempotency_key,method,path,command_kind,state,payload,payload_hash,blockers,created_at,updated_at,execution_evidence`, request.CommandID, request.State, now, evidence))
	if err != nil {
		return orders.Command{}, err
	}
	if request.State == orders.StateUncertain || request.State == orders.StateSubmitted {
		_, err = tx.ExecContext(ctx, `INSERT INTO reconciliation_tasks(command_id,mode,reason,next_attempt_at) VALUES ($1,$2,$3,$4)
			ON CONFLICT (command_id) DO UPDATE SET reason=EXCLUDED.reason,next_attempt_at=EXCLUDED.next_attempt_at,lease_until=NULL`, item.CommandID, item.Mode, request.Reason, request.NextAttemptAt)
	} else {
		_, err = tx.ExecContext(ctx, `DELETE FROM reconciliation_tasks WHERE command_id=$1`, item.CommandID)
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
	rows, err := store.db.QueryContext(ctx, `WITH due AS (SELECT command_id FROM reconciliation_tasks WHERE mode=$1 AND next_attempt_at<=$2 AND (lease_until IS NULL OR lease_until<=$2) ORDER BY next_attempt_at FOR UPDATE SKIP LOCKED LIMIT $3)
		UPDATE reconciliation_tasks r SET lease_until=$4,attempt_count=r.attempt_count+1 FROM due WHERE r.command_id=due.command_id RETURNING r.command_id,r.mode,r.reason,r.next_attempt_at,r.lease_until,r.attempt_count`, mode, now, limit, leaseUntil)
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

const commandProjection = `SELECT command_id,principal_id,account_id,mode,product_id,lineage_id,reservation_id,idempotency_key,method,path,command_kind,state,payload,payload_hash,blockers,created_at,updated_at,execution_evidence FROM commands`

func scanCommand(row scanner) (orders.Command, error) {
	var item orders.Command
	var reservation sql.NullString
	var blockers []byte
	var evidence []byte
	err := row.Scan(&item.CommandID, &item.PrincipalID, &item.AccountID, &item.Mode, &item.ProductID, &item.LineageID, &reservation, &item.IdempotencyKey, &item.Method, &item.Path, &item.CommandKind, &item.State, &item.Payload, &item.PayloadHash, &blockers, &item.CreatedAt, &item.UpdatedAt, &evidence)
	if errors.Is(err, sql.ErrNoRows) {
		return item, orders.ErrNotFound
	}
	if err != nil || json.Unmarshal(blockers, &item.Blockers) != nil || json.Unmarshal(evidence, &item.ExecutionEvidence) != nil {
		return item, fmt.Errorf("scan command")
	}
	item.ReservationID = stringPointer(reservation)
	return item, nil
}

func (store *LedgerStore) Append(ctx context.Context, item ledger.Entry) error {
	item.OccurredAt = postgresTimestamp(item.OccurredAt)
	result, err := store.db.ExecContext(ctx, `INSERT INTO ledger_entries(principal_id,entry_id,mode,product_id,lineage_id,account_id,instrument_id,kind,amount,currency,occurred_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (principal_id,entry_id) DO NOTHING`, item.PrincipalID, item.EntryID, item.Mode, item.ProductID, item.LineageID, item.AccountID, item.InstrumentID, item.Kind, item.Amount, item.Currency, item.OccurredAt)
	if err != nil {
		return fmt.Errorf("append ledger entry")
	}
	rows, _ := result.RowsAffected()
	if rows == 1 {
		return nil
	}
	var existing ledger.Entry
	err = store.db.QueryRowContext(ctx, `SELECT principal_id,entry_id,mode,product_id,lineage_id,account_id,instrument_id,kind,amount::text,currency,occurred_at FROM ledger_entries WHERE principal_id=$1 AND entry_id=$2`, item.PrincipalID, item.EntryID).Scan(&existing.PrincipalID, &existing.EntryID, &existing.Mode, &existing.ProductID, &existing.LineageID, &existing.AccountID, &existing.InstrumentID, &existing.Kind, &existing.Amount, &existing.Currency, &existing.OccurredAt)
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
	err = tx.QueryRowContext(ctx, `SELECT principal_id,entry_id,mode,product_id,lineage_id,account_id,instrument_id,kind,amount::text,currency,occurred_at FROM ledger_entries WHERE principal_id=$1 AND entry_id=$2 FOR UPDATE`, replacement.PrincipalID, replacement.EntryID).Scan(&existing.PrincipalID, &existing.EntryID, &existing.Mode, &existing.ProductID, &existing.LineageID, &existing.AccountID, &existing.InstrumentID, &existing.Kind, &existing.Amount, &existing.Currency, &existing.OccurredAt)
	if err == nil {
		if !sameLedgerEntry(existing, replacement) {
			return ledger.ErrEntryConflict
		}
		var prior ledger.Entry
		err = tx.QueryRowContext(ctx, `SELECT principal_id,entry_id,mode,product_id,lineage_id,account_id,instrument_id,kind,amount::text,currency,occurred_at FROM ledger_entries WHERE principal_id=$1 AND entry_id=$2 FOR UPDATE`, replacement.PrincipalID, priorEntryID).Scan(&prior.PrincipalID, &prior.EntryID, &prior.Mode, &prior.ProductID, &prior.LineageID, &prior.AccountID, &prior.InstrumentID, &prior.Kind, &prior.Amount, &prior.Currency, &prior.OccurredAt)
		if err == nil {
			if !sameLedgerFundingScope(prior, replacement) {
				return ledger.ErrEntryConflict
			}
			if _, err := tx.ExecContext(ctx, `DELETE FROM ledger_entries WHERE principal_id=$1 AND entry_id=$2`, replacement.PrincipalID, priorEntryID); err != nil {
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
	err = tx.QueryRowContext(ctx, `SELECT principal_id,entry_id,mode,product_id,lineage_id,account_id,instrument_id,kind,amount::text,currency,occurred_at FROM ledger_entries WHERE principal_id=$1 AND entry_id=$2 FOR UPDATE`, replacement.PrincipalID, priorEntryID).Scan(&prior.PrincipalID, &prior.EntryID, &prior.Mode, &prior.ProductID, &prior.LineageID, &prior.AccountID, &prior.InstrumentID, &prior.Kind, &prior.Amount, &prior.Currency, &prior.OccurredAt)
	if err != nil || !sameLedgerFundingScope(prior, replacement) {
		return ledger.ErrEntryConflict
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM ledger_entries WHERE principal_id=$1 AND entry_id=$2`, replacement.PrincipalID, priorEntryID); err != nil {
		return fmt.Errorf("remove superseded funding entry: %w", err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO ledger_entries(principal_id,entry_id,mode,product_id,lineage_id,account_id,instrument_id,kind,amount,currency,occurred_at) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11)`, replacement.PrincipalID, replacement.EntryID, replacement.Mode, replacement.ProductID, replacement.LineageID, replacement.AccountID, replacement.InstrumentID, replacement.Kind, replacement.Amount, replacement.Currency, replacement.OccurredAt); err != nil {
		return fmt.Errorf("insert funding replacement: %w", err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit funding supersession: %w", err)
	}
	return nil
}
func (store *LedgerStore) Entries(ctx context.Context, q ledger.Query) ([]ledger.Entry, error) {
	rows, err := store.db.QueryContext(ctx, `SELECT principal_id,entry_id,mode,product_id,lineage_id,account_id,instrument_id,kind,amount::text,currency,occurred_at FROM ledger_entries WHERE principal_id=$1 AND mode=$2 AND ($3='' OR product_id=$3) AND ($4='' OR lineage_id=$4) AND ($5='' OR account_id=$5) AND ($6='' OR instrument_id=$6) ORDER BY occurred_at,entry_id`, q.PrincipalID, q.Mode, q.ProductID, q.LineageID, q.AccountID, q.InstrumentID)
	if err != nil {
		return nil, fmt.Errorf("list ledger entries")
	}
	defer rows.Close()
	items := make([]ledger.Entry, 0)
	for rows.Next() {
		var item ledger.Entry
		if err := rows.Scan(&item.PrincipalID, &item.EntryID, &item.Mode, &item.ProductID, &item.LineageID, &item.AccountID, &item.InstrumentID, &item.Kind, &item.Amount, &item.Currency, &item.OccurredAt); err != nil {
			return nil, fmt.Errorf("scan ledger entry")
		}
		items = append(items, item)
	}
	return items, rows.Err()
}
func (store *LedgerStore) SetUnrealized(ctx context.Context, v ledger.Unrealized) error {
	_, err := store.db.ExecContext(ctx, `INSERT INTO unrealized_pnl(principal_id,mode,product_id,lineage_id,account_id,instrument_id,amount,currency,as_of) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9) ON CONFLICT (principal_id,mode,product_id,lineage_id,account_id,instrument_id) DO UPDATE SET amount=EXCLUDED.amount,currency=EXCLUDED.currency,as_of=EXCLUDED.as_of WHERE unrealized_pnl.as_of < EXCLUDED.as_of`, v.PrincipalID, v.Mode, v.ProductID, v.LineageID, v.AccountID, v.InstrumentID, v.Amount, v.Currency, v.AsOf)
	if err != nil {
		return fmt.Errorf("set unrealized pnl")
	}
	return nil
}

func sameLedgerFundingScope(left, right ledger.Entry) bool {
	return left.Kind == ledger.KindFunding && right.Kind == ledger.KindFunding && left.PrincipalID == right.PrincipalID && left.Mode == right.Mode && left.ProductID == right.ProductID && left.LineageID == right.LineageID && left.AccountID == right.AccountID && left.InstrumentID == right.InstrumentID && left.Currency == right.Currency
}

func sameLedgerEntry(left, right ledger.Entry) bool {
	leftAmount, leftErr := decimal.NewFromString(left.Amount)
	rightAmount, rightErr := decimal.NewFromString(right.Amount)
	return leftErr == nil && rightErr == nil && leftAmount.Equal(rightAmount) && postgresTimestamp(left.OccurredAt).Equal(postgresTimestamp(right.OccurredAt)) &&
		left.PrincipalID == right.PrincipalID && left.EntryID == right.EntryID && left.Mode == right.Mode && left.ProductID == right.ProductID && left.LineageID == right.LineageID &&
		left.AccountID == right.AccountID && left.InstrumentID == right.InstrumentID && left.Kind == right.Kind && left.Currency == right.Currency
}

func postgresTimestamp(value time.Time) time.Time {
	return value.UTC().Truncate(time.Microsecond)
}

func (store *LedgerStore) Unrealized(ctx context.Context, q ledger.Query) (decimal.Decimal, string, time.Time, error) {
	var amount, currency string
	var asOf time.Time
	err := store.db.QueryRowContext(ctx, `SELECT amount::text,currency,as_of FROM unrealized_pnl WHERE principal_id=$1 AND mode=$2 AND product_id=$3 AND lineage_id=$4 AND account_id=$5 AND instrument_id=$6`, q.PrincipalID, q.Mode, q.ProductID, q.LineageID, q.AccountID, q.InstrumentID).Scan(&amount, &currency, &asOf)
	if errors.Is(err, sql.ErrNoRows) {
		return decimal.Zero, "", time.Time{}, nil
	}
	if err != nil {
		return decimal.Zero, "", time.Time{}, fmt.Errorf("load unrealized pnl")
	}
	if currency == "" {
		return decimal.Zero, "", time.Time{}, ledger.ErrCurrencyMismatch
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
func reservationScopeKey(item reservations.Reservation) string {
	scope, _ := json.Marshal(item.Scope)
	fingerprint := sha256.Sum256(append([]byte("boltertrader/reservation-scope/v1\x00"+item.Mode+"\x00"), scope...))
	return fmt.Sprintf("scope_v1_%x", fingerprint)
}
