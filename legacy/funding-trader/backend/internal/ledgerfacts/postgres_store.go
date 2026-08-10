package ledgerfacts

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

type pgDBTX interface {
	Exec(context.Context, string, ...any) (pgconn.CommandTag, error)
	QueryRow(context.Context, string, ...any) pgx.Row
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

type PostgresStore struct {
	pool *pgxpool.Pool
	db   pgDBTX
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool, db: pool}
}

func newPostgresTxStore(tx pgx.Tx) *PostgresStore {
	return &PostgresStore{db: tx}
}

// ReplayPostgresTransaction re-reads immutable facts through the caller's
// already-locked transaction. It is intentionally read-only so derived stores
// can compute from database truth without trusting a caller-supplied payload.
func ReplayPostgresTransaction(ctx context.Context, tx pgx.Tx, scopes []Scope) ([]ReplayEntry, error) {
	if tx == nil {
		return nil, errors.New("ledger postgres transaction is required")
	}
	journal := NewJournal(newPostgresTxStore(tx))
	seen := map[string]struct{}{}
	var replayed []ReplayEntry
	for _, scope := range scopes {
		entries, err := journal.Replay(ctx, scope)
		if err != nil {
			return nil, err
		}
		for _, entry := range entries {
			kind, factID := replayEntryIdentity(entry)
			if kind == "" || factID == "" {
				return nil, errors.New("postgres replay returned fact without stable identity")
			}
			key := kind + "\x00" + factID
			if _, ok := seen[key]; ok {
				continue
			}
			seen[key] = struct{}{}
			replayed = append(replayed, entry)
		}
	}
	return replayed, nil
}

func replayEntryIdentity(entry ReplayEntry) (string, string) {
	switch {
	case entry.Funding != nil:
		return "funding", entry.Funding.FactID
	case entry.Fill != nil:
		return "fill", entry.Fill.FactID
	case entry.Position != nil:
		return "position", entry.Position.FactID
	case entry.Lineage != nil:
		return "lineage", entry.Lineage.FactID
	case entry.RevisionFact != nil:
		return "lineage_revision", entry.RevisionFact.FactID
	case entry.Watermark != nil:
		return "watermark", entry.Watermark.FactID
	default:
		return "", ""
	}
}

