package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

const (
	maxReadModelPageLimit = 500
	maxReplayLimit        = 1000
	eventRetentionRows    = 10000
)

type ReadModelStore struct {
	pool    *pgxpool.Pool
	cursors *readmodel.CursorCodec
}

func NewReadModelStore(pool *pgxpool.Pool, cursorKey []byte) (*ReadModelStore, error) {
	if pool == nil {
		return nil, fmt.Errorf("read-model pool is required")
	}
	codec, err := readmodel.NewCursorCodec(cursorKey)
	if err != nil {
		return nil, err
	}
	return &ReadModelStore{pool: pool, cursors: codec}, nil
}

func (store *ReadModelStore) ApplyMarketSnapshot(
	ctx context.Context,
	bootID string,
	snapshot readmodel.MarketSnapshot,
) ([]readmodel.Event, error) {
	if strings.TrimSpace(bootID) == "" ||
		!isProductMode(snapshot.Mode) ||
		snapshot.ObservedAt.IsZero() {
		return nil, fmt.Errorf("invalid market snapshot identity")
	}
	tx, err := store.pool.Begin(ctx)
	if err != nil {
		return nil, err
	}
	defer tx.Rollback(ctx)

	opportunityIDs := make([]string, 0, len(snapshot.Opportunities))
	alertIDs := make([]string, 0, len(snapshot.Alerts))
	events := make([]readmodel.Event, 0, len(snapshot.Opportunities)+len(snapshot.Alerts)+1)
	for _, projection := range snapshot.Opportunities {
		if err := validateProjection(projection); err != nil {
			return nil, err
		}
		opportunityIDs = append(opportunityIDs, projection.ID)
		tag, execErr := tx.Exec(ctx, `
			insert into opportunities(
				id, symbol, status, payload, mode, long_account_id,
				short_account_id, net_apr, formula_version, observed_at, created_at, updated_at
			) values($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$10,$10)
			on conflict(id) do update set
				symbol=excluded.symbol,
				status=excluded.status,
				payload=excluded.payload,
				long_account_id=excluded.long_account_id,
				short_account_id=excluded.short_account_id,
				net_apr=excluded.net_apr,
				formula_version=excluded.formula_version,
				observed_at=excluded.observed_at,
				updated_at=excluded.updated_at
			where opportunities.mode=excluded.mode
		`, projection.ID, projection.Symbol, projection.Status, projection.Payload,
			snapshot.Mode, projection.LongAccountID, projection.ShortAccountID,
			projection.NetAPR, projection.FormulaVersion, projection.ObservedAt.UTC())
		if execErr != nil {
			return nil, execErr
		}
		if tag.RowsAffected() != 1 {
			return nil, fmt.Errorf("opportunity identity conflicts across environments")
		}
		event, appendErr := appendEventTx(ctx, tx, readmodel.Event{
			BootID: bootID, Mode: snapshot.Mode,
			Kind:  readmodel.OpportunityUpsert,
			Scope: []string{"opportunities"}, EntityID: projection.ID,
			Payload: projection.Payload, OccurredAt: snapshot.ObservedAt.UTC(),
		})
		if appendErr != nil {
			return nil, appendErr
		}
		events = append(events, event)
	}
	for _, alert := range snapshot.Alerts {
		if err := validateProjection(alert.Projection); err != nil {
			return nil, err
		}
		if strings.TrimSpace(alert.AccountID) == "" || strings.TrimSpace(alert.InstrumentID) == "" {
			return nil, fmt.Errorf("invalid alert scope")
		}
		alertIDs = append(alertIDs, alert.ID)
		tag, execErr := tx.Exec(ctx, `
			insert into alerts(
				id, opportunity_id, status, payload, mode, account_id,
				instrument_id, observed_at, created_at, updated_at
			) values($1,$2,$3,$4,$5,$6,$7,$8,$8,$8)
			on conflict(id) do update set
				opportunity_id=excluded.opportunity_id,
				status=excluded.status,
				payload=excluded.payload,
				account_id=excluded.account_id,
				instrument_id=excluded.instrument_id,
				observed_at=excluded.observed_at,
				updated_at=excluded.updated_at
			where alerts.mode=excluded.mode
		`, alert.ID, nullableString(alert.OpportunityID), alert.Status, alert.Payload,
			snapshot.Mode, alert.AccountID, alert.InstrumentID, alert.ObservedAt.UTC())
		if execErr != nil {
			return nil, execErr
		}
		if tag.RowsAffected() != 1 {
			return nil, fmt.Errorf("alert identity conflicts across environments")
		}
		event, appendErr := appendEventTx(ctx, tx, readmodel.Event{
			BootID: bootID, Mode: snapshot.Mode,
			Kind:  readmodel.AlertUpsert,
			Scope: []string{"alerts"}, EntityID: alert.ID,
			Payload: alert.Payload, OccurredAt: snapshot.ObservedAt.UTC(),
		})
		if appendErr != nil {
			return nil, appendErr
		}
		events = append(events, event)
	}
	for _, observation := range snapshot.Funding {
		if strings.TrimSpace(observation.AccountID) == "" ||
			strings.TrimSpace(observation.InstrumentID) == "" ||
			!validVenueEnvironmentForMode(snapshot.Mode, observation.VenueEnvironment) ||
			observation.FundingTime.IsZero() ||
			observation.ObservedAt.IsZero() {
			return nil, fmt.Errorf("invalid funding observation")
		}
		var markPrice any
		if observation.HasMarkPrice {
			markPrice = observation.MarkPrice
		}
		var nextFunding any
		if !observation.NextFundingTime.IsZero() {
			nextFunding = observation.NextFundingTime.UTC()
		}
		if _, execErr := tx.Exec(ctx, `
			insert into funding_observations(
				mode, venue_environment, account_id, instrument_id, funding_time, observed_at,
				next_funding_time, rate, mark_price
			) values($1,$2,$3,$4,$5,$6,$7,$8,$9)
			on conflict(mode, venue_environment, account_id, instrument_id, funding_time) do update set
				observed_at=excluded.observed_at,
				next_funding_time=excluded.next_funding_time,
				rate=excluded.rate,
				mark_price=excluded.mark_price
		`, snapshot.Mode, observation.VenueEnvironment, observation.AccountID, observation.InstrumentID,
			observation.FundingTime.UTC(), observation.ObservedAt.UTC(), nextFunding,
			observation.Rate, markPrice); execErr != nil {
			return nil, execErr
		}
	}

	removedOpportunities, err := deleteMissing(ctx, tx, "opportunities", snapshot.Mode, opportunityIDs)
	if err != nil {
		return nil, err
	}
	removedAlerts, err := deleteMissing(ctx, tx, "alerts", snapshot.Mode, alertIDs)
	if err != nil {
		return nil, err
	}
	if removedOpportunities+removedAlerts > 0 {
		payload, marshalErr := json.Marshal(map[string]any{
			"reason": "projection-membership-changed",
			"scope":  []string{"opportunities", "alerts"},
		})
		if marshalErr != nil {
			return nil, marshalErr
		}
		event, appendErr := appendEventTx(ctx, tx, readmodel.Event{
			BootID: bootID, Mode: snapshot.Mode,
			Kind:    readmodel.ResnapshotRequired,
			Scope:   []string{"opportunities", "alerts"},
			Payload: payload, OccurredAt: snapshot.ObservedAt.UTC(),
		})
		if appendErr != nil {
			return nil, appendErr
		}
		events = append(events, event)
	}
	if err := pruneReadModelEventsTx(ctx, tx); err != nil {
		return nil, err
	}
	if err := tx.Commit(ctx); err != nil {
		return nil, err
	}
	return events, nil
}

