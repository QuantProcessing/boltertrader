package backtest_test

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/backtest"
)

// drainExec non-blockingly collects every exec event currently buffered. The
// backtest venue enqueues all events for a synchronous action (Submit / Feed)
// before returning, so a non-blocking drain captures them deterministically.
func drainExec(ch <-chan contract.ExecEvent) []contract.ExecEvent {
	var out []contract.ExecEvent
	for {
		select {
		case ev := <-ch:
			out = append(out, ev)
		default:
			return out
		}
	}
}

func firstFill(t *testing.T, evs []contract.ExecEvent) model.Fill {
	t.Helper()
	for _, ev := range evs {
		if fe, ok := ev.(contract.FillEvent); ok {
			return fe.Fill
		}
	}
	t.Fatalf("no fill event among %d events", len(evs))
	return model.Fill{}
}

// TestTakerFeeOnMarketOrder: a market order removes liquidity and pays taker.
func TestTakerFeeOnMarketOrder(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	venue := backtest.NewVenue(clk, backtest.Config{
		MakerFeeRate: d("0.0002"),
		TakerFeeRate: d("0.0004"),
		StartBalance: model.AccountBalance{Currency: "USDT"},
	})
	venue.Feed(model.TradeTick{InstrumentID: inst, Price: d("100"), Quantity: d("1"), Timestamp: start.Add(time.Second)})
	_, _ = venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeMarket, Quantity: d("2"),
	})

	f := firstFill(t, drainExec(venue.Execution().Events()))
	if f.Liquidity != enums.LiqTaker {
		t.Errorf("liquidity=%v, want taker", f.Liquidity)
	}
	// fee = notional(100*2) * 0.0004 = 0.08
	if !f.Fee.Equal(d("0.08")) {
		t.Errorf("fee=%s, want 0.08", f.Fee)
	}
	if !f.Price.Equal(d("100")) {
		t.Errorf("price=%s, want 100 (no slippage configured)", f.Price)
	}
}

// TestMakerFeeOnRestingLimitFill: a resting limit filled by a crossing trade
// adds liquidity passively and pays maker, at the limit price.
func TestMakerFeeOnRestingLimitFill(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	venue := backtest.NewVenue(clk, backtest.Config{
		MakerFeeRate: d("0.0002"),
		TakerFeeRate: d("0.0004"),
		StartBalance: model.AccountBalance{Currency: "USDT"},
	})
	venue.Feed(model.TradeTick{InstrumentID: inst, Price: d("105"), Quantity: d("1"), Timestamp: start.Add(time.Second)})
	_, _ = venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeLimit, Quantity: d("2"), Price: d("100"),
	})
	_ = drainExec(venue.Execution().Events()) // discard the New ack
	venue.Feed(model.TradeTick{InstrumentID: inst, Price: d("99"), Quantity: d("1"), Timestamp: start.Add(2 * time.Second)})

	f := firstFill(t, drainExec(venue.Execution().Events()))
	if f.Liquidity != enums.LiqMaker {
		t.Errorf("liquidity=%v, want maker", f.Liquidity)
	}
	// fee = notional(100*2) * 0.0002 = 0.04, filled at the limit price 100.
	if !f.Fee.Equal(d("0.04")) {
		t.Errorf("fee=%s, want 0.04", f.Fee)
	}
	if !f.Price.Equal(d("100")) {
		t.Errorf("price=%s, want 100 (limit price)", f.Price)
	}
}

// TestMarketOrderSlippage: a slippage model moves a taker buy up and sell down.
func TestMarketOrderSlippage(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	venue := backtest.NewVenue(clk, backtest.Config{
		Slippage:     backtest.BpsSlippage(d("10")), // 10 bps = 0.1%
		StartBalance: model.AccountBalance{Currency: "USDT"},
	})
	venue.Feed(model.TradeTick{InstrumentID: inst, Price: d("100"), Quantity: d("1"), Timestamp: start.Add(time.Second)})

	buy, _ := venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeMarket, Quantity: d("1"),
	})
	if !buy.AvgFillPrice.Equal(d("100.1")) { // 100 * (1 + 0.001)
		t.Errorf("buy fill=%s, want 100.1", buy.AvgFillPrice)
	}
	sell, _ := venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst, Side: enums.SideSell, Type: enums.TypeMarket, Quantity: d("1"),
	})
	if !sell.AvgFillPrice.Equal(d("99.9")) { // 100 * (1 - 0.001)
		t.Errorf("sell fill=%s, want 99.9", sell.AvgFillPrice)
	}
}

// TestContractMultiplierFee: notional (and thus fees) scale by the contract
// multiplier of a registered instrument.
func TestContractMultiplierFee(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	mInst := model.InstrumentID{Venue: "BT", Symbol: "ETH-USDT", Kind: enums.KindPerp}
	venue := backtest.NewVenue(clk, backtest.Config{
		TakerFeeRate: d("0.0004"),
		StartBalance: model.AccountBalance{Currency: "USDT"},
		Instruments:  []*model.Instrument{{ID: mInst, ContractMultiplier: d("10")}},
	})
	venue.Feed(model.TradeTick{InstrumentID: mInst, Price: d("100"), Quantity: d("1"), Timestamp: start.Add(time.Second)})
	_, _ = venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: mInst, Side: enums.SideBuy, Type: enums.TypeMarket, Quantity: d("2"),
	})

	f := firstFill(t, drainExec(venue.Execution().Events()))
	// notional = 100 * 2 * 10 = 2000; fee = 2000 * 0.0004 = 0.8
	if !f.Fee.Equal(d("0.8")) {
		t.Errorf("fee=%s, want 0.8 (multiplier 10)", f.Fee)
	}
}

func TestFillFeeCurrencyUsesInstrumentSettle(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	mInst := model.InstrumentID{Venue: "BT", Symbol: "ETH-USDC", Kind: enums.KindPerp}
	venue := backtest.NewVenue(clk, backtest.Config{
		TakerFeeRate: d("0.0004"),
		StartBalance: model.AccountBalance{Currency: "USDT", Total: d("1000"), Available: d("1000")},
		Instruments: []*model.Instrument{{
			ID:     mInst,
			Settle: "USDC",
		}},
	})
	venue.Feed(model.TradeTick{InstrumentID: mInst, Price: d("100"), Quantity: d("1"), Timestamp: start.Add(time.Second)})
	_, _ = venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: mInst, Side: enums.SideBuy, Type: enums.TypeMarket, Quantity: d("1"),
	})

	f := firstFill(t, drainExec(venue.Execution().Events()))
	if f.FeeCurrency != "USDC" {
		t.Fatalf("fee currency=%q, want instrument settle USDC", f.FeeCurrency)
	}
}

// TestLegacyFeeRateFallback: a config that sets only the deprecated FeeRate uses
// it for both maker and taker fills.
func TestLegacyFeeRateFallback(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	venue := backtest.NewVenue(clk, backtest.Config{
		FeeRate:      d("0.0005"),
		StartBalance: model.AccountBalance{Currency: "USDT"},
	})
	venue.Feed(model.TradeTick{InstrumentID: inst, Price: d("100"), Quantity: d("1"), Timestamp: start.Add(time.Second)})
	_, _ = venue.Execution().Submit(context.Background(), model.OrderRequest{
		InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeMarket, Quantity: d("2"),
	})
	f := firstFill(t, drainExec(venue.Execution().Events()))
	// fee = 100 * 2 * 0.0005 = 0.1 via fallback
	if !f.Fee.Equal(d("0.1")) {
		t.Errorf("fee=%s, want 0.1 (FeeRate fallback)", f.Fee)
	}
}
