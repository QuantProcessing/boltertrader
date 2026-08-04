package migrations_test

import (
	"os"
	"strings"
	"testing"
)

func TestTradingCoreMigrationFreezesOwnershipAndIdempotency(t *testing.T) {
	data, err := os.ReadFile("0004_trading_core.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	for _, required := range []string{
		"platform_reservations", "platform_commands", "platform_reconciliation_tasks", "platform_positions", "platform_ledger_entries", "platform_unrealized_pnl",
		"principal_id", "mode", "product_id", "lineage_id", "idempotency_key", "payload_hash",
		"UNIQUE (principal_id, mode, method, path, idempotency_key)",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration missing %q", required)
		}
	}
}