func (s *PostgresStore) IngestFundingPayments(ctx context.Context, batch FundingIngestBatch) ([]FundingPaymentFact, SyncWatermark, error) {
	if s == nil || s.pool == nil {
		return nil, SyncWatermark{}, errors.New("ledger postgres pool is required")
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, SyncWatermark{}, err
	}
	defer tx.Rollback(ctx)
	txStore := newPostgresTxStore(tx)
	items, watermark, err := txStore.ingestFundingPayments(ctx, batch)
	if err != nil {
		return nil, SyncWatermark{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, SyncWatermark{}, err
	}
	return items, watermark, nil
}

func (s *PostgresStore) AppendSyncWatermark(ctx context.Context, watermark SyncWatermark) (SyncWatermark, error) {
	return NewJournal(s).AppendSyncWatermark(ctx, watermark)
}

func (s *PostgresStore) FundingPayments(ctx context.Context, query FundingPaymentQuery) ([]FundingPaymentFact, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("ledger postgres pool is required")
	}
	if strings.TrimSpace(query.PrincipalID) == "" || (query.Mode != "test" && query.Mode != "live") {
		return nil, ErrScopeMismatch
	}
	if !query.From.IsZero() && !query.To.IsZero() && !query.From.Before(query.To) {
		return nil, ErrScopeMismatch
	}
	limit := query.Limit
	if limit == 0 {
		limit = 50
	}
	if limit < 1 || limit > 101 {
		return nil, ErrScopeMismatch
	}
	var from any
	if !query.From.IsZero() {
		from = query.From.UTC()
	}
	var to any
	if !query.To.IsZero() {
		to = query.To.UTC()
	}
	var cursorEffectiveAt any
	if !query.CursorEffectiveAt.IsZero() {
		if query.CursorSequenceID <= 0 {
			return nil, ErrScopeMismatch
		}
		cursorEffectiveAt = query.CursorEffectiveAt.UTC()
	} else if query.CursorSequenceID != 0 {
		return nil, ErrScopeMismatch
	}
	rows, err := s.pool.Query(ctx, `
		select fact_id, supersedes_fact_id, sync_watermark_id, payment_id, position_id,
		  venue, venue_environment, product, formula_version,
		  principal_id, mode, account_id, instrument_id,
		  window_start, window_end, pagination_traversal, source_ref, source_revision,
		  source_version, observed_at, effective_at, amount::text, asset, sequence_id
		from ledger_funding_payment_facts
		where principal_id=$1 and mode=$2
		  and ($3='' or account_id=$3)
		  and ($4='' or instrument_id=$4)
		  and ($5='' or position_id=$5)
		  and ($6::timestamptz is null or effective_at >= $6)
		  and ($7::timestamptz is null or effective_at < $7)
		  and ($8::timestamptz is null or effective_at < $8 or (effective_at = $8 and sequence_id < $9))
		order by effective_at desc, sequence_id desc
		limit $10
	`, query.PrincipalID, query.Mode, strings.TrimSpace(query.AccountID), strings.TrimSpace(query.InstrumentID),
		strings.TrimSpace(query.PositionID), from, to, cursorEffectiveAt, query.CursorSequenceID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	items := make([]FundingPaymentFact, 0)
	for rows.Next() {
		fact, err := scanFundingPayment(rows)
		if err != nil {
			return nil, err
		}
		items = append(items, fact)
	}
	return items, rows.Err()
}

func (s *PostgresStore) SyncWatermarkForFundingQuery(ctx context.Context, query FundingPaymentQuery) (SyncWatermark, bool, error) {
	if s == nil || s.pool == nil {
		return SyncWatermark{}, false, errors.New("ledger postgres pool is required")
	}
	if strings.TrimSpace(query.PrincipalID) == "" || (query.Mode != "test" && query.Mode != "live") ||
		strings.TrimSpace(query.AccountID) == "" || strings.TrimSpace(query.InstrumentID) == "" ||
		query.From.IsZero() || query.To.IsZero() || !query.From.Before(query.To) {
		return SyncWatermark{}, false, ErrScopeMismatch
	}
	var watermark SyncWatermark
	err := s.pool.QueryRow(ctx, `
		select fact_id, principal_id, mode, account_id, instrument_id, window_start, window_end,
		  pagination_traversal, source_ref, cursor, revision, observed_at, effective_through,
		  complete_through, status, verified, sequence_id
		from ledger_sync_watermarks
		where principal_id=$1 and mode=$2 and account_id=$3 and instrument_id=$4
		  and ($5::timestamptz is null or window_start=$5)
		  and ($6::timestamptz is null or window_end=$6)
		order by sequence_id desc
		limit 1
	`, query.PrincipalID, query.Mode, strings.TrimSpace(query.AccountID), strings.TrimSpace(query.InstrumentID), query.From.UTC(), query.To.UTC()).Scan(
		&watermark.FactID, &watermark.Scope.PrincipalID, &watermark.Scope.Mode,
		&watermark.Scope.AccountID, &watermark.Scope.InstrumentID,
		&watermark.Scope.WindowStart, &watermark.Scope.WindowEnd, &watermark.Scope.PaginationTraversal,
		&watermark.SourceRef, &watermark.Cursor, &watermark.Revision, &watermark.ObservedAt,
		&watermark.EffectiveThrough, &watermark.CompleteThrough, &watermark.Status,
		&watermark.Verified, &watermark.SequenceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SyncWatermark{}, false, nil
	}
	if err != nil {
		return SyncWatermark{}, false, err
	}
	return watermark, true, nil
}

func (s *PostgresStore) ResolveFundingFactID(ctx context.Context, scope Scope, sourceRef string) (string, bool, error) {
	if s == nil || s.pool == nil {
		return "", false, errors.New("ledger postgres pool is required")
	}
	if err := validateScope(scope); err != nil || strings.TrimSpace(sourceRef) == "" {
		return "", false, ErrScopeMismatch
	}
	var factID string
	err := s.pool.QueryRow(ctx, `
		select fact_id
		from ledger_funding_payment_facts
		where principal_id=$1 and mode=$2 and account_id=$3 and instrument_id=$4
		  and window_start=$5 and window_end=$6 and pagination_traversal=$7 and source_ref=$8
		order by sequence_id desc
		limit 1
	`, scope.PrincipalID, scope.Mode, scope.AccountID, scope.InstrumentID, scope.WindowStart.UTC(),
		scope.WindowEnd.UTC(), scope.PaginationTraversal, strings.TrimSpace(sourceRef)).Scan(&factID)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return factID, true, nil
}

func (s *PostgresStore) LatestSyncWatermark(ctx context.Context, principalID, mode, accountID, instrumentID string) (SyncWatermark, bool, error) {
	if s == nil || s.pool == nil {
		return SyncWatermark{}, false, errors.New("ledger postgres pool is required")
	}
	if strings.TrimSpace(principalID) == "" || (mode != "test" && mode != "live") ||
		strings.TrimSpace(accountID) == "" || strings.TrimSpace(instrumentID) == "" {
		return SyncWatermark{}, false, ErrScopeMismatch
	}
	var watermark SyncWatermark
	err := s.pool.QueryRow(ctx, `
		select fact_id, principal_id, mode, account_id, instrument_id, window_start, window_end,
		  pagination_traversal, source_ref, cursor, revision, observed_at, effective_through,
		  complete_through, status, verified, sequence_id
		from ledger_sync_watermarks
		where principal_id=$1 and mode=$2 and account_id=$3 and instrument_id=$4
		order by sequence_id desc
		limit 1
	`, principalID, mode, accountID, instrumentID).Scan(&watermark.FactID, &watermark.Scope.PrincipalID,
		&watermark.Scope.Mode, &watermark.Scope.AccountID, &watermark.Scope.InstrumentID,
		&watermark.Scope.WindowStart, &watermark.Scope.WindowEnd, &watermark.Scope.PaginationTraversal,
		&watermark.SourceRef, &watermark.Cursor, &watermark.Revision, &watermark.ObservedAt,
		&watermark.EffectiveThrough, &watermark.CompleteThrough, &watermark.Status,
		&watermark.Verified, &watermark.SequenceID)
	if errors.Is(err, pgx.ErrNoRows) {
		return SyncWatermark{}, false, nil
	}
	if err != nil {
		return SyncWatermark{}, false, err
	}
	return watermark, true, nil
}

func (s *PostgresStore) portfolioScopes(ctx context.Context, principalID, mode, reportingAsset string) ([]Scope, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("ledger postgres pool is required")
	}
	rows, err := s.pool.Query(ctx, `
		select distinct principal_id, mode, account_id, instrument_id,
		  window_start, window_end, pagination_traversal
		from ledger_position_snapshot_facts
		where principal_id=$1 and mode=$2 and reporting_asset=$3
		order by principal_id, mode, account_id, instrument_id, window_start, window_end, pagination_traversal
	`, principalID, mode, reportingAsset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var scopes []Scope
	for rows.Next() {
		var scope Scope
		if err := rows.Scan(&scope.PrincipalID, &scope.Mode, &scope.AccountID, &scope.InstrumentID,
			&scope.WindowStart, &scope.WindowEnd, &scope.PaginationTraversal); err != nil {
			return nil, err
		}
		scopes = append(scopes, scope)
	}
	return scopes, rows.Err()
}

type fundingPaymentScanner interface {
	Scan(...any) error
}

func scanFundingPayment(row fundingPaymentScanner) (FundingPaymentFact, error) {
	var fact FundingPaymentFact
	var supersedes, syncWatermarkID *string
	var amount string
	if err := row.Scan(&fact.FactID, &supersedes, &syncWatermarkID, &fact.PaymentID, &fact.PositionID,
		&fact.Venue, &fact.VenueEnvironment, &fact.Product, &fact.FormulaVersion,
		&fact.Scope.PrincipalID, &fact.Scope.Mode, &fact.Scope.AccountID, &fact.Scope.InstrumentID,
		&fact.Scope.WindowStart, &fact.Scope.WindowEnd, &fact.Scope.PaginationTraversal,
		&fact.Provenance.SourceRef, &fact.Provenance.SourceRevision, &fact.Provenance.SourceVersion,
		&fact.Provenance.ObservedAt, &fact.Provenance.EffectiveAt, &amount, &fact.Payment.Asset,
		&fact.SequenceID); err != nil {
		return FundingPaymentFact{}, err
	}
	if supersedes != nil {
		fact.SupersedesFactID = *supersedes
	}
	if syncWatermarkID != nil {
		fact.SyncWatermarkID = *syncWatermarkID
	}
	value, err := decimal.NewFromString(amount)
	if err != nil {
		return FundingPaymentFact{}, err
	}
	fact.Payment.Amount = value
	return fact, nil
}

func (s *PostgresStore) ingestFundingPayments(ctx context.Context, batch FundingIngestBatch) ([]FundingPaymentFact, SyncWatermark, error) {
	if err := validateFundingIngestBatch(batch); err != nil {
		return nil, SyncWatermark{}, err
	}
	if s.pool != nil {
		return s.IngestFundingPayments(ctx, batch)
	}
	expectedCount := deduplicatedFundingFactCount(batch.Payments)
	watermark := batch.Watermark
	watermark.FactID = stableID("watermark", watermark.Scope, watermark.SourceRef, watermark.Revision+"|"+watermark.Cursor)
	storedWatermark, _, err := s.appendWatermark(ctx, watermark, payloadHash(watermark))
	if err != nil {
		return nil, SyncWatermark{}, err
	}
	items := make([]FundingPaymentFact, 0, len(batch.Payments))
	journal := NewJournal(s)
	for _, payment := range batch.Payments {
		copy := payment
		copy.SyncWatermarkID = storedWatermark.FactID
		var item FundingPaymentFact
		var err error
		if copy.SupersedesFactID != "" {
			item, err = journal.AppendSupersedingFundingPayment(ctx, copy.SupersedesFactID, copy)
		} else {
			item, err = journal.AppendFundingPayment(ctx, copy)
		}
		if err != nil {
			return nil, SyncWatermark{}, err
		}
		items = append(items, item)
	}
	if err := validateWatermarkFundingCount(ctx, s, storedWatermark, expectedCount); err != nil {
		return nil, SyncWatermark{}, err
	}
	return items, storedWatermark, nil
}

func deduplicatedFundingFactCount(payments []FundingPaymentFact) int {
	ids := map[string]struct{}{}
	for _, payment := range payments {
		ids[stableID("funding", payment.Scope, payment.Provenance.SourceRef, payment.Provenance.SourceRevision)] = struct{}{}
	}
	return len(ids)
}

func validateWatermarkFundingCount(ctx context.Context, store *PostgresStore, watermark SyncWatermark, expectedCount int) error {
	var actual int
	if err := store.db.QueryRow(ctx, `
		select count(*)
		from ledger_funding_payment_facts
		where sync_watermark_id=$1
	`, watermark.FactID).Scan(&actual); err != nil {
		return err
	}
	if actual != expectedCount {
		return ErrCompletenessViolation
	}
	switch watermark.Status {
	case Complete:
		if actual == 0 {
			return ErrCompletenessViolation
		}
	case CompleteEmpty:
		if actual != 0 {
			return ErrCompletenessViolation
		}
	}
	return nil
}

func (s *PostgresStore) AcquireLineage(ctx context.Context, fact LineageFact) (LineageFact, error) {
	return NewJournal(s).AcquireLineage(ctx, fact)
}

func (s *PostgresStore) ReleaseLineageWithRevision(ctx context.Context, factID string, revision LineageRevisionFact, closedAt time.Time) (LineageRevisionFact, error) {
	if s == nil || s.pool == nil {
		return LineageRevisionFact{}, errors.New("ledger postgres pool is required")
	}
	if closedAt.IsZero() {
		return LineageRevisionFact{}, ErrCompletenessViolation
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return LineageRevisionFact{}, err
	}
	defer tx.Rollback(ctx)
	txStore := newPostgresTxStore(tx)
	stored, err := NewJournal(txStore).ReleaseLineageWithRevision(ctx, factID, revision, closedAt)
	if err != nil {
		return LineageRevisionFact{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return LineageRevisionFact{}, err
	}
	return stored, nil
}

func (s *PostgresStore) releaseLineageWithRevision(ctx context.Context, factID string, revision LineageRevisionFact, hash string, closedAt time.Time) (LineageRevisionFact, error) {
	if s != nil && s.pool != nil {
		return s.ReleaseLineageWithRevision(ctx, factID, revision, closedAt)
	}
	lineage, _, ok, err := s.getLineage(ctx, factID)
	if err != nil {
		return LineageRevisionFact{}, err
	}
	if !ok {
		return LineageRevisionFact{}, ErrFactNotFound
	}
	if !lineage.Active {
		if !lineage.ClosedAt.Equal(closedAt.UTC()) {
			return LineageRevisionFact{}, ErrFactConflict
		}
		stored, existingHash, exists, err := s.getLineageRevision(ctx, revision.FactID)
		if err != nil {
			return LineageRevisionFact{}, err
		}
		if !exists {
			return LineageRevisionFact{}, ErrFactNotFound
		}
		if existingHash != hash {
			return LineageRevisionFact{}, ErrFactConflict
		}
		return stored, nil
	}
	stored, _, err := s.appendLineageRevision(ctx, revision, hash)
	if err != nil {
		return LineageRevisionFact{}, err
	}
	if err := s.releaseLineage(ctx, factID, closedAt.UTC()); err != nil {
		return LineageRevisionFact{}, err
	}
	return stored, nil
}

func (s *PostgresStore) appendFunding(ctx context.Context, fact FundingPaymentFact, hash string) (FundingPaymentFact, bool, error) {
	var sequence int64
	err := s.db.QueryRow(ctx, `
		insert into ledger_funding_payment_facts(
		  fact_id, supersedes_fact_id, sync_watermark_id, payment_id, position_id,
		  venue, venue_environment, product, formula_version,
		  principal_id, mode, account_id, instrument_id,
		  window_start, window_end, pagination_traversal, source_ref, source_revision,
		  source_version, observed_at, effective_at, amount, asset, payload_hash
		) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24)
		on conflict do nothing
		returning sequence_id
	`, fact.FactID, nullText(fact.SupersedesFactID), nullText(fact.SyncWatermarkID), fact.PaymentID, fact.PositionID,
		fact.Venue, fact.VenueEnvironment, fact.Product, fact.FormulaVersion,
		fact.Scope.PrincipalID, fact.Scope.Mode,
		fact.Scope.AccountID, fact.Scope.InstrumentID, fact.Scope.WindowStart.UTC(), fact.Scope.WindowEnd.UTC(),
		fact.Scope.PaginationTraversal, fact.Provenance.SourceRef, fact.Provenance.SourceRevision,
		fact.Provenance.SourceVersion, fact.Provenance.ObservedAt.UTC(), fact.Provenance.EffectiveAt.UTC(),
		fact.Payment.Amount.String(), fact.Payment.Asset, hash).Scan(&sequence)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, _, ok, err := s.getFunding(ctx, fact.FactID)
		if err != nil {
			return FundingPaymentFact{}, false, err
		}
		if !ok {
			return FundingPaymentFact{}, false, ErrFactConflict
		}
		if !sameFundingPaymentPayload(existing, fact) {
			return FundingPaymentFact{}, false, ErrFactConflict
		}
		return existing, true, nil
	}
	if err != nil {
		return FundingPaymentFact{}, false, err
	}
	fact.SequenceID = sequence
	return fact, false, nil
}

func (s *PostgresStore) getFunding(ctx context.Context, factID string) (FundingPaymentFact, string, bool, error) {
	var fact FundingPaymentFact
	var amount string
	var supersedes *string
	var syncWatermarkID *string
	var hash string
	err := s.db.QueryRow(ctx, `
		select fact_id, supersedes_fact_id, sync_watermark_id, payment_id, position_id,
		  venue, venue_environment, product, formula_version,
		  principal_id, mode, account_id, instrument_id,
		  window_start, window_end, pagination_traversal, source_ref, source_revision,
		  source_version, observed_at, effective_at, amount::text, asset, sequence_id, payload_hash
		from ledger_funding_payment_facts
		where fact_id=$1
	`, factID).Scan(&fact.FactID, &supersedes, &syncWatermarkID, &fact.PaymentID, &fact.PositionID,
		&fact.Venue, &fact.VenueEnvironment, &fact.Product, &fact.FormulaVersion,
		&fact.Scope.PrincipalID, &fact.Scope.Mode,
		&fact.Scope.AccountID, &fact.Scope.InstrumentID, &fact.Scope.WindowStart, &fact.Scope.WindowEnd,
		&fact.Scope.PaginationTraversal, &fact.Provenance.SourceRef, &fact.Provenance.SourceRevision,
		&fact.Provenance.SourceVersion, &fact.Provenance.ObservedAt, &fact.Provenance.EffectiveAt,
		&amount, &fact.Payment.Asset, &fact.SequenceID, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return FundingPaymentFact{}, "", false, nil
	}
	if err != nil {
		return FundingPaymentFact{}, "", false, err
	}
	if supersedes != nil {
		fact.SupersedesFactID = *supersedes
	}
	if syncWatermarkID != nil {
		fact.SyncWatermarkID = *syncWatermarkID
	}
	value, err := decimal.NewFromString(amount)
	if err != nil {
		return FundingPaymentFact{}, "", false, err
	}
	fact.Payment.Amount = value
	return fact, hash, true, nil
}

func (s *PostgresStore) appendFill(ctx context.Context, fact FillCashflowFact, hash string) (FillCashflowFact, bool, error) {
	var sequence int64
	err := s.db.QueryRow(ctx, `
		insert into ledger_fill_cashflow_facts(
		  fact_id, supersedes_fact_id, sync_watermark_id, principal_id, mode, account_id, instrument_id,
		  window_start, window_end, pagination_traversal,
		  position_id, lineage_id, fill_id, source_ref, source_revision, source_version, observed_at, effective_at,
		  fee_amount, fee_asset, rebate_amount, rebate_asset, source_asset, reporting_asset,
		  conversion_source, conversion_as_of, rounding_mode, scale, formula_version,
		  already_signed, payload_hash
		) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31)
		on conflict do nothing
		returning sequence_id
	`, fact.FactID, nullText(fact.SupersedesFactID), nullText(fact.SyncWatermarkID), fact.Scope.PrincipalID, fact.Scope.Mode,
		fact.Scope.AccountID, fact.Scope.InstrumentID, fact.Scope.WindowStart.UTC(), fact.Scope.WindowEnd.UTC(),
		fact.Scope.PaginationTraversal, fact.PositionID, fact.LineageID, fact.FillID, fact.Provenance.SourceRef,
		fact.Provenance.SourceRevision, fact.Provenance.SourceVersion, fact.Provenance.ObservedAt.UTC(),
		fact.Provenance.EffectiveAt.UTC(), fact.Fee.Amount.String(), fact.Fee.Asset,
		fact.Rebate.Amount.String(), fact.Rebate.Asset, fact.SourceAsset, fact.ReportingAsset,
		fact.Conversion.Source, nullableTime(fact.Conversion.AsOf), fact.Conversion.RoundingMode,
		fact.Conversion.Scale, fact.Conversion.FormulaVersion, fact.AlreadySigned, hash).Scan(&sequence)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, existingHash, ok, err := s.getFill(ctx, fact.FactID)
		if err != nil {
			return FillCashflowFact{}, false, err
		}
		if !ok || existingHash != hash {
			return FillCashflowFact{}, false, ErrFactConflict
		}
		return existing, true, nil
	}
	if err != nil {
		if isUniqueViolation(err) {
			return FillCashflowFact{}, false, ErrFactConflict
		}
		return FillCashflowFact{}, false, err
	}
	fact.SequenceID = sequence
	return fact, false, nil
}

