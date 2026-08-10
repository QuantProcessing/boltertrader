package postgres

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

type opportunityRow struct {
	ID         string
	NetAPR     decimal.Decimal
	ObservedAt time.Time
	Payload    json.RawMessage
}

func (store *ReadModelStore) ListOpportunities(
	ctx context.Context,
	scope readmodel.Scope,
	query readmodel.OpportunityQuery,
) (readmodel.Page, error) {
	if err := validateReadModelScope(scope); err != nil {
		return readmodel.Page{}, err
	}
	limit, err := normalizeLimit(query.Limit)
	if err != nil {
		return readmodel.Page{}, err
	}
	if query.Sort == "" {
		query.Sort = readmodel.NetAPRDescending
	}
	if query.Sort != readmodel.NetAPRDescending && query.Sort != readmodel.NetAPRAscending {
		return readmodel.Page{}, fmt.Errorf("invalid opportunity sort")
	}
	fingerprint := strings.Join([]string{
		string(query.Sort), strings.TrimSpace(query.ShortVenueID), strings.TrimSpace(query.Status),
	}, "|")
	expected := readmodel.CursorExpectation{
		Resource: readmodel.Opportunities, PrincipalID: scope.PrincipalID,
		Mode: scope.Mode, ContractVersion: readmodel.ContractVersion, Fingerprint: fingerprint,
	}
	var position readmodel.CursorPosition
	if query.Cursor != "" {
		position, err = store.cursors.Decode(query.Cursor, expected)
		if err != nil {
			return readmodel.Page{}, readmodel.ErrInvalidCursor
		}
	}
	args := []any{scope.Mode}
	where := []string{"mode=$1", "net_apr is not null"}
	if query.ShortVenueID != "" {
		args = append(args, query.ShortVenueID)
		where = append(where, fmt.Sprintf("payload->>'shortVenueId'=$%d", len(args)))
	}
	if query.Status != "" {
		args = append(args, query.Status)
		where = append(where, fmt.Sprintf("status=$%d", len(args)))
	}
	direction := "desc"
	comparison := "<"
	if query.Sort == readmodel.NetAPRAscending {
		direction = "asc"
		comparison = ">"
	}
	if query.Cursor != "" {
		sortValue, parseErr := decimal.NewFromString(position.SortValue)
		if parseErr != nil {
			return readmodel.Page{}, readmodel.ErrInvalidCursor
		}
		args = append(args, sortValue, position.ID)
		where = append(where, fmt.Sprintf(
			"(net_apr %s $%d or (net_apr=$%d and id>$%d))",
			comparison, len(args)-1, len(args)-1, len(args),
		))
	}
	args = append(args, limit+1)
	sql := fmt.Sprintf(`
		select id, net_apr, observed_at, payload
		from opportunities
		where %s
		order by net_apr %s, id asc
		limit $%d
	`, strings.Join(where, " and "), direction, len(args))
	rows, err := store.pool.Query(ctx, sql, args...)
	if err != nil {
		return readmodel.Page{}, err
	}
	defer rows.Close()
	result := make([]opportunityRow, 0, limit+1)
	for rows.Next() {
		var row opportunityRow
		if err := rows.Scan(&row.ID, &row.NetAPR, &row.ObservedAt, &row.Payload); err != nil {
			return readmodel.Page{}, err
		}
		row.ObservedAt = row.ObservedAt.UTC()
		result = append(result, row)
	}
	if err := rows.Err(); err != nil {
		return readmodel.Page{}, err
	}
	page := readmodel.Page{Items: make([]json.RawMessage, 0, min(limit, len(result)))}
	for index, row := range result {
		if index == limit {
			break
		}
		page.Items = append(page.Items, row.Payload)
		if row.ObservedAt.After(page.AsOf) {
			page.AsOf = row.ObservedAt
		}
	}
	if len(result) > limit {
		last := result[limit-1]
		page.NextCursor, err = store.cursors.Encode(readmodel.CursorPosition{
			Resource: readmodel.Opportunities, PrincipalID: scope.PrincipalID,
			Mode: scope.Mode, ContractVersion: readmodel.ContractVersion, Fingerprint: fingerprint,
			SortValue: decimalCursorValue(last.NetAPR), ID: last.ID,
			OccurredAt: last.ObservedAt,
		})
		if err != nil {
			return readmodel.Page{}, err
		}
	}
	return page, nil
}

