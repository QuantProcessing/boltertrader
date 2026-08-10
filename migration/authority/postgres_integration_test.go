package authority_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

func TestAuthorityMatrixRealPostgres(t *testing.T) {
	if os.Getenv("BT_G010_POSTGRES_ACCEPT") != "1" {
		t.Skip("BT_G010_POSTGRES_ACCEPT=1 is required for local PostgreSQL authority verification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, "service=platform_maintenance_test dbname=postgres")
	if err != nil {
		t.Fatal("connect through platform_maintenance_test service")
	}
	defer pool.Close()

	rows, err := pool.Query(ctx, `
		SELECT r.rolname, r.rolcanlogin, r.rolcreatedb, r.rolcreaterole, r.rolinherit
		FROM pg_roles r
		WHERE r.rolname = ANY($1)
		ORDER BY r.rolname`, []string{
		"bolter_funding_test_migrator", "bolter_platform_test_migrator", "bolter_gateway_test_migrator",
		"bolter_platform_test", "bolter_gateway_test", "bolter_platform_test_admin",
	})
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var name string
		var login, createdb, createrole, inherit bool
		if err := rows.Scan(&name, &login, &createdb, &createrole, &inherit); err != nil {
			t.Fatal(err)
		}
		seen++
		switch name {
		case "bolter_platform_test_admin":
			if !login || !createdb || !createrole || inherit {
				t.Fatalf("maintenance attributes are unsafe")
			}
		case "bolter_platform_test", "bolter_gateway_test":
			if !login || createdb || createrole || !inherit {
				t.Fatalf("runtime %s attributes are unsafe", name)
			}
		default:
			if login || createdb || createrole || inherit {
				t.Fatalf("migrator %s attributes are unsafe", name)
			}
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen != 6 {
		t.Fatalf("authority roles found=%d want=6", seen)
	}

	for database, owner := range map[string]string{
		"bolter_funding_test":  "bolter_funding_test_migrator",
		"bolter_platform_test": "bolter_platform_test_migrator",
		"bolter_gateway_test":  "bolter_gateway_test_migrator",
	} {
		var observedOwner string
		var publicConnect bool
		if err := pool.QueryRow(ctx, `
			SELECT pg_get_userbyid(datdba), EXISTS (
				SELECT 1 FROM aclexplode(COALESCE(datacl, acldefault('d', datdba)))
				WHERE grantee=0 AND privilege_type='CONNECT'
			)
			FROM pg_database WHERE datname=$1`, database).Scan(&observedOwner, &publicConnect); err != nil {
			t.Fatal(err)
		}
		if observedOwner != owner || publicConnect {
			t.Fatalf("database=%s owner=%s publicConnect=%t", database, observedOwner, publicConnect)
		}
	}
}
