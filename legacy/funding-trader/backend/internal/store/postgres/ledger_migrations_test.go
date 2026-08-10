package postgres

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/QuantProcessing/funding-trader/backend/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestMigrationLedgerFactsAndProjectionsSchema(t *testing.T) {
	sql, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "20260801_ledger_facts_and_projections.sql"))
	if err != nil {
		t.Fatalf("ledger migration asset missing: %v", err)
	}
	text := string(sql)
	for _, required := range []string{
		"ledger_funding_payment_facts",
		"ledger_fill_cashflow_facts",
		"ledger_position_snapshot_facts",
		"ledger_lineage_facts",
		"ledger_lineage_revision_facts",
		"ledger_sync_watermarks",
		"ledger_closed_position_projections",
		"ledger_portfolio_projections",
		"prevent_ledger_append_only_update",
		"principal_id, mode, account_id, instrument_id",
		"where active",
		"unique",
		"source_revision",
		"conversion_source",
		"formula_version",
	} {
		if !strings.Contains(strings.ToLower(text), strings.ToLower(required)) {
			t.Fatalf("ledger migration missing %q", required)
		}
	}
}

func TestMigrationHistoryIncludesLedgerScopeIsolationLineageAndPositionBackfills(t *testing.T) {
	all := migrations.All()
	got := make([]int, len(all))
	for i, migration := range all {
		got[i] = migration.Version
	}
	if !reflect.DeepEqual(got, []int{1, 2, 3, 4, 5, 6, 7, 8}) {
		t.Fatalf("migration order=%v, want [1 2 3 4 5 6 7 8]", got)
	}
	if migrations.PostCutover().Version != 5 || migrations.LedgerFactsAndProjections().Version != 5 {
		t.Fatalf("post-cutover ledger migration version mismatch")
	}
	if !strings.Contains(migrations.LedgerFactsAndProjections().SQL, "ledger_funding_payment_facts") {
		t.Fatal("version five SQL not embedded")
	}
	if migrations.LedgerProjectionScopeIsolation().Version != 6 ||
		!strings.Contains(migrations.LedgerProjectionScopeIsolation().SQL, "principal_id, mode, reporting_asset, position_id") {
		t.Fatal("version six scoped closed-position key SQL not embedded")
	}
	if migrations.ExecutionLineageBackfill().Version != 7 ||
		!strings.Contains(migrations.ExecutionLineageBackfill().SQL, "payload->>'parentExecutionId'") ||
		!strings.Contains(migrations.ExecutionLineageBackfill().SQL, "payload->>'lineageId'") {
		t.Fatal("version seven execution lineage backfill SQL not embedded")
	}
	if migrations.ExecutionPositionBackfill().Version != 8 ||
		!strings.Contains(migrations.ExecutionPositionBackfill().SQL, "'position-' || id") ||
		!strings.Contains(migrations.ExecutionPositionBackfill().SQL, "jsonb_set") {
		t.Fatal("version eight execution position backfill SQL not embedded")
	}
}

func TestNormalizeLedgerExpressionKeepsWeakenedChecksDistinct(t *testing.T) {
	expected := normalizeLedgerExpression("net_pnl = realized_trading_pnl + unrealized_trading_pnl + actual_funding_cash_flow + fees")
	postgresRendered := normalizeLedgerExpression("CHECK ((net_pnl = (((realized_trading_pnl + unrealized_trading_pnl) + actual_funding_cash_flow) + fees)))")
	if postgresRendered != expected {
		t.Fatalf("postgres-rendered check normalized to %q, want %q", postgresRendered, expected)
	}
	weakened := normalizeLedgerExpression("CHECK ((net_pnl = (((realized_trading_pnl + unrealized_trading_pnl) + actual_funding_cash_flow) + fees)) OR true)")
	if weakened == expected {
		t.Fatal("weakened OR true check must not normalize to the required ledger check")
	}
	status := normalizeLedgerExpression("CHECK ((status = ANY (ARRAY['unsupported'::text, 'temporarily_unavailable'::text, 'incomplete'::text, 'complete_empty'::text, 'complete'::text])))")
	expectedStatus := normalizeLedgerExpression("status = any array['unsupported','temporarily_unavailable','incomplete','complete_empty','complete']")
	if status != expectedStatus {
		t.Fatalf("postgres-rendered status check normalized to %q, want %q", status, expectedStatus)
	}
}

