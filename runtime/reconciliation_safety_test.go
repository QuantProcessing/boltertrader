package runtime_test

import (
	"context"
	"sync/atomic"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/lifecycle"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
	"github.com/shopspring/decimal"
)

type partialMassStatusExec struct {
	*runtimetest.FakeExec
	partial atomic.Bool
	mass    atomic.Pointer[model.ExecutionMassStatus]
}

func (f *partialMassStatusExec) Capabilities() contract.Capabilities {
	caps := f.FakeExec.Capabilities()
	if configured := f.mass.Load(); configured != nil && len(configured.FillReports) != 0 {
		caps.Reports.FillHistory = true
	}
	return caps
}

func (f *partialMassStatusExec) GenerateExecutionMassStatus(ctx context.Context, query model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	mass, err := f.FakeExec.GenerateExecutionMassStatus(ctx, query)
	if configured := f.mass.Load(); configured != nil {
		copy := configured.Clone()
		copy.ClientID = query.ClientID
		ids := make([]model.InstrumentID, 0, len(copy.FillReports))
		for _, reports := range copy.FillReports {
			for _, report := range reports {
				ids = append(ids, report.Fill.InstrumentID)
			}
		}
		ids = model.NormalizeInstrumentIDs(ids)
		copy.OpenOrdersCoverage = model.NewSnapshotCoverage(model.CoverageComplete, copy.AccountID, query.ClientID, ids, query.Until)
		if query.IncludeFills {
			copy.FillsCoverage = model.NewFillCoverage(model.CoverageComplete, copy.AccountID, query.ClientID, ids, query.Since, query.Until)
		} else {
			copy.FillsCoverage = model.ReportCoverage{State: model.CoverageNotRequested}
		}
		copy.PositionsCoverage = model.ReportCoverage{State: model.CoverageNotRequested}
		mass = &copy
	}
	if mass != nil {
		if f.partial.Load() {
			mass.OpenOrdersCoverage.State = model.CoveragePartial
			mass.Warnings = append(mass.Warnings, model.ReportWarning{
				Code:    "SNAPSHOT_PAGE_NOTE",
				Message: "diagnostic accompanies typed partial coverage",
			})
		}
	}
	return mass, err
}

func TestResyncPreservesOperatorTradingRestriction(t *testing.T) {
	tests := []struct {
		name string
		set  func(*runtime.TradingNode)
		want lifecycle.TradingState
	}{
		{name: "halted", set: func(node *runtime.TradingNode) { node.Halt("operator") }, want: lifecycle.TradingHalted},
		{name: "reduce-only", set: func(node *runtime.TradingNode) { node.ReduceOnly("operator") }, want: lifecycle.TradingReducing},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fexec := runtimetest.NewFakeExec()
			node := runtime.NewNode(runtime.Clients{Execution: fexec}, nil, "restriction")
			ctx, cancel := context.WithCancel(context.Background())
			defer cancel()
			go node.Run(ctx)
			waitNodeRunning(t, node)

			tt.set(node)
			if _, err := node.Resync(context.Background()); err != nil {
				t.Fatalf("resync: %v", err)
			}
			if got := node.State(); got.Node != lifecycle.NodeRunning || got.Trading != tt.want {
				t.Fatalf("state after resync=%+v, want running/%s", got, tt.want)
			}
		})
	}
}

func TestReconnectPreservesOperatorHalt(t *testing.T) {
	fmarket := runtimetest.NewFakeMarket()
	fexec := runtimetest.NewFakeExec()
	node := runtime.NewNode(runtime.Clients{Market: fmarket, Execution: fexec}, nil, "halt-reconnect")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)

	node.Halt("operator")
	if _, err := node.Reconnect(context.Background()); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if got := node.State(); got.Node != lifecycle.NodeRunning || got.Trading != lifecycle.TradingHalted {
		t.Fatalf("state after reconnect=%+v, want running/halted", got)
	}
}

func TestPartialResyncDoesNotReactivateTrading(t *testing.T) {
	fexec := &partialMassStatusExec{FakeExec: runtimetest.NewFakeExec()}
	node := runtime.NewNode(runtime.Clients{Execution: fexec}, nil, "partial")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)

	fexec.partial.Store(true)
	rep, err := node.Resync(context.Background())
	if err != nil {
		t.Fatalf("partial resync: %v", err)
	}
	if !rep.Partial {
		t.Fatalf("report=%+v, want partial evidence", rep)
	}
	if got := node.State(); got.Trading == lifecycle.TradingActive {
		t.Fatalf("state after partial resync=%+v, partial evidence must keep trading restricted", got)
	}
}

func TestStartupPartialReconciliationNeverActivatesTrading(t *testing.T) {
	fexec := &partialMassStatusExec{FakeExec: runtimetest.NewFakeExec()}
	fexec.partial.Store(true)
	node := runtime.NewNode(runtime.Clients{Execution: fexec}, nil, "startup-partial")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)

	if got := node.State(); got.Trading == lifecycle.TradingActive {
		t.Fatalf("state after partial startup reconciliation=%+v, incomplete startup evidence must keep trading restricted", got)
	}
}

func TestBlockingReconciliationFindingDoesNotReactivateTrading(t *testing.T) {
	fexec := &partialMassStatusExec{FakeExec: runtimetest.NewFakeExec()}
	fexec.SetAccountID("blocking")
	clk := clock.NewRealClock()
	node := runtime.NewNode(runtime.Clients{Execution: fexec}, clk, "blocking")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)

	generatedAt := clk.Now().UTC()
	mass := model.NewExecutionMassStatus("FAKE", "blocking", generatedAt)
	if err := mass.AddFillReport(model.FillReport{
		Venue:      "FAKE",
		AccountID:  "blocking",
		ReportedAt: generatedAt,
		Fill: model.Fill{
			AccountID:    "blocking",
			InstrumentID: model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindPerp},
			ClientID:     "unknown-order",
			TradeID:      "unmatched-fill",
			Side:         enums.SideBuy,
			Price:        decimal.NewFromInt(100),
			Quantity:     decimal.NewFromInt(1),
			Timestamp:    generatedAt,
		},
	}); err != nil {
		t.Fatalf("add fill report: %v", err)
	}
	fexec.mass.Store(mass)

	rep, err := node.Resync(context.Background())
	if err != nil {
		t.Fatalf("blocking resync: %v", err)
	}
	if len(rep.Findings) == 0 || !rep.Findings[len(rep.Findings)-1].Blocking {
		t.Fatalf("report findings=%+v, want blocking finding", rep.Findings)
	}
	if got := node.State(); got.Trading == lifecycle.TradingActive {
		t.Fatalf("state after blocking resync=%+v, blocking evidence must keep trading restricted", got)
	}
}

func TestSuccessfulResyncRestoresActiveTrading(t *testing.T) {
	fexec := runtimetest.NewFakeExec()
	node := runtime.NewNode(runtime.Clients{Execution: fexec}, nil, "safe")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)

	rep, err := node.Resync(context.Background())
	if err != nil {
		t.Fatalf("resync: %v", err)
	}
	if rep.Partial || len(rep.Findings) != 0 {
		t.Fatalf("safe reconciliation report=%+v", rep)
	}
	if got := node.State(); got.Node != lifecycle.NodeRunning || got.Trading != lifecycle.TradingActive {
		t.Fatalf("state after safe resync=%+v, want running/active", got)
	}
}
