package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	platformpostgres "github.com/QuantProcessing/boltertrader/backend/internal/store/postgres"
	_ "github.com/jackc/pgx/v5/stdlib"
)

const requiredPlatformSchemaVersion = 15

var migratePlatform = migrateProductionPlatform

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("platform-migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	service := flags.String("service", "", "non-secret libpq Platform service")
	database := flags.String("database", "", "non-secret Platform database")
	role := flags.String("role", "", "expected Platform PostgreSQL connection role")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || !safeIdentity(*service) || !safeIdentity(*database) || !safeIdentity(*role) {
		return fmt.Errorf("--service, --database, and --role are required")
	}
	if !containsIdentityToken(*database, "test") || !containsIdentityToken(*role, "test") || containsIdentityToken(*database, "live") || containsIdentityToken(*role, "live") {
		return fmt.Errorf("CROSS_MODE_DATABASE_IDENTITY")
	}
	version, err := migratePlatform(context.Background(), *service, *database, *role)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(struct {
		SchemaVersion int `json:"schemaVersion"`
	}{SchemaVersion: version})
}

func migrateProductionPlatform(ctx context.Context, service, database, role string) (int, error) {
	connection, err := sql.Open("pgx", "service="+service+" dbname="+database)
	if err != nil {
		return 0, fmt.Errorf("PLATFORM_MIGRATION_UNAVAILABLE")
	}
	defer connection.Close()
	if err := connection.PingContext(ctx); err != nil {
		return 0, fmt.Errorf("PLATFORM_MIGRATION_UNAVAILABLE")
	}
	var observedDatabase, observedRole string
	if err := connection.QueryRowContext(ctx, `SELECT current_database(), current_user`).Scan(&observedDatabase, &observedRole); err != nil {
		return 0, fmt.Errorf("PLATFORM_CONNECTED_IDENTITY_UNAVAILABLE")
	}
	if observedDatabase != database || observedRole != role {
		return 0, fmt.Errorf("CONNECTED_DATABASE_IDENTITY_MISMATCH")
	}
	if err := platformpostgres.Migrate(ctx, connection); err != nil {
		return 0, fmt.Errorf("PLATFORM_MIGRATION_BLOCKED")
	}
	var version int
	var ready bool
	if err := connection.QueryRowContext(ctx, `SELECT COALESCE(max(version),0), to_regclass('platform_account_routes') IS NOT NULL AND (SELECT count(*)=2 FROM information_schema.columns WHERE table_schema=current_schema() AND table_name='platform_accounts' AND column_name IN ('mode','native_environment')) FROM platform_schema_migrations`).Scan(&version, &ready); err != nil || version != requiredPlatformSchemaVersion || !ready {
		return 0, fmt.Errorf("PLATFORM_SCHEMA_PREFLIGHT_BLOCKED")
	}
	return version, nil
}

func safeIdentity(value string) bool {
	if strings.TrimSpace(value) != value || value == "" || len(value) > 64 {
		return false
	}
	return !strings.ContainsAny(value, " \t\r\n\x00='\"")
}

func containsIdentityToken(value, want string) bool {
	for _, token := range strings.FieldsFunc(strings.ToLower(value), func(char rune) bool {
		return char == '_' || char == '-' || char == '.'
	}) {
		if token == want {
			return true
		}
	}
	return false
}
