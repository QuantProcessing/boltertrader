package postgres

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
	"github.com/QuantProcessing/funding-trader/backend/internal/uistream"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/shopspring/decimal"
)

func TestReadModelStorePersistsScopedSnapshotsAndOpaqueKeysetPages(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	resetReadModelDatabase(t, ctx, pool)
	applyProductModeMigrationsForTest(t, ctx, pool)
	store, err := NewReadModelStore(pool, bytes.Repeat([]byte{0x2a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	observedAt := time.Date(2026, 7, 29, 16, 0, 0, 0, time.UTC)
	events, err := store.ApplyMarketSnapshot(ctx, "boot-a", readmodel.MarketSnapshot{
		Mode:       "test",
		ObservedAt: observedAt,
		Opportunities: []readmodel.Projection{
			projection("opp-low", "executable", "8.25", observedAt.Add(-time.Second)),
			projection("opp-high", "attention", "12.50", observedAt),
		},
		Alerts: []readmodel.AlertProjection{{
			Projection: readmodel.Projection{
				ID: "alert-1", Status: "warning", ObservedAt: observedAt,
				Payload: json.RawMessage(`{"alertId":"alert-1","severity":"warning","observedAt":"2026-07-29T16:00:00Z"}`),
			},
			OpportunityID: "opp-high",
			AccountID:     "account-short",
			InstrumentID:  "instrument-btc",
		}},
		Funding: []readmodel.FundingObservation{{
			AccountID: "account-short", InstrumentID: "instrument-btc", VenueEnvironment: "demo",
			FundingTime: observedAt, ObservedAt: observedAt,
			Rate: decimal.RequireFromString("0.0004"),
		}, {
			AccountID: "account-short", InstrumentID: "instrument-btc", VenueEnvironment: "testnet",
			FundingTime: observedAt, ObservedAt: observedAt,
			Rate: decimal.RequireFromString("0.0005"),
		}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 3 {
		t.Fatalf("events=%+v", events)
	}
	var fundingRows int
	if err := pool.QueryRow(ctx, `
		select count(*)
		from funding_observations
		where mode='test'
		  and venue_environment in ('demo', 'testnet')
		  and account_id='account-short'
		  and instrument_id='instrument-btc'
		  and funding_time=$1
	`, observedAt).Scan(&fundingRows); err != nil {
		t.Fatal(err)
	}
	if fundingRows != 2 {
		t.Fatalf("funding observations retained %d rows, want demo and testnet", fundingRows)
	}

	scope := readmodel.Scope{PrincipalID: "principal-default", Mode: "test"}
	first, err := store.ListOpportunities(ctx, scope, readmodel.OpportunityQuery{
		PageQuery: readmodel.PageQuery{Limit: 1},
		Sort:      readmodel.NetAPRDescending,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Items) != 1 || first.NextCursor == "" ||
		!jsonObjectEquals(first.Items[0], `{"opportunityId":"opp-high","netAprPct":12.5}`) {
		t.Fatalf("first=%+v payload=%s", first, first.Items[0])
	}
	second, err := store.ListOpportunities(ctx, scope, readmodel.OpportunityQuery{
		PageQuery: readmodel.PageQuery{Limit: 1, Cursor: first.NextCursor},
		Sort:      readmodel.NetAPRDescending,
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(second.Items) != 1 || second.NextCursor != "" ||
		!jsonObjectEquals(second.Items[0], `{"opportunityId":"opp-low","netAprPct":8.25}`) {
		t.Fatalf("second=%+v payload=%s", second, second.Items[0])
	}
	if _, err := store.ListOpportunities(ctx,
		readmodel.Scope{PrincipalID: scope.PrincipalID, Mode: "live"},
		readmodel.OpportunityQuery{
			PageQuery: readmodel.PageQuery{Limit: 1, Cursor: first.NextCursor},
			Sort:      readmodel.NetAPRDescending,
		},
	); !errors.Is(err, readmodel.ErrInvalidCursor) {
		t.Fatalf("cross-mode cursor error=%v", err)
	}

	alerts, err := store.ListAlerts(ctx, scope, readmodel.AlertQuery{
		PageQuery: readmodel.PageQuery{Limit: 10},
		Severity:  "warning",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(alerts.Items) != 1 || alerts.Counts["warning"] != 1 ||
		!jsonObjectEquals(alerts.Items[0], `{"alertId":"alert-1","severity":"warning","observedAt":"2026-07-29T16:00:00Z"}`) {
		t.Fatalf("alerts=%+v", alerts)
	}
}

func TestReadModelStoreReplacesMissingMarketRowsAndPersistsBrowserResnapshot(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	resetReadModelDatabase(t, ctx, pool)
	applyProductModeMigrationsForTest(t, ctx, pool)
	store, err := NewReadModelStore(pool, bytes.Repeat([]byte{0x3b}, 32))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 7, 29, 17, 0, 0, 0, time.UTC)
	if _, err := store.ApplyMarketSnapshot(ctx, "boot-a", readmodel.MarketSnapshot{
		Mode:          "test",
		ObservedAt:    at,
		Opportunities: []readmodel.Projection{projection("opp-1", "executable", "10", at)},
	}); err != nil {
		t.Fatal(err)
	}
	events, err := store.ApplyMarketSnapshot(ctx, "boot-a", readmodel.MarketSnapshot{
		Mode:       "test",
		ObservedAt: at.Add(time.Minute),
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 1 || events[0].Kind != readmodel.ResnapshotRequired ||
		string(events[0].Payload) == "" ||
		bytes.Contains(events[0].Payload, []byte("resync_required")) {
		t.Fatalf("events=%+v", events)
	}
	page, err := store.ListOpportunities(ctx,
		readmodel.Scope{PrincipalID: "principal-default", Mode: "test"},
		readmodel.OpportunityQuery{PageQuery: readmodel.PageQuery{Limit: 10}, Sort: readmodel.NetAPRDescending},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("stale opportunities remain visible: %+v", page)
	}
	replayed, err := store.ReplayEvents(ctx, "boot-a", 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 2 || replayed[1].Kind != readmodel.ResnapshotRequired ||
		replayed[1].ContractVersion != readmodel.ContractVersion {
		t.Fatalf("replayed=%+v", replayed)
	}
}

func TestReadModelStorePersistsPrincipalScopedPositionsAndIdempotentActivities(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	resetReadModelDatabase(t, ctx, pool)
	applyProductModeMigrationsForTest(t, ctx, pool)
	store, err := NewReadModelStore(pool, bytes.Repeat([]byte{0x4a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 7, 29, 18, 0, 0, 0, time.UTC)
	scope := readmodel.Scope{PrincipalID: "principal-default", Mode: "test"}
	positionEvent, err := store.ApplyPosition(ctx, "boot-a", readmodel.PositionProjection{
		ID: "position-1", Scope: scope, Symbol: "BTC-USDT", Status: "open",
		UpdatedAt: at,
		Payload:   json.RawMessage(`{"positionId":"position-1","symbol":"BTC-USDT","state":"open"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if positionEvent.Kind != readmodel.PositionUpsert || positionEvent.PrincipalID != scope.PrincipalID {
		t.Fatalf("position event=%+v", positionEvent)
	}
	activityEvent, err := store.AppendActivity(ctx, "boot-a", readmodel.ActivityProjection{
		ID: "activity-1", Scope: scope, Kind: "position.opened",
		PositionID: "position-1", ExecutionID: "execution-1", OccurredAt: at,
		Payload: json.RawMessage(`{"activityId":"activity-1","kind":"position.opened","state":"open","positionId":"position-1","executionId":"execution-1"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if activityEvent.Kind != readmodel.ActivityAppend || activityEvent.PrincipalID != scope.PrincipalID {
		t.Fatalf("activity event=%+v", activityEvent)
	}
	duplicate, err := store.AppendActivity(ctx, "boot-a", readmodel.ActivityProjection{
		ID: "activity-1", Scope: scope, Kind: "position.opened",
		PositionID: "position-1", ExecutionID: "execution-1", OccurredAt: at,
		Payload: json.RawMessage(`{"activityId":"activity-1","kind":"position.opened","state":"open","positionId":"position-1","executionId":"execution-1"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if duplicate.ID != 0 {
		t.Fatalf("duplicate activity emitted event=%+v", duplicate)
	}
	if _, err := store.AppendActivity(ctx, "boot-a", readmodel.ActivityProjection{
		ID: "activity-1", Scope: scope, Kind: "position.opened",
		PositionID: "position-1", ExecutionID: "execution-1", OccurredAt: at,
		Payload: json.RawMessage(`{"activityId":"activity-1","kind":"position.opened","state":"conflict","positionId":"position-1","executionId":"execution-1"}`),
	}); err == nil {
		t.Fatal("expected conflicting activity replay to fail")
	}

	positions, err := store.ListPositions(ctx, scope, readmodel.PositionQuery{
		PageQuery: readmodel.PageQuery{Limit: 10}, States: []string{"open"},
	})
	if err != nil {
		t.Fatal(err)
	}
	activities, err := store.ListActivities(ctx, scope, readmodel.ActivityQuery{
		PageQuery: readmodel.PageQuery{Limit: 10}, Kinds: []string{"position.opened"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(positions.Items) != 1 || len(activities.Items) != 1 {
		t.Fatalf("positions=%+v activities=%+v", positions, activities)
	}
	positionPayload, err := store.Position(ctx, scope, "position-1")
	if err != nil {
		t.Fatal(err)
	}
	if !jsonObjectEquals(positionPayload, `{"positionId":"position-1","symbol":"BTC-USDT","state":"open"}`) {
		t.Fatalf("position payload=%s", positionPayload)
	}
	if _, err := store.Position(ctx,
		readmodel.Scope{PrincipalID: "principal-other", Mode: "test"},
		"position-1",
	); !errors.Is(err, readmodel.ErrNotFound) {
		t.Fatalf("cross-principal position error=%v", err)
	}
	otherPrincipal, err := store.ListPositions(ctx,
		readmodel.Scope{PrincipalID: "principal-other", Mode: "test"},
		readmodel.PositionQuery{PageQuery: readmodel.PageQuery{Limit: 10}},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(otherPrincipal.Items) != 0 {
		t.Fatalf("cross-principal positions=%+v", otherPrincipal)
	}
	var activityRows, activityEvents int
	if err := pool.QueryRow(ctx, "select count(*) from activities where id='activity-1'").Scan(&activityRows); err != nil {
		t.Fatal(err)
	}
	if err := pool.QueryRow(ctx, "select count(*) from read_model_events where contract_version=$1 and kind='activity.append' and entity_id='activity-1'", readmodel.ContractVersion).Scan(&activityEvents); err != nil {
		t.Fatal(err)
	}
	if activityRows != 1 || activityEvents != 1 {
		t.Fatalf("activity rows=%d events=%d", activityRows, activityEvents)
	}
}

func TestReadModelStoreAppliesExecutionProjectionAtomically(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	resetReadModelDatabase(t, ctx, pool)
	applyProductModeMigrationsForTest(t, ctx, pool)
	store, err := NewReadModelStore(pool, bytes.Repeat([]byte{0x5a}, 32))
	if err != nil {
		t.Fatal(err)
	}
	at := time.Date(2026, 7, 29, 19, 0, 0, 0, time.UTC)
	scope := readmodel.Scope{PrincipalID: "principal-default", Mode: "test"}
	position := readmodel.PositionProjection{
		ID: "position-atomic", Scope: scope, Symbol: "BTC-USDT", Status: "open",
		UpdatedAt: at,
		Payload:   json.RawMessage(`{"positionId":"position-atomic","status":"open"}`),
	}
	activity := readmodel.ActivityProjection{
		ID: "activity-atomic", Scope: scope, Kind: "position.opened",
		PositionID: position.ID, ExecutionID: "execution-atomic", OccurredAt: at,
		Payload: json.RawMessage(`{"positionId":"position-atomic","kind":"position.opened"}`),
	}

	// Seed the immutable activity identity with different content. The atomic
	// projection must roll back its position upsert when that conflict is found.
	conflicting := activity
	conflicting.Payload = json.RawMessage(`{"positionId":"position-atomic","kind":"different"}`)
	if _, err := store.AppendActivity(ctx, "boot-atomic", conflicting); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ApplyExecutionProjection(ctx, "boot-atomic", position, activity); err == nil {
		t.Fatal("expected immutable activity conflict")
	}
	var positionRows int
	if err := pool.QueryRow(ctx,
		"select count(*) from positions where id=$1",
		position.ID,
	).Scan(&positionRows); err != nil {
		t.Fatal(err)
	}
	if positionRows != 0 {
		t.Fatalf("position escaped rolled-back projection transaction: rows=%d", positionRows)
	}

	activity.ID = "activity-atomic-valid"
	events, err := store.ApplyExecutionProjection(ctx, "boot-atomic", position, activity)
	if err != nil {
		t.Fatal(err)
	}
	if len(events) != 2 ||
		events[0].Kind != readmodel.PositionUpsert ||
		events[1].Kind != readmodel.ActivityAppend {
		t.Fatalf("atomic projection events=%+v", events)
	}
	var activityRows int
	if err := pool.QueryRow(ctx,
		"select count(*) from activities where id=$1",
		activity.ID,
	).Scan(&activityRows); err != nil {
		t.Fatal(err)
	}
	if activityRows != 1 {
		t.Fatalf("atomic activity rows=%d", activityRows)
	}
}

func TestUIStreamEventStoreScopesReplayToTheCurrentBootAndProjectionScope(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	ctx := context.Background()
	pool, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer pool.Close()
	resetReadModelDatabase(t, ctx, pool)
	applyProductModeMigrationsForTest(t, ctx, pool)
	readStore, err := NewReadModelStore(pool, bytes.Repeat([]byte{0x4c}, 32))
	if err != nil {
		t.Fatal(err)
	}
	streamStore, err := NewUIStreamEventStore(readStore, "boot-a", readmodel.Scope{
		PrincipalID: "principal-default",
		Mode:        "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := streamStore.Append(ctx, uistream.EventInput{
		Scope: "all", PrincipalID: "principal-default", Mode: "test",
		Kind: "opportunity.upsert",
		Data: json.RawMessage(`{"opportunityId":"opp-1"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := streamStore.Append(ctx, uistream.EventInput{
		Scope: "all", PrincipalID: "principal-default", Mode: "test",
		Kind: "alert.upsert",
		Data: json.RawMessage(`{"alertId":"alert-1"}`),
	}); err != nil {
		t.Fatal(err)
	}
	second, err := streamStore.Append(ctx, uistream.EventInput{
		Scope: "all", PrincipalID: "principal-default", Mode: "test",
		Kind: "opportunity.upsert",
		Data: json.RawMessage(`{"opportunityId":"opp-2"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	latest, err := streamStore.Latest(ctx, "all")
	if err != nil {
		t.Fatal(err)
	}
	if latest != second.Sequence {
		t.Fatalf("latest=%d want=%d", latest, second.Sequence)
	}
	replayed, err := streamStore.Replay(ctx, "all", first.Sequence,
		[]string{"opportunity.upsert"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(replayed) != 1 || replayed[0].Sequence != second.Sequence ||
		replayed[0].PrincipalID != "principal-default" ||
		replayed[0].Mode != "test" ||
		replayed[0].ContractVersion != readmodel.ContractVersion ||
		!jsonObjectEquals(replayed[0].Data, `{"opportunityId":"opp-2"}`) {
		t.Fatalf("replayed=%+v", replayed)
	}
}

func projection(id, status, netAPR string, observedAt time.Time) readmodel.Projection {
	return readmodel.Projection{
		ID: id, Status: status, Symbol: "BTC-USDT",
		LongAccountID: "account-long", ShortAccountID: "account-short",
		NetAPR: decimal.RequireFromString(netAPR), FormulaVersion: "funding-arb-v1",
		ObservedAt: observedAt,
		Payload:    json.RawMessage(`{"opportunityId":"` + id + `","netAprPct":` + netAPR + `}`),
	}
}

func resetReadModelDatabase(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, "drop table if exists cutover_control, schema_migrations, read_model_events, funding_observations, execution_transitions, execution_fills, execution_legs, execution_previews, audit_events, activities, positions, executions, alerts, opportunities, idempotency_records, sessions, users cascade"); err != nil {
		t.Fatal(err)
	}
}

func jsonObjectEquals(actual json.RawMessage, expected string) bool {
	var left, right any
	return json.Unmarshal(actual, &left) == nil &&
		json.Unmarshal([]byte(expected), &right) == nil &&
		reflect.DeepEqual(left, right)
}
