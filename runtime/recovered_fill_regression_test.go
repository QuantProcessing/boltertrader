package runtime

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/journal"
	"github.com/QuantProcessing/boltertrader/runtime/observ"
	"github.com/shopspring/decimal"
)

type failCursorJournal struct {
	*journal.MemoryJournal
	err error
}

func (j *failCursorJournal) CommitReconciliationCursor(context.Context, journal.ReconciliationCursor) error {
	return j.err
}

type scriptedRecoveredFillExec struct {
	*recoveredFillExec
	mu      sync.Mutex
	masses  []*model.ExecutionMassStatus
	queries []model.MassStatusQuery
	next    int
}

func newScriptedRecoveredFillExec(masses ...*model.ExecutionMassStatus) *scriptedRecoveredFillExec {
	first := model.NewExecutionMassStatus("RECOVERY", "acct", time.Time{})
	if len(masses) > 0 {
		first = masses[0]
	}
	return &scriptedRecoveredFillExec{
		recoveredFillExec: &recoveredFillExec{mass: first, events: make(chan contract.ExecEnvelope, 4)},
		masses:            masses,
	}
}

func (e *scriptedRecoveredFillExec) Capabilities() contract.Capabilities {
	return contract.Capabilities{Venue: "RECOVERY", Reports: contract.ReportCapabilities{FillHistory: true}}
}

func (e *scriptedRecoveredFillExec) GenerateExecutionMassStatus(_ context.Context, query model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.queries = append(e.queries, query)
	if len(e.masses) == 0 {
		return model.NewExecutionMassStatus("RECOVERY", "acct", query.Until), nil
	}
	index := e.next
	if index >= len(e.masses) {
		index = len(e.masses) - 1
	} else {
		e.next++
	}
	clone := e.masses[index].Clone()
	return &clone, nil
}

func recoveryOrder(id model.InstrumentID, clientID, venueID, qty string, at time.Time) model.Order {
	return model.Order{
		Request: model.OrderRequest{
			AccountID: "acct", InstrumentID: id, ClientID: clientID,
			Side: enums.SideBuy, Type: enums.TypeLimit, TIF: enums.TifGTC,
			Quantity: decimal.RequireFromString(qty), Price: decimal.NewFromInt(100), PositionSide: enums.PosNet,
		},
		VenueOrderID: venueID, Status: enums.StatusNew, CreatedAt: at, UpdatedAt: at,
	}
}

func TestRecoveredFillDoesNotDoubleCountCumulativeOrderSnapshot(t *testing.T) {
	at := time.Date(2026, 7, 13, 11, 0, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	order := recoveryOrder(id, "cumulative-client", "cumulative-venue", "2", at)
	order.Status = enums.StatusPartiallyFilled
	order.FilledQty = decimal.NewFromInt(1)
	order.AvgFillPrice = decimal.NewFromInt(100)
	fill := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: order.Request.ClientID,
		VenueOrderID: order.VenueOrderID, TradeID: "cumulative-trade", Side: enums.SideBuy,
		Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: at,
	}
	mass := model.NewExecutionMassStatus("RECOVERY", "acct", at.Add(time.Second))
	if err := mass.AddOrderReport(model.OrderStatusReport{Venue: "RECOVERY", AccountID: "acct", Order: order, ReportedAt: at.Add(time.Second)}); err != nil {
		t.Fatalf("add order report: %v", err)
	}
	if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: fill, ReportedAt: at.Add(time.Second)}); err != nil {
		t.Fatalf("add fill report: %v", err)
	}
	node := NewNode(Clients{Execution: newScriptedRecoveredFillExec(mass)}, clock.NewSimulatedClock(at), "cumulative")

	report, err := node.Resync(context.Background())
	if err != nil {
		t.Fatalf("resync: %v", err)
	}
	got, ok := node.Cache.OrderForAccount("acct", order.Request.ClientID)
	if !ok {
		t.Fatal("reconciled order missing")
	}
	if report.FillsApplied != 1 || !got.FilledQty.Equal(decimal.NewFromInt(1)) || got.Status != enums.StatusPartiallyFilled {
		t.Fatalf("report=%+v order=%+v, cumulative snapshot fill was counted twice", report, got)
	}
	if qty := node.Portfolio.NetQtyForAccount("acct", id, enums.PosNet); !qty.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("portfolio qty=%s, want one recovered fill", qty)
	}
}

