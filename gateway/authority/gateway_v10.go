package authority

const (
	GatewayV10MigrationDigest = "811b778d7ae637b8391188033ca4f5af08db51850f9ff9cb5f61153eaff101dd"
	GatewayV10Digest          = "ee336ed3db2a7fb3ee95672302c924da9f600be206c839c8259381eff339ccbb"
)

func GatewayV10() Matrix {
	objects := make([]Object, 0, 24)
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
		Name:            "gateway-v10",
		SchemaVersion:   10,
		MigrationDigest: GatewayV10MigrationDigest,
		Roles: RoleTopology{
			Maintenance: "bolter_platform_admin",
			Migrator:    "bolter_gateway_migrator",
			Runtime:     "bolter_gateway",
			Writer:      "bolter_gateway_writer",
		},
		Objects: objects,
	}
}
