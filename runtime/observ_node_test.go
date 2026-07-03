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
	"github.com/QuantProcessing/boltertrader/runtime/observ"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
	"github.com/QuantProcessing/boltertrader/runtime/strategy"
	"github.com/shopspring/decimal"
)

// recordingObserver counts observer callbacks.
type recordingObserver struct {
	observ.Base
	starts, stops, orders, fills, rejects atomic.Int64
}

func (o *recordingObserver) OnNodeStart()            { o.starts.Add(1) }
func (o *recordingObserver) OnNodeStop()             { o.stops.Add(1) }
func (o *recordingObserver) OnOrder(model.Order)     { o.orders.Add(1) }
func (o *recordingObserver) OnFill(model.Fill)       { o.fills.Add(1) }
func (o *recordingObserver) OnReject(string, string) { o.rejects.Add(1) }

// buyOnFirstTrade buys once at market on the first trade it sees.
type buyOnFirstTrade struct {
	strategy.Base
	done bool
}

func (s *buyOnFirstTrade) OnTrade(c *strategy.Context, t model.TradeTick) {
	if !s.done {
		s.done = true
		_, _ = c.Buy(t.InstrumentID, decimal.RequireFromString("1"), decimal.Zero) // market
	}
}

// TestObserverAndMetrics drives a fake live venue and asserts the observer
// receives lifecycle/order/fill/reject callbacks and Metrics reflects them.
func TestObserverAndMetrics(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	fmarket := runtimetest.NewFakeMarket()
	fexec := runtimetest.NewFakeExec()
	id := model.InstrumentID{Venue: "BT", Symbol: "BTC-USDT", Kind: enums.KindPerp}

	obs := &recordingObserver{}
	node := runtime.NewNode(
		runtime.Clients{Market: fmarket, Execution: fexec},
		clk, "obs",
		runtime.WithStrategy(&buyOnFirstTrade{}),
		runtime.WithObserver(obs),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		node.Run(ctx)
		close(done)
	}()
	waitUntil(t, func() bool { return obs.starts.Load() == 1 }, "timed out waiting for node start")

	fmarket.EmitTrade(model.TradeTick{InstrumentID: id, Price: decimal.RequireFromString("100"), Quantity: decimal.RequireFromString("1"), Timestamp: start.Add(time.Second)})
	waitUntil(t, func() bool { return len(node.Cache.Orders()) == 1 }, "strategy order did not reach cache")

	order := node.Cache.Orders()[0]
	filledOrder := order
	filledOrder.Status = enums.StatusFilled
	filledOrder.FilledQty = decimal.RequireFromString("1")
	filledOrder.AvgFillPrice = decimal.RequireFromString("100")
	filledOrder.UpdatedAt = start.Add(2 * time.Second)
	fexec.EmitOrder(filledOrder)
	fexec.EmitFill(model.Fill{
		InstrumentID: id,
		VenueOrderID: order.VenueOrderID,
		ClientID:     order.Request.ClientID,
		TradeID:      "obs-fill-1",
		Side:         enums.SideBuy,
		Liquidity:    enums.LiqTaker,
		Price:        decimal.RequireFromString("100"),
		Quantity:     decimal.RequireFromString("1"),
		Timestamp:    start.Add(2 * time.Second),
	})
	waitUntil(t, func() bool { return obs.fills.Load() == 1 }, "timed out waiting for fill callback")

	fexec.EmitReject("rejected-client", "definitive fake rejection")
	waitUntil(t, func() bool { return obs.rejects.Load() == 1 }, "timed out waiting for reject callback")

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("node did not stop")
	}

	if obs.starts.Load() != 1 || obs.stops.Load() != 1 {
		t.Fatalf("lifecycle callbacks: starts=%d stops=%d, want 1/1", obs.starts.Load(), obs.stops.Load())
	}
	if obs.orders.Load() == 0 {
		t.Error("expected at least one OnOrder callback")
	}
	if obs.fills.Load() == 0 {
		t.Error("expected at least one OnFill callback")
	}
	if obs.rejects.Load() == 0 {
		t.Error("expected at least one OnReject callback")
	}

	m := node.Metrics()
	if m.FillsSeen == 0 {
		t.Error("metrics FillsSeen should be > 0")
	}
	if m.OrdersSeen == 0 {
		t.Error("metrics OrdersSeen should be > 0")
	}
	if m.RejectsSeen == 0 {
		t.Error("metrics RejectsSeen should be > 0")
	}
	// One unit bought at market 100 — net long 1.
	if got := node.Portfolio.NetQty(id, enums.PosNet); !got.Equal(decimal.RequireFromString("1")) {
		t.Errorf("net qty=%s, want 1", got)
	}
}
