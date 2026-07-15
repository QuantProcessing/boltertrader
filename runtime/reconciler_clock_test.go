package runtime

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
)

type nodeClockMassStatusExec struct {
	*runtimetest.FakeExec
	query model.MassStatusQuery
}

func (e *nodeClockMassStatusExec) GenerateExecutionMassStatus(_ context.Context, query model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	e.query = query
	mass := model.NewExecutionMassStatus("FAKE", query.AccountID, query.Until)
	mass.ClientID = query.ClientID
	mass.OpenOrdersCoverage = model.NewSnapshotCoverage(model.CoverageComplete, query.AccountID, query.ClientID, []model.InstrumentID{}, query.Until)
	mass.FillsCoverage = model.ReportCoverage{State: model.CoverageNotRequested}
	mass.PositionsCoverage = model.ReportCoverage{State: model.CoverageNotRequested}
	return mass, nil
}

func TestReconcilerUsesInjectedNodeClockForCoverage(t *testing.T) {
	start := time.Unix(9876, 0)
	exec := &nodeClockMassStatusExec{FakeExec: runtimetest.NewFakeExec()}
	node := NewNode(Clients{Execution: exec}, clock.NewSimulatedClock(start), "acct")

	report, err := node.reconciler.Run(context.Background())
	if err != nil {
		t.Fatalf("reconcile: %v", err)
	}
	if !exec.query.Until.Equal(start) {
		t.Fatalf("mass-status Until=%s, want node clock %s", exec.query.Until, start)
	}
	if !report.OpenOrdersCoverage.Scope.Through.Equal(start) {
		t.Fatalf("open-order watermark=%s, want node clock %s", report.OpenOrdersCoverage.Scope.Through, start)
	}
}
