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

func TestFundingWorkMigrationFreezesDurableFencingStore(t *testing.T) {
	data, err := os.ReadFile("0005_funding_work_store.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	for _, required := range []string{
		"platform_work_workers", "platform_work_items", "platform_work_leases", "platform_work_facts", "platform_work_intents",
		"worker_generation", "work_revision", "lease_generation", "facts_cursor", "account_fences", "idempotency_key",
		"platform_work_leases_active", "platform_work_items_claimable",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("migration missing %q", required)
		}
	}
}

func TestPortfolioFillMigrationFreezesRestartIdempotency(t *testing.T) {
	data, err := os.ReadFile("0006_portfolio_fills.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	for _, required := range []string{
		"platform_portfolio_fills", "fill_id", "fill_hash", "command_id", "mode", "product_id", "lineage_id",
		"account_id", "instrument_id", "realized_pnl", "fee", "occurred_at",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("portfolio fill migration missing %q", required)
		}
	}
}

func TestCurrencySafeAccountingMigrationPersistsEconomicAuthority(t *testing.T) {
	data, err := os.ReadFile("0011_currency_safe_accounting.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	for _, required := range []string{"platform_positions", "platform_portfolio_fills", "settlement_currency", "platform_unrealized_pnl", "currency"} {
		if !strings.Contains(sql, required) {
			t.Errorf("currency-safe accounting migration missing %q", required)
		}
	}
}

func TestFundingBrowserProducerMigrationFreezesPrincipalOwnership(t *testing.T) {
	data, err := os.ReadFile("0008_funding_browser_producer.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(data)
	for _, required := range []string{"platform_work_items", "principal_id", "platform_work_items_browser_owner"} {
		if !strings.Contains(sql, required) {
			t.Errorf("funding browser producer migration missing %q", required)
		}
	}
}