func (store *ReadModelStore) Opportunity(
	ctx context.Context,
	scope readmodel.Scope,
	id string,
) (json.RawMessage, error) {
	if err := validateReadModelScope(scope); err != nil {
		return nil, err
	}
	var payload json.RawMessage
	err := store.pool.QueryRow(ctx,
		`select payload from opportunities where mode=$1 and id=$2`,
		scope.Mode, id,
	).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, readmodel.ErrNotFound
	}
	return payload, err
}

func (store *ReadModelStore) Position(
	ctx context.Context,
	scope readmodel.Scope,
	id string,
) (json.RawMessage, error) {
	if err := validateReadModelScope(scope); err != nil {
		return nil, err
	}
	var payload json.RawMessage
	err := store.pool.QueryRow(ctx,
		`select payload from positions where principal_id=$1 and mode=$2 and id=$3`,
		scope.PrincipalID, scope.Mode, id,
	).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, readmodel.ErrNotFound
	}
	return payload, err
}

type timeRow struct {
	ID         string
	OccurredAt time.Time
	Payload    json.RawMessage
}

func (store *ReadModelStore) ListAlerts(
	ctx context.Context,
	scope readmodel.Scope,
	query readmodel.AlertQuery,
) (readmodel.Page, error) {
	if err := validateReadModelScope(scope); err != nil {
		return readmodel.Page{}, err
	}
	limit, err := normalizeLimit(query.Limit)
	if err != nil {
		return readmodel.Page{}, err
	}
	severity := strings.TrimSpace(query.Severity)
	if severity == "all" {
		severity = ""
	}
	fingerprint := severity
	if fingerprint == "" {
		fingerprint = "all"
	}
	var position readmodel.CursorPosition
	if query.Cursor != "" {
		position, err = store.cursors.Decode(query.Cursor, readmodel.CursorExpectation{
			Resource: readmodel.Alerts, PrincipalID: scope.PrincipalID,
			Mode: scope.Mode, ContractVersion: readmodel.ContractVersion, Fingerprint: fingerprint,
		})
		if err != nil {
			return readmodel.Page{}, readmodel.ErrInvalidCursor
		}
	}
	args := []any{scope.Mode}
	where := []string{"mode=$1", "observed_at is not null"}
	if severity != "" {
		args = append(args, severity)
		where = append(where, fmt.Sprintf("status=$%d", len(args)))
	}
	if query.Cursor != "" {
		at, parseErr := parseTimeCursor(position.SortValue)
		if parseErr != nil {
			return readmodel.Page{}, parseErr
		}
		args = append(args, at, position.ID)
		where = append(where, fmt.Sprintf(
			"(observed_at<$%d or (observed_at=$%d and id>$%d))",
			len(args)-1, len(args)-1, len(args),
		))
	}
	args = append(args, limit+1)
	sql := fmt.Sprintf(`
		select id, observed_at, payload
		from alerts where %s
		order by observed_at desc, id asc
		limit $%d
	`, strings.Join(where, " and "), len(args))
	rows, err := store.pool.Query(ctx, sql, args...)
	if err != nil {
		return readmodel.Page{}, err
	}
	result, err := scanTimeRows(rows)
	if err != nil {
		return readmodel.Page{}, err
	}
	page, err := store.timePage(scope, readmodel.Alerts, fingerprint, limit, result)
	if err != nil {
		return readmodel.Page{}, err
	}
	countRows, err := store.pool.Query(ctx, `
		select status, count(*)
		from alerts where mode=$1
		group by status
	`, scope.Mode)
	if err != nil {
		return readmodel.Page{}, err
	}
	defer countRows.Close()
	page.Counts = map[string]int{"all": 0, "critical": 0, "warning": 0, "watch": 0}
	for countRows.Next() {
		var status string
		var count int
		if err := countRows.Scan(&status, &count); err != nil {
			return readmodel.Page{}, err
		}
		page.Counts[status] = count
		page.Counts["all"] += count
	}
	return page, countRows.Err()
}

func (store *ReadModelStore) ListPositions(
	ctx context.Context,
	scope readmodel.Scope,
	query readmodel.PositionQuery,
) (readmodel.Page, error) {
	states := stringSet(query.States)
	fingerprint := strings.Join(states, ",")
	if fingerprint == "" {
		fingerprint = "all"
	}
	return store.listScopedTimePayloads(ctx, scope, readmodel.Positions, query.PageQuery,
		fingerprint, "positions", "updated_at", "status", states, time.Time{}, time.Time{})
}

