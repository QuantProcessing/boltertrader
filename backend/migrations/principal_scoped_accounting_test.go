package migrations_test

import (
	"os"
	"strings"
	"testing"
)

func TestPrincipalScopedAccountingMigrationBackfillsTruthfullyAndFailsClosed(t *testing.T) {
	payload, err := os.ReadFile("0013_principal_scoped_accounting.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(payload)
	for _, required := range []string{
		"FROM platform_commands AS command",
		"count(DISTINCT command.principal_id) = 1",
		"bool_and(btrim(command.principal_id) <> '')",
		"CHECK (btrim(principal_id) <> '')",
		"principal accounting ownership is missing or ambiguous",
		"PRIMARY KEY (principal_id, fill_id)",
		"PRIMARY KEY (principal_id, entry_id)",
		"platform_ledger_entries(principal_id, mode",
		"platform_portfolio_fills(principal_id, mode",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"legacy-principal", "unknown-principal", "sentinel"} {
		if strings.Contains(strings.ToLower(sql), forbidden) {
			t.Errorf("migration invents forbidden owner %q", forbidden)
		}
	}
}
