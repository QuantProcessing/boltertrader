package runtime_test

import (
	"context"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
	"github.com/QuantProcessing/boltertrader/runtime/strategy"
)

type mirrorStrategy struct {
	strategy.Base
	starts atomic.Int64
	fills  atomic.Int64
	stops  atomic.Int64
}

func (s *mirrorStrategy) OnStart(*strategy.Context) { s.starts.Add(1) }
func (s *mirrorStrategy) OnFill(*strategy.Context, model.Fill) {
	s.fills.Add(1)
}
func (s *mirrorStrategy) OnStop(*strategy.Context) { s.stops.Add(1) }

func TestOfflineRuntimeMirrorAndBoundedReconcile(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	fexec := runtimetest.NewFakeExec()
	facct := runtimetest.NewFakeAccount()
	obs := &recordingObserver{}
	strat := &mirrorStrategy{}

	node := runtime.NewNode(
		runtime.Clients{Execution: fexec, Account: facct},
		clk,
		"mirror",
		runtime.WithStrategy(strat),
		runtime.WithObserver(obs),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		node.Run(ctx)
		close(done)
	}()
	waitUntil(t, func() bool {
		return strat.starts.Load() == 1 && obs.starts.Load() == 1
	}, "timed out waiting for node start")

	order, err := node.Exec.Submit(ctx, model.OrderRequest{
		InstrumentID: inst,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("2"),
		Price:        d("100"),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if order.Status != enums.StatusNew {
		t.Fatalf("ack status=%v, want NEW", order.Status)
	}

	filled := *order
	filled.Status = enums.StatusFilled
	filled.FilledQty = d("2")
	filled.AvgFillPrice = d("100")
	filled.UpdatedAt = start.Add(time.Second)
	fexec.EmitOrder(filled)
	fexec.EmitFill(model.Fill{
		InstrumentID: inst,
		VenueOrderID: order.VenueOrderID,
		ClientID:     order.Request.ClientID,
		TradeID:      "t-1",
		Side:         enums.SideBuy,
		Liquidity:    enums.LiqMaker,
		Price:        d("100"),
		Quantity:     d("2"),
		Fee:          d("0.1"),
		FeeCurrency:  "USDT",
		Timestamp:    start.Add(time.Second),
	})
	facct.EmitBalance(model.AccountBalance{Currency: "USDT", Total: d("9999.9"), Available: d("9000"), UpdatedAt: start.Add(time.Second)})
	facct.EmitPosition(model.Position{InstrumentID: inst, Side: enums.PosNet, Quantity: d("2"), EntryPrice: d("100"), UpdatedAt: start.Add(time.Second)})
	waitUntil(t, func() bool {
		m := node.Metrics()
		return m.OrdersSeen == 1 && m.FillsSeen == 1 && m.Positions == 1
	}, "timed out waiting for fake venue events")

	if o, ok := node.Cache.Order(order.Request.ClientID); !ok || o.Status != enums.StatusFilled {
		t.Fatalf("order not filled in cache: ok=%v status=%v", ok, o.Status)
	}
	if got := node.Portfolio.NetQty(inst, enums.PosNet); !got.Equal(d("2")) {
		t.Fatalf("netQty=%s, want 2", got)
	}
	if got := node.Portfolio.UnrealizedPnL(inst, enums.PosNet, d("110")); !got.Equal(d("20")) {
		t.Fatalf("unrealized@110=%s, want 20", got)
	}
	if got := node.Portfolio.Fees(); !got.Equal(d("0.1")) {
		t.Fatalf("fees=%s, want 0.1", got)
	}
	if b, ok := node.Cache.Balance("USDT"); !ok || !b.Total.Equal(d("9999.9")) || !b.Available.Equal(d("9000")) {
		t.Fatalf("balance not mirrored: ok=%v balance=%+v", ok, b)
	}
	if p, ok := node.Cache.Position(inst, enums.PosNet); !ok || !p.Quantity.Equal(d("2")) || !p.EntryPrice.Equal(d("100")) {
		t.Fatalf("position not mirrored: ok=%v position=%+v", ok, p)
	}
	if strat.starts.Load() != 1 || strat.fills.Load() != 1 {
		t.Fatalf("strategy callbacks starts=%d fills=%d, want 1/1", strat.starts.Load(), strat.fills.Load())
	}
	if obs.starts.Load() != 1 || obs.orders.Load() != 1 || obs.fills.Load() != 1 {
		t.Fatalf("observer callbacks starts=%d orders=%d fills=%d, want 1/1/1", obs.starts.Load(), obs.orders.Load(), obs.fills.Load())
	}
	before := node.Metrics()
	if before.OpenOrders != 0 || before.Positions != 1 || before.OrdersSeen != 1 || before.FillsSeen != 1 {
		t.Fatalf("metrics before reconcile=%+v, want open=0 positions=1 orders=1 fills=1", before)
	}

	gapOrder := model.Order{
		Request: model.OrderRequest{
			ClientID:     "gap-1",
			InstrumentID: inst,
			Side:         enums.SideSell,
			Type:         enums.TypeLimit,
			TIF:          enums.TifGTC,
			Quantity:     d("1"),
			Price:        d("120"),
		},
		VenueOrderID: "v-gap-1",
		Status:       enums.StatusNew,
	}
	node.Cache.UpsertOrder(gapOrder)
	facct.SetSnapshots(
		[]model.AccountBalance{{Currency: "USDT", Total: d("9999.9"), Available: d("9000"), UpdatedAt: start.Add(2 * time.Second)}},
		[]model.Position{{InstrumentID: inst, Side: enums.PosNet, Quantity: d("2"), EntryPrice: d("100"), UpdatedAt: start.Add(2 * time.Second)}},
	)
	fexec.SetOrderStatusReports()

	rep, err := node.Resync(ctx)
	if err != nil {
		t.Fatalf("resync: %v", err)
	}
	if rep.BalancesUpdated != 1 || rep.PositionsUpdated != 1 || rep.OrdersClosedUnknown != 1 || rep.OrdersCleared != 0 {
		t.Fatalf("reconcile report=%+v, want balances=1 positions=1 closedUnknown=1 cleared=0", rep)
	}
	if o, ok := node.Cache.Order("gap-1"); !ok || o.Status != enums.StatusUnknown {
		t.Fatalf("gap order status=%v ok=%v, want unknown closed", o.Status, ok)
	}
	for _, o := range node.Cache.OpenOrders() {
		if o.Request.ClientID == "gap-1" {
			t.Fatal("gap order still appears in open orders")
		}
	}
	after := node.Metrics()
	if after.FillsSeen != before.FillsSeen || !after.RealizedPnL.Equal(before.RealizedPnL) || !after.Fees.Equal(before.Fees) {
		t.Fatalf("reconcile invented execution impact: before=%+v after=%+v", before, after)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("node did not stop")
	}
	if strat.stops.Load() != 1 || obs.stops.Load() != 1 {
		t.Fatalf("stop callbacks strategy=%d observer=%d, want 1/1", strat.stops.Load(), obs.stops.Load())
	}
}
