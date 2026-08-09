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
	want := []string{"0001_init.sql", "0002_identity_accounts_audit.sql", "0003_market_realtime.sql", "0004_trading_core.sql", "0005_funding_work_store.sql", "0006_portfolio_fills.sql", "0007_command_execution_evidence.sql", "0008_funding_browser_producer.sql", "0009_funding_work_integrity.sql", "0010_principal_scoped_work_intents.sql", "0011_currency_safe_accounting.sql", "0012_portfolio_mark_observation.sql", "0013_principal_scoped_accounting.sql", "0014_account_routes_credentials.sql", "0015_rotation_cleanup_proof.sql"}
	if !reflect.DeepEqual(names, want) {
		t.Fatalf("migration order=%v want=%v", names, want)
	}
}
