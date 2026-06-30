package runtime_test

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/backtest"
	"github.com/QuantProcessing/boltertrader/runtime/observ"
	"github.com/QuantProcessing/boltertrader/runtime/strategy"
	"github.com/shopspring/decimal"
)

// recordingObserver counts observer callbacks.
type recordingObserver struct {
	observ.Base
	starts, stops, orders, fills, rejects int
}

func (o *recordingObserver) OnNodeStart()             { o.starts++ }
func (o *recordingObserver) OnNodeStop()              { o.stops++ }
func (o *recordingObserver) OnOrder(model.Order)      { o.orders++ }
func (o *recordingObserver) OnFill(model.Fill)        { o.fills++ }
func (o *recordingObserver) OnReject(string, string)  { o.rejects++ }

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

// TestObserverAndMetrics drives a backtest and asserts the observer received
// lifecycle/order/fill callbacks and the Metrics snapshot reflects the trade.
func TestObserverAndMetrics(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	venue := backtest.NewVenue(clk, backtest.Config{})
	id := model.InstrumentID{Venue: "BT", Symbol: "BTC-USDT", Kind: enums.KindPerp}

	obs := &recordingObserver{}
	node := runtime.NewNode(
		runtime.Clients{Market: venue.Market(), Execution: venue.Execution(), Account: venue.Account()},
		clk, "obs",
		runtime.WithStrategy(&buyOnFirstTrade{}),
		runtime.WithObserver(obs),
	)

	node.Start(context.Background())
	// First trade seeds price AND triggers the market buy; second trade lets the
	// fill propagate. Market order fills immediately at last price.
	venue.Feed(model.TradeTick{InstrumentID: id, Price: decimal.RequireFromString("100"), Quantity: decimal.RequireFromString("1"), Timestamp: start.Add(time.Second)})
	node.ProcessAvailable()
	node.Stop()

	if obs.starts != 1 || obs.stops != 1 {
		t.Fatalf("lifecycle callbacks: starts=%d stops=%d, want 1/1", obs.starts, obs.stops)
	}
	if obs.orders == 0 {
		t.Error("expected at least one OnOrder callback")
	}
	if obs.fills == 0 {
		t.Error("expected at least one OnFill callback")
	}

	m := node.Metrics()
	if m.FillsSeen == 0 {
		t.Error("metrics FillsSeen should be > 0")
	}
	if m.OrdersSeen == 0 {
		t.Error("metrics OrdersSeen should be > 0")
	}
	// One unit bought at market 100 — net long 1.
	if got := node.Portfolio.NetQty(id, enums.PosNet); !got.Equal(decimal.RequireFromString("1")) {
		t.Errorf("net qty=%s, want 1", got)
	}
}
