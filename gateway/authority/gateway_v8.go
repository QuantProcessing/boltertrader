package authority

const (
	GatewayV8MigrationDigest = "86a44cfeec8742cc636fe52316f120015a373eb6193111c78c69c8034400e6dd"
	GatewayV8Digest          = "9b7f3055cd1d3cead6a7df808bf6c94bd15c2a6fe87bc12892317677a34a686e"
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
			Maintenance: "bolter_platform_admin",
			Migrator:    "bolter_gateway_migrator",
			Runtime:     "bolter_gateway",
			Writer:      "bolter_gateway_writer",
		},
		Objects: objects,
	}
}
