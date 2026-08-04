package postgres

import (
	"embed"
	"regexp"
	"strings"
	"testing"
)

func TestInitialMigrationDeclaresDurabilityConstraints(t *testing.T) {
	sql := InitialMigrationSQL()
	required := []string{
		"schema_migrations", "gateway_accounts", "idempotency_commands", "orders",
		"order_events", "venue_correlations", "fills", "reconciliation_tasks",
		"reconciliation_checkpoints", "audit_records",
		"UNIQUE (principal_id, idempotency_key)",
		"CHECK (portable_client_order_id > 0 AND portable_client_order_id <= 281474976710655)",
		"request_payload jsonb NOT NULL",
		"UNIQUE (gateway_order_id)",
		"FOREIGN KEY",
		"UNIQUE (account_id, product, portable_client_order_id)",
	}
	for _, value := range required {
		if !strings.Contains(sql, value) {
			t.Fatalf("migration missing %q", value)
		}
	}
	forbidden := []string{"api_secret", "private_key", "signed_payload", "raw_request"}
	lower := strings.ToLower(sql)
	for _, value := range forbidden {
		if strings.Contains(lower, value) {
			t.Fatalf("migration contains forbidden storage %q", value)
		}
	}
}

func TestPortableClientOrderIDIsScopedPerAccountProduct(t *testing.T) {
	sql := InitialMigrationSQL()
	if strings.Contains(sql, "UNIQUE (portable_client_order_id)") {
		t.Fatal("portable client order id is globally unique instead of account/product scoped")
	}
	if !strings.Contains(sql, "UNIQUE (account_id, product, portable_client_order_id)") {
		t.Fatal("portable client order id scope uniqueness missing")
	}
}

func TestEveryStateBearingTableFreezesTheOrderStateSet(t *testing.T) {
	sql := strings.ToLower(InitialMigrationSQL())
	tablePattern := regexp.MustCompile(`(?s)create table\s+([a-z_]+)\s*\((.*?)\);`)
	stateCheck := "check (state in ('prepared','dispatching','accepted','open','partially_filled','filled','canceled','rejected','uncertain'))"

	found := map[string]bool{}
	for _, match := range tablePattern.FindAllStringSubmatch(sql, -1) {
		name := match[1]
		body := strings.Join(strings.Fields(match[2]), " ")
		if !strings.Contains(body, "state text") {
			continue
		}
		found[name] = true
		if !strings.Contains(body, stateCheck) {
			t.Errorf("state-bearing table %s does not freeze the complete order-state set", name)
		}
	}

	want := map[string]bool{
		"idempotency_commands": true,
		"orders":               true,
		"order_events":         true,
	}
	if len(found) != len(want) {
		t.Fatalf("state-bearing tables = %v, want %v", found, want)
	}
	for name := range want {
		if !found[name] {
			t.Errorf("state-bearing table %s was not checked", name)
		}
	}
}

func TestInitialMigrationPreventsTerminalOrderStateRegression(t *testing.T) {
	sql := strings.ToLower(InitialMigrationSQL())
	required := []string{
		"prevent_terminal_order_state_regression",
		"old.state in ('filled', 'canceled', 'rejected')",
		"raise exception 'terminal order state cannot change'",
		"before update of state on orders",
	}
	for _, value := range required {
		if !strings.Contains(sql, value) {
			t.Fatalf("migration missing terminal immutability guard %q", value)
		}
	}
}

func TestMigrationsAreEmbedded(t *testing.T) {
	var _ embed.FS = Migrations
}

func TestDesiredAccountMigrationFreezesRecoveryAndCredentialFences(t *testing.T) {
	sql := strings.ToLower(DesiredAccountsMigrationSQL())
	required := []string{
		"gateway_desired_accounts", "secret_ref text not null", "previous_secret_ref text",
		"desired_state_revision bigint not null", "runtime_generation bigint not null",
		"credential_version bigint not null", "rotation_fence_id text",
		"rotation_from_credential_version", "rotation_to_credential_version",
		"rotation_from_runtime_generation", "rotation_to_runtime_generation",
		"requested_state in ('creating', 'active', 'rotating', 'draining')",
	}
	for _, value := range required {
		if !strings.Contains(sql, value) {
			t.Errorf("desired account migration missing %q", value)
		}
	}
	for _, forbidden := range []string{"api_secret", "secret_value", "secret_material", "private_key", "raw_credential"} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("desired account migration contains forbidden credential field %q", forbidden)
		}
	}
}