func TestMigrationRunnerAppliesLedgerFactsAfterProductMode(t *testing.T) {
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
	dropLedgerFixture(t, ctx, pool)

	runner := NewMigrationRunner(pool)
	if err := runner.Up(ctx); !errors.Is(err, ErrProductModeMigrationRequired) {
		t.Fatalf("clean boundary error=%v, want ErrProductModeMigrationRequired", err)
	}
	applyProductModeOnlyForTest(t, ctx, pool)
	if err := runner.Up(ctx); err != nil {
		t.Fatalf("apply v5 after product-mode: %v", err)
	}
	if err := runner.Up(ctx); err != nil {
		t.Fatalf("repeat v5 up: %v", err)
	}
	var versions []int
	rows, err := pool.Query(ctx, "select version from schema_migrations order by version")
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var version int
		if err := rows.Scan(&version); err != nil {
			t.Fatal(err)
		}
		versions = append(versions, version)
	}
	if !reflect.DeepEqual(versions, []int{1, 2, 3, 4, 5, 6, 7, 8}) {
		t.Fatalf("versions=%v, want [1 2 3 4 5 6 7 8]", versions)
	}
	var tableExists bool
	if err := pool.QueryRow(ctx, "select to_regclass('public.ledger_funding_payment_facts') is not null").Scan(&tableExists); err != nil {
		t.Fatal(err)
	}
	if !tableExists {
		t.Fatal("ledger facts table missing")
	}
}

