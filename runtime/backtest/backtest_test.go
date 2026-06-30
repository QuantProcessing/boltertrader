package backtest_test

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/backtest"
	"github.com/QuantProcessing/boltertrader/runtime/strategy"
	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

var inst = model.InstrumentID{Venue: "BT", Symbol: "BTC-USDT", Kind: enums.KindPerp}

// limitBuyThenSell buys a limit on the first trade it sees and, once that buy
// fills, sells a limit. It exercises resting-order matching deterministically.
// All callbacks run on the single backtest-driver goroutine, so no locking is
// needed.
type limitBuyThenSell struct {
	strategy.Base
	buyPx, sellPx, qty decimal.Decimal
	bought, sold       bool
	fills              int
}

func (s *limitBuyThenSell) OnTrade(c *strategy.Context, t model.TradeTick) {
	if !s.bought {
		s.bought = true
		_, _ = c.Buy(inst, s.qty, s.buyPx)
	}
}

func (s *limitBuyThenSell) OnFill(c *strategy.Context, f model.Fill) {
	s.fills++
	if f.Side == enums.SideBuy && !s.sold {
		s.sold = true
		_, _ = c.Sell(inst, s.qty, s.sellPx)
	}
}

// TestBacktestParity is the P9 payoff: the same TradingNode + strategy code that
// runs live drives a deterministic, single-threaded backtest over replayed
// trades on a SimulatedClock. It asserts resting limit orders fill when price
// crosses and the realized PnL is exact.
func TestBacktestParity(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)

	venue := backtest.NewVenue(clk, backtest.Config{
		StartBalance: model.AccountBalance{Currency: "USDT", Total: d("10000"), Available: d("10000")},
	})

	strat := &limitBuyThenSell{buyPx: d("100"), sellPx: d("110"), qty: d("2")}

	node := runtime.NewNode(
		runtime.Clients{Market: venue.Market(), Execution: venue.Execution(), Account: venue.Account()},
		clk, "bt",
		runtime.WithStrategy(strat),
	)

	// Single-threaded stepping via the Runner: feed a tick, drain, repeat.
	node.Start(context.Background())
	ticks := []model.TradeTick{
		{InstrumentID: inst, Price: d("105"), Quantity: d("1"), Timestamp: start.Add(1 * time.Second)},
		{InstrumentID: inst, Price: d("100"), Quantity: d("1"), Timestamp: start.Add(2 * time.Second)}, // fills buy@100
		{InstrumentID: inst, Price: d("108"), Quantity: d("1"), Timestamp: start.Add(3 * time.Second)},
		{InstrumentID: inst, Price: d("110"), Quantity: d("1"), Timestamp: start.Add(4 * time.Second)}, // fills sell@110
	}
	backtest.NewRunner(venue).RunTrades(context.Background(), node, ticks)
	node.Stop()

	if strat.fills != 2 {
		t.Fatalf("fills=%d, want 2 (buy then sell)", strat.fills)
	}
	// Realized PnL: bought 2 @100, sold 2 @110 => +20.
	if got := node.Portfolio.RealizedPnL(); !got.Equal(d("20")) {
		t.Fatalf("realized PnL=%s, want 20", got)
	}
	if got := node.Portfolio.NetQty(inst, enums.PosNet); !got.IsZero() {
		t.Fatalf("net qty=%s, want flat", got)
	}
	if !clk.Now().Equal(start.Add(4 * time.Second)) {
		t.Errorf("clock at %v, want %v", clk.Now(), start.Add(4*time.Second))
	}
}

// TestMarketOrderFillsAtLastPrice checks a market order fills immediately at the
// last replayed price.
func TestMarketOrderFillsAtLastPrice(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	venue := backtest.NewVenue(clk, backtest.Config{})

	venue.Feed(model.TradeTick{InstrumentID: inst, Price: d("100"), Quantity: d("1"), Timestamp: start.Add(time.Second)})

	order, err := venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeMarket, Quantity: d("1"),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if order.Status != enums.StatusFilled || !order.AvgFillPrice.Equal(d("100")) {
		t.Fatalf("market order should fill at 100, got status=%v px=%s", order.Status, order.AvgFillPrice)
	}
}

// TestDeterministicRepeat runs the same backtest twice and asserts identical
// realized PnL — the core determinism guarantee.
func TestDeterministicRepeat(t *testing.T) {
	run := func() decimal.Decimal {
		start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
		clk := clock.NewSimulatedClock(start)
		venue := backtest.NewVenue(clk, backtest.Config{})
		strat := &limitBuyThenSell{buyPx: d("100"), sellPx: d("110"), qty: d("2")}
		node := runtime.NewNode(
			runtime.Clients{Market: venue.Market(), Execution: venue.Execution(), Account: venue.Account()},
			clk, "bt", runtime.WithStrategy(strat),
		)
		node.Start(context.Background())
		for i, px := range []string{"105", "100", "108", "110"} {
			venue.Feed(model.TradeTick{InstrumentID: inst, Price: d(px), Quantity: d("1"), Timestamp: start.Add(time.Duration(i+1) * time.Second)})
			node.ProcessAvailable()
		}
		node.Stop()
		return node.Portfolio.RealizedPnL()
	}
	a, b := run(), run()
	if !a.Equal(b) {
		t.Fatalf("non-deterministic: run1=%s run2=%s", a, b)
	}
	if !a.Equal(d("20")) {
		t.Fatalf("realized=%s, want 20", a)
	}
}
