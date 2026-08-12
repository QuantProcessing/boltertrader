package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantProcessing/boltertrader/gateway/authority"
)

type AuthorityResult = authority.RuntimeAuthorityResult

func ApplyAuthority(ctx context.Context, database *sql.DB, matrix authority.Matrix) error {
	if err := matrix.Validate(); err != nil {
		return err
	}
	var currentRole, currentDatabase string
	if err := database.QueryRowContext(ctx, `SELECT current_user, current_database()`).Scan(&currentRole, &currentDatabase); err != nil {
		return fmt.Errorf("read migration identity: %w", err)
	}
	if currentRole != matrix.Roles.Migrator {
		return fmt.Errorf("CONNECTED_DATABASE_IDENTITY_MISMATCH")
	}
	tx, err := database.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("begin authority transaction: %w", err)
	}
	defer func() { _ = tx.Rollback() }()
	for _, statement := range databaseAuthorityStatements(matrix, currentDatabase) {
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("apply authority grants: %w", err)
		}
	}
	for _, object := range matrix.Objects {
		if err := grantObject(ctx, tx, matrix, object); err != nil {
			return err
		}
	}
	return tx.Commit()
}

func databaseAuthorityStatements(matrix authority.Matrix, currentDatabase string) []string {
	migratorRole := quoteIdentifier(matrix.Roles.Migrator)
	runtimeRole := quoteIdentifier(matrix.Roles.Runtime)
	maintenanceRole := quoteIdentifier(matrix.Roles.Maintenance)
	writerRole := quoteIdentifier(matrix.Roles.Writer)
	databaseName := quoteIdentifier(currentDatabase)
	return []string{
		`ALTER SCHEMA public OWNER TO ` + migratorRole,
		`REVOKE CONNECT ON DATABASE ` + databaseName + ` FROM PUBLIC`,
		`REVOKE CONNECT ON DATABASE ` + databaseName + ` FROM ` + writerRole,
		`GRANT CONNECT ON DATABASE ` + databaseName + ` TO ` + migratorRole + `, ` + runtimeRole + `, ` + maintenanceRole,
		`REVOKE ALL ON SCHEMA public FROM PUBLIC`,
		`REVOKE ALL ON SCHEMA public FROM ` + runtimeRole + `, ` + writerRole,
		`REVOKE ALL ON ALL TABLES IN SCHEMA public FROM PUBLIC`,
		`REVOKE ALL ON ALL TABLES IN SCHEMA public FROM ` + runtimeRole + `, ` + writerRole,
		`REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM PUBLIC`,
		`REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM ` + runtimeRole + `, ` + writerRole,
		`REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM PUBLIC`,
		`REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM ` + runtimeRole + `, ` + writerRole,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON TABLES FROM PUBLIC`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON TABLES FROM ` + runtimeRole + `, ` + writerRole,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON SEQUENCES FROM PUBLIC`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON SEQUENCES FROM ` + runtimeRole + `, ` + writerRole,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON FUNCTIONS FROM ` + runtimeRole + `, ` + writerRole,
		`GRANT USAGE ON SCHEMA public TO ` + runtimeRole,
		`GRANT USAGE ON SCHEMA public TO ` + writerRole,
	}
}

func VerifyRuntimeAuthority(ctx context.Context, database *sql.DB, matrix authority.Matrix) (AuthorityResult, error) {
	if err := matrix.Validate(); err != nil {
		return AuthorityResult{}, err
	}
	migrationDigest, err := verifyPlatformMigrationLedger(ctx, database, matrix)
	if err != nil {
		return AuthorityResult{}, err
	}
	snapshot, err := authority.CollectPostgresRuntimeCatalog(ctx, sqlCatalogQuerier{database: database}, matrix,
		`SELECT COALESCE(max(version), 0) FROM platform_schema_migrations`, migrationDigest)
	if err != nil {
		return AuthorityResult{}, err
	}
	return authority.VerifyRuntimeCatalog(matrix, snapshot)
}

func verifyPlatformMigrationLedger(ctx context.Context, database *sql.DB, matrix authority.Matrix) (string, error) {
	migrations, err := embeddedPlatformMigrations()
	if err != nil || len(migrations) != matrix.SchemaVersion {
		return "", fmt.Errorf("MIGRATION_DIGEST_MISMATCH")
	}
	rows, err := database.QueryContext(ctx, `SELECT version, checksum FROM platform_schema_migrations ORDER BY version`)
	if err != nil {
		return "", fmt.Errorf("AUTHORITY_CATALOG_QUERY_FAILED: migration-ledger")
	}
	defer rows.Close()
	index := 0
	ledger := make([]authority.MigrationLedgerEntry, 0, len(migrations))
	for rows.Next() {
		var version int
		var checksum string
		if rows.Scan(&version, &checksum) != nil || index >= len(migrations) || migrations[index].version != version || migrations[index].checksum != checksum {
			return "", fmt.Errorf("MIGRATION_DIGEST_MISMATCH")
		}
		ledger = append(ledger, authority.MigrationLedgerEntry{Version: version, Checksum: checksum})
		index++
	}
	if rows.Err() != nil {
		return "", fmt.Errorf("AUTHORITY_CATALOG_QUERY_FAILED: migration-ledger")
	}
	if index != len(migrations) {
		return "", fmt.Errorf("MIGRATION_DIGEST_MISMATCH")
	}
	return authority.VerifyMigrationLedger(matrix, ledger)
}

type sqlCatalogQuerier struct{ database *sql.DB }

func (querier sqlCatalogQuerier) QueryRow(ctx context.Context, query string, args ...any) authority.CatalogRow {
	return querier.database.QueryRowContext(ctx, query, args...)
}

func (querier sqlCatalogQuerier) Query(ctx context.Context, query string, args ...any) (authority.CatalogRows, error) {
	rows, err := querier.database.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return sqlCatalogRows{Rows: rows}, nil
}

type sqlCatalogRows struct{ *sql.Rows }

func (rows sqlCatalogRows) Close() { _ = rows.Rows.Close() }

func grantObject(ctx context.Context, tx *sql.Tx, matrix authority.Matrix, object authority.Object) error {
	kind := map[authority.ObjectKind]string{authority.Table: "TABLE", authority.Sequence: "SEQUENCE", authority.Routine: "FUNCTION"}[object.Kind]
	name := quoteIdentifier(object.Schema) + "." + quoteObject(object)
	for _, grant := range []struct {
		role       string
		privileges []string
	}{{matrix.Roles.Runtime, object.Runtime}, {matrix.Roles.Writer, object.Writer}} {
		if len(grant.privileges) == 0 {
			continue
		}
		privileges := append([]string(nil), grant.privileges...)
		sort.Strings(privileges)
		statement := "GRANT " + strings.Join(privileges, ", ") + " ON " + kind + " " + name + " TO " + quoteIdentifier(grant.role)
		if _, err := tx.ExecContext(ctx, statement); err != nil {
			return fmt.Errorf("grant %s authority: %w", object.Name, err)
		}
	}
	return nil
}

func quoteObject(object authority.Object) string {
	if object.Kind != authority.Routine {
		return quoteIdentifier(object.Name)
	}
	open := strings.IndexByte(object.Name, '(')
	if open < 1 || !strings.HasSuffix(object.Name, ")") {
		panic("invalid routine identity")
	}
	return quoteIdentifier(object.Name[:open]) + object.Name[open:]
}

func quoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