func (s *PostgresStore) getFill(ctx context.Context, factID string) (FillCashflowFact, string, bool, error) {
	rows, err := s.db.Query(ctx, `
		select fact_id, supersedes_fact_id, sync_watermark_id, principal_id, mode, account_id, instrument_id,
		  window_start, window_end, pagination_traversal, position_id, lineage_id, fill_id, source_ref,
		  source_revision, source_version, observed_at, effective_at, sequence_id,
		  fee_amount::text, fee_asset, rebate_amount::text, rebate_asset, source_asset, reporting_asset,
		  conversion_source, conversion_as_of, rounding_mode, scale, formula_version, already_signed, payload_hash
		from ledger_fill_cashflow_facts
		where fact_id=$1
	`, factID)
	if err != nil {
		return FillCashflowFact{}, "", false, err
	}
	defer rows.Close()
	entries, err := scanFillRows(rows)
	if err != nil {
		return FillCashflowFact{}, "", false, err
	}
	if len(entries) == 0 {
		return FillCashflowFact{}, "", false, nil
	}
	return *entries[0].Fill, entries[0].OrderKey, true, nil
}

func (s *PostgresStore) appendPosition(ctx context.Context, fact PositionSnapshotFact, hash string) (PositionSnapshotFact, bool, error) {
	var sequence int64
	residual, err := json.Marshal(fact.Residual)
	if err != nil {
		return PositionSnapshotFact{}, false, err
	}
	err = s.db.QueryRow(ctx, `
		insert into ledger_position_snapshot_facts(
		  fact_id, supersedes_fact_id, sync_watermark_id, principal_id, mode, account_id, instrument_id,
		  window_start, window_end, pagination_traversal,
		  position_id, source_ref, source_revision, source_version, observed_at, effective_at,
		  reporting_asset, closed, as_of, realized_trading_pnl, unrealized_trading_pnl, actual_funding_cash_flow,
		  fees, verified, residual, conversion_source, conversion_as_of, rounding_mode, scale, formula_version, payload_hash
		) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22,$23,$24,$25,$26,$27,$28,$29,$30,$31)
		on conflict do nothing
		returning sequence_id
	`, fact.FactID, nullText(fact.SupersedesFactID), nullText(fact.SyncWatermarkID), fact.Scope.PrincipalID, fact.Scope.Mode,
		fact.Scope.AccountID, fact.Scope.InstrumentID, fact.Scope.WindowStart.UTC(), fact.Scope.WindowEnd.UTC(),
		fact.Scope.PaginationTraversal, fact.PositionID, fact.Provenance.SourceRef,
		fact.Provenance.SourceRevision, fact.Provenance.SourceVersion, fact.Provenance.ObservedAt.UTC(),
		fact.Provenance.EffectiveAt.UTC(), fact.ReportingAsset, fact.Closed, nullableTime(fact.AsOf), fact.RealizedTradingPnl.String(),
		fact.UnrealizedTradingPnl.String(), fact.ActualFundingCashFlow.String(), fact.Fees.String(),
		fact.Verified, residual, fact.Conversion.Source, nullableTime(fact.Conversion.AsOf),
		fact.Conversion.RoundingMode, fact.Conversion.Scale, fact.Conversion.FormulaVersion, hash).Scan(&sequence)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, existingHash, ok, err := s.getPosition(ctx, fact.FactID)
		if err != nil {
			return PositionSnapshotFact{}, false, err
		}
		if !ok || existingHash != hash {
			return PositionSnapshotFact{}, false, ErrFactConflict
		}
		return existing, true, nil
	}
	if err != nil {
		if isUniqueViolation(err) {
			return PositionSnapshotFact{}, false, ErrFactConflict
		}
		return PositionSnapshotFact{}, false, err
	}
	fact.SequenceID = sequence
	return fact, false, nil
}