func (store *ReadModelStore) ListActivities(
	ctx context.Context,
	scope readmodel.Scope,
	query readmodel.ActivityQuery,
) (readmodel.Page, error) {
	kinds := stringSet(query.Kinds)
	fingerprint := strings.Join(kinds, ",") + "|" + query.From.UTC().Format(time.RFC3339Nano) +
		"|" + query.To.UTC().Format(time.RFC3339Nano)
	return store.listScopedTimePayloads(ctx, scope, readmodel.Activities, query.PageQuery,
		fingerprint, "activities", "occurred_at", "type", kinds, query.From, query.To)
}

func (store *ReadModelStore) ListExecutions(
	ctx context.Context,
	scope readmodel.Scope,
	query readmodel.ExecutionQuery,
) (readmodel.Page, error) {
	states := stringSet(query.States)
	fingerprint := strings.Join(states, ",")
	if fingerprint == "" {
		fingerprint = "all"
	}
	return store.listScopedTimePayloads(ctx, scope, readmodel.Executions, query.PageQuery,
		fingerprint, "executions", "updated_at", "status", states, time.Time{}, time.Time{})
}

func (store *ReadModelStore) Execution(
	ctx context.Context,
	scope readmodel.Scope,
	id string,
) (json.RawMessage, error) {
	if err := validateReadModelScope(scope); err != nil {
		return nil, err
	}
	var payload json.RawMessage
	err := store.pool.QueryRow(ctx,
		`select payload from executions where principal_id=$1 and mode=$2 and id=$3`,
		scope.PrincipalID, scope.Mode, id,
	).Scan(&payload)
	if errors.Is(err, pgx.ErrNoRows) {
		return nil, readmodel.ErrNotFound
	}
	return payload, err
}

func (store *ReadModelStore) ListAuditEvents(
	ctx context.Context,
	scope readmodel.Scope,
	query readmodel.AuditQuery,
) (readmodel.Page, error) {
	if err := validateReadModelScope(scope); err != nil {
		return readmodel.Page{}, err
	}
	limit, err := normalizeLimit(query.Limit)
	if err != nil {
		return readmodel.Page{}, err
	}
	fingerprint := "all"
	var position readmodel.CursorPosition
	if query.Cursor != "" {
		position, err = store.cursors.Decode(query.Cursor, readmodel.CursorExpectation{
			Resource: readmodel.AuditEvents, PrincipalID: scope.PrincipalID,
			Mode: scope.Mode, ContractVersion: readmodel.ContractVersion, Fingerprint: fingerprint,
		})
		if err != nil {
			return readmodel.Page{}, readmodel.ErrInvalidCursor
		}
	}
	args := []any{scope.PrincipalID, scope.Mode}
	where := []string{"(principal_id=$1 or principal_id is null)", "mode=$2"}
	if query.Cursor != "" {
		at, parseErr := parseTimeCursor(position.SortValue)
		if parseErr != nil {
			return readmodel.Page{}, parseErr
		}
		id, parseErr := strconv.ParseInt(position.ID, 10, 64)
		if parseErr != nil {
			return readmodel.Page{}, readmodel.ErrInvalidCursor
		}
		args = append(args, at, id)
		where = append(where, fmt.Sprintf(
			"(created_at<$%d or (created_at=$%d and id<$%d))",
			len(args)-1, len(args)-1, len(args),
		))
	}
	args = append(args, limit+1)
	sql := fmt.Sprintf(`
		select id, created_at, coalesce(principal_id, ''), action,
			coalesce(target_id, ''), outcome, request_id, coalesce(execution_id, '')
		from audit_events
		where %s
		order by created_at desc, id desc
		limit $%d
	`, strings.Join(where, " and "), len(args))
	rows, err := store.pool.Query(ctx, sql, args...)
	if err != nil {
		return readmodel.Page{}, err
	}
	defer rows.Close()
	result := make([]timeRow, 0, limit+1)
	for rows.Next() {
		var id int64
		var occurredAt time.Time
		var principalID, kind, targetID, outcome, requestID, executionID string
		if err := rows.Scan(&id, &occurredAt, &principalID, &kind, &targetID, &outcome, &requestID, &executionID); err != nil {
			return readmodel.Page{}, err
		}
		payload, marshalErr := json.Marshal(map[string]any{
			"auditId":     strconv.FormatInt(id, 10),
			"time":        occurredAt.UTC().Format(time.RFC3339Nano),
			"principalId": nullableJSON(principalID),
			"kind":        kind,
			"targetId":    nullableJSON(targetID),
			"outcome":     outcome,
			"requestId":   requestID,
			"executionId": nullableJSON(executionID),
		})
		if marshalErr != nil {
			return readmodel.Page{}, marshalErr
		}
		result = append(result, timeRow{
			ID: strconv.FormatInt(id, 10), OccurredAt: occurredAt.UTC(), Payload: payload,
		})
	}
	if err := rows.Err(); err != nil {
		return readmodel.Page{}, err
	}
	return store.timePage(scope, readmodel.AuditEvents, fingerprint, limit, result)
}

