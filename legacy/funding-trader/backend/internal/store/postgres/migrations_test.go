package postgres

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestEmbeddedMigrationsIncludeRequiredTables(t *testing.T) {
	sql := EmbeddedMigrationSQL()
	asset, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "0001_init.sql"))
	if err != nil {
		t.Fatalf("migration asset missing: %v", err)
	}
	if sql != string(asset) {
		t.Fatal("embedded migration SQL must come from migrations/0001_init.sql")
	}
	for _, table := range []string{"schema_migrations", "users", "sessions", "idempotency_records", "opportunities", "alerts", "executions", "positions", "activities", "audit_events"} {
		if !strings.Contains(sql, table) {
			t.Fatalf("migration missing %s", table)
		}
	}
}

func TestReadModelMigrationAddsScopedProjectionAndReplayState(t *testing.T) {
	sql, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "0002_read_model.sql"))
	if err != nil {
		t.Fatalf("read-model migration asset missing: %v", err)
	}
	text := string(sql)
	for _, required := range []string{
		"environment",
		"principal_id",
		"observed_at",
		"updated_at",
		"read_model_events",
		"boot_id",
		"scope",
		"payload",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("read-model migration missing %s", required)
		}
	}
}

func TestExecutionMigrationAddsDurableScopedExecutionState(t *testing.T) {
	sql, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "0003_execution.sql"))
	if err != nil {
		t.Fatalf("execution migration asset missing: %v", err)
	}
	text := string(sql)
	for _, required := range []string{
		"execution_previews",
		"execution_legs",
		"execution_fills",
		"execution_transitions",
		"idempotency_records_scope_key_idx",
		"principal_id, environment, method, path, key",
		"request_hash",
		"version bigint",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("execution migration missing %s", required)
		}
	}
}

func TestProductModeMigrationAddsModeScopingColumns(t *testing.T) {
	sql, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "0004_product_mode.sql"))
	if err != nil {
		t.Fatalf("product-mode migration asset missing: %v", err)
	}
	text := string(sql)
	required := []string{
		"mode",
		"venue_environment",
		"cutover_control",
		"opportunities",
		"positions",
		"activities",
		"audit_events",
		"idempotency_records",
		"execution_previews",
		"executions",
		"execution_transitions",
		"funding_observations",
		"read_model_events",
		"alter table opportunities",
		"drop column environment",
		"service_sessions_revoked",
		"authoritative_collision",
		"missing_native_provenance",
		"check (mode in ('test', 'live'))",
	}
	for _, r := range required {
		if !strings.Contains(text, r) {
			t.Fatalf("product-mode migration missing %q", r)
		}
	}
	for _, forbidden := range []string{
		"mode in ('test', 'live', 'legacy')",
		"default 'legacy'",
		"add constraint if not exists",
	} {
		if strings.Contains(text, forbidden) {
			t.Fatalf("product-mode migration retained forbidden contract %q", forbidden)
		}
	}
}

func TestProductModeMigrationBindsSessionsAndEventsToModeContract(t *testing.T) {
	sql, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", "0004_product_mode.sql"))
	if err != nil {
		t.Fatalf("product-mode migration asset missing: %v", err)
	}
	text := string(sql)
	for _, required := range []string{
		"alter table sessions\n  add column mode text",
		"sessions_mode_check",
		"contract_version text",
		"read_model_events_contract_mode_idx",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("product-mode migration missing session/event contract %q", required)
		}
	}
}

func TestMigrationRunnerIntegrationAndChecksumDrift(t *testing.T) {
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
	if _, err := pool.Exec(ctx, "drop table if exists cutover_control, schema_migrations, read_model_events, funding_observations, execution_transitions, execution_fills, execution_legs, execution_previews, audit_events, activities, positions, executions, alerts, opportunities, idempotency_records, sessions, users cascade"); err != nil {
		t.Fatal(err)
	}
	runner := NewMigrationRunner(pool)
	if err := runner.Up(ctx); !errors.Is(err, ErrProductModeMigrationRequired) {
		t.Fatalf("up error=%v, want ErrProductModeMigrationRequired", err)
	}
	if err := runner.Up(ctx); !errors.Is(err, ErrProductModeMigrationRequired) {
		t.Fatalf("second up error=%v, want ErrProductModeMigrationRequired", err)
	}
	if _, err := pool.Exec(ctx, "update schema_migrations set checksum='drift' where version=1"); err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err == nil {
		t.Fatal("expected checksum drift failure")
	}
}