func (s *PostgresStore) getPosition(ctx context.Context, factID string) (PositionSnapshotFact, string, bool, error) {
	rows, err := s.db.Query(ctx, `
		select fact_id, supersedes_fact_id, sync_watermark_id, principal_id, mode, account_id, instrument_id,
		  window_start, window_end, pagination_traversal, position_id, source_ref, source_revision, source_version,
		  observed_at, effective_at, sequence_id, reporting_asset, closed, as_of,
		  realized_trading_pnl::text, unrealized_trading_pnl::text, actual_funding_cash_flow::text,
		  fees::text, verified, residual, conversion_source, conversion_as_of, rounding_mode, scale, formula_version, payload_hash
		from ledger_position_snapshot_facts
		where fact_id=$1
	`, factID)
	if err != nil {
		return PositionSnapshotFact{}, "", false, err
	}
	defer rows.Close()
	entries, err := scanPositionRows(rows)
	if err != nil {
		return PositionSnapshotFact{}, "", false, err
	}
	if len(entries) == 0 {
		return PositionSnapshotFact{}, "", false, nil
	}
	return *entries[0].Position, entries[0].OrderKey, true, nil
}

func (s *PostgresStore) appendWatermark(ctx context.Context, watermark SyncWatermark, hash string) (SyncWatermark, bool, error) {
	var sequence int64
	err := s.db.QueryRow(ctx, `
		insert into ledger_sync_watermarks(
		  fact_id, principal_id, mode, account_id, instrument_id, window_start, window_end,
		  pagination_traversal, source_ref, cursor, revision, observed_at, effective_through,
		  complete_through, status, verified, payload_hash
		) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		on conflict do nothing
		returning sequence_id
	`, watermark.FactID, watermark.Scope.PrincipalID, watermark.Scope.Mode, watermark.Scope.AccountID,
		watermark.Scope.InstrumentID, watermark.Scope.WindowStart.UTC(), watermark.Scope.WindowEnd.UTC(),
		watermark.Scope.PaginationTraversal, watermark.SourceRef, watermark.Cursor, watermark.Revision,
		watermark.ObservedAt.UTC(), watermark.EffectiveThrough.UTC(), watermark.CompleteThrough.UTC(),
		string(watermark.Status), watermark.Verified, hash).Scan(&sequence)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, _, ok, err := s.getWatermark(ctx, watermark.FactID)
		if err != nil {
			return SyncWatermark{}, false, err
		}
		if !ok || !sameSyncWatermarkPayload(existing, watermark) {
			return SyncWatermark{}, false, ErrFactConflict
		}
		return existing, true, nil
	}
	if err != nil {
		return SyncWatermark{}, false, err
	}
	watermark.SequenceID = sequence
	return watermark, false, nil
}