func (store *ReadModelStore) AppendEvent(ctx context.Context, event readmodel.Event) (readmodel.Event, error) {
	if strings.TrimSpace(event.BootID) == "" ||
		!isProductMode(event.Mode) ||
		event.Kind == "" || len(event.Scope) == 0 ||
		!json.Valid(event.Payload) || event.OccurredAt.IsZero() {
		return readmodel.Event{}, fmt.Errorf("invalid read-model event")
	}
	if event.ContractVersion == "" {
		event.ContractVersion = readmodel.ContractVersion
	}
	if event.ContractVersion != readmodel.ContractVersion {
		return readmodel.Event{}, fmt.Errorf("unsupported read-model contract version")
	}
	return appendEventQuery(ctx, store.pool, event)
}

func (store *ReadModelStore) LatestEventID(ctx context.Context, bootID string) (int64, error) {
	var id int64
	if err := store.pool.QueryRow(ctx,
		`select coalesce(max(id), 0)
		 from read_model_events
		 where boot_id=$1 and contract_version=$2`,
		bootID, readmodel.ContractVersion,
	).Scan(&id); err != nil {
		return 0, err
	}
	return id, nil
}

func (store *ReadModelStore) ReplayEvents(
	ctx context.Context,
	bootID string,
	afterID int64,
	limit int,
) ([]readmodel.Event, error) {
	if strings.TrimSpace(bootID) == "" || afterID < 0 || limit < 1 || limit > maxReplayLimit {
		return nil, fmt.Errorf("invalid replay request")
	}
	var minimum, maximum int64
	if err := store.pool.QueryRow(ctx, `
		select coalesce(min(id), 0), coalesce(max(id), 0)
		from read_model_events
		where boot_id=$1 and contract_version=$2
	`, bootID, readmodel.ContractVersion).Scan(&minimum, &maximum); err != nil {
		return nil, err
	}
	if afterID > 0 && (maximum == 0 || afterID > maximum || afterID < minimum-1) {
		return nil, readmodel.ErrReplayExpired
	}
	rows, err := store.pool.Query(ctx, `
		select id, boot_id, coalesce(principal_id, ''), mode, contract_version,
			kind, scope, coalesce(entity_id, ''), payload, occurred_at
		from read_model_events
		where boot_id=$1 and contract_version=$2 and id>$3
		order by id
		limit $4
	`, bootID, readmodel.ContractVersion, afterID, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	events := make([]readmodel.Event, 0)
	for rows.Next() {
		var event readmodel.Event
		if err := rows.Scan(
			&event.ID, &event.BootID, &event.PrincipalID, &event.Mode,
			&event.ContractVersion,
			&event.Kind, &event.Scope,
			&event.EntityID, &event.Payload, &event.OccurredAt,
		); err != nil {
			return nil, err
		}
		event.OccurredAt = event.OccurredAt.UTC()
		events = append(events, event)
	}
	return events, rows.Err()
}

func appendEventTx(ctx context.Context, tx pgx.Tx, event readmodel.Event) (readmodel.Event, error) {
	return appendEventQuery(ctx, tx, event)
}

func pruneReadModelEventsTx(ctx context.Context, tx pgx.Tx) error {
	_, err := tx.Exec(ctx, `
		delete from read_model_events
		where id in (
			select id
			from read_model_events
			order by id desc
			offset $1
		)
	`, eventRetentionRows)
	return err
}

type rowQuerier interface {
	QueryRow(context.Context, string, ...any) pgx.Row
}

func appendEventQuery(ctx context.Context, query rowQuerier, event readmodel.Event) (readmodel.Event, error) {
	if event.ContractVersion == "" {
		event.ContractVersion = readmodel.ContractVersion
	}
	if !isProductMode(event.Mode) || event.ContractVersion != readmodel.ContractVersion {
		return readmodel.Event{}, fmt.Errorf("invalid read-model event scope")
	}
	event.OccurredAt = event.OccurredAt.UTC()
	err := query.QueryRow(ctx, `
		insert into read_model_events(
			boot_id, principal_id, mode, contract_version, kind, scope, entity_id, payload, occurred_at
		)
		values($1,$2,$3,$4,$5,$6,$7,$8,$9)
		returning id
	`, event.BootID, nullableString(event.PrincipalID), event.Mode,
		event.ContractVersion, event.Kind, event.Scope, nullableString(event.EntityID),
		event.Payload, event.OccurredAt).Scan(&event.ID)
	return event, err
}

func deleteMissing(ctx context.Context, tx pgx.Tx, table, mode string, ids []string) (int64, error) {
	var query string
	var args []any
	if len(ids) == 0 {
		query = "delete from " + table + " where mode=$1"
		args = []any{mode}
	} else {
		query = "delete from " + table + " where mode=$1 and not (id=any($2))"
		args = []any{mode, ids}
	}
	tag, err := tx.Exec(ctx, query, args...)
	if err != nil {
		return 0, err
	}
	return tag.RowsAffected(), nil
}

func validateProjection(projection readmodel.Projection) error {
	if strings.TrimSpace(projection.ID) == "" ||
		strings.TrimSpace(projection.Status) == "" ||
		projection.ObservedAt.IsZero() ||
		!json.Valid(projection.Payload) {
		return fmt.Errorf("invalid read-model projection")
	}
	return nil
}

func nullableString(value string) any {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	return value
}

func normalizeLimit(limit int) (int, error) {
	if limit < 1 || limit > maxReadModelPageLimit {
		return 0, fmt.Errorf("invalid page limit")
	}
	return limit, nil
}

func stringSet(values []string) []string {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		value = strings.TrimSpace(value)
		if value == "" {
			continue
		}
		if _, ok := seen[value]; ok {
			continue
		}
		seen[value] = struct{}{}
		result = append(result, value)
	}
	sort.Strings(result)
	return result
}

func timeCursorValue(value time.Time) string {
	return strconv.FormatInt(value.UTC().UnixNano(), 10)
}

func parseTimeCursor(value string) (time.Time, error) {
	nanos, err := strconv.ParseInt(value, 10, 64)
	if err != nil {
		return time.Time{}, readmodel.ErrInvalidCursor
	}
	return time.Unix(0, nanos).UTC(), nil
}

func decimalCursorValue(value decimal.Decimal) string {
	return value.String()
}

func mapCursorError(err error) error {
	if errors.Is(err, readmodel.ErrInvalidCursor) {
		return readmodel.ErrInvalidCursor
	}
	return err
}

func validateReadModelScope(scope readmodel.Scope) error {
	if strings.TrimSpace(scope.PrincipalID) == "" || !isProductMode(scope.Mode) {
		return fmt.Errorf("invalid scope")
	}
	return nil
}

func isProductMode(mode string) bool {
	return mode == "test" || mode == "live"
}

func validVenueEnvironmentForMode(mode, venueEnvironment string) bool {
	switch mode {
	case "test":
		return venueEnvironment == "demo" || venueEnvironment == "testnet"
	case "live":
		return venueEnvironment == "live"
	default:
		return false
	}
}
