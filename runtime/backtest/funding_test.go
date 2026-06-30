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

// openPosition funds a 1000 USDT 10x account with no fees and opens a position of
// the given signed-by-side quantity at 100.
func openPosition(t *testing.T, side enums.OrderSide) (*backtest.Venue, *clock.SimulatedClock) {
	t.Helper()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	venue := backtest.NewVenue(clk, backtest.Config{
		DefaultLeverage: d("10"),
		StartBalance:    model.AccountBalance{Currency: "USDT", Total: d("1000"), Available: d("1000")},
	})
	venue.Feed(model.TradeTick{InstrumentID: inst, Price: d("100"), Quantity: d("1"), Timestamp: start.Add(time.Second)})
	_, _ = venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst, Side: side, Type: enums.TypeMarket, Quantity: d("10"),
	})
	return venue, clk
}

// TestFundingLongPays: with a positive funding rate a long position pays.
func TestFundingLongPays(t *testing.T) {
	venue, clk := openPosition(t, enums.SideBuy)
	// payment = mark(100) * qty(10) * rate(0.0001) = 0.1; long pays -> 1000 - 0.1.
	venue.FeedFunding(inst, d("0.0001"), clk.Now().Add(time.Second))
	if got := balanceOf(t, mustBalances(t, venue), "USDT").Total; !got.Equal(d("999.9")) {
		t.Fatalf("wallet=%s, want 999.9 (long pays funding)", got)
	}
}

// TestFundingShortReceives: with a positive funding rate a short position
// receives.
func TestFundingShortReceives(t *testing.T) {
	venue, clk := openPosition(t, enums.SideSell)
	venue.FeedFunding(inst, d("0.0001"), clk.Now().Add(time.Second))
	if got := balanceOf(t, mustBalances(t, venue), "USDT").Total; !got.Equal(d("1000.1")) {
		t.Fatalf("wallet=%s, want 1000.1 (short receives funding)", got)
	}
}

// TestFundingNegativeRateLongReceives: a negative rate flips the direction.
func TestFundingNegativeRateLongReceives(t *testing.T) {
	venue, clk := openPosition(t, enums.SideBuy)
	venue.FeedFunding(inst, d("-0.0001"), clk.Now().Add(time.Second))
	if got := balanceOf(t, mustBalances(t, venue), "USDT").Total; !got.Equal(d("1000.1")) {
		t.Fatalf("wallet=%s, want 1000.1 (long receives at negative rate)", got)
	}
}

// marketBuyOnce buys a market order of qty on the first trade it sees.
type marketBuyOnce struct {
	strategy.Base
	qty  decimal.Decimal
	done bool
}

func (s *marketBuyOnce) OnTrade(c *strategy.Context, tick model.TradeTick) {
	if !s.done {
		s.done = true
		_, _ = c.Buy(tick.InstrumentID, s.qty, decimal.Zero) // zero price = market
	}
}

// TestRunnerMixedStream replays a mix of trades and a funding settlement through
// a node and asserts the funding was applied on top of the opened position.
func TestRunnerMixedStream(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	venue := backtest.NewVenue(clk, backtest.Config{
		DefaultLeverage: d("10"),
		StartBalance:    model.AccountBalance{Currency: "USDT", Total: d("1000"), Available: d("1000")},
	})
	node := runtime.NewNode(
		runtime.Clients{Market: venue.Market(), Execution: venue.Execution(), Account: venue.Account()},
		clk, "bt", runtime.WithStrategy(&marketBuyOnce{qty: d("10")}),
	)
	node.Start(context.Background())
	events := []backtest.SimEvent{
		backtest.Trade(model.TradeTick{InstrumentID: inst, Price: d("100"), Quantity: d("1"), Timestamp: start.Add(1 * time.Second)}),
		backtest.Trade(model.TradeTick{InstrumentID: inst, Price: d("100"), Quantity: d("1"), Timestamp: start.Add(2 * time.Second)}),
		backtest.Funding(inst, d("0.0001"), start.Add(3*time.Second)),
	}
	backtest.NewRunner(venue).Run(context.Background(), node, events)
	node.Stop()

	// Long 10 opened on the first trade; funding 0.1 paid -> wallet 999.9.
	if got := balanceOf(t, mustBalances(t, venue), "USDT").Total; !got.Equal(d("999.9")) {
		t.Fatalf("wallet=%s, want 999.9", got)
	}
	if got := node.Portfolio.NetQty(inst, enums.PosNet); !got.Equal(d("10")) {
		t.Fatalf("net qty=%s, want 10", got)
	}
}