func (s *PostgresStore) getWatermark(ctx context.Context, factID string) (SyncWatermark, string, bool, error) {
	var watermark SyncWatermark
	var hash string
	err := s.db.QueryRow(ctx, `
		select fact_id, principal_id, mode, account_id, instrument_id, window_start, window_end,
		  pagination_traversal, source_ref, cursor, revision, observed_at, effective_through,
		  complete_through, status, verified, sequence_id, payload_hash
		from ledger_sync_watermarks
		where fact_id=$1
	`, factID).Scan(&watermark.FactID, &watermark.Scope.PrincipalID, &watermark.Scope.Mode,
		&watermark.Scope.AccountID, &watermark.Scope.InstrumentID, &watermark.Scope.WindowStart,
		&watermark.Scope.WindowEnd, &watermark.Scope.PaginationTraversal, &watermark.SourceRef,
		&watermark.Cursor, &watermark.Revision, &watermark.ObservedAt, &watermark.EffectiveThrough,
		&watermark.CompleteThrough, &watermark.Status, &watermark.Verified, &watermark.SequenceID, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return SyncWatermark{}, "", false, nil
	}
	if err != nil {
		return SyncWatermark{}, "", false, err
	}
	return watermark, hash, true, nil
}

func (s *PostgresStore) acquireLineage(ctx context.Context, lineage LineageFact, hash string) (LineageFact, bool, error) {
	var sequence int64
	err := s.db.QueryRow(ctx, `
		insert into ledger_lineage_facts(
		  fact_id, principal_id, mode, account_id, instrument_id, venue_environment, product,
		  lineage_id, position_id, source_ref, source_revision, source_version,
		  observed_at, effective_at, active, payload_hash
		) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,true,$15)
		on conflict do nothing
		returning sequence_id
	`, lineage.FactID, lineage.Scope.PrincipalID, lineage.Scope.Mode, lineage.Scope.AccountID,
		lineage.Scope.InstrumentID, lineage.VenueEnvironment, lineage.Product, lineage.LineageID, lineage.PositionID,
		lineage.Provenance.SourceRef, lineage.Provenance.SourceRevision, lineage.Provenance.SourceVersion,
		lineage.Provenance.ObservedAt.UTC(), lineage.Provenance.EffectiveAt.UTC(), hash).Scan(&sequence)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, existingHash, ok, err := s.getLineage(ctx, lineage.FactID)
		if err != nil {
			return LineageFact{}, false, err
		}
		if !ok {
			return LineageFact{}, false, ErrActiveLineageExists
		}
		if existingHash != hash {
			return LineageFact{}, false, ErrFactConflict
		}
		return existing, true, nil
	}
	if err != nil {
		var pgErr *pgconn.PgError
		if errors.As(err, &pgErr) && pgErr.Code == "23505" {
			return LineageFact{}, false, ErrActiveLineageExists
		}
		return LineageFact{}, false, err
	}
	lineage.Active = true
	lineage.SequenceID = sequence
	return lineage, false, nil
}

func (s *PostgresStore) getLineage(ctx context.Context, factID string) (LineageFact, string, bool, error) {
	var fact LineageFact
	var closedAt *time.Time
	var hash string
	err := s.db.QueryRow(ctx, `
		select fact_id, principal_id, mode, account_id, instrument_id, venue_environment, product,
		  lineage_id, position_id, source_ref, source_revision, source_version, observed_at,
		  effective_at, active, closed_at, sequence_id, payload_hash
		from ledger_lineage_facts
		where fact_id=$1
	`, factID).Scan(&fact.FactID, &fact.Scope.PrincipalID, &fact.Scope.Mode, &fact.Scope.AccountID,
		&fact.Scope.InstrumentID, &fact.VenueEnvironment, &fact.Product, &fact.LineageID,
		&fact.PositionID, &fact.Provenance.SourceRef, &fact.Provenance.SourceRevision,
		&fact.Provenance.SourceVersion, &fact.Provenance.ObservedAt, &fact.Provenance.EffectiveAt,
		&fact.Active, &closedAt, &fact.SequenceID, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return LineageFact{}, "", false, nil
	}
	if err != nil {
		return LineageFact{}, "", false, err
	}
	if closedAt != nil {
		fact.ClosedAt = *closedAt
	}
	return fact, hash, true, nil
}

func (s *PostgresStore) appendLineageRevision(ctx context.Context, fact LineageRevisionFact, hash string) (LineageRevisionFact, bool, error) {
	var sequence int64
	err := s.db.QueryRow(ctx, `
		insert into ledger_lineage_revision_facts(
		  fact_id, supersedes_fact_id, sync_watermark_id, lineage_fact_id, principal_id, mode, account_id, instrument_id,
		  revision, status, reason, source_ref, source_revision, source_version, observed_at,
		  effective_at, payload_hash
		) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		on conflict do nothing
		returning sequence_id
	`, fact.FactID, nullText(fact.SupersedesFactID), nullText(fact.SyncWatermarkID), fact.LineageFactID, fact.Scope.PrincipalID,
		fact.Scope.Mode, fact.Scope.AccountID, fact.Scope.InstrumentID, fact.Revision, fact.Status,
		fact.Reason, fact.Provenance.SourceRef, fact.Provenance.SourceRevision, fact.Provenance.SourceVersion,
		fact.Provenance.ObservedAt.UTC(), fact.Provenance.EffectiveAt.UTC(), hash).Scan(&sequence)
	if errors.Is(err, pgx.ErrNoRows) {
		existing, existingHash, ok, err := s.getLineageRevision(ctx, fact.FactID)
		if err != nil {
			return LineageRevisionFact{}, false, err
		}
		if !ok || existingHash != hash {
			return LineageRevisionFact{}, false, ErrFactConflict
		}
		return existing, true, nil
	}
	if err != nil {
		if isUniqueViolation(err) {
			return LineageRevisionFact{}, false, ErrFactConflict
		}
		return LineageRevisionFact{}, false, err
	}
	fact.SequenceID = sequence
	return fact, false, nil
}

