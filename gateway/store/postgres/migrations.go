package postgres

import "embed"

//go:embed migrations/*.sql
var Migrations embed.FS

func InitialMigrationSQL() string {
	data, err := Migrations.ReadFile("migrations/001_initial.sql")
	if err != nil {
		panic(err)
	}
	return string(data)
}

func DesiredAccountsMigrationSQL() string {
	data, err := Migrations.ReadFile("migrations/002_desired_accounts.sql")
	if err != nil {
		panic(err)
	}
	return string(data)
}

func RuntimeSnapshotsMigrationSQL() string {
	data, err := Migrations.ReadFile("migrations/003_runtime_snapshots.sql")
	if err != nil {
		panic(err)
	}
	return string(data)
}

func OrderHandoffsMigrationSQL() string {
	data, err := Migrations.ReadFile("migrations/004_order_handoffs.sql")
	if err != nil {
		panic(err)
	}
	return string(data)
}

func AccountRetirementsMigrationSQL() string {
	data, err := Migrations.ReadFile("migrations/005_account_retirements.sql")
	if err != nil {
		panic(err)
	}
	return string(data)
}

func RotationFinalizationsMigrationSQL() string {
	data, err := Migrations.ReadFile("migrations/006_rotation_finalizations.sql")
	if err != nil {
		panic(err)
	}
	return string(data)
}

func AccountConsolidationMigrationSQL() string {
	data, err := Migrations.ReadFile("migrations/007_account_consolidation.sql")
	if err != nil {
		panic(err)
	}
	return string(data)
}

func TerminalAccountSecretRefsMigrationSQL() string {
	data, err := Migrations.ReadFile("migrations/009_terminal_account_secret_refs.sql")
	if err != nil {
		panic(err)
	}
	return string(data)
}

func SimpleAccountsMigrationSQL() string {
	data, err := Migrations.ReadFile("migrations/010_simple_accounts.sql")
	if err != nil {
		panic(err)
	}
	return string(data)
}

func MarketDataStateMigrationSQL() string {
	data, err := Migrations.ReadFile("migrations/011_market_data_state.sql")
	if err != nil {
		panic(err)
	}
	return string(data)
}