func TestMigrationRunnerBackfillsExecutionLineageColumnsAfterVersionSix(t *testing.T) {
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
	dropLedgerFixture(t, ctx, pool)
	applyProductModeOnlyForTest(t, ctx, pool)

	if err := NewMigrationRunner(pool).Up(ctx); err != nil {
		t.Fatalf("apply post-cutover migrations: %v", err)
	}
	if _, err := pool.Exec(ctx, "delete from schema_migrations where version=7"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		insert into executions(
		  id, principal_id, mode, operation_kind, position_id,
		  parent_execution_id, lineage_id, idempotency_key, request_hash,
		  status, reconciliation_state, manual_intervention, payload, version,
		  created_at, updated_at
		) values
		  (
		    'exec-backfill-missing','principal-a','test','close','position-a',
		    null,null,'idem-a','hash-a','closed','ready',false,
		    '{"id":"exec-backfill-missing","parentExecutionId":"exec-open-a","lineageId":"lineage-a","mode":"test"}'::jsonb,
		    1,now(),now()
		  ),
		  (
		    'exec-backfill-preserve','principal-a','test','close','position-b',
		    'existing-parent','existing-lineage','idem-b','hash-b','closed','ready',false,
		    '{"id":"exec-backfill-preserve","parentExecutionId":"payload-parent","lineageId":"payload-lineage","mode":"test"}'::jsonb,
		    1,now(),now()
		  ),
		  (
		    'exec-backfill-empty-payload','principal-a','test','open','position-c',
		    null,null,'idem-c','hash-c','hedged','ready',false,
		    '{"id":"exec-backfill-empty-payload","parentExecutionId":"","lineageId":"","mode":"test"}'::jsonb,
		    1,now(),now()
		  )
	`); err != nil {
		t.Fatal(err)
	}

	if err := NewMigrationRunner(pool).Up(ctx); err != nil {
		t.Fatalf("apply lineage backfill: %v", err)
	}
	if err := NewMigrationRunner(pool).Up(ctx); err != nil {
		t.Fatalf("repeat lineage backfill: %v", err)
	}
	assertMigrationExecutionLineage(t, ctx, pool, "exec-backfill-missing", "exec-open-a", "lineage-a")
	assertMigrationExecutionLineage(t, ctx, pool, "exec-backfill-preserve", "existing-parent", "existing-lineage")
	assertMigrationExecutionLineage(t, ctx, pool, "exec-backfill-empty-payload", "", "")
	var versionSevenCount int
	if err := pool.QueryRow(ctx, "select count(*) from schema_migrations where version=7").Scan(&versionSevenCount); err != nil || versionSevenCount != 1 {
		t.Fatalf("version seven record count=%d err=%v", versionSevenCount, err)
	}
}

func TestMigrationRunnerBackfillsCanonicalOpenPositionIDsAfterVersionSeven(t *testing.T) {
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
	dropLedgerFixture(t, ctx, pool)
	applyProductModeOnlyForTest(t, ctx, pool)

	if err := NewMigrationRunner(pool).Up(ctx); err != nil {
		t.Fatalf("apply post-cutover migrations: %v", err)
	}
	if _, err := pool.Exec(ctx, "delete from schema_migrations where version=8"); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		insert into executions(
		  id, principal_id, mode, operation_kind, position_id,
		  parent_execution_id, lineage_id, idempotency_key, request_hash,
		  status, reconciliation_state, manual_intervention, payload, version,
		  created_at, updated_at
		) values
		  (
		    'exec-open-missing','principal-a','test','open',null,
		    null,null,'idem-pos-a','hash-pos-a','hedged','ready',false,
		    '{"id":"exec-open-missing","mode":"test","operation":"open"}'::jsonb,
		    1,now(),now()
		  ),
		  (
		    'exec-open-column-present','principal-a','test','open','existing-column-position',
		    null,null,'idem-pos-b','hash-pos-b','hedged','ready',false,
		    '{"id":"exec-open-column-present","mode":"test","operation":"open"}'::jsonb,
		    1,now(),now()
		  ),
		  (
		    'exec-open-payload-present','principal-a','test','open',null,
		    null,null,'idem-pos-c','hash-pos-c','hedged','ready',false,
		    '{"id":"exec-open-payload-present","mode":"test","operation":"open","positionId":"payload-position"}'::jsonb,
		    1,now(),now()
		  ),
		  (
		    'exec-close-missing','principal-a','test','close',null,
		    'exec-open-missing','lineage-a','idem-pos-d','hash-pos-d','closed','ready',false,
		    '{"id":"exec-close-missing","mode":"test","operation":"close"}'::jsonb,
		    1,now(),now()
		  )
	`); err != nil {
		t.Fatal(err)
	}

	if err := NewMigrationRunner(pool).Up(ctx); err != nil {
		t.Fatalf("apply position backfill: %v", err)
	}
	if err := NewMigrationRunner(pool).Up(ctx); err != nil {
		t.Fatalf("repeat position backfill: %v", err)
	}
	assertMigrationExecutionPosition(t, ctx, pool, "exec-open-missing", "position-exec-open-missing", "position-exec-open-missing")
	assertMigrationExecutionPosition(t, ctx, pool, "exec-open-column-present", "existing-column-position", "existing-column-position")
	assertMigrationExecutionPosition(t, ctx, pool, "exec-open-payload-present", "payload-position", "payload-position")
	assertMigrationExecutionPosition(t, ctx, pool, "exec-close-missing", "", "")
	var versionEightCount int
	if err := pool.QueryRow(ctx, "select count(*) from schema_migrations where version=8").Scan(&versionEightCount); err != nil || versionEightCount != 1 {
		t.Fatalf("version eight record count=%d err=%v", versionEightCount, err)
	}
}

