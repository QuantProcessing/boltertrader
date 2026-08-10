package authority

const (
	PlatformV15MigrationDigest = "0584d556fe4344e02f940605aae39c29d8be609b72d481d2bb41b2b03b9c6ae8"
	PlatformV15Digest          = "037b7ab18641b4e6019369d39a4c8d991af8855e141d84668bc3011a3fc4d2be"
)

func PlatformV15() Matrix {
	objects := make([]Object, 0, 28)
	objects = append(objects, table("platform_schema_migrations", MigrationOnly, []string{"SELECT"}, nil))
	objects = append(objects, table("platform_sessions", ValidationAuthSession, []string{"SELECT", "INSERT", "UPDATE", "DELETE"}, nil))
	for _, name := range []string{
		"platform_account_routes",
		"platform_accounts",
		"platform_audit",
		"platform_commands",
		"platform_credential_operations",
		"platform_funding_command_plans",
		"platform_funding_execution_transitions",
		"platform_funding_executions",
		"platform_identities",
		"platform_ledger_entries",
		"platform_portfolio_fills",
		"platform_positions",
		"platform_public_projection_state",
		"platform_realtime_events",
		"platform_reconciliation_tasks",
		"platform_reservations",
		"platform_unrealized_pnl",
		"platform_work_facts",
		"platform_work_intents",
		"platform_work_items",
		"platform_work_leases",
		"platform_work_operations",
		"platform_work_workers",
	} {
		objects = append(objects, table(name, Canonical, []string{"SELECT"}, []string{"INSERT", "UPDATE", "DELETE"}))
	}
	objects = append(objects,
		sequence("platform_realtime_events_sequence_seq"),
		sequence("platform_work_lease_seq"),
		Object{Schema: "public", Name: "platform_valid_funding_account_fences(jsonb)", Kind: Routine, Class: MigrationOnly, Writer: []string{"EXECUTE"}, Probe: "SELECT has_function_privilege(current_user, 'public.platform_valid_funding_account_fences(jsonb)', 'EXECUTE')"},
	)
	return Matrix{
		Name:            "platform-v15",
		SchemaVersion:   15,
		MigrationDigest: PlatformV15MigrationDigest,
		Roles: RoleTopology{
			Maintenance: "bolter_platform_test_admin",
			Migrator:    "bolter_platform_test_migrator",
			Runtime:     "bolter_platform_test",
			Writer:      "bolter_platform_test_writer",
		},
		Objects: objects,
	}
}

func table(name string, class ObjectClass, runtime, writer []string) Object {
	return Object{Schema: "public", Name: name, Kind: Table, Class: class, Runtime: runtime, Writer: writer, Probe: "SELECT 1 FROM public." + name + " LIMIT 0"}
}

func sequence(name string) Object {
	return Object{Schema: "public", Name: name, Kind: Sequence, Class: Canonical, Writer: []string{"USAGE", "SELECT", "UPDATE"}, Probe: "SELECT last_value FROM public." + name}
}
