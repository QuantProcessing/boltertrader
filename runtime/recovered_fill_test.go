package runtime

import (
	"context"
	"encoding/json"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
	"github.com/QuantProcessing/boltertrader/runtime/strategy"
	"github.com/shopspring/decimal"
)

func TestRecoveredFillUsesNodeApplySemanticsExactlyOnce(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 7, 13, 3, 0, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	order := model.Order{
		Request: model.OrderRequest{
			AccountID:    "acct",
			InstrumentID: id,
			ClientID:     "recovered-client",
			Side:         enums.SideBuy,
			Type:         enums.TypeLimit,
			TIF:          enums.TifGTC,
			Quantity:     decimal.NewFromInt(1),
			Price:        decimal.NewFromInt(100),
			PositionSide: enums.PosNet,
		},
		VenueOrderID: "recovered-venue",
		Status:       enums.StatusNew,
		CreatedAt:    at,
		UpdatedAt:    at,
	}
	fill := model.Fill{
		AccountID:    "acct",
		InstrumentID: id,
		ClientID:     order.Request.ClientID,
		VenueOrderID: order.VenueOrderID,
		TradeID:      "recovered-trade",
		Side:         enums.SideBuy,
		Price:        decimal.NewFromInt(100),
		Quantity:     decimal.NewFromInt(1),
		Timestamp:    at,
	}
	mass := model.NewExecutionMassStatus(id.Venue, "acct", at)
	for i := 0; i < 2; i++ {
		if err := mass.AddFillReport(model.FillReport{Venue: id.Venue, AccountID: "acct", Fill: fill, ReportedAt: at}); err != nil {
			t.Fatalf("add fill report: %v", err)
		}
	}

	exec := &recoveredFillExec{mass: mass, events: make(chan contract.ExecEnvelope, 1)}
	callbacks := 0
	node := NewNode(Clients{Execution: exec}, clock.NewSimulatedClock(at), "recovery",
		WithOnFill(func(got model.Fill) {
			callbacks++
			if got.TradeID != fill.TradeID {
				t.Fatalf("callback fill=%+v, want trade %s", got, fill.TradeID)
			}
		}),
	)
	node.Cache.UpsertOrder(order)

	report, err := node.Resync(ctx)
	if err != nil {
		t.Fatalf("first resync: %v", err)
	}
	if report.FillsApplied != 1 || report.FillsDuplicate != 1 {
		t.Fatalf("first report=%+v, want one applied and one duplicate fill", report)
	}
	if callbacks != 1 {
		t.Fatalf("fill callbacks=%d, want 1", callbacks)
	}
	if qty := node.Portfolio.NetQtyForAccount("acct", id, enums.PosNet); !qty.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("portfolio qty=%s, want 1", qty)
	}
	if metrics := node.Metrics(); metrics.FillsSeen != 1 {
		t.Fatalf("fills seen=%d, want 1", metrics.FillsSeen)
	}

	if _, err := node.Resync(ctx); err != nil {
		t.Fatalf("second resync: %v", err)
	}
	if callbacks != 1 {
		t.Fatalf("fill callbacks after duplicate recovery=%d, want 1", callbacks)
	}
	if qty := node.Portfolio.NetQtyForAccount("acct", id, enums.PosNet); !qty.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("portfolio qty after duplicate recovery=%s, want 1", qty)
	}
}

