package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/fundingsettlement"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

type CatalogRevision struct {
	ScopeKey    string
	Revision    int64
	Fingerprint string
	ObservedAt  time.Time
	Instruments json.RawMessage
}

// SaveCatalogRevision appends only when the immutable catalog fingerprint
// differs from the current head, then atomically advances the head. A scope may
// legitimately return to an older fingerprint (A -> B -> A), so fingerprints
// are not globally unique across its revision history. The transaction-scoped
// advisory lock serializes both first creation and later refreshes for a scope.
func SaveCatalogRevision(ctx context.Context, pool *pgxpool.Pool, revision CatalogRevision) (CatalogRevision, bool, error) {
	if pool == nil || ctx == nil || ctx.Err() != nil || strings.TrimSpace(revision.ScopeKey) == "" || strings.TrimSpace(revision.Fingerprint) == "" || revision.ObservedAt.IsZero() || !json.Valid(revision.Instruments) {
		return CatalogRevision{}, false, fmt.Errorf("catalog revision is invalid")
	}
	tx, err := pool.BeginTx(ctx, pgx.TxOptions{})
	if err != nil {
		return CatalogRevision{}, false, err
	}
	defer func() { _ = tx.Rollback(ctx) }()
	if _, err = tx.Exec(ctx, `SELECT pg_advisory_xact_lock(hashtextextended($1, 0))`, revision.ScopeKey); err != nil {
		return CatalogRevision{}, false, err
	}

	var current CatalogRevision
	err = tx.QueryRow(ctx, `
		SELECT h.current_revision, h.fingerprint, r.observed_at, r.instruments
		FROM market_catalog_heads h
		JOIN market_catalog_revisions r ON r.scope_key = h.scope_key AND r.revision = h.current_revision
		WHERE h.scope_key = $1
		FOR UPDATE OF h`, revision.ScopeKey).Scan(&current.Revision, &current.Fingerprint, &current.ObservedAt, &current.Instruments)
	if err == nil && current.Fingerprint == revision.Fingerprint {
		current.ScopeKey = revision.ScopeKey
		if commitErr := tx.Commit(ctx); commitErr != nil {
			return CatalogRevision{}, false, commitErr
		}
		return current, false, nil
	}
	if err != nil && !errors.Is(err, pgx.ErrNoRows) {
		return CatalogRevision{}, false, err
	}
	if current.Revision == 0 {
		if err = tx.QueryRow(ctx, `SELECT COALESCE(MAX(revision), 0) FROM market_catalog_revisions WHERE scope_key = $1`, revision.ScopeKey).Scan(&current.Revision); err != nil {
			return CatalogRevision{}, false, err
		}
	}
	revision.Revision = current.Revision + 1
	_, err = tx.Exec(ctx, `
		INSERT INTO market_catalog_revisions (scope_key, revision, fingerprint, observed_at, instruments)
		VALUES ($1, $2, $3, $4, $5)`, revision.ScopeKey, revision.Revision, revision.Fingerprint, revision.ObservedAt.UTC(), revision.Instruments)
	if err != nil {
		return CatalogRevision{}, false, err
	}
	_, err = tx.Exec(ctx, `
		INSERT INTO market_catalog_heads (scope_key, current_revision, fingerprint, updated_at)
		VALUES ($1, $2, $3, $4)
		ON CONFLICT (scope_key) DO UPDATE SET current_revision = EXCLUDED.current_revision,
		fingerprint = EXCLUDED.fingerprint, updated_at = EXCLUDED.updated_at`,
		revision.ScopeKey, revision.Revision, revision.Fingerprint, revision.ObservedAt.UTC())
	if err != nil {
		return CatalogRevision{}, false, err
	}
	if err = tx.Commit(ctx); err != nil {
		return CatalogRevision{}, false, err
	}
	return revision, true, nil
}

