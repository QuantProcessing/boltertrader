package postgres

import (
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/migration/authority"
)

func TestPlatformAuthorityRestoresMigratorRuntimeAndMaintenanceConnect(t *testing.T) {
	matrix := authority.PlatformV15()
	statements := strings.Join(databaseAuthorityStatements(matrix, "bolter_platform_test"), "\n")
	for _, want := range []string{
		`REVOKE CONNECT ON DATABASE "bolter_platform_test" FROM PUBLIC`,
		`REVOKE CONNECT ON DATABASE "bolter_platform_test" FROM "bolter_platform_test_writer"`,
		`GRANT CONNECT ON DATABASE "bolter_platform_test" TO "bolter_platform_test_migrator", "bolter_platform_test", "bolter_platform_test_admin"`,
	} {
		if !strings.Contains(statements, want) {
			t.Fatalf("authority statements missing %q: %s", want, statements)
		}
	}
}

func TestPlatformAuthorityMatrixIsValid(t *testing.T) {
	matrix := authority.PlatformV15()
	if err := matrix.Validate(); err != nil {
		t.Fatal(err)
	}
	if matrix.Roles.Migrator != "bolter_platform_test_migrator" || matrix.Roles.Runtime != "bolter_platform_test" {
		t.Fatalf("roles=%+v", matrix.Roles)
	}
}

func TestPlatformAuthorityResultExposesEffectiveCatalogFacts(t *testing.T) {
	result := AuthorityResult{
		FullCapabilityGrants: true, EffectiveTablePrivileges: true,
		EffectiveSequencePrivileges: true, EffectiveRoutinePrivileges: true,
	}
	if !result.FullCapabilityGrants || !result.EffectiveTablePrivileges || !result.EffectiveSequencePrivileges || !result.EffectiveRoutinePrivileges {
		t.Fatalf("result=%+v", result)
	}
}

func TestEmbeddedPlatformLedgerDigestMatchesAuthorityMatrix(t *testing.T) {
	migrations, err := embeddedPlatformMigrations()
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
	if matrix := authority.PlatformV15(); digest != matrix.MigrationDigest || len(entries) != matrix.SchemaVersion {
		t.Fatalf("embedded Platform ledger digest=%q entries=%d matrix=%+v", digest, len(entries), matrix)
	}
}
