package authority_test

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

// roleAttributes are the two properties that decide whether a role works in
// this stack. login says whether a process may authenticate as it; inherit says
// whether it silently receives the privileges of roles it belongs to. Both are
// read back for the runtime role by CollectPostgresRuntimeCatalog, and both are
// the difference between a stack that serves and one that fails several layers
// away from the cause.
type roleAttributes struct {
	login   bool
	inherit bool
}

// stackRoles is docker/postgres/initdb/010-databases-and-roles.sql restated as
// an expectation. Every entry is load-bearing for a check some process makes.
var stackRoles = map[string]roleAttributes{
	// Serving identities. They log in, and they must inherit: the gateway
	// runtime holds no grant of its own, only what its writer membership
	// carries.
	"bolter_gateway":  {login: true, inherit: true},
	"bolter_trading":  {login: true, inherit: true},
	"bolter_authedge": {login: true, inherit: true},
	// The migrator owns the gateway database, which is what leaves the serving
	// role owning nothing it could drop. It logs in because nothing in this
	// repository issues SET ROLE — cmd/gateway-migrate has to arrive as this
	// role rather than become it.
	"bolter_gateway_migrator": {login: true, inherit: true},
	// A privilege container, not a connection. VerifyRuntimeCatalog fails when
	// it finds the writer able to reach the database in its own right.
	"bolter_gateway_writer": {login: false, inherit: true},
	// Exists only to receive the CONNECT grant ApplyAuthority issues, which
	// would error against a missing role.
	"bolter_platform_admin": {login: false, inherit: true},
}

// TestStackClusterTopologyRealPostgres checks the cluster the stack is started
// against, rather than the Go that talks to it. Everything asserted here is
// created by the initdb script above and then depended on by a specific
// refusal: the gateway reads its role attributes and memberships back through
// VerifyRuntimeCatalog at every start, and cmd/trading and cmd/authedge each
// pin their database and role name in config.
//
// This matters because the initdb script is triggered by an empty data
// directory, not by a version check. A named volume surviving an edit to that
// file means it never runs, and the cluster then starts cleanly and fails deep
// inside a migration. This test, and the equivalent check in `make doctor`,
// exist to name that failure at the cluster instead.
//
// Gated because it needs a real cluster provisioned that way: point PGSERVICEFILE
// at the stack's service file and set BT_G010_POSTGRES_ACCEPT=1. It only reads,
// so it is safe against a running stack. It does assume the three migrators have
// already run, since the gateway database's ACL is applied by ApplyAuthority
// rather than at init.
func TestStackClusterTopologyRealPostgres(t *testing.T) {
	if testing.Short() {
		t.Skip("cluster verification requires explicit non-short PostgreSQL tests")
	}
	if os.Getenv("BT_G010_POSTGRES_ACCEPT") != "1" {
		t.Skip("BT_G010_POSTGRES_ACCEPT=1 is required for local PostgreSQL authority verification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	// Through the migrator's service entry, because it is the only role in the
	// stack that both logs in and has no serving duty to disturb. The catalogs
	// read below are cluster-wide, so which database it lands in does not
	// matter — but the service file supplies no dbname, so one is named here.
	pool, err := pgxpool.New(ctx, "service=gateway_test dbname=bolter_gateway")
	if err != nil {
		t.Fatalf("connect through the gateway_test service: %v", err)
	}
	defer pool.Close()

	expectedNames := make([]string, 0, len(stackRoles))
	for name := range stackRoles {
		expectedNames = append(expectedNames, name)
	}
	rows, err := pool.Query(ctx, `
		SELECT rolname, rolcanlogin, rolinherit, rolcreatedb, rolcreaterole, rolsuper
		FROM pg_roles WHERE rolname = ANY($1) ORDER BY rolname`, expectedNames)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	seen := 0
	for rows.Next() {
		var name string
		var observed roleAttributes
		var createdb, createrole, superuser bool
		if err := rows.Scan(&name, &observed.login, &observed.inherit, &createdb, &createrole, &superuser); err != nil {
			t.Fatal(err)
		}
		seen++
		if observed != stackRoles[name] {
			t.Fatalf("role %s attributes=%+v want=%+v", name, observed, stackRoles[name])
		}
		// No role in this stack creates databases, creates roles, or is a
		// superuser. The last one is not redundant with the ownership checks
		// below: every REVOKE the authority pass applies is ignored for a
		// superuser, so a superuser runtime would make the whole split
		// decorative while VerifyRuntimeCatalog still reported it intact.
		if createdb || createrole || superuser {
			t.Fatalf("role %s holds cluster-wide rights: createdb=%t createrole=%t superuser=%t", name, createdb, createrole, superuser)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if seen != len(stackRoles) {
		t.Fatalf("stack roles found=%d want=%d", seen, len(stackRoles))
	}

	// The runtime reaches every privilege it has through this membership.
	// Losing it does not produce a connection error; it produces a permission
	// denial on the first write, which is a bad thing to learn from an order.
	var writerMembership, runtimeInherits bool
	if err := pool.QueryRow(ctx, `
		SELECT pg_has_role('bolter_gateway', 'bolter_gateway_writer', 'MEMBER'),
		       (SELECT rolinherit FROM pg_roles WHERE rolname = 'bolter_gateway')`).Scan(&writerMembership, &runtimeInherits); err != nil {
		t.Fatal(err)
	}
	if !writerMembership || !runtimeInherits {
		t.Fatalf("gateway runtime writerMembership=%t inherits=%t", writerMembership, runtimeInherits)
	}

	// The maintenance role is granted nothing. It is a CONNECT recipient and
	// nothing else, and a role no process authenticates as is exactly the one
	// whose accumulated privileges nobody would notice.
	var maintenanceMemberships int
	if err := pool.QueryRow(ctx, `
		SELECT count(*) FROM pg_auth_members membership
		JOIN pg_roles member ON member.oid = membership.member
		WHERE member.rolname = 'bolter_platform_admin'`).Scan(&maintenanceMemberships); err != nil {
		t.Fatal(err)
	}
	if maintenanceMemberships != 0 {
		t.Fatalf("maintenance role holds %d memberships, want 0", maintenanceMemberships)
	}

	// One database per service, none reachable by PUBLIC. The gateway's is
	// owned by a role it does not serve as, which is what stops a compromised
	// serving role from dropping the tables underneath itself; trading and the
	// auth edge own their own, since neither verifies an authority matrix and a
	// split nothing checks would drift in silence.
	for database, owner := range map[string]string{
		"bolter_gateway":  "bolter_gateway_migrator",
		"bolter_trading":  "bolter_trading",
		"bolter_authedge": "bolter_authedge",
	} {
		var observedOwner string
		var publicConnect bool
		if err := pool.QueryRow(ctx, `
			SELECT pg_get_userbyid(datdba), EXISTS (
				SELECT 1 FROM aclexplode(COALESCE(datacl, acldefault('d', datdba)))
				WHERE grantee = 0 AND privilege_type = 'CONNECT'
			)
			FROM pg_database WHERE datname = $1`, database).Scan(&observedOwner, &publicConnect); err != nil {
			t.Fatalf("inspect database %s: %v", database, err)
		}
		if observedOwner != owner || publicConnect {
			t.Fatalf("database=%s owner=%s want=%s publicConnect=%t", database, observedOwner, owner, publicConnect)
		}
	}
}