func LoadCatalogHead(ctx context.Context, pool *pgxpool.Pool, scopeKey string) (CatalogRevision, bool, error) {
	if pool == nil || ctx == nil || strings.TrimSpace(scopeKey) == "" {
		return CatalogRevision{}, false, fmt.Errorf("catalog scope is required")
	}
	var revision CatalogRevision
	err := pool.QueryRow(ctx, `
		SELECT r.scope_key, r.revision, r.fingerprint, r.observed_at, r.instruments
		FROM market_catalog_heads h
		JOIN market_catalog_revisions r ON r.scope_key = h.scope_key AND r.revision = h.current_revision
		WHERE h.scope_key = $1`, scopeKey).Scan(&revision.ScopeKey, &revision.Revision, &revision.Fingerprint, &revision.ObservedAt, &revision.Instruments)
	if errors.Is(err, pgx.ErrNoRows) {
		return CatalogRevision{}, false, nil
	}
	return revision, err == nil, err
}

type SettledFundingRate struct {
	RouteProfileID string
	InstrumentID   string
	FundingTime    time.Time
	RealizedRate   decimal.Decimal
	ObservedAt     time.Time
	Source         string
}

func UpsertSettledFundingRate(ctx context.Context, pool *pgxpool.Pool, rate SettledFundingRate) error {
	if pool == nil || ctx == nil || strings.TrimSpace(rate.RouteProfileID) == "" || strings.TrimSpace(rate.InstrumentID) == "" || rate.FundingTime.IsZero() || rate.ObservedAt.IsZero() || strings.TrimSpace(rate.Source) == "" {
		return fmt.Errorf("settled funding rate is invalid")
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO settled_funding_rates (route_profile_id, instrument_id, funding_time, realized_rate, observed_at, source)
		VALUES ($1, $2, $3, $4, $5, $6)
		ON CONFLICT (route_profile_id, instrument_id, funding_time) DO UPDATE SET
		realized_rate = EXCLUDED.realized_rate, observed_at = EXCLUDED.observed_at, source = EXCLUDED.source`,
		rate.RouteProfileID, rate.InstrumentID, rate.FundingTime.UTC(), rate.RealizedRate.String(), rate.ObservedAt.UTC(), rate.Source)
	return err
}

type FundingTracking struct {
	InstrumentID          string
	RouteProfileID        string
	ActiveReasons         []string
	TrackingStartedAt     time.Time
	TargetNextFundingTime time.Time
	State                 string
	RetryAt               time.Time
	RetryCount            int
	ErrorClass            string
	UpdatedAt             time.Time
}

func SaveFundingTracking(ctx context.Context, pool *pgxpool.Pool, tracking FundingTracking) error {
	if pool == nil || ctx == nil || strings.TrimSpace(tracking.InstrumentID) == "" || strings.TrimSpace(tracking.RouteProfileID) == "" || tracking.TrackingStartedAt.IsZero() || tracking.TargetNextFundingTime.IsZero() || tracking.UpdatedAt.IsZero() {
		return fmt.Errorf("funding tracking state is invalid")
	}
	_, err := pool.Exec(ctx, `
		INSERT INTO settled_funding_tracking
		(instrument_id, route_profile_id, active_reasons, tracking_started_at, target_next_funding_time, state, retry_at, retry_count, error_class, updated_at)
		VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10)
		ON CONFLICT (instrument_id) DO UPDATE SET route_profile_id=EXCLUDED.route_profile_id,
		active_reasons=EXCLUDED.active_reasons, target_next_funding_time=EXCLUDED.target_next_funding_time,
		state=EXCLUDED.state, retry_at=EXCLUDED.retry_at, retry_count=EXCLUDED.retry_count,
		error_class=EXCLUDED.error_class, updated_at=EXCLUDED.updated_at`,
		tracking.InstrumentID, tracking.RouteProfileID, tracking.ActiveReasons, tracking.TrackingStartedAt.UTC(),
		tracking.TargetNextFundingTime.UTC(), tracking.State, nullableTime(tracking.RetryAt), tracking.RetryCount,
		tracking.ErrorClass, tracking.UpdatedAt.UTC())
	return err
}

func LoadFundingTracking(ctx context.Context, pool *pgxpool.Pool) ([]FundingTracking, error) {
	if pool == nil || ctx == nil {
		return nil, fmt.Errorf("funding tracking store is unavailable")
	}
	rows, err := pool.Query(ctx, `
		SELECT instrument_id, route_profile_id, active_reasons, tracking_started_at,
		target_next_funding_time, state, retry_at, retry_count, error_class, updated_at
		FROM settled_funding_tracking ORDER BY instrument_id`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	result := make([]FundingTracking, 0)
	for rows.Next() {
		var item FundingTracking
		var retryAt *time.Time
		if err := rows.Scan(&item.InstrumentID, &item.RouteProfileID, &item.ActiveReasons, &item.TrackingStartedAt,
			&item.TargetNextFundingTime, &item.State, &retryAt, &item.RetryCount, &item.ErrorClass, &item.UpdatedAt); err != nil {
			return nil, err
		}
		if retryAt != nil {
			item.RetryAt = retryAt.UTC()
		}
		result = append(result, item)
	}
	return result, rows.Err()
}

func DeleteFundingTracking(ctx context.Context, pool *pgxpool.Pool, instrumentID string) error {
	if pool == nil || ctx == nil || strings.TrimSpace(instrumentID) == "" {
		return fmt.Errorf("funding tracking identity is invalid")
	}
	_, err := pool.Exec(ctx, `DELETE FROM settled_funding_tracking WHERE instrument_id = $1`, instrumentID)
	return err
}

type FundingSettlementStore struct{ pool *pgxpool.Pool }

func NewFundingSettlementStore(pool *pgxpool.Pool) *FundingSettlementStore {
	return &FundingSettlementStore{pool: pool}
}

func (store *FundingSettlementStore) LoadTracking(ctx context.Context) ([]fundingsettlement.Record, error) {
	items, err := LoadFundingTracking(ctx, store.pool)
	if err != nil {
		return nil, err
	}
	result := make([]fundingsettlement.Record, 0, len(items))
	for _, item := range items {
		result = append(result, fundingsettlement.Record{
			InstrumentID: item.InstrumentID, RouteProfileID: item.RouteProfileID, ActiveReasons: item.ActiveReasons,
			TrackingStartedAt: item.TrackingStartedAt, TargetNextFundingTime: item.TargetNextFundingTime,
			State: item.State, RetryAt: item.RetryAt, RetryCount: item.RetryCount, ErrorClass: item.ErrorClass, UpdatedAt: item.UpdatedAt,
		})
	}
	return result, nil
}

func (store *FundingSettlementStore) SaveTracking(ctx context.Context, record fundingsettlement.Record) error {
	return SaveFundingTracking(ctx, store.pool, FundingTracking{
		InstrumentID: record.InstrumentID, RouteProfileID: record.RouteProfileID, ActiveReasons: record.ActiveReasons,
		TrackingStartedAt: record.TrackingStartedAt, TargetNextFundingTime: record.TargetNextFundingTime,
		State: record.State, RetryAt: record.RetryAt, RetryCount: record.RetryCount, ErrorClass: record.ErrorClass, UpdatedAt: record.UpdatedAt,
	})
}

func (store *FundingSettlementStore) DeleteTracking(ctx context.Context, instrumentID string) error {
	return DeleteFundingTracking(ctx, store.pool, instrumentID)
}

func (store *FundingSettlementStore) UpsertRate(ctx context.Context, rate fundingsettlement.SettledRate) error {
	return UpsertSettledFundingRate(ctx, store.pool, SettledFundingRate{
		RouteProfileID: rate.RouteProfileID, InstrumentID: rate.InstrumentID, FundingTime: rate.FundingTime,
		RealizedRate: rate.RealizedRate, ObservedAt: rate.ObservedAt, Source: rate.Source,
	})
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}