func TestMigrationRunnerUpgradesExistingVersionFiveProjectionWithoutDataLoss(t *testing.T) {
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
	dropLedgerFixture(t, ctx, pool)
	applyProductModeOnlyForTest(t, ctx, pool)

	v5 := migrations.LedgerFactsAndProjections()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, v5.SQL); err != nil {
		t.Fatalf("apply exact v5 fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, "insert into schema_migrations(version, checksum) values($1,$2)", v5.Version, checksum(v5.SQL)); err != nil {
		t.Fatalf("record exact v5 fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		insert into ledger_closed_position_projections(
		  position_id, principal_id, mode, account_id, instrument_id, reporting_asset,
		  verified, verification_status
		) values('shared-position','principal-before','test','account-before','BTC-USDT-PERP','USDT',true,'verified')
	`); err != nil {
		t.Fatalf("seed v5 projection: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}

	if err := NewMigrationRunner(pool).Up(ctx); err != nil {
		t.Fatalf("upgrade exact v5 fixture to v6: %v", err)
	}
	var accountID string
	if err := pool.QueryRow(ctx, `
		select account_id from ledger_closed_position_projections
		where principal_id='principal-before' and mode='test' and reporting_asset='USDT' and position_id='shared-position'
	`).Scan(&accountID); err != nil || accountID != "account-before" {
		t.Fatalf("v5 projection not preserved account=%q err=%v", accountID, err)
	}
	var versionSixCount int
	if err := pool.QueryRow(ctx, "select count(*) from schema_migrations where version=6").Scan(&versionSixCount); err != nil || versionSixCount != 1 {
		t.Fatalf("version six record count=%d err=%v", versionSixCount, err)
	}
	if _, err := pool.Exec(ctx, `
		insert into ledger_closed_position_projections(
		  position_id, principal_id, mode, account_id, instrument_id, reporting_asset,
		  verified, verification_status
		) values('shared-position','principal-after','test','account-after','ETH-USDT-PERP','USDT',true,'verified')
	`); err != nil {
		t.Fatalf("v6 composite key rejected same position id in another scope: %v", err)
	}
	var scopedRows int
	if err := pool.QueryRow(ctx, "select count(*) from ledger_closed_position_projections where position_id='shared-position'").Scan(&scopedRows); err != nil || scopedRows != 2 {
		t.Fatalf("same position id scoped rows=%d err=%v", scopedRows, err)
	}
}

func TestMigrationRejectsLedgerSchemaDrift(t *testing.T) {
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
	dropLedgerFixture(t, ctx, pool)
	applyProductModeOnlyForTest(t, ctx, pool)
	if err := NewMigrationRunner(pool).Up(ctx); err != nil {
		t.Fatalf("apply v5 before drift: %v", err)
	}
	if _, err := pool.Exec(ctx, "alter table ledger_funding_payment_facts drop column source_revision"); err != nil {
		t.Fatal(err)
	}
	if err := NewMigrationRunner(pool).Up(ctx); !errors.Is(err, ErrSchemaDrift) {
		t.Fatalf("ledger drift error=%v, want ErrSchemaDrift", err)
	}
}

func TestMigrationRejectsLedgerIndexAndTriggerDrift(t *testing.T) {
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
	dropLedgerFixture(t, ctx, pool)
	applyProductModeOnlyForTest(t, ctx, pool)
	if err := NewMigrationRunner(pool).Up(ctx); err != nil {
		t.Fatalf("apply v5 before drift: %v", err)
	}
	if _, err := pool.Exec(ctx, "drop index ledger_lineage_facts_active_scope_idx"); err != nil {
		t.Fatal(err)
	}
	if err := NewMigrationRunner(pool).Up(ctx); !errors.Is(err, ErrSchemaDrift) {
		t.Fatalf("ledger index drift error=%v, want ErrSchemaDrift", err)
	}

	dropLedgerFixture(t, ctx, pool)
	applyProductModeOnlyForTest(t, ctx, pool)
	if err := NewMigrationRunner(pool).Up(ctx); err != nil {
		t.Fatalf("reapply v5 before trigger drift: %v", err)
	}
	if _, err := pool.Exec(ctx, "drop trigger ledger_sync_watermarks_no_delete on ledger_sync_watermarks"); err != nil {
		t.Fatal(err)
	}
	if err := NewMigrationRunner(pool).Up(ctx); !errors.Is(err, ErrSchemaDrift) {
		t.Fatalf("ledger trigger drift error=%v, want ErrSchemaDrift", err)
	}
}

func TestMigrationRejectsLedgerDetailedSchemaDrift(t *testing.T) {
	dsn, ok := destructiveTestDatabaseURL(t)
	if !ok {
		t.Skip("TEST_DATABASE_URL not set")
	}
	cases := []struct {
		name  string
		drift string
	}{
		{
			name: "same name wrong index",
			drift: `
				drop index ledger_funding_payment_facts_idempotency_idx;
				create unique index ledger_funding_payment_facts_idempotency_idx
				  on ledger_funding_payment_facts(principal_id, mode);
			`,
		},
		{
			name:  "missing sync watermark fk",
			drift: `alter table ledger_funding_payment_facts drop constraint ledger_funding_payment_facts_sync_watermark_fk;`,
		},
		{
			name: "wrong fk target",
			drift: `
				alter table ledger_funding_payment_facts drop constraint ledger_funding_payment_facts_sync_watermark_fk;
				alter table ledger_funding_payment_facts
				  add constraint ledger_funding_payment_facts_sync_watermark_fk
				  foreign key(sync_watermark_id) references ledger_funding_payment_facts(fact_id);
			`,
		},
		{
			name:  "missing status check",
			drift: `alter table ledger_sync_watermarks drop constraint ledger_sync_watermarks_status_check;`,
		},
		{
			name:  "alter omitted column nullability",
			drift: `alter table ledger_position_snapshot_facts alter column conversion_source drop not null;`,
		},
		{
			name: "wrong trigger function",
			drift: `
				drop trigger ledger_fill_cashflow_facts_no_delete on ledger_fill_cashflow_facts;
				create trigger ledger_fill_cashflow_facts_no_delete before delete on ledger_fill_cashflow_facts
				  for each row execute function prevent_ledger_append_only_update();
			`,
		},
		{
			name: "weakened check with or true",
			drift: `
				do $do$
				declare
				  constraint_name text;
				begin
				  select c.conname into constraint_name
				  from pg_constraint c
				  join pg_class t on t.oid=c.conrelid
				  where t.relname='ledger_closed_position_projections'
				    and c.contype='c'
				    and pg_get_constraintdef(c.oid) ilike '%net_pnl%'
				  limit 1;
				  execute format('alter table ledger_closed_position_projections drop constraint %I', constraint_name);
				end $do$;
				alter table ledger_closed_position_projections
				  add constraint ledger_closed_position_projections_net_pnl_weakened_check
				  check (net_pnl = realized_trading_pnl + unrealized_trading_pnl + actual_funding_cash_flow + fees or true);
			`,
		},
		{
			name:  "missing non funding mode check",
			drift: `alter table ledger_fill_cashflow_facts drop constraint ledger_fill_cashflow_facts_mode_check;`,
		},
		{
			name:  "missing append sequence unique",
			drift: `alter table ledger_funding_payment_facts drop constraint ledger_funding_payment_facts_sequence_id_key;`,
		},
		{
			name: "wrong trigger timing and granularity",
			drift: `
				drop trigger ledger_sync_watermarks_no_delete on ledger_sync_watermarks;
				create trigger ledger_sync_watermarks_no_delete after delete on ledger_sync_watermarks
				  for each statement execute function prevent_ledger_append_only_delete();
			`,
		},
		{
			name:  "global append sequence owned by one column",
			drift: `alter sequence ledger_append_sequence owned by ledger_funding_payment_facts.sequence_id;`,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			ctx := context.Background()
			pool, err := pgxpool.New(ctx, dsn)
			if err != nil {
				t.Fatal(err)
			}
			defer pool.Close()
			dropLedgerFixture(t, ctx, pool)
			applyProductModeOnlyForTest(t, ctx, pool)
			if err := NewMigrationRunner(pool).Up(ctx); err != nil {
				t.Fatalf("apply v5 before drift: %v", err)
			}
			if _, err := pool.Exec(ctx, tc.drift); err != nil {
				t.Fatalf("apply drift: %v", err)
			}
			if err := NewMigrationRunner(pool).Up(ctx); !errors.Is(err, ErrSchemaDrift) {
				t.Fatalf("ledger drift error=%v, want ErrSchemaDrift", err)
			}
		})
	}
}

func assertMigrationExecutionLineage(t *testing.T, ctx context.Context, pool *pgxpool.Pool, executionID, parentID, lineageID string) {
	t.Helper()
	var parentColumn, lineageColumn string
	if err := pool.QueryRow(ctx, `
		select coalesce(parent_execution_id, ''), coalesce(lineage_id, '')
		from executions
		where id=$1
	`, executionID).Scan(&parentColumn, &lineageColumn); err != nil {
		t.Fatal(err)
	}
	if parentColumn != parentID || lineageColumn != lineageID {
		t.Fatalf("execution %s lineage columns parent=%q lineage=%q, want parent=%q lineage=%q", executionID, parentColumn, lineageColumn, parentID, lineageID)
	}
}

func assertMigrationExecutionPosition(t *testing.T, ctx context.Context, pool *pgxpool.Pool, executionID, columnID, payloadID string) {
	t.Helper()
	var positionColumn, payloadPosition string
	if err := pool.QueryRow(ctx, `
		select coalesce(position_id, ''), coalesce(payload->>'positionId', '')
		from executions
		where id=$1
	`, executionID).Scan(&positionColumn, &payloadPosition); err != nil {
		t.Fatal(err)
	}
	if positionColumn != columnID || payloadPosition != payloadID {
		t.Fatalf("execution %s position column=%q payload=%q, want column=%q payload=%q", executionID, positionColumn, payloadPosition, columnID, payloadID)
	}
}

func applyProductModeOnlyForTest(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	applyLegacyMigrationsForTest(t, ctx, pool)
	migration := migrations.ProductMode()
	tx, err := pool.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, migration.SQL); err != nil {
		t.Fatalf("apply product-mode test fixture: %v", err)
	}
	if _, err := tx.Exec(ctx,
		"insert into schema_migrations(version, checksum) values($1,$2)",
		migration.Version,
		checksum(migration.SQL),
	); err != nil {
		t.Fatalf("record product-mode test fixture: %v", err)
	}
	if _, err := tx.Exec(ctx, `
		insert into cutover_control(
			singleton, contract_version, phase, backup_id, backup_sha256,
			backup_verified_at, old_service_stopped, auto_restart_disabled,
			validated_at
		)
		values(true,$1,'validated','test-fixture',$2,now(),true,true,now())
	`, migration.Version, strings.Repeat("f", 64)); err != nil {
		t.Fatalf("record product-mode control fixture: %v", err)
	}
	if err := tx.Commit(ctx); err != nil {
		t.Fatal(err)
	}
	var hasV5 bool
	if err := pool.QueryRow(ctx, "select exists(select 1 from schema_migrations where version=5)").Scan(&hasV5); err != nil {
		t.Fatal(err)
	}
	if hasV5 {
		t.Fatal("test fixture must stop at exact v4")
	}
}

func dropLedgerFixture(t *testing.T, ctx context.Context, pool *pgxpool.Pool) {
	t.Helper()
	if _, err := pool.Exec(ctx, `
		drop table if exists
		  ledger_portfolio_projections,
		  ledger_closed_position_projections,
		  ledger_sync_watermarks,
		  ledger_lineage_revision_facts,
		  ledger_lineage_facts,
		  ledger_position_snapshot_facts,
		  ledger_fill_cashflow_facts,
		  ledger_funding_payment_facts,
		  cutover_control,
		  schema_migrations,
		  read_model_events,
		  funding_observations,
		  execution_transitions,
		  execution_fills,
		  execution_legs,
		  execution_previews,
		  audit_events,
		  activities,
		  positions,
		  executions,
		  alerts,
		  opportunities,
		  idempotency_records,
		  sessions,
		  users
		cascade
	`); err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, `
		drop function if exists enforce_ledger_lineage_update();
		drop function if exists prevent_ledger_append_only_update();
		drop function if exists prevent_ledger_append_only_delete();
		drop sequence if exists ledger_append_sequence;
	`); err != nil {
		t.Fatal(err)
	}
}
