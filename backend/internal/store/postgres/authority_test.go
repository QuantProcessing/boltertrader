package postgres

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/migration/authority"
)

func TestPlatformAuthorityMatrixIsValid(t *testing.T) {
	matrix := authority.PlatformV15()
	if err := matrix.Validate(); err != nil {
		t.Fatal(err)
	}
	if matrix.Roles.Migrator != "bolter_platform_test_migrator" || matrix.Roles.Runtime != "bolter_platform_test" {
		t.Fatalf("roles=%+v", matrix.Roles)
	}
}
