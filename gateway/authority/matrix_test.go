package authority_test

import (
	"errors"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/gateway/authority"
)

func TestPlatformV15IsExhaustiveAndStable(t *testing.T) {
	matrix := authority.PlatformV15()
	if err := matrix.Validate(); err != nil {
		t.Fatal(err)
	}
	if matrix.SchemaVersion != 15 {
		t.Fatalf("schema=%d", matrix.SchemaVersion)
	}
	if got := matrix.Class("platform_sessions"); got != authority.ValidationAuthSession {
		t.Fatalf("class=%s", got)
	}
	if got := matrix.Class("platform_audit"); got != authority.Canonical {
		t.Fatalf("class=%s", got)
	}
	if got := matrix.Class("activities"); got != authority.MigrationOnly {
		t.Fatalf("legacy class=%s", got)
	}
	if got := matrix.Digest(); got != authority.PlatformV15Digest {
		t.Fatalf("digest=%s", got)
	}
	assertInventory(t, matrix, 41, 6, 1)
}

func TestGatewayV8IsExhaustiveAndStable(t *testing.T) {
	matrix := authority.GatewayV8()
	if err := matrix.Validate(); err != nil {
		t.Fatal(err)
	}
	if matrix.SchemaVersion != 8 || matrix.MigrationDigest != authority.GatewayV8MigrationDigest {
		t.Fatalf("identity=%+v", matrix)
	}
	if got := matrix.Digest(); got != authority.GatewayV8Digest {
		t.Fatalf("digest=%s", got)
	}
	assertInventory(t, matrix, 23, 0, 1)
}

func TestGatewayV9IsExhaustiveAndStable(t *testing.T) {
	matrix := authority.GatewayV9()
	if err := matrix.Validate(); err != nil {
		t.Fatal(err)
	}
	if matrix.SchemaVersion != 9 || matrix.MigrationDigest != authority.GatewayV9MigrationDigest {
		t.Fatalf("identity=%+v", matrix)
	}
	if got := matrix.Digest(); got != authority.GatewayV9Digest {
		t.Fatalf("digest=%s", got)
	}
	assertInventory(t, matrix, 23, 0, 1)
}

func TestGatewayV10IsExhaustiveAndStable(t *testing.T) {
	matrix := authority.GatewayV10()
	if err := matrix.Validate(); err != nil {
		t.Fatal(err)
	}
	if matrix.SchemaVersion != 10 || matrix.MigrationDigest != authority.GatewayV10MigrationDigest {
		t.Fatalf("identity=%+v", matrix)
	}
	if got := matrix.Digest(); got != authority.GatewayV10Digest {
		t.Fatalf("digest=%s", got)
	}
	assertInventory(t, matrix, 22, 0, 1)
}

func TestGatewayV11IsExhaustiveAndStable(t *testing.T) {
	matrix := authority.GatewayV11()
	if err := matrix.Validate(); err != nil {
		t.Fatal(err)
	}
	if matrix.SchemaVersion != 11 || matrix.MigrationDigest != authority.GatewayV11MigrationDigest {
		t.Fatalf("identity=%+v", matrix)
	}
	if got := matrix.Digest(); got != authority.GatewayV11Digest {
		t.Fatalf("digest=%s", got)
	}
	assertInventory(t, matrix, 26, 0, 1)
}

func TestMatricesRejectUnknownDuplicateOrUnsafeObjects(t *testing.T) {
	matrix := authority.PlatformV15()
	matrix.Objects = append(matrix.Objects, authority.Object{Name: "platform_unknown", Kind: authority.Table})
	if err := matrix.Validate(); !errors.Is(err, authority.ErrUnclassifiedObject) {
		t.Fatalf("err=%v", err)
	}

	matrix = authority.PlatformV15()
	matrix.Objects = append(matrix.Objects, matrix.Objects[0])
	if err := matrix.Validate(); !errors.Is(err, authority.ErrInvalidMatrix) {
		t.Fatalf("duplicate err=%v", err)
	}

	for _, matrix := range []authority.Matrix{authority.PlatformV15(), authority.GatewayV11()} {
		for _, object := range matrix.Objects {
			probe := strings.ToLower(object.Probe)
			for _, unsafe := range []string{"nextval", "setval", "insert ", "update ", "delete ", ";"} {
				if strings.Contains(probe, unsafe) {
					t.Fatalf("unsafe probe: %s: %s", object.Name, object.Probe)
				}
			}
		}
	}
}

func TestMatricesFreezeExactRoleTopology(t *testing.T) {
	platform := authority.PlatformV15().Roles
	if platform.Maintenance != "bolter_platform_admin" || platform.Migrator != "bolter_platform_migrator" || platform.Runtime != "bolter_platform" || platform.Writer != "bolter_platform_writer" {
		t.Fatalf("platform roles=%+v", platform)
	}
	gateway := authority.GatewayV11().Roles
	if gateway.Maintenance != "bolter_platform_admin" || gateway.Migrator != "bolter_gateway_migrator" || gateway.Runtime != "bolter_gateway" || gateway.Writer != "bolter_gateway_writer" {
		t.Fatalf("gateway roles=%+v", gateway)
	}
}

func assertInventory(t *testing.T, matrix authority.Matrix, tables, sequences, routines int) {
	t.Helper()
	counts := map[authority.ObjectKind]int{}
	for _, object := range matrix.Objects {
		counts[object.Kind]++
	}
	if counts[authority.Table] != tables || counts[authority.Sequence] != sequences || counts[authority.Routine] != routines {
		t.Fatalf("inventory=%v", counts)
	}
}
