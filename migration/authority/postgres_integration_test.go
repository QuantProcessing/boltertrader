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

	type membershipOptions struct {
		admin   bool
		inherit bool
		set     bool
	}
	expectedMemberships := map[string]membershipOptions{
		"bolter_funding_test_migrator":  {set: true},
		"bolter_platform_test_migrator": {set: true},
		"bolter_gateway_test_migrator":  {set: true},
		"bolter_funding_test_writer":    {admin: true},
		"bolter_platform_test":          {admin: true},
		"bolter_platform_test_writer":   {admin: true},
		"bolter_gateway_test":           {admin: true},
		"bolter_gateway_test_writer":    {admin: true},
		"pg_signal_backend":             {set: true},
	}
	membershipRows, err := pool.Query(ctx, `
		SELECT granted.rolname, grantor.rolname,
		       membership.admin_option, membership.inherit_option, membership.set_option
		FROM pg_auth_members membership
		JOIN pg_roles granted ON granted.oid = membership.roleid
		JOIN pg_roles member ON member.oid = membership.member
		JOIN pg_roles grantor ON grantor.oid = membership.grantor
		WHERE member.rolname = 'bolter_platform_test_admin'
		ORDER BY granted.rolname`)
	if err != nil {
		t.Fatal(err)
	}
	defer membershipRows.Close()
	seenMemberships := 0
	for membershipRows.Next() {
		var grantedRole, grantor string
		var observed membershipOptions
		if err := membershipRows.Scan(&grantedRole, &grantor, &observed.admin, &observed.inherit, &observed.set); err != nil {
			t.Fatal(err)
		}
		expected, ok := expectedMemberships[grantedRole]
		if !ok || observed != expected || grantor == "bolter_platform_test_admin" {
			t.Fatalf("unsafe maintenance membership role=%s grantor=%s options=%+v", grantedRole, grantor, observed)
		}
		seenMemberships++
	}
	if err := membershipRows.Err(); err != nil {
		t.Fatal(err)
	}
	if seenMemberships != len(expectedMemberships) {
		t.Fatalf("maintenance memberships found=%d want=%d", seenMemberships, len(expectedMemberships))
	}

	for database, owner := range map[string]string{
		"bolter_funding_test":  "bolter_funding_test_migrator",
		"bolter_platform_test": "bolter_platform_test_migrator",
		"bolter_gateway_test":  "bolter_gateway_test_migrator",
	} {
		var observedOwner string
		var publicConnect, maintenanceConnect bool
		if err := pool.QueryRow(ctx, `
			SELECT pg_get_userbyid(datdba), EXISTS (
				SELECT 1 FROM aclexplode(COALESCE(datacl, acldefault('d', datdba)))
				WHERE grantee=0 AND privilege_type='CONNECT'
			), has_database_privilege('bolter_platform_test_admin', datname, 'CONNECT')
			FROM pg_database WHERE datname=$1`, database).Scan(&observedOwner, &publicConnect, &maintenanceConnect); err != nil {
			t.Fatal(err)
		}
		if observedOwner != owner || publicConnect || maintenanceConnect {
			t.Fatalf("database=%s owner=%s publicConnect=%t maintenanceConnect=%t", database, observedOwner, publicConnect, maintenanceConnect)
		}
	}
}
