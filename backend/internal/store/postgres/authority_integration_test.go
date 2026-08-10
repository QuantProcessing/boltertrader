package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/migration/authority"
	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/stdlib"
)

func TestPlatformRuntimeAuthorityRealPostgres(t *testing.T) {
	if os.Getenv("BT_G010_POSTGRES_ACCEPT") != "1" {
		t.Skip("BT_G010_POSTGRES_ACCEPT=1 is required for local PostgreSQL authority verification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	config, err := pgx.ParseConfig("service=platform_runtime_test")
	if err != nil {
		t.Fatal("parse platform_runtime_test service")
	}
	database := stdlib.OpenDB(*config)
	database.SetMaxOpenConns(1)
	defer database.Close()
	if err := database.PingContext(ctx); err != nil {
		t.Fatal("connect platform_runtime_test service")
	}
	matrix := authority.PlatformV15()
	result, err := VerifyRuntimeAuthority(ctx, database, matrix)
	if err != nil {
		t.Fatalf("verify real Platform runtime authority: %v", err)
	}
	if result.SchemaVersion != matrix.SchemaVersion || result.MigrationDigest != matrix.MigrationDigest || result.MatrixDigest != matrix.Digest() ||
		result.RuntimeRole != matrix.Roles.Runtime || !result.RuntimeInherits || !result.FullCapabilityGrants ||
		!result.EffectiveTablePrivileges || !result.EffectiveSequencePrivileges || !result.EffectiveRoutinePrivileges ||
		len(result.OwnershipViolations) != 0 || len(result.PrivilegeViolations) != 0 {
		t.Fatalf("Platform runtime authority=%+v", result)
	}
	t.Logf("Platform writer membership post-activation=%t", result.WriterMembership)
}
