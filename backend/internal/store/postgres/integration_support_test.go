package postgres

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

var integrationSchemaSequence atomic.Uint64

func openIntegrationDatabaseWithMigrations(t *testing.T, applyMigrations bool) (*sql.DB, func() *sql.DB) {
	t.Helper()
	dsn := strings.TrimSpace(os.Getenv("BT_TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("BT_TEST_POSTGRES_DSN is not set")
	}
	adminConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	admin := stdlib.OpenDB(*adminConfig)
	if err := admin.Ping(); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	schema := fmt.Sprintf("bolter_test_%d_%d", os.Getpid(), integrationSchemaSequence.Add(1))
	if _, err := admin.Exec(`CREATE SCHEMA ` + schema); err != nil {
		admin.Close()
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_, _ = admin.Exec(`DROP SCHEMA ` + schema + ` CASCADE`)
		_ = admin.Close()
	})
	testConfig, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	testConfig.RuntimeParams["search_path"] = schema
	var databases []*sql.DB
	reopen := func() *sql.DB {
		database := stdlib.OpenDB(*testConfig)
		database.SetMaxOpenConns(1)
		databases = append(databases, database)
		return database
	}
	database := reopen()
	t.Cleanup(func() {
		for _, opened := range databases {
			_ = opened.Close()
		}
	})
	if applyMigrations {
		applyIntegrationMigrations(t, database, 1, 9999)
	}
	return database, reopen
}

func applyIntegrationMigrations(t *testing.T, database *sql.DB, first, last int) {
	t.Helper()
	entries, err := os.ReadDir(filepath.Join("..", "..", "..", "migrations"))
	if err != nil {
		t.Fatal(err)
	}
	var migrations []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		var version int
		if _, err := fmt.Sscanf(entry.Name(), "%04d_", &version); err == nil && version >= first && version <= last {
			migrations = append(migrations, entry.Name())
		}
	}
	sort.Strings(migrations)
	for _, name := range migrations {
		migration, err := os.ReadFile(filepath.Join("..", "..", "..", "migrations", name))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := database.Exec(string(migration)); err != nil {
			t.Fatalf("apply %s: %v", name, err)
		}
	}
}
