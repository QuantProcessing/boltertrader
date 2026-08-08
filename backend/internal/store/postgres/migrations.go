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
	legacyPlatformVersion          = 4
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

type legacyPlatformTableSignature struct {
	name        string
	columns     int
	constraints int
}

type legacyPlatformSchemaSignature struct {
	columns     []string
	constraints []string
	indexes     []string
	sequences   []string
}

var legacyPlatformTables = []legacyPlatformTableSignature{
	{name: "platform_accounts", columns: 13, constraints: 8},
	{name: "platform_audit", columns: 8, constraints: 1},
	{name: "platform_commands", columns: 17, constraints: 4},
	{name: "platform_identities", columns: 6, constraints: 3},
	{name: "platform_ledger_entries", columns: 10, constraints: 2},
	{name: "platform_positions", columns: 11, constraints: 2},
	{name: "platform_public_projection_state", columns: 5, constraints: 3},
	{name: "platform_realtime_events", columns: 6, constraints: 4},
	{name: "platform_reconciliation_tasks", columns: 6, constraints: 3},
	{name: "platform_reservations", columns: 17, constraints: 3},
	{name: "platform_sessions", columns: 7, constraints: 3},
	{name: "platform_unrealized_pnl", columns: 7, constraints: 2},
}

var legacyPlatformRelations = []struct {
	name string
	kind string
}{
	{name: "platform_accounts_pkey", kind: "i"},
	{name: "platform_audit_pkey", kind: "i"},
	{name: "platform_audit_resource_idx", kind: "i"},
	{name: "platform_commands_pkey", kind: "i"},
	{name: "platform_commands_principal_id_mode_method_path_idempotency_key", kind: "i"},
	{name: "platform_identities_mode_username_key", kind: "i"},
	{name: "platform_identities_pkey", kind: "i"},
	{name: "platform_ledger_attribution", kind: "i"},
	{name: "platform_ledger_entries_pkey", kind: "i"},
	{name: "platform_positions_pkey", kind: "i"},
	{name: "platform_public_projection_state_pkey", kind: "i"},
	{name: "platform_realtime_events_cursor_key", kind: "i"},
	{name: "platform_realtime_events_event_id_key", kind: "i"},
	{name: "platform_realtime_events_observed_at_idx", kind: "i"},
	{name: "platform_realtime_events_pkey", kind: "i"},
	{name: "platform_realtime_events_sequence_seq", kind: "S"},
	{name: "platform_reconciliation_tasks_pkey", kind: "i"},
	{name: "platform_reservations_held_scope", kind: "i"},
	{name: "platform_reservations_pkey", kind: "i"},
	{name: "platform_reservations_principal_id_mode_idempotency_key_key", kind: "i"},
	{name: "platform_sessions_expires_at_idx", kind: "i"},
	{name: "platform_sessions_pkey", kind: "i"},
	{name: "platform_unrealized_pnl_pkey", kind: "i"},
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
	applied, err := loadAppliedPlatformMigrations(ctx, connection, migrations)
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

func loadAppliedPlatformMigrations(ctx context.Context, connection *sql.Conn, migrations []platformMigration) (map[int]string, error) {
	rows, err := connection.QueryContext(ctx, `SELECT version,checksum FROM platform_schema_migrations ORDER BY version`)
	if err != nil {
		var postgresError *pgconn.PgError
		if errors.As(err, &postgresError) && postgresError.Code == "42P01" {
			return adoptLegacyPlatformSchema(ctx, connection, migrations)
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

func adoptLegacyPlatformSchema(ctx context.Context, connection *sql.Conn, migrations []platformMigration) (map[int]string, error) {
	transaction, err := connection.BeginTx(ctx, nil)
	if err != nil {
		return nil, fmt.Errorf("begin legacy Platform migration adoption: %w", err)
	}
	defer transaction.Rollback()
	var targetSchema, targetSearchPath string
	if err := transaction.QueryRowContext(ctx, `SELECT current_schema(), current_setting('search_path')`).Scan(&targetSchema, &targetSearchPath); err != nil {
		return nil, fmt.Errorf("inspect legacy Platform schema namespace: %w", err)
	}

	present := 0
	for _, signature := range legacyPlatformTables {
		var columns, constraints int
		err := transaction.QueryRowContext(ctx, `
			SELECT count(a.attname), (SELECT count(*) FROM pg_constraint constraint_row WHERE constraint_row.conrelid=relation.oid)
			FROM pg_class relation
			LEFT JOIN pg_attribute a ON a.attrelid=relation.oid AND a.attnum>0 AND NOT a.attisdropped
			WHERE relation.oid=to_regclass($1) AND relation.relkind='r'
			GROUP BY relation.oid`, signature.name).Scan(&columns, &constraints)
		if errors.Is(err, sql.ErrNoRows) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("inspect legacy Platform table %s: %w", signature.name, err)
		}
		present++
		if columns != signature.columns || constraints != signature.constraints {
			return nil, fmt.Errorf("%w: legacy table %s has an unsupported signature", ErrMigrationMismatch, signature.name)
		}
	}
	if present == 0 {
		return map[int]string{}, nil
	}
	if present != len(legacyPlatformTables) {
		return nil, fmt.Errorf("%w: legacy Platform schema is incomplete", ErrMigrationMismatch)
	}
	for _, signature := range legacyPlatformRelations {
		var kind string
		if err := transaction.QueryRowContext(ctx, `SELECT relkind::text FROM pg_class WHERE oid=to_regclass($1)`, signature.name).Scan(&kind); errors.Is(err, sql.ErrNoRows) {
			return nil, fmt.Errorf("%w: legacy relation %s is missing", ErrMigrationMismatch, signature.name)
		} else if err != nil {
			return nil, fmt.Errorf("inspect legacy Platform relation %s: %w", signature.name, err)
		}
		if kind != signature.kind {
			return nil, fmt.Errorf("%w: legacy relation %s has unsupported kind %s", ErrMigrationMismatch, signature.name, kind)
		}
	}

	byVersion := make(map[int]platformMigration, len(migrations))
	for _, migration := range migrations {
		byVersion[migration.version] = migration
	}
	targetSignature, err := readLegacyPlatformSchemaSignature(ctx, transaction, targetSchema)
	if err != nil {
		return nil, fmt.Errorf("inspect legacy Platform schema semantics: %w", err)
	}
	referenceSignature, err := buildLegacyPlatformReferenceSignature(ctx, transaction, byVersion, targetSearchPath)
	if err != nil {
		return nil, err
	}
	if mismatch := targetSignature.mismatch(referenceSignature); mismatch != "" {
		return nil, fmt.Errorf("%w: legacy Platform schema has unsupported %s semantics", ErrMigrationMismatch, mismatch)
	}
	ledger, ok := byVersion[1]
	if !ok {
		return nil, fmt.Errorf("%w: local migration version 1 is missing", ErrMigrationMismatch)
	}
	if _, err := transaction.ExecContext(ctx, ledger.sql); err != nil {
		return nil, fmt.Errorf("create legacy Platform migration ledger: %w", err)
	}
	applied := make(map[int]string, legacyPlatformVersion)
	for version := 1; version <= legacyPlatformVersion; version++ {
		migration, ok := byVersion[version]
		if !ok {
			return nil, fmt.Errorf("%w: local migration version %d is missing", ErrMigrationMismatch, version)
		}
		if _, err := transaction.ExecContext(ctx, `INSERT INTO platform_schema_migrations(version,checksum) VALUES ($1,$2)`, version, migration.checksum); err != nil {
			return nil, fmt.Errorf("record adopted Platform migration %d: %w", version, err)
		}
		applied[version] = migration.checksum
	}
	if err := transaction.Commit(); err != nil {
		return nil, fmt.Errorf("commit legacy Platform migration adoption: %w", err)
	}
	return applied, nil
}

func buildLegacyPlatformReferenceSignature(ctx context.Context, transaction *sql.Tx, migrations map[int]platformMigration, targetSearchPath string) (legacyPlatformSchemaSignature, error) {
	if _, err := transaction.ExecContext(ctx, `SELECT set_config('search_path', 'pg_temp', true)`); err != nil {
		return legacyPlatformSchemaSignature{}, fmt.Errorf("select legacy Platform reference namespace: %w", err)
	}
	for version := 2; version <= legacyPlatformVersion; version++ {
		migration, ok := migrations[version]
		if !ok {
			return legacyPlatformSchemaSignature{}, fmt.Errorf("%w: local migration version %d is missing", ErrMigrationMismatch, version)
		}
		if _, err := transaction.ExecContext(ctx, migration.sql); err != nil {
			return legacyPlatformSchemaSignature{}, fmt.Errorf("construct legacy Platform reference version %d: %w", version, err)
		}
	}
	var referenceSchema string
	if err := transaction.QueryRowContext(ctx, `SELECT nspname FROM pg_namespace WHERE oid=pg_my_temp_schema()`).Scan(&referenceSchema); err != nil {
		return legacyPlatformSchemaSignature{}, fmt.Errorf("inspect legacy Platform reference namespace: %w", err)
	}
	signature, err := readLegacyPlatformSchemaSignature(ctx, transaction, referenceSchema)
	if err != nil {
		return legacyPlatformSchemaSignature{}, fmt.Errorf("inspect legacy Platform reference semantics: %w", err)
	}
	tableNames := make([]string, 0, len(legacyPlatformTables))
	for _, table := range legacyPlatformTables {
		tableNames = append(tableNames, table.name)
	}
	if _, err := transaction.ExecContext(ctx, `DROP TABLE `+strings.Join(tableNames, ", ")+` CASCADE`); err != nil {
		return legacyPlatformSchemaSignature{}, fmt.Errorf("remove legacy Platform reference schema: %w", err)
	}
	if _, err := transaction.ExecContext(ctx, `SELECT set_config('search_path', $1, true)`, targetSearchPath); err != nil {
		return legacyPlatformSchemaSignature{}, fmt.Errorf("restore legacy Platform schema namespace: %w", err)
	}
	return signature, nil
}

func readLegacyPlatformSchemaSignature(ctx context.Context, transaction *sql.Tx, schema string) (legacyPlatformSchemaSignature, error) {
	if _, err := transaction.ExecContext(ctx, `SELECT set_config('search_path', $1, true)`, schema); err != nil {
		return legacyPlatformSchemaSignature{}, err
	}
	tables := make(map[string]struct{}, len(legacyPlatformTables))
	for _, table := range legacyPlatformTables {
		tables[table.name] = struct{}{}
	}
	sequences := make(map[string]struct{})
	for _, relation := range legacyPlatformRelations {
		if relation.kind == "S" {
			sequences[relation.name] = struct{}{}
		}
	}

	var signature legacyPlatformSchemaSignature
	rows, err := transaction.QueryContext(ctx, `
		SELECT relation.relname, attribute.attnum, attribute.attname,
		       format_type(attribute.atttypid, attribute.atttypmod), attribute.attnotnull,
		       attribute.attidentity::text, attribute.attgenerated::text,
		       COALESCE(pg_get_expr(default_row.adbin, default_row.adrelid, true), '')
		FROM pg_class relation
		JOIN pg_namespace namespace ON namespace.oid=relation.relnamespace
		JOIN pg_attribute attribute ON attribute.attrelid=relation.oid AND attribute.attnum>0 AND NOT attribute.attisdropped
		LEFT JOIN pg_attrdef default_row ON default_row.adrelid=relation.oid AND default_row.adnum=attribute.attnum
		WHERE namespace.nspname=$1 AND relation.relkind='r'
		ORDER BY relation.relname, attribute.attnum`, schema)
	if err != nil {
		return legacyPlatformSchemaSignature{}, err
	}
	for rows.Next() {
		var table, name, dataType, identity, generated, defaultExpression string
		var position int
		var notNull bool
		if err := rows.Scan(&table, &position, &name, &dataType, &notNull, &identity, &generated, &defaultExpression); err != nil {
			rows.Close()
			return legacyPlatformSchemaSignature{}, err
		}
		if _, ok := tables[table]; ok {
			signature.columns = append(signature.columns, fmt.Sprintf("%s|%d|%s|%s|%t|%s|%s|%s", table, position, name, dataType, notNull, identity, generated, defaultExpression))
		}
	}
	if err := rows.Close(); err != nil {
		return legacyPlatformSchemaSignature{}, err
	}

	rows, err = transaction.QueryContext(ctx, `
		SELECT relation.relname, constraint_row.conname, constraint_row.contype::text,
		       pg_get_constraintdef(constraint_row.oid, true)
		FROM pg_constraint constraint_row
		JOIN pg_class relation ON relation.oid=constraint_row.conrelid
		JOIN pg_namespace namespace ON namespace.oid=relation.relnamespace
		WHERE namespace.nspname=$1
		ORDER BY relation.relname, constraint_row.conname`, schema)
	if err != nil {
		return legacyPlatformSchemaSignature{}, err
	}
	for rows.Next() {
		var table, name, kind, definition string
		if err := rows.Scan(&table, &name, &kind, &definition); err != nil {
			rows.Close()
			return legacyPlatformSchemaSignature{}, err
		}
		if _, ok := tables[table]; ok {
			signature.constraints = append(signature.constraints, strings.Join([]string{table, name, kind, definition}, "|"))
		}
	}
	if err := rows.Close(); err != nil {
		return legacyPlatformSchemaSignature{}, err
	}

	rows, err = transaction.QueryContext(ctx, `
		SELECT table_relation.relname, index_relation.relname, access_method.amname,
		       index_row.indisunique, index_row.indisprimary, index_row.indisexclusion,
		       index_row.indimmediate, index_row.indnkeyatts, index_row.indnatts,
		       index_row.indkey::text, index_row.indcollation::text,
		       index_row.indclass::text, index_row.indoption::text,
		       COALESCE(pg_get_expr(index_row.indexprs, index_row.indrelid, true), ''),
		       COALESCE(pg_get_expr(index_row.indpred, index_row.indrelid, true), '')
		FROM pg_index index_row
		JOIN pg_class table_relation ON table_relation.oid=index_row.indrelid
		JOIN pg_class index_relation ON index_relation.oid=index_row.indexrelid
		JOIN pg_am access_method ON access_method.oid=index_relation.relam
		JOIN pg_namespace namespace ON namespace.oid=table_relation.relnamespace
		WHERE namespace.nspname=$1
		ORDER BY table_relation.relname, index_relation.relname`, schema)
	if err != nil {
		return legacyPlatformSchemaSignature{}, err
	}
	for rows.Next() {
		var table, name, accessMethod, keys, collations, classes, options, expressions, predicate string
		var unique, primary, exclusion, immediate bool
		var keyAttributes, attributes int
		if err := rows.Scan(
			&table, &name, &accessMethod, &unique, &primary, &exclusion, &immediate,
			&keyAttributes, &attributes, &keys, &collations, &classes, &options, &expressions, &predicate,
		); err != nil {
			rows.Close()
			return legacyPlatformSchemaSignature{}, err
		}
		if _, ok := tables[table]; ok {
			signature.indexes = append(signature.indexes, fmt.Sprintf(
				"%s|%s|%s|%t|%t|%t|%t|%d|%d|%s|%s|%s|%s|%s|%s",
				table, name, accessMethod, unique, primary, exclusion, immediate, keyAttributes, attributes,
				keys, collations, classes, options, expressions, predicate,
			))
		}
	}
	if err := rows.Close(); err != nil {
		return legacyPlatformSchemaSignature{}, err
	}

	rows, err = transaction.QueryContext(ctx, `
		SELECT relation.relname, format_type(sequence_row.seqtypid, NULL), sequence_row.seqstart,
		       sequence_row.seqincrement, sequence_row.seqmax, sequence_row.seqmin,
		       sequence_row.seqcache, sequence_row.seqcycle
		FROM pg_sequence sequence_row
		JOIN pg_class relation ON relation.oid=sequence_row.seqrelid
		JOIN pg_namespace namespace ON namespace.oid=relation.relnamespace
		WHERE namespace.nspname=$1
		ORDER BY relation.relname`, schema)
	if err != nil {
		return legacyPlatformSchemaSignature{}, err
	}
	for rows.Next() {
		var name, dataType string
		var start, increment, maximum, minimum, cache int64
		var cycle bool
		if err := rows.Scan(&name, &dataType, &start, &increment, &maximum, &minimum, &cache, &cycle); err != nil {
			rows.Close()
			return legacyPlatformSchemaSignature{}, err
		}
		if _, ok := sequences[name]; ok {
			signature.sequences = append(signature.sequences, fmt.Sprintf("%s|%s|%d|%d|%d|%d|%d|%t", name, dataType, start, increment, maximum, minimum, cache, cycle))
		}
	}
	if err := rows.Close(); err != nil {
		return legacyPlatformSchemaSignature{}, err
	}
	return signature, nil
}

func (signature legacyPlatformSchemaSignature) mismatch(other legacyPlatformSchemaSignature) string {
	for _, comparison := range []struct {
		name        string
		left, right []string
	}{
		{name: "column", left: signature.columns, right: other.columns},
		{name: "constraint", left: signature.constraints, right: other.constraints},
		{name: "index", left: signature.indexes, right: other.indexes},
		{name: "sequence", left: signature.sequences, right: other.sequences},
	} {
		if strings.Join(comparison.left, "\n") != strings.Join(comparison.right, "\n") {
			return comparison.name
		}
	}
	return ""
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
