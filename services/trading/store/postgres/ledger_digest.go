package postgres

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/hex"
	"fmt"
	"sort"
	"strconv"
)

// LedgerDigest reports the highest applied version and a digest over the whole
// ledger — every version and the checksum of the file applied under it.
//
// The version alone is not enough to compare two deployments. Two databases can
// both say "version 3" while one of them applied a file that has since been
// edited, and the mismatch would only surface the next time this binary
// verified checksums. The digest makes that visible at the moment of migration,
// which is when someone is still watching.
func LedgerDigest(ctx context.Context, database *sql.DB) (int, string, error) {
	connection, err := database.Conn(ctx)
	if err != nil {
		return 0, "", fmt.Errorf("acquire trading ledger connection: %w", err)
	}
	defer connection.Close()
	applied, err := appliedMigrations(ctx, connection)
	if err != nil {
		return 0, "", err
	}
	versions := make([]int, 0, len(applied))
	for version := range applied {
		versions = append(versions, version)
	}
	sort.Ints(versions)

	digest := sha256.New()
	highest := 0
	for _, version := range versions {
		fmt.Fprintf(digest, "%s:%s\n", strconv.Itoa(version), applied[version])
		highest = version
	}
	return highest, hex.EncodeToString(digest.Sum(nil)), nil
}