func TestMigrationRunnerIntegrationSchemaDrift(t *testing.T) {
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
	if _, err := pool.Exec(ctx, "drop table if exists cutover_control, schema_migrations, read_model_events, funding_observations, execution_transitions, execution_fills, execution_legs, execution_previews, audit_events, activities, positions, executions, alerts, opportunities, idempotency_records, sessions, users cascade"); err != nil {
		t.Fatal(err)
	}
	runner := NewMigrationRunner(pool)
	applyLegacyMigrationsForTest(t, ctx, pool)
	if _, err := pool.Exec(ctx, "alter table sessions drop column csrf_digest"); err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err == nil {
		t.Fatal("expected schema drift failure")
	}
}

func TestMigrationRunnerDetectsReadModelSchemaDrift(t *testing.T) {
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
	if _, err := pool.Exec(ctx, "drop table if exists cutover_control, schema_migrations, read_model_events, funding_observations, execution_transitions, execution_fills, execution_legs, execution_previews, audit_events, activities, positions, executions, alerts, opportunities, idempotency_records, sessions, users cascade"); err != nil {
		t.Fatal(err)
	}
	runner := NewMigrationRunner(pool)
	applyLegacyMigrationsForTest(t, ctx, pool)
	if _, err := pool.Exec(ctx, "alter table read_model_events drop column scope"); err != nil {
		t.Fatal(err)
	}
	if err := runner.Up(ctx); err == nil {
		t.Fatal("expected read-model schema drift failure")
	}
}

func TestMigrationRunnerUpgradesVersionOneDatabaseWithoutRewritingHistory(t *testing.T) {
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
	if _, err := pool.Exec(ctx, "drop table if exists cutover_control, schema_migrations, read_model_events, funding_observations, execution_transitions, execution_fills, execution_legs, execution_previews, audit_events, activities, positions, executions, alerts, opportunities, idempotency_records, sessions, users cascade"); err != nil {
		t.Fatal(err)
	}
	versionOneSQL := EmbeddedMigrationSQL()
	versionOneChecksum := checksum(versionOneSQL)
	if _, err := pool.Exec(ctx, versionOneSQL); err != nil {
		t.Fatalf("apply version one fixture: %v", err)
	}
	if _, err := pool.Exec(ctx, "insert into schema_migrations(version, checksum) values(1, $1)", versionOneChecksum); err != nil {
		t.Fatalf("record version one fixture: %v", err)
	}

	if err := NewMigrationRunner(pool).Up(ctx); !errors.Is(err, ErrProductModeMigrationRequired) {
		t.Fatalf("upgrade error=%v, want ErrProductModeMigrationRequired", err)
	}
	var count int
	var retainedChecksum string
	if err := pool.QueryRow(ctx, "select count(*), min(checksum) filter (where version=1) from schema_migrations where version in (1,2,3)").Scan(&count, &retainedChecksum); err != nil {
		t.Fatal(err)
	}
	if count != 3 || retainedChecksum != versionOneChecksum {
		t.Fatalf("migration history count=%d versionOneChecksum=%q", count, retainedChecksum)
	}
	var replayTableExists bool
	if err := pool.QueryRow(ctx, "select to_regclass('public.read_model_events') is not null and to_regclass('public.execution_transitions') is not null").Scan(&replayTableExists); err != nil {
		t.Fatal(err)
	}
	if !replayTableExists {
		t.Fatal("version two replay or version three execution table was not created")
	}
}

func TestDestructiveTestDatabaseGuardRejectsUnsafeTargets(t *testing.T) {
	t.Setenv("TEST_DATABASE_URL", "postgres://user:pass@localhost/funding_trader_prod")
	t.Setenv("FT_ALLOW_DESTRUCTIVE_DB_TESTS", "true")
	if _, ok := destructiveTestDatabaseURL(t); ok {
		t.Fatal("expected unsafe database name to be rejected")
	}
	t.Setenv("TEST_DATABASE_URL", "postgres://user:pass@localhost/funding_trader_test")
	t.Setenv("FT_ALLOW_DESTRUCTIVE_DB_TESTS", "false")
	if _, ok := destructiveTestDatabaseURL(t); ok {
		t.Fatal("expected missing destructive flag to be rejected")
	}
}
