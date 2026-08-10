package postgres

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/QuantProcessing/boltertrader/migration/authority"
	"github.com/jackc/pgx/v5/pgxpool"
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

func ApplyAuthority(ctx context.Context, pool *pgxpool.Pool, matrix authority.Matrix) error {
	if err := matrix.Validate(); err != nil {
		return err
	}
	var currentRole string
	if err := pool.QueryRow(ctx, `SELECT current_user`).Scan(&currentRole); err != nil {
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
	statements := []string{
		`REVOKE ALL ON SCHEMA public FROM PUBLIC`,
		`REVOKE ALL ON ALL TABLES IN SCHEMA public FROM PUBLIC`,
		`REVOKE ALL ON ALL SEQUENCES IN SCHEMA public FROM PUBLIC`,
		`REVOKE ALL ON ALL FUNCTIONS IN SCHEMA public FROM PUBLIC`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON TABLES FROM PUBLIC`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE ALL ON SEQUENCES FROM PUBLIC`,
		`ALTER DEFAULT PRIVILEGES IN SCHEMA public REVOKE EXECUTE ON FUNCTIONS FROM PUBLIC`,
		`GRANT USAGE ON SCHEMA public TO ` + gatewayQuoteIdentifier(matrix.Roles.Runtime),
		`GRANT USAGE ON SCHEMA public TO ` + gatewayQuoteIdentifier(matrix.Roles.Writer),
	}
	for _, statement := range statements {
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

func VerifyRuntimeAuthority(ctx context.Context, pool *pgxpool.Pool, matrix authority.Matrix) (AuthorityResult, error) {
	result := AuthorityResult{MatrixDigest: matrix.Digest()}
	if err := matrix.Validate(); err != nil {
		return result, err
	}
	if err := pool.QueryRow(ctx, `SELECT current_user, rolinherit FROM pg_roles WHERE rolname=current_user`).Scan(&result.RuntimeRole, &result.RuntimeInherits); err != nil {
		return result, redactErr("verify runtime identity", err)
	}
	if result.RuntimeRole != matrix.Roles.Runtime || !result.RuntimeInherits {
		return result, fmt.Errorf("RUNTIME_IDENTITY_MISMATCH")
	}
	if err := pool.QueryRow(ctx, `SELECT pg_has_role(current_user, $1, 'MEMBER')`, matrix.Roles.Writer).Scan(&result.WriterMembership); err != nil {
		return result, redactErr("verify writer membership", err)
	}
	if err := pool.QueryRow(ctx, `SELECT COALESCE(max(version),0) FROM schema_migrations`).Scan(&result.SchemaVersion); err != nil {
		return result, redactErr("verify schema version", err)
	}
	if result.SchemaVersion != matrix.SchemaVersion {
		return result, fmt.Errorf("SCHEMA_VERSION_MISMATCH")
	}
	rows, err := pool.Query(ctx, `SELECT c.relname FROM pg_class c JOIN pg_namespace n ON n.oid=c.relnamespace WHERE n.nspname='public' AND c.relkind IN ('r','S') AND pg_get_userbyid(c.relowner)=current_user ORDER BY c.relname`)
	if err != nil {
		return result, redactErr("verify object ownership", err)
	}
	defer rows.Close()
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return result, redactErr("scan object ownership", err)
		}
		result.OwnershipViolations = append(result.OwnershipViolations, name)
	}
	if len(result.OwnershipViolations) != 0 {
		return result, fmt.Errorf("RUNTIME_OWNS_CANONICAL_OBJECT")
	}
	result.MigrationDigest = matrix.MigrationDigest
	return result, nil
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
