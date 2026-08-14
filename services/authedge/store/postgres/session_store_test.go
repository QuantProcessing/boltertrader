package postgres

import (
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/services/authedge/session"
)

func TestStoresSatisfyTheSessionContracts(t *testing.T) {
	var _ session.Store = (*SessionStore)(nil)
	var _ session.IdentityStore = (*IdentityStore)(nil)
}

// The auth edge holds the only credential material in the system, so what its
// schema may and may not name is a contract rather than a style preference. A
// column carrying a password in the clear, or an exchange credential of any
// shape, would put material into backups and replicas that the design says
// never leaves the gateway.
func TestMigrationsStoreOnlyDigestsAndHashes(t *testing.T) {
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	if len(migrations) == 0 {
		t.Fatal("no embedded migrations")
	}
	var combined strings.Builder
	for _, migration := range migrations {
		combined.WriteString(migration.sql)
	}
	schema := strings.ToLower(combined.String())
	for _, required := range []string{
		"create table schema_migrations",
		"create table identities",
		"create table sessions",
		"password_hash",
		"token_digest",
		"csrf_digest",
		"expires_at",
		"check (mode in ('test', 'live'))",
	} {
		if !strings.Contains(schema, required) {
			t.Errorf("schema missing %q", required)
		}
	}
	for _, forbidden := range []string{
		"secret_material", "api_key", "api_secret", "private_key",
		"passphrase", "raw_secret", "credential_bundle", "secret_ref",
	} {
		if strings.Contains(schema, forbidden) {
			t.Errorf("schema contains forbidden secret column %q", forbidden)
		}
	}
	// Raw session and CSRF tokens exist only in the browser. A column that
	// stored them would turn a leaked table into a set of usable cookies.
	for _, forbidden := range []string{"token text", "csrf_token", "session_token"} {
		if strings.Contains(schema, forbidden) {
			t.Errorf("schema stores a raw token as %q", forbidden)
		}
	}
}
