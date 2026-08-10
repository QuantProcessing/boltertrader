package postgres

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/migration/authority"
)

func TestGatewayAuthorityMatrixIsValid(t *testing.T) {
	matrix := authority.GatewayV6()
	if err := matrix.Validate(); err != nil {
		t.Fatal(err)
	}
	if matrix.Roles.Migrator != "bolter_gateway_test_migrator" || matrix.Roles.Runtime != "bolter_gateway_test" {
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
	if matrix := authority.GatewayV6(); digest != matrix.MigrationDigest || len(entries) != matrix.SchemaVersion {
		t.Fatalf("embedded Gateway ledger digest=%q entries=%d matrix=%+v", digest, len(entries), matrix)
	}
}
