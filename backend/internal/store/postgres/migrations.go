package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"errors"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	backendmigrations "github.com/QuantProcessing/boltertrader/backend/migrations"
	"github.com/jackc/pgx/v5/pgconn"
)

const (
	platformMigrationLockKey int64 = 0x4254504c415446
	migrationUnlockTimeout         = 2 * time.Second
)

var (
	ErrMigrationMismatch  = errors.New("platform postgres migration mismatch")
	platformMigrationFile = regexp.MustCompile(`^([0-9]{4})_.+\.sql$`)
)

type platformMigration struct {
	version  int
	path     string
	sql      string
	checksum string
}

func Migrate(ctx context.Context, database *sql.DB) (returnErr error) {
	if database == nil {
		return fmt.Errorf("Platform database is required")
	}
	connection, err := database.Conn(ctx)
	if err != nil {
		return fmt.Errorf("acquire Platform migration connection: %w", err)
	}
	defer connection.Close()
	if _, err := connection.ExecContext(ctx, `SELECT pg_advisory_lock($1)`, platformMigrationLockKey); err != nil {
		return fmt.Errorf("acquire Platform migration lock: %w", err)
	}
	defer func() {
		unlockCtx, cancel := context.WithTimeout(context.Background(), migrationUnlockTimeout)
		defer cancel()
		var unlocked bool
		if err := connection.QueryRowContext(unlockCtx, `SELECT pg_advisory_unlock($1)`, platformMigrationLockKey).Scan(&unlocked); err != nil {
			unlockErr := fmt.Errorf("release Platform migration lock: %w", err)
			if returnErr == nil {
				returnErr = unlockErr
			} else {
				returnErr = errors.Join(returnErr, unlockErr)
			}
		} else if !unlocked {
			unlockErr := fmt.Errorf("release Platform migration lock: lock was not held")
			if returnErr == nil {
				returnErr = unlockErr
			} else {
				returnErr = errors.Join(returnErr, unlockErr)
			}
		}
	}()

	migrations, err := embeddedPlatformMigrations()
	if err != nil {
		return err
	}
	applied, err := loadAppliedPlatformMigrations(ctx, connection)
	if err != nil {
		return err
	}
	byVersion := make(map[int]platformMigration, len(migrations))
	for _, migration := range migrations {
		byVersion[migration.version] = migration
	}
	for version, checksum := range applied {
		migration, ok := byVersion[version]
		if !ok {
			return fmt.Errorf("%w: database version %d is unsupported", ErrMigrationMismatch, version)
		}
		if checksum != migration.checksum {
			return fmt.Errorf("%w: checksum mismatch for version %d", ErrMigrationMismatch, version)
		}
	}
	for _, migration := range migrations {
		if _, ok := applied[migration.version]; ok {
			continue
		}
		transaction, err := connection.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin Platform migration %d: %w", migration.version, err)
		}
		if _, err := transaction.ExecContext(ctx, migration.sql); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("apply Platform migration %d: %w", migration.version, err)
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO platform_schema_migrations(version,checksum) VALUES ($1,$2)`, migration.version, migration.checksum); err != nil {
			_ = transaction.Rollback()
			return fmt.Errorf("record Platform migration %d: %w", migration.version, err)
		}
		if err := transaction.Commit(); err != nil {
			return fmt.Errorf("commit Platform migration %d: %w", migration.version, err)
		}
	}
	return nil
}

func loadAppliedPlatformMigrations(ctx context.Context, connection *sql.Conn) (map[int]string, error) {
	rows, err := connection.QueryContext(ctx, `SELECT version,checksum FROM platform_schema_migrations ORDER BY version`)
	if err != nil {
		var postgresError *pgconn.PgError
		// A missing ledger table is an empty ledger, not a failure: this is a
		// database nobody has migrated yet.
		if errors.As(err, &postgresError) && postgresError.Code == "42P01" {
			return map[int]string{}, nil
		}
		return nil, fmt.Errorf("read Platform migrations: %w", err)
	}
	defer rows.Close()
	applied := map[int]string{}
	for rows.Next() {
		var version int
		var checksum string
		if err := rows.Scan(&version, &checksum); err != nil {
			return nil, fmt.Errorf("scan Platform migrations: %w", err)
		}
		applied[version] = checksum
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("scan Platform migrations: %w", err)
	}
	return applied, nil
}

func embeddedPlatformMigrations() ([]platformMigration, error) {
	entries, err := backendmigrations.Files.ReadDir(".")
	if err != nil {
		return nil, fmt.Errorf("read embedded Platform migrations: %w", err)
	}
	var migrations []platformMigration
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		match := platformMigrationFile.FindStringSubmatch(entry.Name())
		if match == nil {
			continue
		}
		version, err := strconv.Atoi(match[1])
		if err != nil {
			return nil, fmt.Errorf("parse Platform migration %s: %w", entry.Name(), err)
		}
		data, err := backendmigrations.Files.ReadFile(entry.Name())
		if err != nil {
			return nil, fmt.Errorf("read Platform migration %s: %w", entry.Name(), err)
		}
		checksum := sha256.Sum256(data)
		migrations = append(migrations, platformMigration{version: version, path: entry.Name(), sql: string(data), checksum: hex.EncodeToString(checksum[:])})
	}
	sort.Slice(migrations, func(left, right int) bool { return migrations[left].version < migrations[right].version })
	for index, migration := range migrations {
		if expected := index + 1; migration.version != expected {
			return nil, fmt.Errorf("%w: missing local migration version %d before %s", ErrMigrationMismatch, expected, migration.path)
		}
		if strings.TrimSpace(migration.sql) == "" {
			return nil, fmt.Errorf("%w: empty migration %s", ErrMigrationMismatch, migration.path)
		}
	}
	return migrations, nil
}
