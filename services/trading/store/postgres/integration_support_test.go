package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

var integrationSchemaSequence atomic.Uint64

// openIntegrationDatabase hands back a connection pointed at a schema of its
// own, plus a reopen function so a test can prove a fact survives losing every
// connection. Each test gets a fresh schema rather than a fresh database, which
// is the same isolation the deployment relies on.
func openIntegrationDatabase(t *testing.T) (*sql.DB, func() *sql.DB) {
	t.Helper()
	return openIntegrationDatabaseAtVersion(t, 0)
}

// A positive version prepares an older real schema for upgrade regression
// tests. Zero uses the normal production migration runner.
func openIntegrationDatabaseAtVersion(t *testing.T, version int) (*sql.DB, func() *sql.DB) {
	t.Helper()
	if testing.Short() {
		t.Skip("PostgreSQL integration requires explicit non-short tests")
	}
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
	schema := fmt.Sprintf("bolter_trading_%d_%d", os.Getpid(), integrationSchemaSequence.Add(1))
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
	// Going through Migrate rather than replaying the SQL by hand means these
	// tests run against the schema the service would actually create, and the
	// runner itself is covered by every integration test that follows.
	if version == 0 {
		if err := Migrate(context.Background(), database); err != nil {
			t.Fatal(err)
		}
	} else {
		migrations, err := embeddedMigrations()
		if err != nil {
			t.Fatal(err)
		}
		connection, err := database.Conn(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		defer connection.Close()
		for _, migration := range migrations {
			if migration.version > version {
				break
			}
			if err := applyMigration(context.Background(), connection, migration); err != nil {
				t.Fatal(err)
			}
		}
	}
	return database, reopen
}
