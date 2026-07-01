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
)

var spotInst = model.InstrumentID{Venue: "BT", Symbol: "BTC-USDT", Kind: enums.KindSpot}

func TestRuntimeSpotFlowMirrorsOrdersFillsAndBalances(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	venue := backtest.NewVenue(clk, backtest.Config{
		TakerFeeRate: d("0.001"),
		StartBalance: model.AccountBalance{Currency: "USDT", Total: d("1000"), Available: d("1000")},
		Instruments: []*model.Instrument{{
			ID:    spotInst,
			Base:  "BTC",
			Quote: "USDT",
		}},
	})
	node := runtime.NewNode(
		runtime.Clients{Market: venue.Market(), Execution: venue.Execution(), Account: venue.Account()},
		clk, "spot",
	)

	ctx := context.Background()
	venue.Feed(model.TradeTick{InstrumentID: spotInst, Price: d("100"), Quantity: d("1"), Timestamp: start.Add(time.Second)})
	node.ProcessAvailable()

	order, err := node.Exec.Submit(ctx, model.OrderRequest{
		InstrumentID: spotInst,
		Side:         enums.SideBuy,
		Type:         enums.TypeMarket,
		Quantity:     d("2"),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	node.ProcessAvailable()

	if cached, ok := node.Cache.Order(order.Request.ClientID); !ok || cached.Status != enums.StatusFilled {
		t.Fatalf("order not filled in cache: ok=%v order=%+v", ok, cached)
	}
	usdt, ok := node.Cache.Balance("USDT")
	if !ok || !usdt.Total.Equal(d("799.8")) || !usdt.Available.Equal(d("799.8")) || !usdt.Locked.IsZero() {
		t.Fatalf("USDT cache balance=%+v ok=%v, want total=available=799.8 locked=0", usdt, ok)
	}
	btc, ok := node.Cache.Balance("BTC")
	if !ok || !btc.Total.Equal(d("2")) || !btc.Available.Equal(d("2")) || !btc.Locked.IsZero() {
		t.Fatalf("BTC cache balance=%+v ok=%v, want total=available=2 locked=0", btc, ok)
	}
	if got := len(node.Cache.Positions()); got != 0 {
		t.Fatalf("cache positions=%d, want 0 because spot inventory is balance-sourced", got)
	}
	if got := node.Portfolio.NetQty(spotInst, enums.PosNet); !got.Equal(d("2")) {
		t.Fatalf("derived portfolio spot exposure=%s, want 2", got)
	}
}
