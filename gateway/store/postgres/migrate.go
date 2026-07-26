package postgres

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/pgxpool"
)

const (
	migrationLockKey        int32 = 0x470003
	migrationCleanupTimeout       = 2 * time.Second
)

var migrationFile = regexp.MustCompile(`^([0-9]+)_.+\.sql$`)

type migration struct {
	version  int
	path     string
	sql      string
	checksum string
}

func Migrate(ctx context.Context, pool *pgxpool.Pool) error {
	return migrateWithOptions(ctx, pool, migrateOptions{})
}

type migrateOptions struct {
	afterLock    func(context.Context) error
	beforeUnlock func(*pgxpool.Conn)
}

func migrateWithOptions(ctx context.Context, pool *pgxpool.Pool, options migrateOptions) (returnErr error) {
	conn, err := pool.Acquire(ctx)
	if err != nil {
		return redactErr("acquire migration connection", err)
	}

	if _, err := conn.Exec(ctx, `SELECT pg_advisory_lock($1, $2)`, migrationLockKey, int32(1)); err != nil {
		lockErr := redactErr("acquire migration lock", err)
		if closeErr := discardMigrationConnection(conn); closeErr != nil {
			return errors.Join(lockErr, closeErr)
		}
		return lockErr
	}
	defer func() {
		if options.beforeUnlock != nil {
			options.beforeUnlock(conn)
		}
		if cleanupErr := releaseMigrationLock(conn); cleanupErr != nil {
			if returnErr == nil {
				returnErr = cleanupErr
			} else {
				returnErr = errors.Join(returnErr, cleanupErr)
			}
		}
	}()

	if options.afterLock != nil {
		if err := options.afterLock(ctx); err != nil {
			return redactErr("run migration after-lock hook", err)
		}
	}

	migrations, err := embeddedMigrations()
	if err != nil {
		return err
	}
	applied := map[int]string{}
	rows, err := conn.Query(ctx, `SELECT version, checksum FROM schema_migrations ORDER BY version`)
	if err != nil {
		var pgErr *pgconn.PgError
		if !errors.As(err, &pgErr) || pgErr.Code != "42P01" {
			return redactErr("read schema_migrations", err)
		}
	} else {
		defer rows.Close()
		for rows.Next() {
			var version int
			var checksum string
			if err := rows.Scan(&version, &checksum); err != nil {
				return redactErr("scan schema_migrations", err)
			}
			applied[version] = checksum
		}
		if err := rows.Err(); err != nil {
			return redactErr("scan schema_migrations", err)
		}
	}

	byVersion := map[int]migration{}
	for _, migration := range migrations {
		byVersion[migration.version] = migration
	}
	for version, checksum := range applied {
		local, ok := byVersion[version]
		if !ok {
			return fmt.Errorf("%w: database version %d is unsupported", ErrMigrationMismatch, version)
		}
		if checksum != local.checksum {
			return fmt.Errorf("%w: checksum mismatch for version %d", ErrMigrationMismatch, version)
		}
	}

	for _, migration := range migrations {
		if _, ok := applied[migration.version]; ok {
			continue
		}
		tx, err := conn.BeginTx(ctx, pgx.TxOptions{})
		if err != nil {
			return redactErr(fmt.Sprintf("begin migration %d", migration.version), err)
		}
		if _, err := tx.Exec(ctx, migration.sql); err != nil {
			_ = tx.Rollback(ctx)
			return redactErr(fmt.Sprintf("apply migration %d", migration.version), err)
		}
		if _, err := tx.Exec(ctx, `INSERT INTO schema_migrations(version, checksum) VALUES ($1, $2)`, migration.version, migration.checksum); err != nil {
			_ = tx.Rollback(ctx)
			return redactErr(fmt.Sprintf("record migration %d", migration.version), err)
		}
		if err := tx.Commit(ctx); err != nil {
			return redactErr(fmt.Sprintf("commit migration %d", migration.version), err)
		}
	}
	return nil
}

func releaseMigrationLock(conn *pgxpool.Conn) error {
	ctx, cancel := context.WithTimeout(context.Background(), migrationCleanupTimeout)
	var unlocked bool
	err := conn.QueryRow(ctx, `SELECT pg_advisory_unlock($1, $2)`, migrationLockKey, int32(1)).Scan(&unlocked)
	cancel()

	if err == nil && unlocked {
		conn.Release()
		return nil
	}

	var unlockErr error
	if err != nil {
		unlockErr = redactErr("release migration lock", err)
	} else {
		unlockErr = newRedactedError("release migration lock", "postgres advisory lock was not held", "", nil)
	}
	if closeErr := discardMigrationConnection(conn); closeErr != nil {
		return errors.Join(unlockErr, closeErr)
	}
	return unlockErr
}

func discardMigrationConnection(conn *pgxpool.Conn) error {
	hijacked := conn.Hijack()
	ctx, cancel := context.WithTimeout(context.Background(), migrationCleanupTimeout)
	defer cancel()
	if err := hijacked.Close(ctx); err != nil {
		return redactErr("close migration connection", err)
	}
	return nil
}

func embeddedMigrations() ([]migration, error) {
	entries, err := Migrations.ReadDir("migrations")
	if err != nil {
		return nil, fmt.Errorf("read embedded migrations: %w", err)
	}
	var migrations []migration
	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		match := migrationFile.FindStringSubmatch(name)
		if match == nil {
			continue
		}
		version, err := strconv.Atoi(match[1])
		if err != nil {
			return nil, fmt.Errorf("parse migration version %s: %w", name, err)
		}
		path := filepath.ToSlash(filepath.Join("migrations", name))
		data, err := Migrations.ReadFile(path)
		if err != nil {
			return nil, fmt.Errorf("read migration %s: %w", name, err)
		}
		sum := sha256.Sum256(data)
		migrations = append(migrations, migration{
			version:  version,
			path:     path,
			sql:      string(data),
			checksum: hex.EncodeToString(sum[:]),
		})
	}
	sort.Slice(migrations, func(i, j int) bool {
		return migrations[i].version < migrations[j].version
	})
	for index, migration := range migrations {
		if want := index + 1; migration.version != want {
			return nil, fmt.Errorf("%w: missing local migration version %d before %s", ErrMigrationMismatch, want, migration.path)
		}
		if strings.TrimSpace(migration.sql) == "" {
			return nil, fmt.Errorf("%w: empty migration %s", ErrMigrationMismatch, migration.path)
		}
	}
	return migrations, nil
}
