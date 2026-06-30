package backtest_test

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/backtest"
)

// openMaxLong funds 1000 USDT at 10x with maintenance margin 0.5% and no fees,
// then opens a 100-contract long at 100 (notional 10000, the full 1000 of
// initial margin). It returns the venue and a pointer that captures any
// liquidation.
func openMaxLong(t *testing.T) (*backtest.Venue, *clock.SimulatedClock, **backtest.Liquidation) {
	t.Helper()
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	liq := new(*backtest.Liquidation)
	venue := backtest.NewVenue(clk, backtest.Config{
		DefaultLeverage: d("10"),
		MaintMarginRate: d("0.005"),
		StartBalance:    model.AccountBalance{Currency: "USDT", Total: d("1000"), Available: d("1000")},
		OnLiquidation:   func(l backtest.Liquidation) { *liq = &l },
	})
	venue.Feed(model.TradeTick{InstrumentID: inst, Price: d("100"), Quantity: d("1"), Timestamp: start.Add(time.Second)})
	order, _ := venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeMarket, Quantity: d("100"),
	})
	if order.Status != enums.StatusFilled {
		t.Fatalf("open status=%v, want Filled", order.Status)
	}
	return venue, clk, liq
}

// TestLiquidationClosesUnderwaterAccount: a long that loses enough that equity
// falls to/below maintenance margin is force-closed.
func TestLiquidationClosesUnderwaterAccount(t *testing.T) {
	venue, clk, liq := openMaxLong(t)

	// Drop to 90: unrealized = (90-100)*100 = -1000, equity = 0; maintenance =
	// 90*100*0.005 = 45. equity 0 <= 45 -> liquidate.
	venue.Feed(model.TradeTick{InstrumentID: inst, Price: d("90"), Quantity: d("1"), Timestamp: clk.Now().Add(time.Second)})

	if *liq == nil {
		t.Fatal("expected a liquidation")
	}
	got := **liq
	if !got.EquityBefore.Equal(d("0")) {
		t.Errorf("EquityBefore=%s, want 0", got.EquityBefore)
	}
	if !got.MaintMargin.Equal(d("45")) {
		t.Errorf("MaintMargin=%s, want 45", got.MaintMargin)
	}
	if !got.WalletAfter.Equal(d("0")) {
		t.Errorf("WalletAfter=%s, want 0", got.WalletAfter)
	}
	if len(got.Closed) != 1 || got.Closed[0].Liquidity != enums.LiqTaker {
		t.Errorf("Closed=%+v, want 1 taker fill", got.Closed)
	}
	if poss := mustPositions(t, venue); len(poss) != 0 {
		t.Fatalf("positions=%d, want flat after liquidation", len(poss))
	}
	if bal := balanceOf(t, mustBalances(t, venue), "USDT"); !bal.Total.Equal(d("0")) {
		t.Errorf("wallet=%s, want 0", bal.Total)
	}
}

// TestLiquidationBankruptcyFloor: a gap down past bankruptcy floors the wallet at
// zero rather than going negative.
func TestLiquidationBankruptcyFloor(t *testing.T) {
	venue, clk, liq := openMaxLong(t)

	// Gap to 80: unrealized = -2000, equity = -1000 (bankrupt). Force close at 80
	// realizes -2000 -> wallet would be -1000, floored to 0.
	venue.Feed(model.TradeTick{InstrumentID: inst, Price: d("80"), Quantity: d("1"), Timestamp: clk.Now().Add(time.Second)})

	if *liq == nil {
		t.Fatal("expected a liquidation")
	}
	got := **liq
	if !got.EquityBefore.Equal(d("-1000")) {
		t.Errorf("EquityBefore=%s, want -1000", got.EquityBefore)
	}
	if !got.WalletAfter.Equal(d("0")) {
		t.Errorf("WalletAfter=%s, want 0 (floored)", got.WalletAfter)
	}
	if bal := balanceOf(t, mustBalances(t, venue), "USDT"); !bal.Total.Equal(d("0")) {
		t.Errorf("wallet=%s, want 0 (floored)", bal.Total)
	}
}

// TestNoLiquidationWhileHealthy: a small adverse move that keeps equity above
// maintenance margin does not liquidate.
func TestNoLiquidationWhileHealthy(t *testing.T) {
	venue, clk, liq := openMaxLong(t)
	// Drop to 99: unrealized = -100, equity = 900, maintenance = 49.5. Healthy.
	venue.Feed(model.TradeTick{InstrumentID: inst, Price: d("99"), Quantity: d("1"), Timestamp: clk.Now().Add(time.Second)})
	if *liq != nil {
		t.Fatalf("unexpected liquidation while healthy: %+v", **liq)
	}
	if poss := mustPositions(t, venue); len(poss) != 1 {
		t.Fatalf("positions=%d, want 1 (still open)", len(poss))
	}
}

func TestLiquidationCancelsRestingOrders(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	venue := backtest.NewVenue(clk, backtest.Config{
		DefaultLeverage: d("10"),
		MaintMarginRate: d("0.005"),
		StartBalance:    model.AccountBalance{Currency: "USDT", Total: d("1000"), Available: d("1000")},
	})
	venue.Feed(model.TradeTick{InstrumentID: inst, Price: d("100"), Quantity: d("1"), Timestamp: start.Add(time.Second)})
	_, _ = venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeMarket, Quantity: d("90"),
	})
	resting, _ := venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst, Side: enums.SideSell, Type: enums.TypeLimit, Quantity: d("1"), Price: d("101"),
	})
	if resting.Status != enums.StatusNew {
		t.Fatalf("resting status=%v, want New", resting.Status)
	}

	venue.Feed(model.TradeTick{InstrumentID: inst, Price: d("89"), Quantity: d("1"), Timestamp: clk.Now().Add(time.Second)})

	open, _ := venue.Execution().OpenOrders(context.Background(), inst)
	if len(open) != 0 {
		t.Fatalf("open orders=%+v, want all resting orders canceled by liquidation", open)
	}
	venue.Feed(model.TradeTick{InstrumentID: inst, Price: d("101"), Quantity: d("1"), Timestamp: clk.Now().Add(time.Second)})
	if poss := mustPositions(t, venue); len(poss) != 0 {
		t.Fatalf("positions=%+v, want liquidation not to allow stale resting orders to reopen exposure", poss)
	}
}
