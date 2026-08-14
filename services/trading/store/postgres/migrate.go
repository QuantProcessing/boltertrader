// Package postgres is the trading service's durable store: reservations,
// commands, the accounting ledger, positions and the audit trail.
//
// Every statement names its tables unqualified, so where they land is decided
// by the connection rather than by the code: a database of trading's own the
// way the gateway does it, or a search_path pointed at a throwaway schema the
// way the integration tests do it. Neither choice reaches this package.
package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"embed"
	"encoding/hex"
	"errors"
	"fmt"
	"path"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

//go:embed migrations/*.sql
var Migrations embed.FS

// ErrMigrationMismatch reports that the database and this binary disagree about
// the schema. It is deliberately distinct from a failure to apply a migration:
// a mismatch means the operator has to decide something, not that a retry might
// work.
var ErrMigrationMismatch = errors.New("trading postgres migration mismatch")

// migrationLockKey namespaces the advisory lock so a concurrent gateway or auth
// edge migration on the same database does not serialize against this one.
const migrationLockKey int64 = 0x54524144

var migrationFile = regexp.MustCompile(`^([0-9]+)_.+\.sql$`)

type migration struct {
	version  int
	path     string
	sql      string
	checksum string
}

// Migrate brings the connected schema up to the embedded definition. It is safe
// to call from every starting instance: an advisory lock makes concurrent
// callers queue rather than race, and already-applied versions are verified by
// checksum instead of re-run.
func Migrate(ctx context.Context, database *sql.DB) (returnErr error) {
	migrations, err := embeddedMigrations()
	if err != nil {
		return err
	}
	connection, err := database.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire trading migration connection: %w", err)
	}
	defer func() {
		if closeErr := connection.Close(); closeErr != nil && returnErr == nil {
			returnErr = fmt.Errorf("close trading migration connection: %w", closeErr)
		}
	}()
	if _, err := connection.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, migrationLockKey); err != nil {
		return fmt.Errorf("acquire trading migration lock: %w", err)
	}
	defer func() {
		// The unlock runs on its own context: if the caller's context is already
		// cancelled, dropping the lock still has to happen or the next instance
		// blocks forever.
		if _, err := connection.ExecContext(context.WithoutCancel(ctx), `SELECT pg_advisory_unlock($1)`, migrationLockKey); err != nil && returnErr == nil {
			returnErr = fmt.Errorf("release trading migration lock: %w", err)
		}
	}()

	applied, err := appliedMigrations(ctx, connection)
	if err != nil {
		return err
	}
	local := map[int]migration{}
	for _, item := range migrations {
		local[item.version] = item
	}
	for version, checksum := range applied {
		item, ok := local[version]
		if !ok {
			return fmt.Errorf("%w: database version %d is unsupported", ErrMigrationMismatch, version)
		}
		if item.checksum != checksum {
			return fmt.Errorf("%w: checksum mismatch for version %d", ErrMigrationMismatch, version)
		}
	}

	for _, item := range migrations {
		if _, ok := applied[item.version]; ok {
			continue
		}
		if err := applyMigration(ctx, connection, item); err != nil {
			return err
		}
	}
	return nil
}

// appliedMigrations reads the ledger, treating "table does not exist" as an
// empty ledger so a first run needs no bootstrap step of its own.
func appliedMigrations(ctx context.Context, connection *sql.Conn) (map[int]string, error) {
	var exists bool
	if err := connection.QueryRowContext(ctx, `SELECT to_regclass('schema_migrations') IS NOT NULL`).Scan(&exists); err != nil {
		return nil, fmt.Errorf("inspect trading schema_migrations: %w", err)
	}
	applied := map[int]string{}
	if !exists {
		return applied, nil
	}
	rows, err := connection.QueryContext(ctx, `SELECT version, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		return nil, fmt.Errorf("read trading schema_migrations: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var version int
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, fmt.Errorf("scan trading schema_migrations: %w", err)
		}
		applied[version] = checksum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan trading schema_migrations: %w", err)
	}
	return applied, nil
}

// applyMigration runs one file and records it in the same transaction, so a
// crash between the two is not possible.
func applyMigration(ctx context.Context, connection *sql.Conn, item migration) error {
	tx, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin trading migration %d: %w", item.version, err)
	}
	defer tx.Rollback()
	if _, err := tx.ExecContext(ctx, item.sql); err != nil {
		return fmt.Errorf("apply trading migration %d: %w", item.version, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO schema_migrations(version, checksum) VALUES ($1, $2)`, item.version, item.checksum); err != nil {
		return fmt.Errorf("record trading migration %d: %w", item.version, err)
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("commit trading migration %d: %w", item.version, err)
	}
	return nil
}

func embeddedMigrations() ([]migration, error) {
	entries, err := Migrations.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded trading migrations: %w", err)
	}
	migrations := make([]migration, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := migrationFile.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		version, err := strconv.Atoi(match[1])
		if err != nil {
			return nil, fmt.Errorf("parse trading migration version %s: %w", entry.Name(), err)
		}
		location := path.Join("migrations", entry.Name())
		data, err := Migrations.ReadFile(location)
		if err != nil {
			return nil, fmt.Errorf("read trading migration %s: %w", entry.Name(), err)
		}
		sum := sha256.Sum256(data)
		migrations = append(migrations, migration{version: version, path: location, sql: string(data), checksum: hex.EncodeToString(sum[:])})
	}
	sort.Slice(migrations, func(left, right int) bool { return migrations[left].version < migrations[right].version })
	for index, item := range migrations {
		if want := index + 1; item.version != want {
			return nil, fmt.Errorf("%w: missing local migration version %d before %s", ErrMigrationMismatch, want, item.path)
		}
		if strings.TrimSpace(item.sql) == "" {
			return nil, fmt.Errorf("%w: empty migration %s", ErrMigrationMismatch, item.path)
		}
	}
	return migrations, nil
}