func TestCumulativeSnapshotDoesNotRegressPreviouslyAppliedFills(t *testing.T) {
	at := time.Date(2026, 7, 13, 11, 5, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	order := recoveryOrder(id, "nonregression-client", "nonregression-venue", "3", at)
	callbacks := 0
	mass := model.NewExecutionMassStatus("RECOVERY", "acct", at.Add(time.Minute))
	snapshot := order
	snapshot.Status = enums.StatusPartiallyFilled
	snapshot.FilledQty = decimal.NewFromInt(1)
	snapshot.AvgFillPrice = decimal.NewFromInt(100)
	snapshot.UpdatedAt = time.Time{}
	if err := mass.AddOrderReport(model.OrderStatusReport{Venue: "RECOVERY", AccountID: "acct", Order: snapshot, ReportedAt: at.Add(time.Minute)}); err != nil {
		t.Fatalf("add order report: %v", err)
	}
	second := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: order.Request.ClientID,
		VenueOrderID: order.VenueOrderID, TradeID: "nonregression-2", Side: enums.SideBuy,
		Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: at.Add(2 * time.Second),
	}
	if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: second, ReportedAt: second.Timestamp}); err != nil {
		t.Fatalf("add fill report: %v", err)
	}
	node := NewNode(Clients{Execution: newScriptedRecoveredFillExec(mass)}, clock.NewSimulatedClock(at), "nonregression", WithOnFill(func(model.Fill) { callbacks++ }))
	node.Cache.UpsertOrder(order)
	for i, tradeID := range []string{"nonregression-1", "nonregression-2"} {
		fill := second
		fill.TradeID = tradeID
		fill.Timestamp = at.Add(time.Duration(i+1) * time.Second)
		node.onExec(contract.NewExecEnvelope(contract.FillEvent{Fill: fill}))
	}
	if callbacks != 2 {
		t.Fatalf("live callbacks=%d, want 2", callbacks)
	}

	report, err := node.Resync(context.Background())
	if err != nil {
		t.Fatalf("resync: %v", err)
	}
	got, ok := node.Cache.OrderForAccount("acct", order.Request.ClientID)
	if !ok || !got.FilledQty.Equal(decimal.NewFromInt(2)) || got.Status != enums.StatusPartiallyFilled {
		t.Fatalf("report=%+v order=%+v, older cumulative snapshot regressed applied fills", report, got)
	}
	if callbacks != 2 || report.FillsDuplicate != 1 {
		t.Fatalf("callbacks=%d report=%+v, duplicate fill was re-emitted", callbacks, report)
	}
}

func TestCumulativeSnapshotReappliesOnlyUncoveredRecoveredFills(t *testing.T) {
	at := time.Date(2026, 7, 13, 11, 7, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	snapshot := recoveryOrder(id, "partial-coverage-client", "partial-coverage-venue", "3", at)
	snapshot.Status = enums.StatusPartiallyFilled
	snapshot.FilledQty = decimal.NewFromInt(1)
	snapshot.AvgFillPrice = decimal.NewFromInt(100)
	mass := model.NewExecutionMassStatus("RECOVERY", "acct", at.Add(time.Minute))
	if err := mass.AddOrderReport(model.OrderStatusReport{Venue: "RECOVERY", AccountID: "acct", Order: snapshot, ReportedAt: at.Add(time.Minute)}); err != nil {
		t.Fatalf("add order report: %v", err)
	}
	for i, price := range []int64{100, 200} {
		fill := model.Fill{
			AccountID: "acct", InstrumentID: id, ClientID: snapshot.Request.ClientID,
			VenueOrderID: snapshot.VenueOrderID, TradeID: fmt.Sprintf("partial-coverage-%d", i+1),
			Side: enums.SideBuy, Price: decimal.NewFromInt(price), Quantity: decimal.NewFromInt(1),
			Timestamp: at.Add(time.Duration(i+1) * time.Second),
		}
		if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: fill, ReportedAt: fill.Timestamp}); err != nil {
			t.Fatalf("add fill report: %v", err)
		}
	}
	node := NewNode(Clients{Execution: newScriptedRecoveredFillExec(mass)}, clock.NewSimulatedClock(at), "partial-coverage")
	report, err := node.Resync(context.Background())
	if err != nil {
		t.Fatalf("resync: %v", err)
	}
	got, ok := node.Cache.OrderForAccount("acct", snapshot.Request.ClientID)
	if !ok || !got.FilledQty.Equal(decimal.NewFromInt(2)) || got.Status != enums.StatusPartiallyFilled {
		t.Fatalf("report=%+v order=%+v, snapshot-covered fill was added twice", report, got)
	}
	if qty := node.Portfolio.NetQtyForAccount("acct", id, enums.PosNet); !qty.Equal(decimal.NewFromInt(2)) {
		t.Fatalf("portfolio qty=%s, want both recovered fills", qty)
	}
}