func (store *ReadModelStore) Snapshot(
	ctx context.Context,
	scope readmodel.Scope,
	kinds []readmodel.EventKind,
) (readmodel.Snapshot, error) {
	if err := validateReadModelScope(scope); err != nil {
		return readmodel.Snapshot{}, err
	}
	include := snapshotKinds(kinds)
	result := readmodel.Snapshot{AsOf: time.Now().UTC()}
	var err error
	if include["opportunities"] {
		result.Opportunities, err = collectPages(func(cursor string) (readmodel.Page, error) {
			return store.ListOpportunities(ctx, scope, readmodel.OpportunityQuery{
				PageQuery: readmodel.PageQuery{Cursor: cursor, Limit: maxReadModelPageLimit},
				Sort:      readmodel.NetAPRDescending,
			})
		})
		if err != nil {
			return readmodel.Snapshot{}, err
		}
	}
	if include["alerts"] {
		result.Alerts, err = collectPages(func(cursor string) (readmodel.Page, error) {
			return store.ListAlerts(ctx, scope, readmodel.AlertQuery{
				PageQuery: readmodel.PageQuery{Cursor: cursor, Limit: maxReadModelPageLimit},
			})
		})
		if err != nil {
			return readmodel.Snapshot{}, err
		}
	}
	if include["executions"] {
		result.Executions, err = collectPages(func(cursor string) (readmodel.Page, error) {
			return store.ListExecutions(ctx, scope, readmodel.ExecutionQuery{
				PageQuery: readmodel.PageQuery{Cursor: cursor, Limit: maxReadModelPageLimit},
			})
		})
		if err != nil {
			return readmodel.Snapshot{}, err
		}
	}
	if include["positions"] {
		result.Positions, err = collectPages(func(cursor string) (readmodel.Page, error) {
			return store.ListPositions(ctx, scope, readmodel.PositionQuery{
				PageQuery: readmodel.PageQuery{Cursor: cursor, Limit: maxReadModelPageLimit},
			})
		})
		if err != nil {
			return readmodel.Snapshot{}, err
		}
	}
	if include["activities"] {
		result.Activities, err = collectPages(func(cursor string) (readmodel.Page, error) {
			return store.ListActivities(ctx, scope, readmodel.ActivityQuery{
				PageQuery: readmodel.PageQuery{Cursor: cursor, Limit: maxReadModelPageLimit},
			})
		})
		if err != nil {
			return readmodel.Snapshot{}, err
		}
	}
	if include["audit"] {
		result.AuditEvents, err = collectPages(func(cursor string) (readmodel.Page, error) {
			return store.ListAuditEvents(ctx, scope, readmodel.AuditQuery{
				PageQuery: readmodel.PageQuery{Cursor: cursor, Limit: maxReadModelPageLimit},
			})
		})
		if err != nil {
			return readmodel.Snapshot{}, err
		}
	}
	return result, nil
}

