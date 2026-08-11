package migration

import (
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/migration/authority"
)

func TestValidationAuthorityQueryExcludesOnlyVolatileWorkerHeartbeatTimes(t *testing.T) {
	query, args := validationAuthorityQuery(authority.PlatformV15(), authority.Object{
		Schema: "public", Name: "platform_work_workers", Kind: authority.Table,
	})
	if len(args) != 0 {
		t.Fatalf("worker snapshot args=%v", args)
	}
	for _, column := range []string{
		"worker_id", "instance_id", "product_id", "mode", "contract_version", "generation",
		"state", "draining", "registered_at", "last_applied_facts_cursor",
	} {
		if !strings.Contains(query, column) {
			t.Fatalf("worker snapshot query %q omits stable column %s", query, column)
		}
	}
	for _, forbidden := range []string{"SELECT *", "heartbeat_expires_at", "last_heartbeat_at"} {
		if strings.Contains(query, forbidden) {
			t.Fatalf("worker snapshot query %q contains volatile selection %q", query, forbidden)
		}
	}
}

func TestValidationAuthorityQueryStillCapturesEveryColumnForOtherCanonicalTables(t *testing.T) {
	query, args := validationAuthorityQuery(authority.PlatformV15(), authority.Object{
		Schema: "public", Name: "platform_work_operations", Kind: authority.Table,
	})
	if len(args) != 0 || !strings.HasPrefix(query, "SELECT * FROM ") {
		t.Fatalf("canonical table query=%q args=%v", query, args)
	}
}