func TestLiveFillIdentityEnrichmentRemainsDuplicate(t *testing.T) {
	at := time.Date(2026, 7, 13, 11, 10, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "ETH-USDT", Kind: enums.KindPerp}
	order := recoveryOrder(id, "identity-client", "identity-venue", "2", at)
	recovered := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: order.Request.ClientID,
		VenueOrderID: order.VenueOrderID, TradeID: "identity-trade", Side: enums.SideBuy,
		Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: at,
	}
	mass := model.NewExecutionMassStatus("RECOVERY", "acct", at.Add(time.Minute))
	if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: recovered, ReportedAt: at}); err != nil {
		t.Fatalf("add fill report: %v", err)
	}
	callbacks := 0
	node := NewNode(Clients{Execution: newScriptedRecoveredFillExec(mass)}, clock.NewSimulatedClock(at), "identity", WithOnFill(func(model.Fill) { callbacks++ }))
	node.Cache.UpsertOrder(order)
	live := recovered
	live.ClientID = ""
	node.onExec(contract.NewExecEnvelope(contract.FillEvent{Fill: live}))

	report, err := node.Resync(context.Background())
	if err != nil {
		t.Fatalf("resync: %v", err)
	}
	if callbacks != 1 || report.FillsApplied != 0 || report.FillsDuplicate != 1 {
		t.Fatalf("callbacks=%d report=%+v, identity enrichment re-applied one venue trade", callbacks, report)
	}
}

func TestRestartSeedsDurableRecoveredFillDedupe(t *testing.T) {
	at := time.Date(2026, 7, 13, 11, 20, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "SOL-USDT", Kind: enums.KindPerp}
	order := recoveryOrder(id, "restart-client", "restart-venue", "2", at)
	fill := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: order.Request.ClientID,
		VenueOrderID: order.VenueOrderID, TradeID: "restart-trade", Side: enums.SideBuy,
		Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: at,
	}
	mass := model.NewExecutionMassStatus("RECOVERY", "acct", at)
	if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: fill, ReportedAt: at}); err != nil {
		t.Fatalf("add fill report: %v", err)
	}
	j := journal.NewMemory()
	firstCallbacks := 0
	first := NewNode(Clients{Execution: newScriptedRecoveredFillExec(mass)}, clock.NewSimulatedClock(at), "restart",
		WithJournal(j), WithOnFill(func(model.Fill) { firstCallbacks++ }))
	first.Cache.UpsertOrder(order)
	if _, err := first.Resync(context.Background()); err != nil {
		t.Fatalf("first resync: %v", err)
	}
	if firstCallbacks != 1 {
		t.Fatalf("first callbacks=%d, want 1", firstCallbacks)
	}

	secondCallbacks := 0
	second := NewNode(Clients{Execution: newScriptedRecoveredFillExec(mass)}, clock.NewSimulatedClock(at), "restart",
		WithJournal(j), WithOnFill(func(model.Fill) { secondCallbacks++ }))
	second.Cache.UpsertOrder(order)
	report, err := second.Resync(context.Background())
	if err != nil {
		t.Fatalf("second resync: %v", err)
	}
	if secondCallbacks != 0 || report.FillsApplied != 0 || report.FillsDuplicate != 1 {
		t.Fatalf("second callbacks=%d report=%+v, durable terminal fill was emitted again", secondCallbacks, report)
	}
}

func TestRestartSeedsAppliedFillWhenCursorCommitFailed(t *testing.T) {
	at := time.Date(2026, 7, 13, 11, 25, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "SOL-USDT", Kind: enums.KindPerp}
	order := recoveryOrder(id, "failed-cursor-client", "failed-cursor-venue", "2", at)
	fill := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: order.Request.ClientID,
		VenueOrderID: order.VenueOrderID, TradeID: "failed-cursor-trade", Side: enums.SideBuy,
		Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: at,
	}
	mass := model.NewExecutionMassStatus("RECOVERY", "acct", at)
	if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: fill, ReportedAt: at}); err != nil {
		t.Fatalf("add fill report: %v", err)
	}
	underlying := journal.NewMemory()
	fail := errors.New("injected cursor commit failure")
	firstCallbacks := 0
	first := NewNode(Clients{Execution: newScriptedRecoveredFillExec(mass)}, clock.NewSimulatedClock(at), "failed-cursor",
		WithJournal(&failCursorJournal{MemoryJournal: underlying, err: fail}),
		WithOnFill(func(model.Fill) { firstCallbacks++ }))
	first.Cache.UpsertOrder(order)
	if _, err := first.Resync(context.Background()); !errors.Is(err, fail) {
		t.Fatalf("first resync err=%v, want %v", err, fail)
	}
	if firstCallbacks != 1 {
		t.Fatalf("first callbacks=%d, want 1", firstCallbacks)
	}

	secondCallbacks := 0
	second := NewNode(Clients{Execution: newScriptedRecoveredFillExec(mass)}, clock.NewSimulatedClock(at), "failed-cursor",
		WithJournal(underlying), WithOnFill(func(model.Fill) { secondCallbacks++ }))
	second.Cache.UpsertOrder(order)
	report, err := second.Resync(context.Background())
	if err != nil {
		t.Fatalf("second resync: %v", err)
	}
	if secondCallbacks != 0 || report.FillsApplied != 0 || report.FillsDuplicate != 1 {
		t.Fatalf("second callbacks=%d report=%+v, durable fill was re-applied after cursor failure", secondCallbacks, report)
	}
}

