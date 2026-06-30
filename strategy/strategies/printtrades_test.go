package strategies_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/risk"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
	"github.com/QuantProcessing/boltertrader/strategy/strategies"
	"github.com/shopspring/decimal"
)

var inst = model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindPerp}

// TestPrintTradesAssembly assembles the example strategy into a TradingNode with
// a fake venue + risk gate, exactly as cmd/livedemo does but offline, and
// verifies the strategy trades on the first bar and the order reaches the cache.
func TestPrintTradesAssembly(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fmarket := runtimetest.NewFakeMarket()
	fexec := runtimetest.NewFakeExec()

	var mu sync.Mutex
	var lines int
	strat := &strategies.PrintTrades{
		Instrument: inst,
		BuyOnceQty: decimal.RequireFromString("0.001"),
		Logf: func(string, ...any) {
			mu.Lock()
			lines++
			mu.Unlock()
		},
	}

	node := runtime.NewNode(
		runtime.Clients{Market: fmarket, Execution: fexec},
		clk, "demo",
		runtime.WithStrategy(strat),
		runtime.WithBars(inst, time.Minute, "1m"),
	)
	riskEng := risk.New(risk.Limits{MaxOrderQty: decimal.RequireFromString("1")}, node.Cache)
	runtime.WithRisk(riskEng, nil)(node)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Two trades in minute 0, one in minute 1 -> completes minute-0 bar -> OnBar.
	fmarket.EmitTrade(model.TradeTick{InstrumentID: inst, Price: decimal.RequireFromString("100"), Quantity: decimal.RequireFromString("1"), Timestamp: base.Add(1 * time.Second)})
	fmarket.EmitTrade(model.TradeTick{InstrumentID: inst, Price: decimal.RequireFromString("101"), Quantity: decimal.RequireFromString("1"), Timestamp: base.Add(30 * time.Second)})
	fmarket.EmitTrade(model.TradeTick{InstrumentID: inst, Price: decimal.RequireFromString("102"), Quantity: decimal.RequireFromString("1"), Timestamp: base.Add(70 * time.Second)})

	deadline := time.After(2 * time.Second)
	for {
		if all := node.Cache.Orders(); len(all) > 0 {
			break
		}
		select {
		case <-deadline:
			t.Fatal("strategy did not submit an order from OnBar")
		case <-time.After(5 * time.Millisecond):
		}
	}

	mu.Lock()
	gotLines := lines
	mu.Unlock()
	if gotLines == 0 {
		t.Error("strategy produced no log lines")
	}
}
