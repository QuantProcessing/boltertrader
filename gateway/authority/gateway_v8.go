package authority

const (
	GatewayV8MigrationDigest = "662852a2fa5c3e126cf420e5108e8d1e4c04d2c584a0edecfe833cf9a1dcfc75"
	GatewayV8Digest          = "07b6851eaec3707e2264526e4472b6175065ac5bd721ab3b209d499c09189b92"
)

func GatewayV8() Matrix {
	objects := make([]Object, 0, 25)
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
		Name:            "gateway-v8",
		SchemaVersion:   8,
		MigrationDigest: GatewayV8MigrationDigest,
		Roles: RoleTopology{
			Maintenance: "bolter_platform_test_admin",
			Migrator:    "bolter_gateway_test_migrator",
			Runtime:     "bolter_gateway_test",
			Writer:      "bolter_gateway_test_writer",
		},
		Objects: objects,
	}
}
