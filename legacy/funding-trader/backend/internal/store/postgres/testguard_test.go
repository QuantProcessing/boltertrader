package postgres

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"

	"github.com/QuantProcessing/funding-trader/backend/migrations"
	"github.com/jackc/pgx/v5/pgxpool"
)

func destructiveTestDatabaseURL(t *testing.T) (string, bool) {
	t.Helper()
	dsn := os.Getenv("TEST_DATABASE_URL")
	if dsn == "" {
		return "", false
	}
	if os.Getenv("FT_ALLOW_DESTRUCTIVE_DB_TESTS") != "true" {
		return "", false
	}
	parsed, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("invalid TEST_DATABASE_URL: %v", err)
	}
	dbName := strings.TrimPrefix(parsed.Path, "/")
	if !strings.HasSuffix(dbName, "_test") {
		return "", false
	}
	return dsn, true
}

func applyLegacyMigrationsForTest(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) {
	t.Helper()
	err := NewMigrationRunner(pool).Up(ctx)
	if !errors.Is(err, ErrProductModeMigrationRequired) {
		t.Fatalf("legacy migration boundary error=%v, want ErrProductModeMigrationRequired", err)
	}
}

func applyProductModeMigrationsForTest(
	t *testing.T,
	ctx context.Context,
	pool *pgxpool.Pool,
) {
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
	if err := NewMigrationRunner(pool).Up(ctx); err != nil {
		t.Fatalf("validate product-mode test fixture: %v", err)
	}
}