func (store *ReadModelStore) listScopedTimePayloads(
	ctx context.Context,
	scope readmodel.Scope,
	resource readmodel.Resource,
	pageQuery readmodel.PageQuery,
	fingerprint, table, timeColumn, filterColumn string,
	filters []string,
	from, to time.Time,
) (readmodel.Page, error) {
	if err := validateReadModelScope(scope); err != nil {
		return readmodel.Page{}, err
	}
	limit, err := normalizeLimit(pageQuery.Limit)
	if err != nil {
		return readmodel.Page{}, err
	}
	var position readmodel.CursorPosition
	if pageQuery.Cursor != "" {
		position, err = store.cursors.Decode(pageQuery.Cursor, readmodel.CursorExpectation{
			Resource: resource, PrincipalID: scope.PrincipalID,
			Mode: scope.Mode, ContractVersion: readmodel.ContractVersion, Fingerprint: fingerprint,
		})
		if err != nil {
			return readmodel.Page{}, readmodel.ErrInvalidCursor
		}
	}
	args := []any{scope.PrincipalID, scope.Mode}
	where := []string{"principal_id=$1", "mode=$2"}
	if len(filters) > 0 {
		args = append(args, filters)
		where = append(where, fmt.Sprintf("%s=any($%d)", filterColumn, len(args)))
	}
	if !from.IsZero() {
		args = append(args, from.UTC())
		where = append(where, fmt.Sprintf("%s>=$%d", timeColumn, len(args)))
	}
	if !to.IsZero() {
		args = append(args, to.UTC())
		where = append(where, fmt.Sprintf("%s<=$%d", timeColumn, len(args)))
	}
	if pageQuery.Cursor != "" {
		at, parseErr := parseTimeCursor(position.SortValue)
		if parseErr != nil {
			return readmodel.Page{}, parseErr
		}
		args = append(args, at, position.ID)
		where = append(where, fmt.Sprintf(
			"(%s<$%d or (%s=$%d and id>$%d))",
			timeColumn, len(args)-1, timeColumn, len(args)-1, len(args),
		))
	}
	args = append(args, limit+1)
	sql := fmt.Sprintf(`
		select id, %s, payload
		from %s where %s
		order by %s desc, id asc
		limit $%d
	`, timeColumn, table, strings.Join(where, " and "), timeColumn, len(args))
	rows, err := store.pool.Query(ctx, sql, args...)
	if err != nil {
		return readmodel.Page{}, err
	}
	result, err := scanTimeRows(rows)
	if err != nil {
		return readmodel.Page{}, err
	}
	return store.timePage(scope, resource, fingerprint, limit, result)
}

func scanTimeRows(rows pgx.Rows) ([]timeRow, error) {
	defer rows.Close()
	result := make([]timeRow, 0)
	for rows.Next() {
		var row timeRow
		if err := rows.Scan(&row.ID, &row.OccurredAt, &row.Payload); err != nil {
			return nil, err
		}
		row.OccurredAt = row.OccurredAt.UTC()
		result = append(result, row)
	}
	return result, rows.Err()
}

func (store *ReadModelStore) timePage(
	scope readmodel.Scope,
	resource readmodel.Resource,
	fingerprint string,
	limit int,
	rows []timeRow,
) (readmodel.Page, error) {
	page := readmodel.Page{Items: make([]json.RawMessage, 0, min(limit, len(rows)))}
	for index, row := range rows {
		if index == limit {
			break
		}
		page.Items = append(page.Items, row.Payload)
		if row.OccurredAt.After(page.AsOf) {
			page.AsOf = row.OccurredAt
		}
	}
	if len(rows) > limit {
		last := rows[limit-1]
		cursor, err := store.cursors.Encode(readmodel.CursorPosition{
			Resource: resource, PrincipalID: scope.PrincipalID,
			Mode: scope.Mode, ContractVersion: readmodel.ContractVersion, Fingerprint: fingerprint,
			SortValue: timeCursorValue(last.OccurredAt), ID: last.ID,
			OccurredAt: last.OccurredAt,
		})
		if err != nil {
			return readmodel.Page{}, err
		}
		page.NextCursor = cursor
	}
	return page, nil
}

func collectPages(list func(string) (readmodel.Page, error)) ([]json.RawMessage, error) {
	var result []json.RawMessage
	cursor := ""
	for pages := 0; pages < 20; pages++ {
		page, err := list(cursor)
		if err != nil {
			return nil, err
		}
		result = append(result, page.Items...)
		if page.NextCursor == "" {
			return result, nil
		}
		cursor = page.NextCursor
	}
	return nil, fmt.Errorf("read-model snapshot exceeds safe page bound")
}

func snapshotKinds(kinds []readmodel.EventKind) map[string]bool {
	include := map[string]bool{
		"opportunities": true,
		"alerts":        true,
		"executions":    true,
		"positions":     true,
		"activities":    true,
		"audit":         true,
	}
	if len(kinds) == 0 {
		return include
	}
	for key := range include {
		include[key] = false
	}
	for _, kind := range kinds {
		switch kind {
		case readmodel.OpportunityUpsert:
			include["opportunities"] = true
		case readmodel.AlertUpsert:
			include["alerts"] = true
		case readmodel.ExecutionUpsert:
			include["executions"] = true
		case readmodel.PositionUpsert:
			include["positions"] = true
		case readmodel.ActivityAppend:
			include["activities"] = true
		case readmodel.AuditAppend:
			include["audit"] = true
		case readmodel.SnapshotEvent:
			for key := range include {
				include[key] = true
			}
		}
	}
	return include
}

func nullableJSON(value string) any {
	if value == "" {
		return nil
	}
	return value
}
