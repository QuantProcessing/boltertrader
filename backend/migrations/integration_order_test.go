package migrations_test

import (
	"os"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestPlatformMigrationsAreOrderedAcrossLaneOwnership(t *testing.T) {
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, entry := range entries {
		if !entry.IsDir() && strings.HasSuffix(entry.Name(), ".sql") {
			names = append(names, entry.Name())
		}
	}
	sort.Strings(names)
	want := []string{"0001_init.sql", "0002_identity_accounts_audit.sql", "0003_market_realtime.sql", "0004_trading_core.sql"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("migration order=%v want=%v", names, want)
	}
}
