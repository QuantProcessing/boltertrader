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
	"github.com/shopspring/decimal"
)

func balanceOf(t *testing.T, bals []model.AccountBalance, ccy string) model.AccountBalance {
	t.Helper()
	for _, b := range bals {
		if b.Currency == ccy {
			return b
		}
	}
	t.Fatalf("no %s balance among %v", ccy, bals)
	return model.AccountBalance{}
}

// TestSimAccountWalletAndPosition drives a market open then close and asserts the
// wallet tracks realized PnL minus fees, and positions mark to the latest price.
func TestSimAccountWalletAndPosition(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	venue := backtest.NewVenue(clk, backtest.Config{
		TakerFeeRate: d("0.001"),
		StartBalance: model.AccountBalance{Currency: "USDT", Total: d("10000"), Available: d("10000")},
	})
	exec := venue.Execution()

	venue.Feed(model.TradeTick{InstrumentID: inst, Price: d("100"), Quantity: d("1"), Timestamp: start.Add(time.Second)})
	// Open long 2 @ 100: fee = 100*2*0.001 = 0.2 -> wallet 9999.8.
	_, _ = exec.Submit(context.Background(), model.OrderRequest{InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeMarket, Quantity: d("2")})
	if got := balanceOf(t, mustBalances(t, venue), "USDT").Total; !got.Equal(d("9999.8")) {
		t.Fatalf("after open: wallet=%s, want 9999.8", got)
	}

	// Mark to 110: unrealized = (110-100)*2 = 20, wallet unchanged (realized cash).
	venue.Feed(model.TradeTick{InstrumentID: inst, Price: d("110"), Quantity: d("1"), Timestamp: start.Add(2 * time.Second)})
	poss := mustPositions(t, venue)
	if len(poss) != 1 {
		t.Fatalf("positions=%d, want 1", len(poss))
	}
	if p := poss[0]; !p.Quantity.Equal(d("2")) || !p.EntryPrice.Equal(d("100")) || !p.UnrealizedPnL.Equal(d("20")) {
		t.Fatalf("position=%+v, want qty 2 entry 100 uPnL 20", p)
	}
	if got := balanceOf(t, mustBalances(t, venue), "USDT").Total; !got.Equal(d("9999.8")) {
		t.Fatalf("after mark: wallet=%s, want 9999.8 (unrealized not booked)", got)
	}

	// Close 2 @ 110: realized = 20, fee = 110*2*0.001 = 0.22 -> wallet 9999.8+20-0.22 = 10019.58.
	_, _ = exec.Submit(context.Background(), model.OrderRequest{InstrumentID: inst, Side: enums.SideSell, Type: enums.TypeMarket, Quantity: d("2")})
	if got := balanceOf(t, mustBalances(t, venue), "USDT").Total; !got.Equal(d("10019.58")) {
		t.Fatalf("after close: wallet=%s, want 10019.58", got)
	}
	if poss := mustPositions(t, venue); len(poss) != 0 {
		t.Fatalf("positions=%d, want 0 (flat)", len(poss))
	}
}

func mustBalances(t *testing.T, v *backtest.Venue) []model.AccountBalance {
	t.Helper()
	b, err := v.Account().Balances(context.Background())
	if err != nil {
		t.Fatalf("balances: %v", err)
	}
	return b
}

func mustPositions(t *testing.T, v *backtest.Venue) []model.Position {
	t.Helper()
	p, err := v.Account().Positions(context.Background())
	if err != nil {
		t.Fatalf("positions: %v", err)
	}
	return p
}

// TestSimAccountPortfolioParity is the A2 parity self-check: the change in the
// simulated wallet equals the runtime portfolio's realized PnL net of fees. Both
// compute average-cost realized PnL independently; for a linear (multiplier-1)
// instrument they must agree to the cent.
func TestSimAccountPortfolioParity(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	startBal := decimal.RequireFromString("10000")
	venue := backtest.NewVenue(clk, backtest.Config{
		MakerFeeRate: d("0.0002"),
		TakerFeeRate: d("0.0004"),
		StartBalance: model.AccountBalance{Currency: "USDT", Total: startBal, Available: startBal},
	})
	strat := &limitBuyThenSell{buyPx: d("100"), sellPx: d("110"), qty: d("2")}
	node := runtime.NewNode(
		runtime.Clients{Market: venue.Market(), Execution: venue.Execution(), Account: venue.Account()},
		clk, "bt", runtime.WithStrategy(strat),
	)
	node.Start(context.Background())
	ticks := []model.TradeTick{
		{InstrumentID: inst, Price: d("105"), Quantity: d("1"), Timestamp: start.Add(1 * time.Second)},
		{InstrumentID: inst, Price: d("100"), Quantity: d("1"), Timestamp: start.Add(2 * time.Second)}, // fills buy @100 (maker)
		{InstrumentID: inst, Price: d("108"), Quantity: d("1"), Timestamp: start.Add(3 * time.Second)},
		{InstrumentID: inst, Price: d("110"), Quantity: d("1"), Timestamp: start.Add(4 * time.Second)}, // fills sell @110 (maker)
	}
	backtest.NewRunner(venue).RunTrades(context.Background(), node, ticks)
	node.Stop()

	walletChange := balanceOf(t, mustBalances(t, venue), "USDT").Total.Sub(startBal)
	netFees := node.Portfolio.RealizedPnLNetFees()
	if !walletChange.Equal(netFees) {
		t.Fatalf("parity broken: wallet change=%s, portfolio realized-net-fees=%s", walletChange, netFees)
	}
	// Sanity: realized gross is +20, fees are maker on both legs.
	if !node.Portfolio.RealizedPnL().Equal(d("20")) {
		t.Fatalf("realized gross=%s, want 20", node.Portfolio.RealizedPnL())
	}
}