func TestLiveFillThenReconciliationIsCountedAsDuplicate(t *testing.T) {
	ctx := context.Background()
	at := time.Date(2026, 7, 13, 3, 30, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "ETH-USDT", Kind: enums.KindPerp}
	order := model.Order{
		Request: model.OrderRequest{
			AccountID:    "acct",
			InstrumentID: id,
			ClientID:     "live-client",
			Side:         enums.SideBuy,
			Type:         enums.TypeLimit,
			TIF:          enums.TifGTC,
			Quantity:     decimal.NewFromInt(1),
			Price:        decimal.NewFromInt(200),
			PositionSide: enums.PosNet,
		},
		VenueOrderID: "live-venue",
		Status:       enums.StatusNew,
		CreatedAt:    at,
		UpdatedAt:    at,
	}
	fill := model.Fill{
		AccountID:    "acct",
		InstrumentID: id,
		ClientID:     order.Request.ClientID,
		VenueOrderID: order.VenueOrderID,
		TradeID:      "live-trade",
		Side:         enums.SideBuy,
		Price:        decimal.NewFromInt(200),
		Quantity:     decimal.NewFromInt(1),
		Timestamp:    at,
	}
	mass := model.NewExecutionMassStatus(id.Venue, "acct", at.Add(time.Minute))
	if err := mass.AddFillReport(model.FillReport{Venue: id.Venue, AccountID: "acct", Fill: fill, ReportedAt: at.Add(time.Minute)}); err != nil {
		t.Fatalf("add fill report: %v", err)
	}

	exec := &recoveredFillExec{mass: mass, events: make(chan contract.ExecEnvelope, 1)}
	j := journal.NewMemory()
	callbacks := 0
	node := NewNode(Clients{Execution: exec}, clock.NewSimulatedClock(at), "recovery",
		WithJournal(j),
		WithOnFill(func(model.Fill) { callbacks++ }),
	)
	node.Cache.UpsertOrder(order)
	node.onExec(contract.NewExecEnvelopeWithMeta(contract.FillEvent{Fill: fill}, contract.EventMeta{
		Source: contract.SourceAdapterStream,
		Flags:  contract.EventFlagFromStream,
	}))
	if callbacks != 1 {
		t.Fatalf("live fill callbacks=%d, want 1", callbacks)
	}

	report, err := node.Resync(ctx)
	if err != nil {
		t.Fatalf("resync: %v", err)
	}
	if report.FillsApplied != 0 || report.FillsDuplicate != 1 {
		t.Fatalf("report=%+v, want live fill counted only as duplicate", report)
	}
	if callbacks != 1 {
		t.Fatalf("callbacks after reconciliation=%d, want live callback only", callbacks)
	}
	if qty := node.Portfolio.NetQtyForAccount("acct", id, enums.PosNet); !qty.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("portfolio qty=%s, want 1", qty)
	}
	appliedRecords := 0
	var appliedRecordID string
	var committed journal.ReconciliationCursor
	for _, record := range j.Records() {
		switch record.Type {
		case journal.RecordAppliedEvent:
			appliedRecords++
			appliedRecordID = record.RecordID
		case journal.RecordReconciliationCursor:
			if err := json.Unmarshal(record.Payload, &committed); err != nil {
				t.Fatalf("decode cursor record: %v", err)
			}
		}
	}
	if appliedRecords != 1 {
		t.Fatalf("applied-event records=%d, want exactly one durable reconciliation record", appliedRecords)
	}
	if len(committed.AppliedEventRecordIDs) != 1 || committed.AppliedEventRecordIDs[0] != appliedRecordID {
		t.Fatalf("cursor dependencies=%v, want [%s]", committed.AppliedEventRecordIDs, appliedRecordID)
	}
}

type recoveredFillStrategy struct {
	strategy.Base
	order  []string
	fills  chan model.Fill
	startQ decimal.Decimal
}

func (s *recoveredFillStrategy) OnStart(ctx *strategy.Context) {
	s.order = append(s.order, "start")
	s.startQ = ctx.Portfolio.NetQtyForAccount("acct", model.InstrumentID{Venue: "RECOVERY", Symbol: "SOL-USDT", Kind: enums.KindPerp}, enums.PosNet)
}

func (s *recoveredFillStrategy) OnFill(_ *strategy.Context, fill model.Fill) {
	s.order = append(s.order, "fill")
	s.fills <- fill
}

