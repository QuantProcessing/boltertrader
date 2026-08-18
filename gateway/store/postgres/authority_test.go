package postgres

import (
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/gateway/authority"
)

func TestGatewayAuthorityRestoresMigratorRuntimeAndMaintenanceConnect(t *testing.T) {
	matrix := authority.GatewayV8()
	statements := strings.Join(databaseAuthorityStatements(matrix, "bolter_gateway"), "\n")
	for _, want := range []string{
		`ALTER SCHEMA public OWNER TO "bolter_gateway_migrator"`,
		`REVOKE CONNECT ON DATABASE "bolter_gateway" FROM PUBLIC`,
		`REVOKE CONNECT ON DATABASE "bolter_gateway" FROM "bolter_gateway_writer"`,
		`GRANT CONNECT ON DATABASE "bolter_gateway" TO "bolter_gateway_migrator", "bolter_gateway", "bolter_platform_admin"`,
	} {
		if !strings.Contains(statements, want) {
			t.Fatalf("authority statements missing %q: %s", want, statements)
		}
	}
}

func TestGatewayAuthorityMatrixIsValid(t *testing.T) {
	matrix := authority.GatewayV8()
	if err := matrix.Validate(); err != nil {
		t.Fatal(err)
	}
	if matrix.Roles.Migrator != "bolter_gateway_migrator" || matrix.Roles.Runtime != "bolter_gateway" {
		t.Fatalf("roles=%+v", matrix.Roles)
	}
}

func TestGatewayAuthorityResultExposesEffectiveCatalogFacts(t *testing.T) {
	result := AuthorityResult{
		FullCapabilityGrants: true, EffectiveTablePrivileges: true,
		EffectiveSequencePrivileges: true, EffectiveRoutinePrivileges: true,
	}
	if !result.FullCapabilityGrants || !result.EffectiveTablePrivileges || !result.EffectiveSequencePrivileges || !result.EffectiveRoutinePrivileges {
		t.Fatalf("result=%+v", result)
	}
}

func TestEmbeddedGatewayLedgerDigestMatchesAuthorityMatrix(t *testing.T) {
	migrations, err := embeddedMigrations()
	if err != nil {
		t.Fatal(err)
	}
	entries := make([]authority.MigrationLedgerEntry, len(migrations))
	for index, migration := range migrations {
		entries[index] = authority.MigrationLedgerEntry{Version: migration.version, Checksum: migration.checksum}
	}
	digest, err := authority.CanonicalMigrationLedgerDigest(entries)
	if err != nil {
		t.Fatal(err)
	}
	if matrix := authority.GatewayV8(); digest != matrix.MigrationDigest || len(entries) != matrix.SchemaVersion {
		t.Fatalf("embedded Gateway ledger digest=%q entries=%d matrix=%+v", digest, len(entries), matrix)
	}
}
