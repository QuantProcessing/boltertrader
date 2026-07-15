package runtime_test

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
	"github.com/QuantProcessing/boltertrader/runtime/strategy"
	"github.com/shopspring/decimal"
)

// recordingStrategy buys one unit on the first bar and records every callback.
type recordingStrategy struct {
	strategy.Base
	mu        sync.Mutex
	started   bool
	bars      int
	trades    int
	fills     int
	submitted bool
	submitErr error
	metaIDs   []string
}

func (s *recordingStrategy) OnStart(c *strategy.Context) {
	s.mu.Lock()
	s.started = true
	s.mu.Unlock()
}

func (s *recordingStrategy) OnTrade(c *strategy.Context, t model.TradeTick) {
	s.mu.Lock()
	s.trades++
	if meta := c.CurrentEventMeta(); meta.EventID != "" {
		s.metaIDs = append(s.metaIDs, string(meta.EventID))
	}
	s.mu.Unlock()
}

func (s *recordingStrategy) OnBar(c *strategy.Context, bar model.Bar) {
	s.mu.Lock()
	first := s.bars == 0
	s.bars++
	if meta := c.CurrentEventMeta(); meta.EventID != "" {
		s.metaIDs = append(s.metaIDs, string(meta.EventID))
	}
	s.mu.Unlock()
	if first {
		// Strategy acts through the context, not an adapter.
		_, err := c.Buy(bar.InstrumentID, decimal.RequireFromString("1"), bar.Close)
		s.mu.Lock()
		s.submitted = true
		s.submitErr = err
		s.mu.Unlock()
	}
}

func (s *recordingStrategy) OnFill(c *strategy.Context, f model.Fill) {
	s.mu.Lock()
	s.fills++
	s.mu.Unlock()
}

// TestStrategyCallbacksAndBars drives the node with a fake market feed, asserts
// the strategy receives trade and bar callbacks, and that an order it submits
// from OnBar reaches the cache.
func TestStrategyCallbacksAndBars(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fmarket := runtimetest.NewFakeMarket()
	fexec := runtimetest.NewFakeExec()

	strat := &recordingStrategy{}

	node := runtime.NewNode(
		runtime.Clients{Market: fmarket, Execution: fexec},
		clk, "strat",
		runtime.WithStrategy(strat),
		runtime.WithBars(inst, time.Minute, "1m"),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)

	base := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	// Two trades in minute 0, one in minute 1 (which completes the minute-0 bar).
	fmarket.EmitTrade(model.TradeTick{InstrumentID: inst, Price: decimal.RequireFromString("100"), Quantity: decimal.RequireFromString("1"), Timestamp: base.Add(1 * time.Second)})
	fmarket.EmitTrade(model.TradeTick{InstrumentID: inst, Price: decimal.RequireFromString("102"), Quantity: decimal.RequireFromString("1"), Timestamp: base.Add(30 * time.Second)})
	fmarket.EmitTrade(model.TradeTick{InstrumentID: inst, Price: decimal.RequireFromString("104"), Quantity: decimal.RequireFromString("1"), Timestamp: base.Add(70 * time.Second)})

	// Wait until the bar callback's synchronous submission has returned. The
	// bar count is published before Buy starts, so it is not a completion fence.
	deadline := time.After(2 * time.Second)
	for {
		strat.mu.Lock()
		bars := strat.bars
		started := strat.started
		submitted := strat.submitted
		submitErr := strat.submitErr
		strat.mu.Unlock()
		if started && bars >= 1 && submitted {
			if submitErr != nil {
				t.Fatalf("strategy order submission failed: %v", submitErr)
			}
			break
		}
		select {
		case <-deadline:
			t.Fatalf("strategy did not complete bar submission in time (started=%v bars=%d submitted=%v err=%v)", started, bars, submitted, submitErr)
		case <-time.After(5 * time.Millisecond):
		}
	}

	strat.mu.Lock()
	if strat.trades != 3 {
		t.Errorf("trades=%d, want 3", strat.trades)
	}
	if len(strat.metaIDs) == 0 {
		t.Error("strategy callbacks did not receive event metadata")
	}
	strat.mu.Unlock()

	// The order submitted from OnBar should be in the cache.
	if all := node.Cache.Orders(); len(all) == 0 {
		t.Fatal("strategy order did not reach the cache")
	}
}
