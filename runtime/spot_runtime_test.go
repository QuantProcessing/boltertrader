package runtime_test

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
)

var spotInst = model.InstrumentID{Venue: "BT", Symbol: "BTC-USDT", Kind: enums.KindSpot}

func TestRuntimeSpotFlowMirrorsOrdersFillsAndBalances(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	fexec := runtimetest.NewFakeExec().WithClock(clk)
	facct := runtimetest.NewFakeAccount()
	fexec.SetAccountID("spot")
	facct.SetAccountID("spot")
	facct.SetAccountStateSnapshot(authoritativeAccountState("spot", model.AccountCash, start))
	filled := make(chan model.Fill, 1)
	node := runtime.NewNode(
		runtime.Clients{Execution: fexec, Account: facct},
		clk, "spot",
		runtime.WithOnFill(func(f model.Fill) { filled <- f }),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)

	order, err := node.Exec.Submit(ctx, model.OrderRequest{
		InstrumentID: spotInst,
		Side:         enums.SideBuy,
		Type:         enums.TypeMarket,
		Quantity:     d("2"),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}

	orderFill := *order
	orderFill.Status = enums.StatusFilled
	orderFill.FilledQty = d("2")
	orderFill.AvgFillPrice = d("100")
	orderFill.UpdatedAt = start.Add(time.Second)
	fexec.EmitOrder(orderFill)
	fexec.EmitFill(model.Fill{
		InstrumentID: spotInst,
		VenueOrderID: order.VenueOrderID,
		ClientID:     order.Request.ClientID,
		TradeID:      "spot-fill-1",
		Side:         enums.SideBuy,
		Liquidity:    enums.LiqTaker,
		Price:        d("100"),
		Quantity:     d("2"),
		Fee:          d("0.2"),
		FeeCurrency:  "USDT",
		Timestamp:    start.Add(time.Second),
	})
	facct.EmitBalance(model.AccountBalance{Currency: "USDT", Total: d("799.8"), Free: d("799.8"), UpdatedAt: start.Add(time.Second)})
	facct.EmitBalance(model.AccountBalance{Currency: "BTC", Total: d("2"), Free: d("2"), UpdatedAt: start.Add(time.Second)})
	waitFill(t, filled)
	waitUntil(t, func() bool {
		_, usdtOK := node.Cache.Balance("USDT")
		_, btcOK := node.Cache.Balance("BTC")
		return usdtOK && btcOK
	}, "timed out waiting for spot balances")

	if cached, ok := node.Cache.Order(order.Request.ClientID); !ok || cached.Status != enums.StatusFilled {
		t.Fatalf("order not filled in cache: ok=%v order=%+v", ok, cached)
	}
	usdt, ok := node.Cache.Balance("USDT")
	if !ok || !usdt.Total.Equal(d("799.8")) || !usdt.Free.Equal(d("799.8")) || !usdt.Locked.IsZero() {
		t.Fatalf("USDT cache balance=%+v ok=%v, want total=available=799.8 locked=0", usdt, ok)
	}
	btc, ok := node.Cache.Balance("BTC")
	if !ok || !btc.Total.Equal(d("2")) || !btc.Free.Equal(d("2")) || !btc.Locked.IsZero() {
		t.Fatalf("BTC cache balance=%+v ok=%v, want total=available=2 locked=0", btc, ok)
	}
	if got := len(node.Cache.Positions()); got != 0 {
		t.Fatalf("cache positions=%d, want 0 because spot inventory is balance-sourced", got)
	}
	if got := node.Portfolio.NetQty(spotInst, enums.PosNet); !got.Equal(d("2")) {
		t.Fatalf("derived portfolio spot exposure=%s, want 2", got)
	}
}
