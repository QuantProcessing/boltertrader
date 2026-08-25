package authority

const (
	GatewayV11MigrationDigest = "205f14d9e41b4a3a6f5488154a293cd4cd0ae204d578660bd44f0da8decf0253"
	GatewayV11Digest          = "60df28344283767f9bced0303bc7d4adfda4cbec247e30d49f4c787c7b49b59f"
)

func GatewayV11() Matrix {
	objects := make([]Object, 0, 28)
	objects = append(objects, table("schema_migrations", MigrationOnly, []string{"SELECT"}, nil))
	for _, name := range []string{
		"audit_records",
		"fills",
		"gateway_account_retirements",
		"gateway_account_routes",
		"gateway_account_runtime_heads",
		"gateway_candidate_observations",
		"gateway_credential_operations",
		"gateway_desired_accounts",
		"gateway_desired_operations",
		"gateway_order_handoffs",
		"gateway_retired_secret_refs",
		"gateway_retiring_generations",
		"gateway_runtime_snapshot_routes",
		"gateway_runtime_snapshots",
		"idempotency_commands",
		"market_catalog_heads",
		"market_catalog_revisions",
		"next_client_order_ids",
		"order_events",
		"orders",
		"reconciliation_checkpoints",
		"reconciliation_tasks",
		"settled_funding_rates",
		"settled_funding_tracking",
		"venue_correlations",
	} {
		objects = append(objects, table(name, Canonical, []string{"SELECT"}, []string{"INSERT", "UPDATE", "DELETE"}))
	}
	objects = append(objects, Object{
		Schema: "public", Name: "prevent_terminal_order_state_regression()", Kind: Routine, Class: MigrationOnly,
		Probe: "SELECT has_function_privilege(current_user, 'public.prevent_terminal_order_state_regression()', 'EXECUTE')",
	})
	return Matrix{
		Name: "gateway-v11", SchemaVersion: 11, MigrationDigest: GatewayV11MigrationDigest,
		Roles: RoleTopology{
			Maintenance: "bolter_platform_admin", Migrator: "bolter_gateway_migrator",
			Runtime: "bolter_gateway", Writer: "bolter_gateway_writer",
		},
		Objects: objects,
	}
}
