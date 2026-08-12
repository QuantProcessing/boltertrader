package postgres

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/authority"
	"github.com/jackc/pgx/v5/pgxpool"
)

func TestGatewayRuntimeAuthorityRealPostgres(t *testing.T) {
	if os.Getenv("BT_G010_POSTGRES_ACCEPT") != "1" {
		t.Skip("BT_G010_POSTGRES_ACCEPT=1 is required for local PostgreSQL authority verification")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	pool, err := pgxpool.New(ctx, "service=gateway_runtime_test")
	if err != nil {
		t.Fatal("connect gateway_runtime_test service")
	}
	defer pool.Close()
	if err := pool.Ping(ctx); err != nil {
		t.Fatal("ping gateway_runtime_test service")
	}
	matrix := authority.GatewayV7()
	result, err := VerifyRuntimeAuthority(ctx, pool, matrix)
	if err != nil {
		t.Fatalf("verify real Gateway runtime authority: %v", err)
	}
	if result.SchemaVersion != matrix.SchemaVersion || result.MigrationDigest != matrix.MigrationDigest || result.MatrixDigest != matrix.Digest() ||
		result.RuntimeRole != matrix.Roles.Runtime || !result.RuntimeInherits || !result.FullCapabilityGrants ||
		!result.EffectiveTablePrivileges || !result.EffectiveSequencePrivileges || !result.EffectiveRoutinePrivileges ||
		len(result.OwnershipViolations) != 0 || len(result.PrivilegeViolations) != 0 {
		t.Fatalf("Gateway runtime authority=%+v", result)
	}
	t.Logf("Gateway writer membership post-activation=%t", result.WriterMembership)
}
