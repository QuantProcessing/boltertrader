package postgres

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantProcessing/boltertrader/migration/authority"
)

type AuthorityResult struct {
	SchemaVersion       int
	MigrationDigest     string
	MatrixDigest        string
	RuntimeRole         string
	RuntimeInherits     bool
	WriterMembership    bool
	OwnershipViolations []string
	PrivilegeViolations []string
}

func ApplyAuthority(ctx context.Context, database *sql.DB, matrix authority.Matrix) error {
	if err := matrix.Validate(); err != nil {
		return err
	}
	var currentRole string
	if err := database.QueryRowContext(ctx, `SELECT current_user`).Scan(&currentRole); err != nil {
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
	statements := []string{
		`REVOKE ALL ON SCHEMA public FROM PUBLIC`,
		`REVOKE ALL ON ALL TABLES IN SCHEMA public FROM PUBLIC`,
		`REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM PUBLIC`,
		`REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM PUBLIC`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON TABLES FROM PUBLIC`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON SEQUENCES FROM PUBLIC`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC`,
		`GRANT USAGE ON SCHEMA public TO ` + quoteIdentifier(matrix.Roles.Runtime),
		`GRANT USAGE ON SCHEMA public TO ` + quoteIdentifier(matrix.Roles.Writer),
	}
	for _, statement := range statements {
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

func VerifyRuntimeAuthority(ctx context.Context, database *sql.DB, matrix authority.Matrix) (AuthorityResult, error) {
	result := AuthorityResult{MatrixDigest: matrix.Digest()}
	if err := matrix.Validate(); err != nil {
		return result, err
	}
	if err := database.QueryRowContext(ctx, `SELECT current_user, rolinherit FROM pg_roles WHERE rolname = current_user`).Scan(&result.RuntimeRole, &result.RuntimeInherits); err != nil {
		return result, fmt.Errorf("verify runtime identity: %w", err)
	}
	if result.RuntimeRole != matrix.Roles.Runtime || !result.RuntimeInherits {
		return result, fmt.Errorf("RUNTIME_IDENTITY_MISMATCH")
	}
	if err := database.QueryRowContext(ctx, `SELECT pg_has_role(current_user, $1, 'MEMBER')`, matrix.Roles.Writer).Scan(&result.WriterMembership); err != nil {
		return result, fmt.Errorf("verify writer membership: %w", err)
	}
	if err := database.QueryRowContext(ctx, `SELECT COALESCE(max(version), 0) FROM platform_schema_migrations`).Scan(&result.SchemaVersion); err != nil {
		return result, fmt.Errorf("verify schema version: %w", err)
	}
	if result.SchemaVersion != matrix.SchemaVersion {
		return result, fmt.Errorf("SCHEMA_VERSION_MISMATCH")
	}
	rows, err := database.QueryContext(ctx, `
		SELECT c.relname
		FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace
		WHERE n.nspname='public' AND c.relkind IN ('r','S') AND pg_get_userbyid(c.relowner)=current_user
		ORDER BY c.relname`)
	if err != nil {
		return result, fmt.Errorf("verify object ownership: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return result, err
		}
		result.OwnershipViolations = append(result.OwnershipViolations, name)
	}
	if len(result.OwnershipViolations) != 0 {
		return result, fmt.Errorf("RUNTIME_OWNS_CANONICAL_OBJECT")
	}
	result.MigrationDigest = matrix.MigrationDigest
	return result, nil
}

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
