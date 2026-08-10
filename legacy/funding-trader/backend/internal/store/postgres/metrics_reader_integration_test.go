package postgres

import (
	"testing"

	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
)

func TestMetricsReaderCountsOnlyAuthenticatedScope(t *testing.T) {
	ctx, _, pool, cleanup := newExecutionStoreTest(t)
	defer cleanup()

	rows := []struct {
		id, status, principal, mode string
		manual                      bool
	}{
		{"metric-partial", "partially_hedged", "principal-a", "test", false},
		{"metric-compensating", "compensating", "principal-a", "test", false},
		{"metric-uncertain", "uncertain", "principal-a", "test", false},
		{"metric-manual-state", "manual_intervention_required", "principal-a", "test", false},
		{"metric-manual-flag", "failed", "principal-a", "test", true},
		{"metric-other-principal", "uncertain", "principal-b", "test", false},
		{"metric-other-mode", "uncertain", "principal-a", "live", false},
	}
	for _, row := range rows {
		if _, err := pool.Exec(ctx, `
			insert into executions(id, status, principal_id, mode, manual_intervention)
			values($1,$2,$3,$4,$5)
		`, row.id, row.status, row.principal, row.mode, row.manual); err != nil {
			t.Fatal(err)
		}
	}
	for _, event := range []struct {
		principal, mode, kind string
	}{
		{"principal-a", "test", "resnapshot.required"},
		{"", "test", "resnapshot.required"},
		{"principal-b", "test", "resnapshot.required"},
		{"principal-a", "live", "resnapshot.required"},
		{"principal-a", "test", "execution.upsert"},
	} {
		var principal any = event.principal
		if event.principal == "" {
			principal = nil
		}
		if _, err := pool.Exec(ctx, `
			insert into read_model_events(
				boot_id, principal_id, mode, contract_version, kind, scope, payload
			) values('metrics-boot',$1,$2,$3,$4,array['all'],'{}'::jsonb)
		`, principal, event.mode, readmodel.ContractVersion, event.kind); err != nil {
			t.Fatal(err)
		}
	}

	reader, err := NewMetricsReader(pool)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := reader.Snapshot(ctx, readmodel.Scope{
		PrincipalID: "principal-a",
		Mode:        "test",
	})
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.PartiallyHedged != 1 ||
		snapshot.Compensating != 1 ||
		snapshot.Uncertain != 1 ||
		snapshot.ManualInterventionRequired != 2 ||
		snapshot.ResnapshotRequired != 2 {
		t.Fatalf("metrics snapshot=%+v", snapshot)
	}
}
