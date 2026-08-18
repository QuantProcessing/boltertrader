package main

import (
	"bytes"
	"context"
	"strings"
	"testing"
)

func TestGatewayMigrateUsesExactDedicatedIdentity(t *testing.T) {
	prior := migrateGateway
	migrateGateway = func(_ context.Context, service, database, role string) (migrationResult, error) {
		if service != "gateway_test" || database != "bolter_gateway" || role != "bolter_gateway_migrator" {
			t.Fatalf("identity=%s/%s/%s", service, database, role)
		}
		return migrationResult{SchemaVersion: 6, MigrationDigest: strings.Repeat("a", 64), MatrixDigest: strings.Repeat("b", 64)}, nil
	}
	t.Cleanup(func() { migrateGateway = prior })
	var stdout bytes.Buffer
	if err := run([]string{"--service", "gateway_test", "--database", "bolter_gateway", "--role", "bolter_gateway_migrator"}, &stdout, &bytes.Buffer{}); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(stdout.String(), `"schemaVersion":6`) {
		t.Fatalf("stdout=%q", stdout.String())
	}
}

func TestGatewayMigrateRejectsRuntimeAndLiveIdentities(t *testing.T) {
	for _, args := range [][]string{
		{"--service", "gateway_test", "--database", "bolter_gateway", "--role", "bolter_gateway"},
		{"--service", "gateway_live", "--database", "bolter_gateway_live", "--role", "bolter_gateway_live_migrator"},
	} {
		if err := run(args, &bytes.Buffer{}, &bytes.Buffer{}); err == nil {
			t.Fatalf("accepted %v", args)
		}
	}
}
