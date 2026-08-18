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
		// The venue environment is pinned rather than enumerated. This column
		// used to be cross-checked against a mode column on the same row; the
		// mode axis is gone, and the pin is the whole of what is left to say.
		"check (venue_environment = 'live')",
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

func TestRuntimeSnapshotMigrationFreezesImmutableHeadsAndRecovery(t *testing.T) {
	sql := strings.ToLower(RuntimeSnapshotsMigrationSQL())
	for _, required := range []string{
		"gateway_runtime_snapshots", "operation_id uuid not null unique",
		"gateway_runtime_snapshot_routes", "gateway_account_runtime_heads",
		"active_snapshot_id", "pending_snapshot_id",
		"gateway_desired_operations", "references gateway_runtime_snapshots(operation_id)",
		"gateway_candidate_observations", "gateway_retiring_generations",
		"gateway_retired_secret_refs", "gateway_desired_operations_recovery",
		// Same pin as the desired-account row, on the venue's own environment
		// string, and for the same reason: the mode column that used to be
		// cross-checked against it no longer exists.
		"check (native_environment = 'live')",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("runtime snapshot migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"api_secret", "secret_value", "secret_material", "private_key", "raw_credential"} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("runtime snapshot migration contains forbidden credential field %q", forbidden)
		}
	}
}

func TestOrderHandoffMigrationFreezesGenerationScopedDrainState(t *testing.T) {
	sql := strings.ToLower(OrderHandoffsMigrationSQL())
	for _, required := range []string{
		"gateway_order_handoffs", "command_id uuid not null unique", "route_id text not null",
		"desired_state_revision bigint not null", "runtime_generation bigint not null",
		"credential_version bigint not null", "state in ('started','accepted','rejected','uncertain')",
		"where state = 'started'",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("order handoff migration missing %q", required)
		}
	}
}

func TestAccountRetirementMigrationFreezesReplayableTerminalState(t *testing.T) {
	sql := strings.ToLower(AccountRetirementsMigrationSQL())
	for _, required := range []string{
		"gateway_account_retirements", "operation_id uuid primary key", "account_id text not null unique",
		"request_digest text not null", "expected_desired_state_revision bigint not null",
		"expected_runtime_generation bigint not null", "expected_credential_version bigint not null",
		"tombstone_revision bigint not null", "state in ('tombstoned')", "completed_at timestamptz not null",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("account retirement migration missing %q", required)
		}
	}
}

func TestRotationFinalizationMigrationFreezesExactReplayProof(t *testing.T) {
	sql := strings.ToLower(RotationFinalizationsMigrationSQL())
	for _, required := range []string{
		"gateway_rotation_finalizations", "operation_id uuid primary key", "request_digest text not null",
		"request_digest ~ '^[0-9a-f]{64}$'",
		"active_snapshot_id uuid not null", "retiring_snapshot_id uuid not null unique",
		"expected_active_desired_state_revision bigint not null", "expected_retiring_desired_state_revision bigint not null",
		"retired_secret_ref text not null", "tombstone_revision bigint not null",
		"namespace text not null", "key_version text not null",
		"retiring_state in ('retired')", "secret_reference_state in ('tombstoned')",
		"completed_at timestamptz not null",
		"expected_active_desired_state_revision = expected_retiring_desired_state_revision + 1",
		"expected_active_runtime_generation = expected_retiring_runtime_generation + 1",
		"expected_active_credential_version = expected_retiring_credential_version + 1",
		"unique (operation_id, snapshot_id)",
		"unique (snapshot_id, account_id, desired_state_revision, runtime_generation, credential_version)",
		"foreign key (operation_id, active_snapshot_id)",
		"foreign key (active_snapshot_id, account_id, expected_active_desired_state_revision, expected_active_runtime_generation, expected_active_credential_version)",
		"foreign key (account_id, retiring_snapshot_id)",
		"foreign key (retiring_snapshot_id, account_id, expected_retiring_desired_state_revision, expected_retiring_runtime_generation, expected_retiring_credential_version, retired_secret_ref)",
		"foreign key (namespace, retired_secret_ref, account_id)",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("rotation finalization migration missing %q", required)
		}
	}
}

func TestAccountConsolidationMigrationFreezesTheSingleAccountAuthority(t *testing.T) {
	sql := strings.ToLower(AccountConsolidationMigrationSQL())
	for _, required := range []string{
		// The control-plane columns move onto the data-plane row rather than
		// living in a second account table.
		"alter table gateway_desired_accounts",
		"add column display_name text not null",
		"add column product_ids jsonb not null",
		"add column last_validated_at timestamptz",
		"check (jsonb_typeof(product_ids) = 'array')",
		// A credential-free row is only legitimate mid-create.
		"alter column secret_ref drop not null",
		"check (secret_ref is not null or requested_state = 'creating')",
		"gateway_account_routes",
		"references gateway_desired_accounts(account_id) on delete cascade",
		"check (product in ('spot', 'perp'))",
		"check (requested_state in ('active', 'disabled'))",
		"primary key (account_id, route_id)",
		"gateway_credential_operations",
		"check (kind in ('create', 'rotate', 'retire'))",
		"check (state in ('awaiting_material', 'stored', 'applying', 'active', 'cleanup_pending', 'abandoned', 'failed'))",
		"unique (principal_id, kind, idempotency_key_digest)",
		"check (target_desired_state_revision > 0)",
		"check (target_runtime_generation > 0)",
		"check (target_credential_version > 0)",
		// Rotation cleanup proof must name the exact revision it tombstoned.
		"rotation_cleanup_tombstone_revision = target_desired_state_revision",
		"kind = 'rotate'",
		"gateway_credential_operations_recovery",
		"where state in ('awaiting_material', 'stored', 'applying', 'cleanup_pending')",
		"create unique index gateway_credential_operations_one_recoverable_per_account",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("account consolidation migration missing %q", required)
		}
	}
	for _, forbidden := range []string{"api_secret", "secret_value", "secret_material", "private_key", "raw_credential"} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("account consolidation migration contains forbidden credential field %q", forbidden)
		}
	}
}
