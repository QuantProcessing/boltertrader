package authority

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"fmt"
	"math"
)

const migrationLedgerDigestDomain = "boltertrader:migration-ledger:v1"

type MigrationLedgerEntry struct {
	Version  int
	Checksum string
}

func CanonicalMigrationLedgerDigest(entries []MigrationLedgerEntry) (string, error) {
	if len(entries) == 0 || len(entries) > math.MaxUint32 {
		return "", fmt.Errorf("SCHEMA_VERSION_MISMATCH")
	}
	digest := sha256.New()
	_, _ = digest.Write([]byte(migrationLedgerDigestDomain))
	var encoded [4]byte
	binary.BigEndian.PutUint32(encoded[:], uint32(len(entries)))
	_, _ = digest.Write(encoded[:])
	for index, entry := range entries {
		if entry.Version != index+1 {
			return "", fmt.Errorf("SCHEMA_VERSION_MISMATCH")
		}
		checksum, err := hex.DecodeString(entry.Checksum)
		if err != nil || len(checksum) != sha256.Size || entry.Checksum != hex.EncodeToString(checksum) {
			return "", fmt.Errorf("MIGRATION_DIGEST_MISMATCH")
		}
		binary.BigEndian.PutUint32(encoded[:], uint32(entry.Version))
		_, _ = digest.Write(encoded[:])
		_, _ = digest.Write(checksum)
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

func VerifyMigrationLedger(matrix Matrix, entries []MigrationLedgerEntry) (string, error) {
	if err := matrix.Validate(); err != nil {
		return "", err
	}
	if len(entries) != matrix.SchemaVersion {
		return "", fmt.Errorf("SCHEMA_VERSION_MISMATCH")
	}
	digest, err := CanonicalMigrationLedgerDigest(entries)
	if err != nil {
		return "", err
	}
	if digest != matrix.MigrationDigest {
		return "", fmt.Errorf("MIGRATION_DIGEST_MISMATCH")
	}
	return digest, nil
}
