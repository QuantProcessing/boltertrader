package ledgerprojection

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/ledgerfacts"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

type PostgresStore struct {
	pool *pgxpool.Pool
}

func NewPostgresStore(pool *pgxpool.Pool) *PostgresStore {
	return &PostgresStore{pool: pool}
}

func (s *PostgresStore) RebuildPortfolioFromReplay(ctx context.Context, bundle AuthoritativePortfolioReplay) ([]ClosedPositionProjection, PortfolioProjection, error) {
	if s == nil || s.pool == nil {
		return nil, PortfolioProjection{}, errors.New("ledger projection postgres pool is required")
	}
	if err := validateAuthoritativeBundle(bundle); err != nil {
		return nil, PortfolioProjection{}, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return nil, PortfolioProjection{}, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		lock table ledger_funding_payment_facts,
		  ledger_fill_cashflow_facts,
		  ledger_position_snapshot_facts,
		  ledger_lineage_facts,
		  ledger_lineage_revision_facts,
		  ledger_sync_watermarks
		in share mode
	`); err != nil {
		return nil, PortfolioProjection{}, err
	}
	if err := validateAuthoritativeScopeManifest(ctx, tx, bundle); err != nil {
		return nil, PortfolioProjection{}, err
	}
	authoritativeEntries, err := loadAuthoritativePersistedReplay(ctx, tx, bundle)
	if err != nil {
		return nil, PortfolioProjection{}, err
	}
	authoritative := bundle
	authoritative.Entries = authoritativeEntries
	mem := NewMemoryStore()
	closed, portfolio, err := NewProjector(mem).RebuildPortfolioFromReplay(ctx, authoritative)
	if err != nil {
		return nil, PortfolioProjection{}, err
	}
	if _, err := tx.Exec(ctx, `
		delete from ledger_closed_position_projections
		where principal_id=$1 and mode=$2 and reporting_asset=$3
	`, portfolio.PrincipalID, portfolio.Mode, portfolio.ReportingAsset); err != nil {
		return nil, PortfolioProjection{}, err
	}
	if _, err := tx.Exec(ctx, `
		delete from ledger_portfolio_projections
		where principal_id=$1 and mode=$2 and reporting_asset=$3
	`, portfolio.PrincipalID, portfolio.Mode, portfolio.ReportingAsset); err != nil {
		return nil, PortfolioProjection{}, err
	}
	for _, item := range closed {
		if err := replaceClosedPosition(ctx, tx, item); err != nil {
			return nil, PortfolioProjection{}, err
		}
	}
	if err := replacePortfolio(ctx, tx, portfolio); err != nil {
		return nil, PortfolioProjection{}, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, PortfolioProjection{}, err
	}
	return closed, portfolio, nil
}

type projectionQueryer interface {
	Query(context.Context, string, ...any) (pgx.Rows, error)
}

func validateAuthoritativeScopeManifest(ctx context.Context, q projectionQueryer, bundle AuthoritativePortfolioReplay) error {
	rows, err := q.Query(ctx, `
		select distinct principal_id, mode, account_id, instrument_id,
		  window_start, window_end, pagination_traversal
		from ledger_position_snapshot_facts
		where principal_id=$1 and mode=$2 and reporting_asset=$3
	`, bundle.PrincipalID, bundle.Mode, bundle.ReportingAsset)
	if err != nil {
		return err
	}
	defer rows.Close()
	persisted := map[string]struct{}{}
	for rows.Next() {
		var scope ledgerfacts.Scope
		if err := rows.Scan(
			&scope.PrincipalID,
			&scope.Mode,
			&scope.AccountID,
			&scope.InstrumentID,
			&scope.WindowStart,
			&scope.WindowEnd,
			&scope.PaginationTraversal,
		); err != nil {
			return err
		}
		persisted[exactScopeKey(scope)] = struct{}{}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	declared := make(map[string]struct{}, len(bundle.Scopes))
	for _, scope := range bundle.Scopes {
		declared[exactScopeKey(scope)] = struct{}{}
	}
	if len(persisted) != len(declared) {
		return errors.New("authoritative replay scope manifest does not cover persisted portfolio facts")
	}
	for key := range persisted {
		if _, ok := declared[key]; !ok {
			return errors.New("authoritative replay scope manifest omits persisted portfolio scope")
		}
	}
	return nil
}

func loadAuthoritativePersistedReplay(ctx context.Context, tx pgx.Tx, bundle AuthoritativePortfolioReplay) ([]ledgerfacts.ReplayEntry, error) {
	persisted, err := loadPersistedReplayBoundary(ctx, tx, bundle)
	if err != nil {
		return nil, err
	}
	supplied := make(map[string]ledgerfacts.ReplayEntry, len(bundle.Entries))
	for _, entry := range bundle.Entries {
		kind := replayEntryKind(entry)
		factID := entryFactID(entry)
		if kind == "" || factID == "" {
			return nil, errors.New("authoritative replay contains fact without stable identity")
		}
		supplied[kind+"\x00"+factID] = entry
	}
	if len(persisted) != len(supplied) {
		return nil, errors.New("authoritative replay fact set does not match persisted ledger")
	}
	for key, sequenceID := range persisted {
		entry, ok := supplied[key]
		if !ok || entry.SequenceID != sequenceID {
			return nil, errors.New("authoritative replay fact boundary does not match persisted ledger")
		}
	}

	databaseReplay, err := ledgerfacts.ReplayPostgresTransaction(ctx, tx, bundle.Scopes)
	if err != nil {
		return nil, err
	}
	authoritativeByKey := make(map[string]ledgerfacts.ReplayEntry, len(persisted))
	for _, entry := range databaseReplay {
		key := replayEntryKind(entry) + "\x00" + entryFactID(entry)
		if _, required := persisted[key]; required {
			authoritativeByKey[key] = entry
		}
	}
	if len(authoritativeByKey) != len(persisted) {
		return nil, errors.New("persisted ledger could not be reconstructed from declared scopes")
	}
	authoritative := make([]ledgerfacts.ReplayEntry, 0, len(persisted))
	for key, sequenceID := range persisted {
		stored := authoritativeByKey[key]
		if stored.SequenceID != sequenceID {
			return nil, errors.New("persisted replay sequence changed during locked rebuild")
		}
		if !sameReplayFactPayload(supplied[key], stored) {
			return nil, errors.New("authoritative replay payload does not match persisted ledger")
		}
		authoritative = append(authoritative, stored)
	}
	return authoritative, nil
}

func loadPersistedReplayBoundary(ctx context.Context, q projectionQueryer, bundle AuthoritativePortfolioReplay) (map[string]int64, error) {
	rows, err := q.Query(ctx, `
		select 'funding', fact_id, sequence_id, account_id, instrument_id,
		  window_start, window_end, pagination_traversal
		from ledger_funding_payment_facts
		where principal_id=$1 and mode=$2 and asset=$3
		union all
		select 'fill', fact_id, sequence_id, account_id, instrument_id,
		  window_start, window_end, pagination_traversal
		from ledger_fill_cashflow_facts
		where principal_id=$1 and mode=$2 and reporting_asset=$3
		union all
		select 'position', fact_id, sequence_id, account_id, instrument_id,
		  window_start, window_end, pagination_traversal
		from ledger_position_snapshot_facts
		where principal_id=$1 and mode=$2 and reporting_asset=$3
		union all
		select 'watermark', fact_id, sequence_id, account_id, instrument_id,
		  window_start, window_end, pagination_traversal
		from ledger_sync_watermarks
		where principal_id=$1 and mode=$2
		union all
		select 'lineage', l.fact_id, l.sequence_id, l.account_id, l.instrument_id,
		  null::timestamptz, null::timestamptz, null::text
		from ledger_lineage_facts l
		where l.principal_id=$1 and l.mode=$2 and exists (
		  select 1 from ledger_position_snapshot_facts p
		  where p.principal_id=l.principal_id and p.mode=l.mode
		    and p.position_id=l.position_id and p.reporting_asset=$3
		)
		union all
		select 'lineage_revision', r.fact_id, r.sequence_id, r.account_id, r.instrument_id,
		  null::timestamptz, null::timestamptz, null::text
		from ledger_lineage_revision_facts r
		join ledger_lineage_facts l on l.fact_id=r.lineage_fact_id
		where r.principal_id=$1 and r.mode=$2 and exists (
		  select 1 from ledger_position_snapshot_facts p
		  where p.principal_id=l.principal_id and p.mode=l.mode
		    and p.position_id=l.position_id and p.reporting_asset=$3
		)
	`, bundle.PrincipalID, bundle.Mode, bundle.ReportingAsset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	declaredExact := make(map[string]struct{}, len(bundle.Scopes))
	declaredIdentity := make(map[string]struct{}, len(bundle.Scopes))
	for _, scope := range bundle.Scopes {
		declaredExact[exactScopeKey(scope)] = struct{}{}
		declaredIdentity[identityScopeKey(scope)] = struct{}{}
	}
	persisted := map[string]int64{}
	for rows.Next() {
		var kind, factID, accountID, instrumentID string
		var sequenceID int64
		var windowStart, windowEnd *time.Time
		var traversal *string
		if err := rows.Scan(&kind, &factID, &sequenceID, &accountID, &instrumentID, &windowStart, &windowEnd, &traversal); err != nil {
			return nil, err
		}
		scope := ledgerfacts.Scope{
			PrincipalID:  bundle.PrincipalID,
			Mode:         bundle.Mode,
			AccountID:    accountID,
			InstrumentID: instrumentID,
		}
		if kind == "lineage" || kind == "lineage_revision" {
			if _, ok := declaredIdentity[identityScopeKey(scope)]; !ok {
				continue
			}
		} else {
			if windowStart == nil || windowEnd == nil || traversal == nil {
				return nil, errors.New("persisted replay fact lost exact scope")
			}
			scope.WindowStart = *windowStart
			scope.WindowEnd = *windowEnd
			scope.PaginationTraversal = *traversal
			if _, ok := declaredExact[exactScopeKey(scope)]; !ok {
				continue
			}
		}
		persisted[kind+"\x00"+factID] = sequenceID
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	return persisted, nil
}

func sameReplayFactPayload(left, right ledgerfacts.ReplayEntry) bool {
	if left.Kind != right.Kind || left.Revision != right.Revision || left.Marker != right.Marker || left.SequenceID != right.SequenceID {
		return false
	}
	switch {
	case left.Funding != nil && right.Funding != nil:
		return reflect.DeepEqual(*left.Funding, *right.Funding)
	case left.Fill != nil && right.Fill != nil:
		return reflect.DeepEqual(*left.Fill, *right.Fill)
	case left.Position != nil && right.Position != nil:
		return reflect.DeepEqual(*left.Position, *right.Position)
	case left.Lineage != nil && right.Lineage != nil:
		return reflect.DeepEqual(*left.Lineage, *right.Lineage)
	case left.RevisionFact != nil && right.RevisionFact != nil:
		return reflect.DeepEqual(*left.RevisionFact, *right.RevisionFact)
	case left.Watermark != nil && right.Watermark != nil:
		return reflect.DeepEqual(*left.Watermark, *right.Watermark)
	default:
		return false
	}
}

func replayEntryKind(entry ledgerfacts.ReplayEntry) string {
	kind, _, count := replayEntryIdentity(entry)
	if count != 1 {
		return ""
	}
	return kind
}

func (s *PostgresStore) ClosedPositionForScope(ctx context.Context, principalID, mode, asset, positionID string) (ClosedPositionProjection, bool, error) {
	if s == nil || s.pool == nil {
		return ClosedPositionProjection{}, false, errors.New("ledger projection postgres pool is required")
	}
	if principalID == "" || (mode != "test" && mode != "live") || asset == "" || positionID == "" {
		return ClosedPositionProjection{}, false, errors.New("ledger projection scope is required")
	}
	var p ClosedPositionProjection
	var realized, unrealized, funding, fees, net, upnl string
	var residual, watermark []byte
	var asOf, conversionAsOf *time.Time
	err := s.pool.QueryRow(ctx, `
		select position_id, principal_id, mode, account_id, instrument_id, reporting_asset,
		  realized_trading_pnl::text, unrealized_trading_pnl::text, actual_funding_cash_flow::text,
		  fees::text, net_pnl::text, closed_trading_upnl::text, verified, verification_status,
		  residual, watermark, as_of, conversion_source, conversion_as_of, rounding_mode, scale, formula_version
		from ledger_closed_position_projections
		where principal_id=$1 and mode=$2 and reporting_asset=$3 and position_id=$4
	`, principalID, mode, asset, positionID).Scan(&p.PositionID, &p.Scope.PrincipalID, &p.Scope.Mode, &p.Scope.AccountID,
		&p.Scope.InstrumentID, &p.ReportingAsset, &realized, &unrealized, &funding, &fees, &net,
		&upnl, &p.Verified, &p.VerificationStatus, &residual, &watermark, &asOf, &p.Conversion.ConversionSource,
		&conversionAsOf, &p.Conversion.RoundingMode, &p.Conversion.Scale, &p.Conversion.FormulaVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return ClosedPositionProjection{}, false, nil
	}
	if err != nil {
		return ClosedPositionProjection{}, false, err
	}
	if asOf != nil {
		p.AsOf = *asOf
	}
	if conversionAsOf != nil {
		p.Conversion.ConversionAsOf = *conversionAsOf
	}
	if err := decodeClosedAmounts(&p, realized, unrealized, funding, fees, net, upnl); err != nil {
		return ClosedPositionProjection{}, false, err
	}
	if err := json.Unmarshal(residual, &p.Residual); err != nil {
		return ClosedPositionProjection{}, false, err
	}
	if err := json.Unmarshal(watermark, &p.Watermark); err != nil {
		return ClosedPositionProjection{}, false, err
	}
	return p, true, nil
}

func (s *PostgresStore) ClosedPositions(ctx context.Context, principalID, mode, asset string) ([]ClosedPositionProjection, error) {
	if s == nil || s.pool == nil {
		return nil, errors.New("ledger projection postgres pool is required")
	}
	if principalID == "" || (mode != "test" && mode != "live") || asset == "" {
		return nil, errors.New("ledger projection scope is required")
	}
	return s.closedPositions(ctx, principalID, mode, asset)
}

func (s *PostgresStore) Portfolio(ctx context.Context, principalID, mode, asset string) (PortfolioProjection, bool, error) {
	var p PortfolioProjection
	var cumulative, open, total, funding, fees string
	var residual, watermark []byte
	var asOf, conversionAsOf *time.Time
	err := s.pool.QueryRow(ctx, `
		select principal_id, mode, reporting_asset, cumulative_realized_net_pnl::text,
		  current_open_unrealized_pnl::text, total_net_pnl::text, funding_total::text,
		  fee_total::text, verification_status, open_position_count, closed_position_count, residual, watermark,
		  as_of, conversion_source, conversion_as_of, rounding_mode, scale, formula_version
		from ledger_portfolio_projections
		where principal_id=$1 and mode=$2 and reporting_asset=$3
	`, principalID, mode, asset).Scan(&p.PrincipalID, &p.Mode, &p.ReportingAsset, &cumulative,
		&open, &total, &funding, &fees, &p.VerificationStatus, &p.OpenPositionCount, &p.ClosedPositionCount, &residual,
		&watermark, &asOf, &p.Conversion.ConversionSource, &conversionAsOf, &p.Conversion.RoundingMode,
		&p.Conversion.Scale, &p.Conversion.FormulaVersion)
	if errors.Is(err, pgx.ErrNoRows) {
		return PortfolioProjection{}, false, nil
	}
	if err != nil {
		return PortfolioProjection{}, false, err
	}
	if asOf != nil {
		p.AsOf = *asOf
	}
	if conversionAsOf != nil {
		p.Conversion.ConversionAsOf = *conversionAsOf
	}
	if p.CumulativeRealizedNetPnl, err = parseMoney(asset, cumulative); err != nil {
		return PortfolioProjection{}, false, err
	}
	if p.CurrentOpenUnrealizedPnl, err = parseMoney(asset, open); err != nil {
		return PortfolioProjection{}, false, err
	}
	if p.TotalNetPnl, err = parseMoney(asset, total); err != nil {
		return PortfolioProjection{}, false, err
	}
	if p.FundingTotal, err = parseMoney(asset, funding); err != nil {
		return PortfolioProjection{}, false, err
	}
	if p.FeeTotal, err = parseMoney(asset, fees); err != nil {
		return PortfolioProjection{}, false, err
	}
	if err := json.Unmarshal(residual, &p.Residual); err != nil {
		return PortfolioProjection{}, false, err
	}
	if err := json.Unmarshal(watermark, &p.Watermark); err != nil {
		return PortfolioProjection{}, false, err
	}
	return p, true, nil
}

func (s *PostgresStore) Watermark(ctx context.Context, principalID, mode, asset string) (Watermark, bool, error) {
	portfolio, ok, err := s.Portfolio(ctx, principalID, mode, asset)
	if err != nil || !ok {
		return Watermark{}, ok, err
	}
	return portfolio.Watermark, true, nil
}

func (s *PostgresStore) closedPositions(ctx context.Context, principalID, mode, asset string) ([]ClosedPositionProjection, error) {
	rows, err := s.pool.Query(ctx, `select position_id from ledger_closed_position_projections where principal_id=$1 and mode=$2 and reporting_asset=$3`, principalID, mode, asset)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var ids []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	rows.Close()
	var out []ClosedPositionProjection
	for _, id := range ids {
		item, ok, err := s.ClosedPositionForScope(ctx, principalID, mode, asset, id)
		if err != nil {
			return nil, err
		}
		if ok {
			out = append(out, item)
		}
	}
	return out, nil
}

func replaceClosedPosition(ctx context.Context, tx pgx.Tx, p ClosedPositionProjection) error {
	residual, _ := json.Marshal(p.Residual)
	watermark, _ := json.Marshal(p.Watermark)
	_, err := tx.Exec(ctx, `
		insert into ledger_closed_position_projections(
		  position_id, principal_id, mode, account_id, instrument_id, reporting_asset,
		  realized_trading_pnl, unrealized_trading_pnl, actual_funding_cash_flow, fees,
		  net_pnl, closed_trading_upnl, verified, verification_status, residual, watermark, as_of,
		  conversion_source, conversion_as_of, rounding_mode, scale, formula_version
		) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21,$22)
		on conflict(principal_id, mode, reporting_asset, position_id) do update set
		  account_id=excluded.account_id,
		  instrument_id=excluded.instrument_id,
		  realized_trading_pnl=excluded.realized_trading_pnl,
		  unrealized_trading_pnl=excluded.unrealized_trading_pnl,
		  actual_funding_cash_flow=excluded.actual_funding_cash_flow,
		  fees=excluded.fees,
		  net_pnl=excluded.net_pnl,
		  closed_trading_upnl=excluded.closed_trading_upnl,
		  verified=excluded.verified,
		  verification_status=excluded.verification_status,
		  residual=excluded.residual,
		  watermark=excluded.watermark,
		  as_of=excluded.as_of,
		  conversion_source=excluded.conversion_source,
		  conversion_as_of=excluded.conversion_as_of,
		  rounding_mode=excluded.rounding_mode,
		  scale=excluded.scale,
		  formula_version=excluded.formula_version,
		  updated_at=now()
	`, p.PositionID, p.Scope.PrincipalID, p.Scope.Mode, p.Scope.AccountID, p.Scope.InstrumentID,
		p.ReportingAsset, p.RealizedTradingPnl.Amount.String(), p.UnrealizedTradingPnl.Amount.String(),
		p.ActualFundingCashFlow.Amount.String(), p.Fees.Amount.String(), p.NetPnl.Amount.String(),
		p.ClosedTradingUPnL.Amount.String(), p.Verified, p.VerificationStatus, residual, watermark,
		nullableTime(p.AsOf), p.Conversion.ConversionSource, nullableTime(p.Conversion.ConversionAsOf), p.Conversion.RoundingMode,
		p.Conversion.Scale, p.Conversion.FormulaVersion)
	return err
}

func replacePortfolio(ctx context.Context, tx pgx.Tx, p PortfolioProjection) error {
	residual, _ := json.Marshal(p.Residual)
	watermark, _ := json.Marshal(p.Watermark)
	_, err := tx.Exec(ctx, `
		insert into ledger_portfolio_projections(
		  principal_id, mode, reporting_asset, cumulative_realized_net_pnl,
		  current_open_unrealized_pnl, total_net_pnl, funding_total, fee_total,
		  verification_status, open_position_count, closed_position_count, residual, watermark, as_of,
		  conversion_source, conversion_as_of, rounding_mode, scale, formula_version
		) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19)
		on conflict(principal_id, mode, reporting_asset) do update set
		  cumulative_realized_net_pnl=excluded.cumulative_realized_net_pnl,
		  current_open_unrealized_pnl=excluded.current_open_unrealized_pnl,
		  total_net_pnl=excluded.total_net_pnl,
		  funding_total=excluded.funding_total,
		  fee_total=excluded.fee_total,
		  verification_status=excluded.verification_status,
		  open_position_count=excluded.open_position_count,
		  closed_position_count=excluded.closed_position_count,
		  residual=excluded.residual,
		  watermark=excluded.watermark,
		  as_of=excluded.as_of,
		  conversion_source=excluded.conversion_source,
		  conversion_as_of=excluded.conversion_as_of,
		  rounding_mode=excluded.rounding_mode,
		  scale=excluded.scale,
		  formula_version=excluded.formula_version,
		  updated_at=now()
	`, p.PrincipalID, p.Mode, p.ReportingAsset, p.CumulativeRealizedNetPnl.Amount.String(),
		p.CurrentOpenUnrealizedPnl.Amount.String(), p.TotalNetPnl.Amount.String(), p.FundingTotal.Amount.String(),
		p.FeeTotal.Amount.String(), p.VerificationStatus, p.OpenPositionCount, p.ClosedPositionCount, residual, watermark,
		nullableTime(p.AsOf), p.Conversion.ConversionSource, nullableTime(p.Conversion.ConversionAsOf), p.Conversion.RoundingMode,
		p.Conversion.Scale, p.Conversion.FormulaVersion)
	return err
}

func decodeClosedAmounts(p *ClosedPositionProjection, realized, unrealized, funding, fees, net, upnl string) error {
	asset := p.ReportingAsset
	var err error
	if p.RealizedTradingPnl, err = parseMoney(asset, realized); err != nil {
		return err
	}
	if p.UnrealizedTradingPnl, err = parseMoney(asset, unrealized); err != nil {
		return err
	}
	if p.ActualFundingCashFlow, err = parseMoney(asset, funding); err != nil {
		return err
	}
	if p.Fees, err = parseMoney(asset, fees); err != nil {
		return err
	}
	if p.NetPnl, err = parseMoney(asset, net); err != nil {
		return err
	}
	if p.ClosedTradingUPnL, err = parseMoney(asset, upnl); err != nil {
		return err
	}
	return nil
}

func parseMoney(asset, raw string) (ledgerfacts.Money, error) {
	value, err := decimal.NewFromString(raw)
	if err != nil {
		return ledgerfacts.Money{}, err
	}
	return ledgerfacts.Money{Asset: asset, Amount: value}, nil
}

func nullableTime(value time.Time) any {
	if value.IsZero() {
		return nil
	}
	return value.UTC()
}
