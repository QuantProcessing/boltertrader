package authority

const (
	GatewayV6MigrationDigest = "123eba978969da58a9080a058a4cf2765f9d259d4cd45bf01d930b708a515721"
	GatewayV6Digest          = "76b00f5441ac046981069b5d825743c2e80b8553acd02926365ad9d2db617d97"
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