func TestRecoveredFillsApplyInVenueTimestampOrder(t *testing.T) {
	at := time.Date(2026, 7, 13, 11, 30, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "XRP-USDT", Kind: enums.KindPerp}
	for attempt := 0; attempt < 40; attempt++ {
		orders := []model.Order{
			recoveryOrder(id, "buy-100", "venue-buy-100", "1", at),
			recoveryOrder(id, "buy-200", "venue-buy-200", "1", at),
			recoveryOrder(id, "sell-150", "venue-sell-150", "1", at),
		}
		orders[2].Request.Side = enums.SideSell
		fills := []model.Fill{
			{AccountID: "acct", InstrumentID: id, ClientID: "buy-100", VenueOrderID: "venue-buy-100", TradeID: "buy-100", Side: enums.SideBuy, Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: at},
			{AccountID: "acct", InstrumentID: id, ClientID: "buy-200", VenueOrderID: "venue-buy-200", TradeID: "buy-200", Side: enums.SideBuy, Price: decimal.NewFromInt(200), Quantity: decimal.NewFromInt(1), Timestamp: at.Add(time.Second)},
			{AccountID: "acct", InstrumentID: id, ClientID: "sell-150", VenueOrderID: "venue-sell-150", TradeID: "sell-150", Side: enums.SideSell, Price: decimal.NewFromInt(150), Quantity: decimal.NewFromInt(1), Timestamp: at.Add(2 * time.Second)},
		}
		mass := model.NewExecutionMassStatus("RECOVERY", "acct", at.Add(time.Minute))
		for _, fill := range fills {
			if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: fill, ReportedAt: fill.Timestamp}); err != nil {
				t.Fatalf("add fill report: %v", err)
			}
		}
		node := NewNode(Clients{Execution: newScriptedRecoveredFillExec(mass)}, clock.NewSimulatedClock(at), "ordered")
		for _, order := range orders {
			node.Cache.UpsertOrder(order)
		}
		if _, err := node.Resync(context.Background()); err != nil {
			t.Fatalf("attempt %d resync: %v", attempt, err)
		}
		pnl := node.Portfolio.RealizedPnLForAccount("acct")
		avg := node.Portfolio.AvgPriceForAccount("acct", id, enums.PosNet)
		if !pnl.IsZero() || !avg.Equal(decimal.NewFromInt(150)) {
			t.Fatalf("attempt %d realized=%s avg=%s, want venue-time result 0/150", attempt, pnl, avg)
		}
	}
}

type startupOrderObserver struct {
	observ.Base
	events chan string
}

func (o *startupOrderObserver) OnNodeStart()      { o.events <- "start" }
func (o *startupOrderObserver) OnFill(model.Fill) { o.events <- "fill" }

func TestStartupObserverReceivesFillAfterNodeStart(t *testing.T) {
	at := time.Date(2026, 7, 13, 11, 40, 0, 0, time.UTC)
	id := model.InstrumentID{Venue: "RECOVERY", Symbol: "ADA-USDT", Kind: enums.KindPerp}
	order := recoveryOrder(id, "observer-client", "observer-venue", "1", at)
	fill := model.Fill{
		AccountID: "acct", InstrumentID: id, ClientID: order.Request.ClientID,
		VenueOrderID: order.VenueOrderID, TradeID: "observer-trade", Side: enums.SideBuy,
		Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1), Timestamp: at,
	}
	mass := model.NewExecutionMassStatus("RECOVERY", "acct", at)
	if err := mass.AddFillReport(model.FillReport{Venue: "RECOVERY", AccountID: "acct", Fill: fill, ReportedAt: at}); err != nil {
		t.Fatalf("add fill report: %v", err)
	}
	observer := &startupOrderObserver{events: make(chan string, 2)}
	node := NewNode(Clients{Execution: newScriptedRecoveredFillExec(mass)}, clock.NewSimulatedClock(at), "observer", WithObserver(observer))
	node.Cache.UpsertOrder(order)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		node.Run(ctx)
		close(done)
	}()

	var got []string
	for len(got) < 2 {
		select {
		case event := <-observer.events:
			got = append(got, event)
		case <-time.After(time.Second):
			cancel()
			t.Fatalf("observer events=%v, want [start fill]", got)
		}
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("node did not stop")
	}
	if got[0] != "start" || got[1] != "fill" {
		t.Fatalf("observer events=%v, want [start fill]", got)
	}
}
