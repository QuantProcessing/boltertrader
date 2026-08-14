package postgres

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantProcessing/boltertrader/gateway/authority"
	"github.com/jackc/pgx/v5/pgxpool"
)

type AuthorityResult = authority.RuntimeAuthorityResult

func ApplyAuthority(ctx context.Context, pool *pgxpool.Pool, matrix authority.Matrix) error {
	if err := matrix.Validate(); err != nil {
		return err
	}
	var currentRole, currentDatabase string
	if err := pool.QueryRow(ctx, `SELECT current_user, current_database()`).Scan(&currentRole, &currentDatabase); err != nil {
		return redactErr("read migration identity", err)
	}
	if currentRole != matrix.Roles.Migrator {
		return fmt.Errorf("CONNECTED_DATABASE_IDENTITY_MISMATCH")
	}
	tx, err := pool.Begin(ctx)
	if err != nil {
		return redactErr("begin authority transaction", err)
	}
	defer func() { _ = tx.Rollback(ctx) }()
	for _, statement := range databaseAuthorityStatements(matrix, currentDatabase) {
		if _, err := tx.Exec(ctx, statement); err != nil {
			return redactErr("apply authority grants", err)
		}
	}
	for _, object := range matrix.Objects {
		kind := map[authority.ObjectKind]string{authority.Table: "TABLE", authority.Sequence: "SEQUENCE", authority.Routine: "FUNCTION"}[object.Kind]
		name := gatewayQuoteIdentifier(object.Schema) + "." + gatewayQuoteObject(object)
		for _, grant := range []struct {
			role       string
			privileges []string
		}{{matrix.Roles.Runtime, object.Runtime}, {matrix.Roles.Writer, object.Writer}} {
			if len(grant.privileges) == 0 {
				continue
			}
			privileges := append([]string(nil), grant.privileges...)
			sort.Strings(privileges)
			if _, err := tx.Exec(ctx, "GRANT "+strings.Join(privileges, ", ")+" ON "+kind+" "+name+" TO "+gatewayQuoteIdentifier(grant.role)); err != nil {
				return redactErr("grant object authority", err)
			}
		}
	}
	return tx.Commit(ctx)
}

func databaseAuthorityStatements(matrix authority.Matrix, currentDatabase string) []string {
	migratorRole := gatewayQuoteIdentifier(matrix.Roles.Migrator)
	runtimeRole := gatewayQuoteIdentifier(matrix.Roles.Runtime)
	maintenanceRole := gatewayQuoteIdentifier(matrix.Roles.Maintenance)
	writerRole := gatewayQuoteIdentifier(matrix.Roles.Writer)
	databaseName := gatewayQuoteIdentifier(currentDatabase)
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

func VerifyRuntimeAuthority(ctx context.Context, pool *pgxpool.Pool, matrix authority.Matrix) (AuthorityResult, error) {
	if err := matrix.Validate(); err != nil {
		return AuthorityResult{}, err
	}
	migrationDigest, err := verifyGatewayMigrationLedger(ctx, pool, matrix)
	if err != nil {
		return AuthorityResult{}, err
	}
	snapshot, err := authority.CollectPostgresRuntimeCatalog(ctx, pgxCatalogQuerier{pool: pool}, matrix,
		`SELECT COALESCE(max(version),0) FROM schema_migrations`, migrationDigest)
	if err != nil {
		return AuthorityResult{}, err
	}
	return authority.VerifyRuntimeCatalog(matrix, snapshot)
}

func verifyGatewayMigrationLedger(ctx context.Context, pool *pgxpool.Pool, matrix authority.Matrix) (string, error) {
	migrations, err := embeddedMigrations()
	if err != nil || len(migrations) != matrix.SchemaVersion {
		return "", fmt.Errorf("MIGRATION_DIGEST_MISMATCH")
	}
	rows, err := pool.Query(ctx, `SELECT version, checksum FROM schema_migrations ORDER BY version`)
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

type pgxCatalogQuerier struct{ pool *pgxpool.Pool }

func (querier pgxCatalogQuerier) QueryRow(ctx context.Context, query string, args ...any) authority.CatalogRow {
	return querier.pool.QueryRow(ctx, query, args...)
}

func (querier pgxCatalogQuerier) Query(ctx context.Context, query string, args ...any) (authority.CatalogRows, error) {
	return querier.pool.Query(ctx, query, args...)
}

func gatewayQuoteObject(object authority.Object) string {
	if object.Kind != authority.Routine {
		return gatewayQuoteIdentifier(object.Name)
	}
	open := strings.IndexByte(object.Name, '(')
	return gatewayQuoteIdentifier(object.Name[:open]) + object.Name[open:]
}

func gatewayQuoteIdentifier(value string) string {
	return `"` + strings.ReplaceAll(value, `"`, `""`) + `"`
}
