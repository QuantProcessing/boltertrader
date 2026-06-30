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
)

// TestCapstonePerpRoundTrip drives a full perp round trip through a TradingNode
// and the SimEvent Runner — maker entry, a funding settlement, maker exit — and
// hand-verifies every economic effect, including that the simulated wallet, the
// runtime portfolio, and the funding cost reconcile to the cent.
//
// Trade script (qty 5, leverage 10x, maker 2bps, taker 4bps, start 10000 USDT):
//
//	t1 @105  strategy posts limit buy 5 @100 (rests)
//	t2 @100  buy fills @100 (maker): fee 100*5*0.0002 = 0.10  -> wallet 9999.90
//	         OnFill posts limit sell 5 @110 (rests)
//	t3 fund  rate +0.0001, mark 100: long pays 100*5*0.0001 = 0.05 -> wallet 9999.85
//	t4 @110  sell fills @110 (maker): realized (110-100)*5 = 50, fee 110*5*0.0002 = 0.11
//	         -> wallet 9999.85 + 50 - 0.11 = 10049.74
func TestCapstonePerpRoundTrip(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	venue := backtest.NewVenue(clk, backtest.Config{
		MakerFeeRate:    d("0.0002"),
		TakerFeeRate:    d("0.0004"),
		DefaultLeverage: d("10"),
		StartBalance:    model.AccountBalance{Currency: "USDT", Total: d("10000"), Available: d("10000")},
	})
	strat := &limitBuyThenSell{buyPx: d("100"), sellPx: d("110"), qty: d("5")}
	node := runtime.NewNode(
		runtime.Clients{Market: venue.Market(), Execution: venue.Execution(), Account: venue.Account()},
		clk, "bt", runtime.WithStrategy(strat),
	)
	node.Start(context.Background())
	events := []backtest.SimEvent{
		backtest.Trade(model.TradeTick{InstrumentID: inst, Price: d("105"), Quantity: d("1"), Timestamp: start.Add(1 * time.Second)}),
		backtest.Trade(model.TradeTick{InstrumentID: inst, Price: d("100"), Quantity: d("1"), Timestamp: start.Add(2 * time.Second)}),
		backtest.Funding(inst, d("0.0001"), start.Add(3*time.Second)),
		backtest.Trade(model.TradeTick{InstrumentID: inst, Price: d("110"), Quantity: d("1"), Timestamp: start.Add(4 * time.Second)}),
	}
	backtest.NewRunner(venue).Run(context.Background(), node, events)
	node.Stop()

	// Wallet: exact hand-computed end balance.
	if got := balanceOf(t, mustBalances(t, venue), "USDT").Total; !got.Equal(d("10049.74")) {
		t.Fatalf("wallet=%s, want 10049.74", got)
	}
	// Portfolio: realized +50 gross, fees 0.21, net 49.79.
	if got := node.Portfolio.RealizedPnL(); !got.Equal(d("50")) {
		t.Errorf("realized=%s, want 50", got)
	}
	if got := node.Portfolio.Fees(); !got.Equal(d("0.21")) {
		t.Errorf("fees=%s, want 0.21", got)
	}
	netFees := node.Portfolio.RealizedPnLNetFees()
	if !netFees.Equal(d("49.79")) {
		t.Errorf("net=%s, want 49.79", netFees)
	}
	// Reconciliation: walletChange = portfolio net PnL - funding paid (0.05).
	// The portfolio is funding-unaware, so funding is the only gap between the
	// two views — and it closes exactly.
	walletChange := balanceOf(t, mustBalances(t, venue), "USDT").Total.Sub(d("10000"))
	if !walletChange.Equal(netFees.Sub(d("0.05"))) {
		t.Fatalf("reconcile failed: walletChange=%s, netFees-funding=%s", walletChange, netFees.Sub(d("0.05")))
	}
	// Flat at the end in both books.
	if poss := mustPositions(t, venue); len(poss) != 0 {
		t.Errorf("venue positions=%d, want flat", len(poss))
	}
	if got := node.Portfolio.NetQty(inst, enums.PosNet); !got.IsZero() {
		t.Errorf("portfolio net qty=%s, want flat", got)
	}
}

// TestCapstoneLiquidationThroughNode shows liquidation is fully wired into the
// runtime: the forced-close fills reach the portfolio and cache, the position is
// cleared everywhere, the wallet is floored, and OnLiquidation fires — all driven
// through a TradingNode and the Runner.
func TestCapstoneLiquidationThroughNode(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	var liq *backtest.Liquidation
	venue := backtest.NewVenue(clk, backtest.Config{
		DefaultLeverage: d("10"),
		MaintMarginRate: d("0.005"),
		StartBalance:    model.AccountBalance{Currency: "USDT", Total: d("1000"), Available: d("1000")},
		OnLiquidation:   func(l backtest.Liquidation) { liq = &l },
	})
	node := runtime.NewNode(
		runtime.Clients{Market: venue.Market(), Execution: venue.Execution(), Account: venue.Account()},
		clk, "bt", runtime.WithStrategy(&marketBuyOnce{qty: d("100")}),
	)
	node.Start(context.Background())
	events := []backtest.SimEvent{
		backtest.Trade(model.TradeTick{InstrumentID: inst, Price: d("100"), Quantity: d("1"), Timestamp: start.Add(1 * time.Second)}), // opens long 100
		backtest.Trade(model.TradeTick{InstrumentID: inst, Price: d("90"), Quantity: d("1"), Timestamp: start.Add(2 * time.Second)}),  // equity 0 <= maint -> liquidate
	}
	backtest.NewRunner(venue).Run(context.Background(), node, events)
	node.Stop()

	if liq == nil {
		t.Fatal("expected liquidation")
	}
	if !liq.WalletAfter.Equal(d("0")) {
		t.Errorf("WalletAfter=%s, want 0", liq.WalletAfter)
	}
	// The runtime saw the forced close: portfolio flat, realized -1000.
	if got := node.Portfolio.NetQty(inst, enums.PosNet); !got.IsZero() {
		t.Errorf("portfolio net qty=%s, want flat after liquidation", got)
	}
	if got := node.Portfolio.RealizedPnL(); !got.Equal(d("-1000")) {
		t.Errorf("realized=%s, want -1000", got)
	}
	if got := len(node.Cache.Positions()); got != 0 {
		t.Errorf("cache positions=%d, want 0 after liquidation", got)
	}
}
