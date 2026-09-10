package authority

const (
	GatewayV12MigrationDigest = "965bfe7fed42718581d2cf3bab9117767f941d58dfd7c5da57eb6df60f0dd056"
	GatewayV12Digest          = "0777c7c1642355eb8c9f7a910d9ac8d0c4f3e0c47761816be11f6298ed6c9fe2"
)

func GatewayV12() Matrix {
	objects := make([]Object, 0, 28)
	objects = append(objects, table("schema_migrations", MigrationOnly, []string{"SELECT"}, nil))
	for _, name := range []string{
		"audit_records",
		"gateway_accounts",
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
		Name: "gateway-v12", SchemaVersion: 12, MigrationDigest: GatewayV12MigrationDigest,
		Roles: RoleTopology{
			Maintenance: "bolter_platform_admin", Migrator: "bolter_gateway_migrator",
			Runtime: "bolter_gateway", Writer: "bolter_gateway_writer",
		},
		Objects: objects,
	}
}
