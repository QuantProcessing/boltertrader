package authority

const (
	GatewayV6MigrationDigest = "9eccd015e687316f44726e282ee3b76289bf8f28c4e61cb90f0fb168d0bf1df0"
	GatewayV6Digest          = "b1f7b0fe0ff2961ae7806b12999b1b1be485057b8d2e1fe61e4a37f80e31aa65"
)

func GatewayV6() Matrix {
	objects := make([]Object, 0, 23)
	objects = append(objects, table("schema_migrations", MigrationOnly, []string{"SELECT"}, nil))
	for _, name := range []string{
		"audit_records",
		"fills",
		"gateway_account_retirements",
		"gateway_account_runtime_heads",
		"gateway_accounts",
		"gateway_candidate_observations",
		"gateway_desired_accounts",
		"gateway_desired_operations",
		"gateway_order_handoffs",
		"gateway_retired_secret_refs",
		"gateway_retiring_generations",
		"gateway_rotation_finalizations",
		"gateway_runtime_snapshot_routes",
		"gateway_runtime_snapshots",
		"idempotency_commands",
		"next_client_order_ids",
		"order_events",
		"orders",
		"reconciliation_checkpoints",
		"reconciliation_tasks",
		"venue_correlations",
	} {
		objects = append(objects, table(name, Canonical, []string{"SELECT"}, []string{"INSERT", "UPDATE", "DELETE"}))
	}
	objects = append(objects, Object{
		Schema: "public", Name: "prevent_terminal_order_state_regression()", Kind: Routine, Class: MigrationOnly,
		Probe: "SELECT has_function_privilege(current_user, 'public.prevent_terminal_order_state_regression()', 'EXECUTE')",
	})
	return Matrix{
		Name:            "gateway-v6",
		SchemaVersion:   6,
		MigrationDigest: GatewayV6MigrationDigest,
		Roles: RoleTopology{
			Maintenance: "bolter_platform_test_admin",
			Migrator:    "bolter_gateway_test_migrator",
			Runtime:     "bolter_gateway_test",
			Writer:      "bolter_gateway_test_writer",
		},
		Objects: objects,
	}
}
