package authority

const (
	GatewayV7MigrationDigest = "8faae3d3ce4c36825f67617c5f3c996978a7bb65fd275956854e1048ab0a8800"
	GatewayV7Digest          = "4196af9f019401ec55e077be6a34514650b8a32dcd4468a424e73d565ef57c44"
)

func GatewayV7() Matrix {
	objects := make([]Object, 0, 25)
	objects = append(objects, table("schema_migrations", MigrationOnly, []string{"SELECT"}, nil))
	for _, name := range []string{
		"audit_records",
		"fills",
		"gateway_account_retirements",
		"gateway_account_routes",
		"gateway_account_runtime_heads",
		"gateway_accounts",
		"gateway_candidate_observations",
		"gateway_credential_operations",
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
		Name:            "gateway-v7",
		SchemaVersion:   7,
		MigrationDigest: GatewayV7MigrationDigest,
		Roles: RoleTopology{
			Maintenance: "bolter_platform_test_admin",
			Migrator:    "bolter_gateway_test_migrator",
			Runtime:     "bolter_gateway_test",
			Writer:      "bolter_gateway_test_writer",
		},
		Objects: objects,
	}
}
