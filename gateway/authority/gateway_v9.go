package authority

const (
	GatewayV9MigrationDigest = "50edcd421149a43cd2a287a756ccb760aa4fae150958386f792166c7dc3ef42d"
	GatewayV9Digest          = "f9757a085ded6da80222a65faa7d2ea6470c46b3d3f7918b6296e88d8b0404d7"
)

func GatewayV9() Matrix {
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
		Name:            "gateway-v9",
		SchemaVersion:   9,
		MigrationDigest: GatewayV9MigrationDigest,
		Roles: RoleTopology{
			Maintenance: "bolter_platform_admin",
			Migrator:    "bolter_gateway_migrator",
			Runtime:     "bolter_gateway",
			Writer:      "bolter_gateway_writer",
		},
		Objects: objects,
	}
}
