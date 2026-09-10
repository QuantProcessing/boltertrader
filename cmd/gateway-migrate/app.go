package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"strings"

	"github.com/QuantProcessing/boltertrader/gateway/authority"
	"github.com/QuantProcessing/boltertrader/gateway/store/postgres"
	"github.com/jackc/pgx/v5/pgxpool"
)

type migrationResult struct {
	SchemaVersion   int    `json:"schemaVersion"`
	MigrationDigest string `json:"migrationDigest"`
	MatrixDigest    string `json:"matrixDigest"`
}

var migrateGateway = migrateProductionGateway

func run(args []string, stdout, stderr io.Writer) error {
	flags := flag.NewFlagSet("gateway-migrate", flag.ContinueOnError)
	flags.SetOutput(stderr)
	service := flags.String("service", "", "non-secret libpq Gateway service")
	database := flags.String("database", "", "non-secret Gateway database")
	role := flags.String("role", "", "expected Gateway PostgreSQL connection role")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if flags.NArg() != 0 || *service != "gateway_test" || *database != "bolter_gateway" || *role != "bolter_gateway_migrator" {
		return fmt.Errorf("CROSS_MODE_DATABASE_IDENTITY")
	}
	result, err := migrateGateway(context.Background(), *service, *database, *role)
	if err != nil {
		return err
	}
	return json.NewEncoder(stdout).Encode(result)
}

func migrateProductionGateway(ctx context.Context, service, database, role string) (migrationResult, error) {
	config, err := pgxpool.ParseConfig("service=" + service + " dbname=" + database)
	if err != nil {
		return migrationResult{}, fmt.Errorf("GATEWAY_MIGRATION_UNAVAILABLE")
	}
	pool, err := pgxpool.NewWithConfig(ctx, config)
	if err != nil {
		return migrationResult{}, fmt.Errorf("GATEWAY_MIGRATION_UNAVAILABLE")
	}
	defer pool.Close()
	var observedDatabase, observedRole string
	if err := pool.QueryRow(ctx, `SELECT current_database(), current_user`).Scan(&observedDatabase, &observedRole); err != nil {
		return migrationResult{}, fmt.Errorf("GATEWAY_CONNECTED_IDENTITY_UNAVAILABLE")
	}
	if observedDatabase != database || observedRole != role {
		return migrationResult{}, fmt.Errorf("CONNECTED_DATABASE_IDENTITY_MISMATCH")
	}
	if err := postgres.Migrate(ctx, pool); err != nil {
		return migrationResult{}, fmt.Errorf("GATEWAY_MIGRATION_BLOCKED")
	}
	matrix := authority.GatewayV12()
	if err := postgres.ApplyAuthority(ctx, pool, matrix); err != nil {
		return migrationResult{}, fmt.Errorf("GATEWAY_AUTHORITY_BLOCKED")
	}
	var version int
	if err := pool.QueryRow(ctx, `SELECT COALESCE(max(version),0) FROM schema_migrations`).Scan(&version); err != nil || version != matrix.SchemaVersion {
		return migrationResult{}, fmt.Errorf("GATEWAY_SCHEMA_PREFLIGHT_BLOCKED")
	}
	return migrationResult{SchemaVersion: version, MigrationDigest: matrix.MigrationDigest, MatrixDigest: matrix.Digest()}, nil
}

func safeIdentity(value string) bool {
	return strings.TrimSpace(value) == value && value != "" && len(value) <= 64 && !strings.ContainsAny(value, " \t\r\n\x00='\"")
}