func (s *PostgresStore) getLineageRevision(ctx context.Context, factID string) (LineageRevisionFact, string, bool, error) {
	var fact LineageRevisionFact
	var supersedes, syncWatermarkID *string
	var hash string
	err := s.db.QueryRow(ctx, `
		select fact_id, supersedes_fact_id, sync_watermark_id, lineage_fact_id, principal_id, mode, account_id,
		  instrument_id, revision, status, reason, source_ref, source_revision, source_version,
		  observed_at, effective_at, sequence_id, payload_hash
		from ledger_lineage_revision_facts
		where fact_id=$1
	`, factID).Scan(&fact.FactID, &supersedes, &syncWatermarkID, &fact.LineageFactID, &fact.Scope.PrincipalID,
		&fact.Scope.Mode, &fact.Scope.AccountID, &fact.Scope.InstrumentID, &fact.Revision, &fact.Status,
		&fact.Reason, &fact.Provenance.SourceRef, &fact.Provenance.SourceRevision, &fact.Provenance.SourceVersion,
		&fact.Provenance.ObservedAt, &fact.Provenance.EffectiveAt, &fact.SequenceID, &hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return LineageRevisionFact{}, "", false, nil
	}
	if err != nil {
		return LineageRevisionFact{}, "", false, err
	}
	if supersedes != nil {
		fact.SupersedesFactID = *supersedes
	}
	if syncWatermarkID != nil {
		fact.SyncWatermarkID = *syncWatermarkID
	}
	return fact, hash, true, nil
}

func (s *PostgresStore) releaseLineage(ctx context.Context, factID string, closedAt time.Time) error {
	tag, err := s.db.Exec(ctx, `update ledger_lineage_facts set active=false, closed_at=$2 where fact_id=$1 and active`, factID, closedAt.UTC())
	if err != nil {
		return err
	}
	if tag.RowsAffected() != 1 {
		return ErrFactNotFound
	}
	return nil
}

func (s *PostgresStore) replay(ctx context.Context, scope Scope) ([]ReplayEntry, error) {
	var entries []ReplayEntry
	funding, err := s.replayFunding(ctx, scope)
	if err != nil {
		return nil, err
	}
	entries = append(entries, funding...)
	fills, err := s.replayFills(ctx, scope)
	if err != nil {
		return nil, err
	}
	entries = append(entries, fills...)
	positions, err := s.replayPositions(ctx, scope)
	if err != nil {
		return nil, err
	}
	entries = append(entries, positions...)
	lineages, err := s.replayLineages(ctx, scope)
	if err != nil {
		return nil, err
	}
	entries = append(entries, lineages...)
	revisions, err := s.replayLineageRevisions(ctx, scope)
	if err != nil {
		return nil, err
	}
	entries = append(entries, revisions...)
	watermarks, err := s.replayWatermarks(ctx, scope)
	if err != nil {
		return nil, err
	}
	entries = append(entries, watermarks...)
	return entries, nil
}