func TestStartupRecoveredFillReachesStrategyAfterOnStart(t *testing.T) {
	at := time.Date(2026, 7, 13, 4, 0, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "SOL-USDT", Kind: enums.KindPerp}
	order := model.Order{
		Request: model.OrderRequest{
			AccountID: "acct", InstrumentID: id, ClientID: "startup-client",
			Side: enums.SideBuy, Type: enums.TypeLimit, Quantity: decimal.NewFromInt(2),
			Price: decimal.NewFromInt(50), PositionSide: enums.PosNet,
		},
		VenueOrderID: "startup-venue", Status: enums.StatusNew, CreatedAt: at, UpdatedAt: at,
	}
	fill := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: order.Request.ClientID,
		VenueOrderID: order.VenueOrderID, TradeID: "startup-trade", Side: enums.SideBuy,
		Price: decimal.NewFromInt(50), Quantity: decimal.NewFromInt(2), Timestamp: at,
	}
	mass := model.NewExecutionMassStatus(id.Venue, "acct", at)
	if err := mass.AddFillReport(model.FillReport{Venue: id.Venue, AccountID: "acct", Fill: fill, ReportedAt: at}); err != nil {
		t.Fatalf("add fill report: %v", err)
	}
	exec := &recoveredFillExec{mass: mass, events: make(chan contract.ExecEnvelope, 1)}
	strat := &recoveredFillStrategy{fills: make(chan model.Fill, 1)}
	node := NewNode(Clients{Execution: exec}, clock.NewSimulatedClock(at), "recovery", WithStrategy(strat))
	node.Cache.UpsertOrder(order)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)

	select {
	case got := <-strat.fills:
		if got.TradeID != fill.TradeID {
			t.Fatalf("strategy fill=%+v, want trade %s", got, fill.TradeID)
		}
	case <-time.After(time.Second):
		t.Fatal("startup recovered fill did not reach strategy")
	}
	if len(strat.order) != 2 || strat.order[0] != "start" || strat.order[1] != "fill" {
		t.Fatalf("strategy callback order=%v, want [start fill]", strat.order)
	}
	if !strat.startQ.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("OnStart portfolio qty=%s, want recovered qty 2", strat.startQ)
	}
}

type recoveredFillExec struct {
	mass   *model.ExecutionMassStatus
	events chan contract.ExecEnvelope
}

func (e *recoveredFillExec) AccountID() string { return "acct" }

func (e *recoveredFillExec) Capabilities() contract.Capabilities {
	return contract.Capabilities{
		Venue:   "RECOVERY",
		Reports: contract.ReportCapabilities{FillHistory: true},
	}
}

func (*recoveredFillExec) ValidateSubmit(model.OrderRequest) error { return nil }

func (e *recoveredFillExec) Submit(context.Context, model.OrderRequest) (*model.Order, error) {
	return nil, contract.ErrNotSupported
}

func (e *recoveredFillExec) Cancel(context.Context, model.InstrumentID, string) error {
	return contract.ErrNotSupported
}

func (e *recoveredFillExec) CancelAll(context.Context, model.InstrumentID) error {
	return contract.ErrNotSupported
}

func (e *recoveredFillExec) Modify(context.Context, model.InstrumentID, string, decimal.Decimal, decimal.Decimal) (*model.Order, error) {
	return nil, contract.ErrNotSupported
}

func (e *recoveredFillExec) OpenOrders(context.Context, model.InstrumentID) ([]model.Order, error) {
	return nil, nil
}

func (e *recoveredFillExec) GenerateOrderStatusReports(context.Context, model.OrderStatusReportQuery) ([]model.OrderStatusReport, error) {
	return nil, nil
}

func (e *recoveredFillExec) GenerateOrderStatusReport(context.Context, model.SingleOrderStatusQuery) (*model.OrderStatusReport, error) {
	return nil, nil
}

func (e *recoveredFillExec) GenerateFillReports(context.Context, model.FillReportQuery) ([]model.FillReport, error) {
	return nil, nil
}

func (e *recoveredFillExec) GeneratePositionReports(context.Context, model.PositionReportQuery) ([]model.PositionReport, error) {
	return nil, nil
}

func (e *recoveredFillExec) GenerateExecutionMassStatus(_ context.Context, query model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	clone := e.mass.Clone()
	ids := make([]model.InstrumentID, 0, len(clone.FillReports))
	for _, reports := range clone.FillReports {
		for _, report := range reports {
			ids = append(ids, report.Fill.InstrumentID)
		}
	}
	ids = model.NormalizeInstrumentIDs(ids)
	clone.ClientID = query.ClientID
	clone.OpenOrdersCoverage = model.NewSnapshotCoverage(model.CoverageComplete, clone.AccountID, query.ClientID, ids, query.Until)
	clone.FillsCoverage = model.NewFillCoverage(model.CoverageComplete, clone.AccountID, query.ClientID, ids, query.Since, query.Until)
	clone.PositionsCoverage = model.ReportCoverage{State: model.CoverageNotRequested}
	if err := clone.ValidateFor(query); err != nil {
		return nil, err
	}
	return &clone, nil
}

func (e *recoveredFillExec) Events() <-chan contract.ExecEnvelope { return e.events }

func (e *recoveredFillExec) Close() error { return nil }
