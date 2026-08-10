package authority_test

import (
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/migration/authority"
)

func TestVerifyMigrationLedgerRejectsChecksumVersionAndOrderDrift(t *testing.T) {
	valid := []authority.MigrationLedgerEntry{
		{Version: 1, Checksum: strings.Repeat("a", 64)},
		{Version: 2, Checksum: strings.Repeat("b", 64)},
	}
	digest, err := authority.CanonicalMigrationLedgerDigest(valid)
	if err != nil {
		t.Fatal(err)
	}
	matrix := authority.PlatformV15()
	matrix.SchemaVersion = len(valid)
	matrix.MigrationDigest = digest
	if got, err := authority.VerifyMigrationLedger(matrix, valid); err != nil || got != digest {
		t.Fatalf("valid ledger digest=%q err=%v", got, err)
	}

	tests := []struct {
		name    string
		entries []authority.MigrationLedgerEntry
		want    string
	}{
		{name: "checksum", entries: []authority.MigrationLedgerEntry{{Version: 1, Checksum: strings.Repeat("c", 64)}, valid[1]}, want: "MIGRATION_DIGEST_MISMATCH"},
		{name: "version", entries: []authority.MigrationLedgerEntry{valid[0], {Version: 3, Checksum: valid[1].Checksum}}, want: "SCHEMA_VERSION_MISMATCH"},
		{name: "order", entries: []authority.MigrationLedgerEntry{valid[1], valid[0]}, want: "SCHEMA_VERSION_MISMATCH"},
		{name: "missing", entries: valid[:1], want: "SCHEMA_VERSION_MISMATCH"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if _, err := authority.VerifyMigrationLedger(matrix, test.entries); err == nil || !strings.Contains(err.Error(), test.want) {
				t.Fatalf("error=%v want=%s", err, test.want)
			}
		})
	}
}

func TestCanonicalMigrationLedgerDigestUsesTypedBoundaries(t *testing.T) {
	left := []authority.MigrationLedgerEntry{{Version: 1, Checksum: strings.Repeat("0", 63) + "1"}}
	right := []authority.MigrationLedgerEntry{{Version: 1, Checksum: "1" + strings.Repeat("0", 63)}}
	leftDigest, leftErr := authority.CanonicalMigrationLedgerDigest(left)
	rightDigest, rightErr := authority.CanonicalMigrationLedgerDigest(right)
	if leftErr != nil || rightErr != nil || leftDigest == rightDigest {
		t.Fatalf("left=%q/%v right=%q/%v", leftDigest, leftErr, rightDigest, rightErr)
	}
}