func (s *PostgresStore) replayFills(ctx context.Context, scope Scope) ([]ReplayEntry, error) {
	rows, err := s.db.Query(ctx, `
		select fact_id, supersedes_fact_id, sync_watermark_id, window_start, window_end, pagination_traversal,
		  position_id, lineage_id, fill_id, source_ref, source_revision, source_version, observed_at, effective_at,
		  sequence_id, fee_amount::text, fee_asset, rebate_amount::text, rebate_asset, source_asset, reporting_asset,
		  conversion_source, conversion_as_of, rounding_mode, scale, formula_version, already_signed
		from ledger_fill_cashflow_facts
		where principal_id=$1 and mode=$2 and account_id=$3 and instrument_id=$4
		  and window_start=$5 and window_end=$6 and pagination_traversal=$7
	`, scope.PrincipalID, scope.Mode, scope.AccountID, scope.InstrumentID, scope.WindowStart.UTC(), scope.WindowEnd.UTC(), scope.PaginationTraversal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []ReplayEntry
	for rows.Next() {
		var fact FillCashflowFact
		var supersedes, syncWatermarkID *string
		var conversionAsOf *time.Time
		var fee, rebate string
		fact.Scope.PrincipalID = scope.PrincipalID
		fact.Scope.Mode = scope.Mode
		fact.Scope.AccountID = scope.AccountID
		fact.Scope.InstrumentID = scope.InstrumentID
		if err := rows.Scan(&fact.FactID, &supersedes, &syncWatermarkID, &fact.Scope.WindowStart, &fact.Scope.WindowEnd,
			&fact.Scope.PaginationTraversal, &fact.PositionID, &fact.LineageID, &fact.FillID, &fact.Provenance.SourceRef,
			&fact.Provenance.SourceRevision, &fact.Provenance.SourceVersion, &fact.Provenance.ObservedAt,
			&fact.Provenance.EffectiveAt, &fact.SequenceID, &fee, &fact.Fee.Asset, &rebate, &fact.Rebate.Asset,
			&fact.SourceAsset, &fact.ReportingAsset, &fact.Conversion.Source, &conversionAsOf, &fact.Conversion.RoundingMode,
			&fact.Conversion.Scale, &fact.Conversion.FormulaVersion, &fact.AlreadySigned); err != nil {
			return nil, err
		}
		if supersedes != nil {
			fact.SupersedesFactID = *supersedes
		}
		if syncWatermarkID != nil {
			fact.SyncWatermarkID = *syncWatermarkID
		}
		if conversionAsOf != nil {
			fact.Conversion.AsOf = *conversionAsOf
		}
		value, err := decimal.NewFromString(fee)
		if err != nil {
			return nil, err
		}
		fact.Fee.Amount = value
		value, err = decimal.NewFromString(rebate)
		if err != nil {
			return nil, err
		}
		fact.Rebate.Amount = value
		copy := fact
		entries = append(entries, ReplayEntry{Kind: "fill", Revision: fact.Provenance.SourceRevision, SequenceID: fact.SequenceID, Fill: &copy})
	}
	return entries, rows.Err()
}

func scanFillRows(rows pgx.Rows) ([]ReplayEntry, error) {
	var entries []ReplayEntry
	for rows.Next() {
		var fact FillCashflowFact
		var supersedes, syncWatermarkID *string
		var conversionAsOf *time.Time
		var fee, rebate, hash string
		if err := rows.Scan(&fact.FactID, &supersedes, &syncWatermarkID, &fact.Scope.PrincipalID, &fact.Scope.Mode,
			&fact.Scope.AccountID, &fact.Scope.InstrumentID, &fact.Scope.WindowStart, &fact.Scope.WindowEnd,
			&fact.Scope.PaginationTraversal, &fact.PositionID, &fact.LineageID, &fact.FillID, &fact.Provenance.SourceRef,
			&fact.Provenance.SourceRevision, &fact.Provenance.SourceVersion, &fact.Provenance.ObservedAt,
			&fact.Provenance.EffectiveAt, &fact.SequenceID, &fee, &fact.Fee.Asset, &rebate, &fact.Rebate.Asset,
			&fact.SourceAsset, &fact.ReportingAsset, &fact.Conversion.Source, &conversionAsOf, &fact.Conversion.RoundingMode,
			&fact.Conversion.Scale, &fact.Conversion.FormulaVersion, &fact.AlreadySigned, &hash); err != nil {
			return nil, err
		}
		if supersedes != nil {
			fact.SupersedesFactID = *supersedes
		}
		if syncWatermarkID != nil {
			fact.SyncWatermarkID = *syncWatermarkID
		}
		if conversionAsOf != nil {
			fact.Conversion.AsOf = *conversionAsOf
		}
		value, err := decimal.NewFromString(fee)
		if err != nil {
			return nil, err
		}
		fact.Fee.Amount = value
		value, err = decimal.NewFromString(rebate)
		if err != nil {
			return nil, err
		}
		fact.Rebate.Amount = value
		copy := fact
		entries = append(entries, ReplayEntry{Kind: "fill", Revision: fact.Provenance.SourceRevision, SequenceID: fact.SequenceID, OrderKey: hash, Fill: &copy})
	}
	return entries, rows.Err()
}

func (s *PostgresStore) replayPositions(ctx context.Context, scope Scope) ([]ReplayEntry, error) {
	rows, err := s.db.Query(ctx, `
		select fact_id, supersedes_fact_id, sync_watermark_id, window_start, window_end, pagination_traversal,
		  position_id, source_ref, source_revision, source_version, observed_at, effective_at, sequence_id, reporting_asset,
		  closed, as_of, realized_trading_pnl::text, unrealized_trading_pnl::text, actual_funding_cash_flow::text,
		  fees::text, verified, residual, conversion_source, conversion_as_of, rounding_mode, scale, formula_version
		from ledger_position_snapshot_facts
		where principal_id=$1 and mode=$2 and account_id=$3 and instrument_id=$4
		  and window_start=$5 and window_end=$6 and pagination_traversal=$7
	`, scope.PrincipalID, scope.Mode, scope.AccountID, scope.InstrumentID, scope.WindowStart.UTC(), scope.WindowEnd.UTC(), scope.PaginationTraversal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []ReplayEntry
	for rows.Next() {
		var fact PositionSnapshotFact
		var supersedes, syncWatermarkID *string
		var asOf, conversionAsOf *time.Time
		var realized, unrealized, funding, fees string
		var residual []byte
		fact.Scope.PrincipalID = scope.PrincipalID
		fact.Scope.Mode = scope.Mode
		fact.Scope.AccountID = scope.AccountID
		fact.Scope.InstrumentID = scope.InstrumentID
		if err := rows.Scan(&fact.FactID, &supersedes, &syncWatermarkID, &fact.Scope.WindowStart, &fact.Scope.WindowEnd,
			&fact.Scope.PaginationTraversal, &fact.PositionID, &fact.Provenance.SourceRef, &fact.Provenance.SourceRevision,
			&fact.Provenance.SourceVersion, &fact.Provenance.ObservedAt, &fact.Provenance.EffectiveAt,
			&fact.SequenceID, &fact.ReportingAsset, &fact.Closed, &asOf, &realized, &unrealized, &funding, &fees,
			&fact.Verified, &residual, &fact.Conversion.Source, &conversionAsOf, &fact.Conversion.RoundingMode,
			&fact.Conversion.Scale, &fact.Conversion.FormulaVersion); err != nil {
			return nil, err
		}
		if supersedes != nil {
			fact.SupersedesFactID = *supersedes
		}
		if syncWatermarkID != nil {
			fact.SyncWatermarkID = *syncWatermarkID
		}
		if asOf != nil {
			fact.AsOf = *asOf
		}
		if conversionAsOf != nil {
			fact.Conversion.AsOf = *conversionAsOf
		}
		if err := json.Unmarshal(residual, &fact.Residual); err != nil {
			return nil, err
		}
		value, err := decimal.NewFromString(realized)
		if err != nil {
			return nil, err
		}
		fact.RealizedTradingPnl = value
		value, err = decimal.NewFromString(unrealized)
		if err != nil {
			return nil, err
		}
		fact.UnrealizedTradingPnl = value
		value, err = decimal.NewFromString(funding)
		if err != nil {
			return nil, err
		}
		fact.ActualFundingCashFlow = value
		value, err = decimal.NewFromString(fees)
		if err != nil {
			return nil, err
		}
		fact.Fees = value
		copy := fact
		entries = append(entries, ReplayEntry{Kind: "position", Revision: fact.Provenance.SourceRevision, SequenceID: fact.SequenceID, Position: &copy})
	}
	return entries, rows.Err()
}

func scanPositionRows(rows pgx.Rows) ([]ReplayEntry, error) {
	var entries []ReplayEntry
	for rows.Next() {
		var fact PositionSnapshotFact
		var supersedes, syncWatermarkID *string
		var asOf, conversionAsOf *time.Time
		var realized, unrealized, funding, fees, hash string
		var residual []byte
		if err := rows.Scan(&fact.FactID, &supersedes, &syncWatermarkID, &fact.Scope.PrincipalID, &fact.Scope.Mode,
			&fact.Scope.AccountID, &fact.Scope.InstrumentID, &fact.Scope.WindowStart, &fact.Scope.WindowEnd,
			&fact.Scope.PaginationTraversal, &fact.PositionID, &fact.Provenance.SourceRef, &fact.Provenance.SourceRevision,
			&fact.Provenance.SourceVersion, &fact.Provenance.ObservedAt, &fact.Provenance.EffectiveAt,
			&fact.SequenceID, &fact.ReportingAsset, &fact.Closed, &asOf, &realized, &unrealized, &funding, &fees,
			&fact.Verified, &residual, &fact.Conversion.Source, &conversionAsOf, &fact.Conversion.RoundingMode,
			&fact.Conversion.Scale, &fact.Conversion.FormulaVersion, &hash); err != nil {
			return nil, err
		}
		if supersedes != nil {
			fact.SupersedesFactID = *supersedes
		}
		if syncWatermarkID != nil {
			fact.SyncWatermarkID = *syncWatermarkID
		}
		if asOf != nil {
			fact.AsOf = *asOf
		}
		if conversionAsOf != nil {
			fact.Conversion.AsOf = *conversionAsOf
		}
		if err := json.Unmarshal(residual, &fact.Residual); err != nil {
			return nil, err
		}
		value, err := decimal.NewFromString(realized)
		if err != nil {
			return nil, err
		}
		fact.RealizedTradingPnl = value
		value, err = decimal.NewFromString(unrealized)
		if err != nil {
			return nil, err
		}
		fact.UnrealizedTradingPnl = value
		value, err = decimal.NewFromString(funding)
		if err != nil {
			return nil, err
		}
		fact.ActualFundingCashFlow = value
		value, err = decimal.NewFromString(fees)
		if err != nil {
			return nil, err
		}
		fact.Fees = value
		copy := fact
		entries = append(entries, ReplayEntry{Kind: "position", Revision: fact.Provenance.SourceRevision, SequenceID: fact.SequenceID, OrderKey: hash, Position: &copy})
	}
	return entries, rows.Err()
}

func (s *PostgresStore) replayLineages(ctx context.Context, scope Scope) ([]ReplayEntry, error) {
	rows, err := s.db.Query(ctx, `
		select fact_id, venue_environment, product, lineage_id, position_id, source_ref,
		  source_revision, source_version, observed_at, effective_at, sequence_id, active, closed_at
		from ledger_lineage_facts
		where principal_id=$1 and mode=$2 and account_id=$3 and instrument_id=$4
	`, scope.PrincipalID, scope.Mode, scope.AccountID, scope.InstrumentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []ReplayEntry
	for rows.Next() {
		var fact LineageFact
		var closedAt *time.Time
		fact.Scope = lineageScope(scope)
		if err := rows.Scan(&fact.FactID, &fact.VenueEnvironment, &fact.Product, &fact.LineageID, &fact.PositionID,
			&fact.Provenance.SourceRef, &fact.Provenance.SourceRevision, &fact.Provenance.SourceVersion,
			&fact.Provenance.ObservedAt, &fact.Provenance.EffectiveAt, &fact.SequenceID, &fact.Active, &closedAt); err != nil {
			return nil, err
		}
		if closedAt != nil {
			fact.ClosedAt = *closedAt
		}
		copy := fact
		entries = append(entries, ReplayEntry{Kind: "lineage", Revision: fact.Provenance.SourceRevision, SequenceID: fact.SequenceID, Lineage: &copy})
	}
	return entries, rows.Err()
}

func (s *PostgresStore) replayLineageRevisions(ctx context.Context, scope Scope) ([]ReplayEntry, error) {
	rows, err := s.db.Query(ctx, `
		select fact_id, supersedes_fact_id, sync_watermark_id, lineage_fact_id, revision, status, reason,
		  source_ref, source_revision, source_version, observed_at, effective_at, sequence_id
		from ledger_lineage_revision_facts
		where principal_id=$1 and mode=$2 and account_id=$3 and instrument_id=$4
	`, scope.PrincipalID, scope.Mode, scope.AccountID, scope.InstrumentID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []ReplayEntry
	for rows.Next() {
		var fact LineageRevisionFact
		var supersedes, syncWatermarkID *string
		fact.Scope = lineageScope(scope)
		if err := rows.Scan(&fact.FactID, &supersedes, &syncWatermarkID, &fact.LineageFactID, &fact.Revision,
			&fact.Status, &fact.Reason, &fact.Provenance.SourceRef, &fact.Provenance.SourceRevision,
			&fact.Provenance.SourceVersion, &fact.Provenance.ObservedAt, &fact.Provenance.EffectiveAt,
			&fact.SequenceID); err != nil {
			return nil, err
		}
		if supersedes != nil {
			fact.SupersedesFactID = *supersedes
		}
		if syncWatermarkID != nil {
			fact.SyncWatermarkID = *syncWatermarkID
		}
		copy := fact
		entries = append(entries, ReplayEntry{Kind: "lineage_revision", Revision: fact.Provenance.SourceRevision, SequenceID: fact.SequenceID, RevisionFact: &copy})
	}
	return entries, rows.Err()
}

func (s *PostgresStore) replayFunding(ctx context.Context, scope Scope) ([]ReplayEntry, error) {
	rows, err := s.db.Query(ctx, `
		select fact_id, supersedes_fact_id, sync_watermark_id, payment_id, position_id,
		  venue, venue_environment, product, formula_version, source_ref, source_revision, source_version,
		  observed_at, effective_at, sequence_id, amount::text, asset
		from ledger_funding_payment_facts
		where principal_id=$1 and mode=$2 and account_id=$3 and instrument_id=$4
		  and window_start=$5 and window_end=$6 and pagination_traversal=$7
	`, scope.PrincipalID, scope.Mode, scope.AccountID, scope.InstrumentID, scope.WindowStart.UTC(), scope.WindowEnd.UTC(), scope.PaginationTraversal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []ReplayEntry
	for rows.Next() {
		var fact FundingPaymentFact
		var supersedes, syncWatermarkID *string
		var amount string
		fact.Scope = scope
		if err := rows.Scan(&fact.FactID, &supersedes, &syncWatermarkID, &fact.PaymentID, &fact.PositionID,
			&fact.Venue, &fact.VenueEnvironment, &fact.Product, &fact.FormulaVersion, &fact.Provenance.SourceRef,
			&fact.Provenance.SourceRevision, &fact.Provenance.SourceVersion, &fact.Provenance.ObservedAt,
			&fact.Provenance.EffectiveAt, &fact.SequenceID, &amount, &fact.Payment.Asset); err != nil {
			return nil, err
		}
		if supersedes != nil {
			fact.SupersedesFactID = *supersedes
		}
		if syncWatermarkID != nil {
			fact.SyncWatermarkID = *syncWatermarkID
		}
		value, err := decimal.NewFromString(amount)
		if err != nil {
			return nil, err
		}
		fact.Payment.Amount = value
		copy := fact
		entries = append(entries, ReplayEntry{Kind: "funding", Revision: fact.Provenance.SourceRevision, SequenceID: fact.SequenceID, Funding: &copy})
	}
	return entries, rows.Err()
}

func lineageScope(scope Scope) Scope {
	return Scope{
		PrincipalID:  scope.PrincipalID,
		Mode:         scope.Mode,
		AccountID:    scope.AccountID,
		InstrumentID: scope.InstrumentID,
	}
}

func (s *PostgresStore) replayWatermarks(ctx context.Context, scope Scope) ([]ReplayEntry, error) {
	rows, err := s.db.Query(ctx, `
		select fact_id, source_ref, cursor, revision, observed_at, effective_through,
		  complete_through, status, verified, sequence_id
		from ledger_sync_watermarks
		where principal_id=$1 and mode=$2 and account_id=$3 and instrument_id=$4
		  and window_start=$5 and window_end=$6 and pagination_traversal=$7
	`, scope.PrincipalID, scope.Mode, scope.AccountID, scope.InstrumentID, scope.WindowStart.UTC(), scope.WindowEnd.UTC(), scope.PaginationTraversal)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var entries []ReplayEntry
	for rows.Next() {
		var watermark SyncWatermark
		watermark.Scope = scope
		if err := rows.Scan(&watermark.FactID, &watermark.SourceRef, &watermark.Cursor, &watermark.Revision,
			&watermark.ObservedAt, &watermark.EffectiveThrough, &watermark.CompleteThrough,
			&watermark.Status, &watermark.Verified, &watermark.SequenceID); err != nil {
			return nil, err
		}
		copy := watermark
		entries = append(entries, ReplayEntry{Kind: "watermark", Revision: watermark.Revision, Marker: watermark.Cursor, SequenceID: watermark.SequenceID, Watermark: &copy})
	}
	return entries, rows.Err()
}

func nullText(value string) any {
	if value == "" {
		return nil
	}
	return value
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}

func (s *PostgresStore) payloadHashByID(ctx context.Context, table, factID string) (string, bool, error) {
	var hash string
	err := s.db.QueryRow(ctx, "select payload_hash from "+table+" where fact_id=$1", factID).Scan(&hash)
	if errors.Is(err, pgx.ErrNoRows) {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}
	return hash, true, nil
}

func (s *PostgresStore) payloadHashMatches(ctx context.Context, table, factID, hash string) (bool, error) {
	existing, ok, err := s.payloadHashByID(ctx, table, factID)
	if err != nil || !ok {
		return false, err
	}
	return existing == hash, nil
}

func isUniqueViolation(err error) bool {
	var pgErr *pgconn.PgError
	return errors.As(err, &pgErr) && pgErr.Code == "23505"
}

func (s *PostgresStore) lineageInactive(ctx context.Context, factID string) (bool, error) {
	var inactive bool
	err := s.db.QueryRow(ctx, `select not active from ledger_lineage_facts where fact_id=$1`, factID).Scan(&inactive)
	if errors.Is(err, pgx.ErrNoRows) {
		return false, nil
	}
	return inactive, err
}
