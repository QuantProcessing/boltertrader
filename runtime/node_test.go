package runtime_test

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/lifecycle"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
	"github.com/shopspring/decimal"
)

func d(s string) decimal.Decimal { return decimal.RequireFromString(s) }

var inst = model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindPerp}

// TestVerticalSlice is the P4 acceptance test: submit -> ack -> fill -> cache +
// portfolio, end to end, through the TradingNode on a SimulatedClock with no
// network. It proves the runtime manages order/fill/position/PnL state purely
// over the contract interfaces.
func TestVerticalSlice(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fexec := runtimetest.NewFakeExec()
	facct := runtimetest.NewFakeAccount()

	filled := make(chan model.Fill, 8)
	node := runtime.NewNode(
		runtime.Clients{Execution: fexec, Account: facct},
		clk, "test",
		runtime.WithOnFill(func(f model.Fill) { filled <- f }),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)

	// 1. Submit a buy order through the exec engine.
	order, err := node.Exec.Submit(ctx, model.OrderRequest{
		InstrumentID: inst,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("2"),
		Price:        d("100"),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if order.Status != enums.StatusNew {
		t.Fatalf("ack status=%v, want NEW", order.Status)
	}

	// Cache should hold the acknowledged order under its client id.
	if o, ok := node.Cache.Order(order.Request.ClientID); !ok || o.Status != enums.StatusNew {
		t.Fatalf("cache missing acked order: ok=%v", ok)
	}

	// 2. Venue pushes a full fill for the order.
	fexec.EmitFill(model.Fill{
		InstrumentID: inst,
		VenueOrderID: order.VenueOrderID,
		ClientID:     order.Request.ClientID,
		Side:         enums.SideBuy,
		Liquidity:    enums.LiqMaker,
		Price:        d("100"),
		Quantity:     d("2"),
		Fee:          d("0.1"),
	})
	waitFill(t, filled)

	// Portfolio reflects the open long at avg 100.
	if got := node.Portfolio.NetQty(inst, enums.PosNet); !got.Equal(d("2")) {
		t.Fatalf("netQty=%s, want 2", got)
	}
	if got := node.Portfolio.UnrealizedPnL(inst, enums.PosNet, d("110")); !got.Equal(d("20")) {
		t.Fatalf("unrealized@110=%s, want 20", got)
	}

	// 3. Venue pushes a closing sell fill at 110 => +20 realized, minus fees.
	fexec.EmitFill(model.Fill{
		InstrumentID: inst,
		VenueOrderID: order.VenueOrderID,
		ClientID:     order.Request.ClientID,
		Side:         enums.SideSell,
		Liquidity:    enums.LiqTaker,
		Price:        d("110"),
		Quantity:     d("2"),
		Fee:          d("0.1"),
	})
	waitFill(t, filled)

	if got := node.Portfolio.RealizedPnL(); !got.Equal(d("20")) {
		t.Fatalf("realized=%s, want 20", got)
	}
	if got := node.Portfolio.RealizedPnLNetFees(); !got.Equal(d("19.8")) { // 20 - 0.2
		t.Fatalf("net=%s, want 19.8", got)
	}
	if got := node.Portfolio.NetQty(inst, enums.PosNet); !got.IsZero() {
		t.Fatalf("netQty=%s, want flat", got)
	}

	// 4. Account push updates the cache balance/position.
	facct.EmitBalance(model.AccountBalance{Currency: "USDT", Total: d("10019.8"), Available: d("10019.8")})
	facct.EmitPosition(model.Position{InstrumentID: inst, Side: enums.PosNet, Quantity: d("0")})

	// Drain by advancing through a sync point: emit a sentinel fill and wait.
	fexec.EmitFill(model.Fill{InstrumentID: inst, ClientID: order.Request.ClientID, Side: enums.SideBuy, Price: d("0"), Quantity: d("0")})
	waitFill(t, filled)

	if b, ok := node.Cache.Balance("USDT"); !ok || !b.Total.Equal(d("10019.8")) {
		t.Fatalf("cache balance not updated: ok=%v", ok)
	}
}

func TestAccountStateEventAppliesToCache(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fexec := runtimetest.NewFakeExec()
	facct := runtimetest.NewFakeAccount()
	filled := make(chan model.Fill, 1)
	node := runtime.NewNode(
		runtime.Clients{Execution: fexec, Account: facct},
		clk, "test",
		runtime.WithOnFill(func(f model.Fill) { filled <- f }),
	)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)

	ts := clk.Now()
	state := model.AccountState{
		AccountID: model.AccountIDBinanceDefault,
		Venue:     "BINANCE",
		Type:      model.AccountCash,
		Balances: []model.AccountBalance{{
			Currency: "USDT",
			Total:    d("100"),
			Free:     d("100"),
		}},
		ModeInfo: model.AccountModeInfo{
			Venue:        "BINANCE",
			AccountID:    model.AccountIDBinanceDefault,
			AccountMode:  "spot",
			ProductScope: []enums.InstrumentKind{enums.KindSpot},
			Verified:     true,
			VerifiedAt:   ts,
			Source:       "test",
		},
		Reported: true,
		TsEvent:  ts,
	}
	facct.EmitAccountState(state)
	waitUntil(t, func() bool {
		_, ok := node.Cache.Account(model.AccountIDBinanceDefault)
		return ok
	}, "timed out waiting for account state")

	acct, ok := node.Cache.Account(model.AccountIDBinanceDefault)
	if !ok || acct.ID() != model.AccountIDBinanceDefault {
		t.Fatalf("cache account missing: ok=%v acct=%v", ok, acct)
	}
	if b, ok := node.Cache.Balance("USDT"); !ok || !b.Free.Equal(d("100")) {
		t.Fatalf("compat balance=%+v ok=%v, want free 100", b, ok)
	}
	if m := node.Metrics(); m.Accounts != 1 || m.AccountStateAgeNs < 0 {
		t.Fatalf("metrics did not expose account state: %+v", m)
	}
}

// TestReconnectForcesReconnectAndReconciles proves node.Reconnect drives a
// Reconnectable client's Reconnect and THEN reconciles state: an open order the
// venue reports but the cache never saw is adopted (the post-reconnect repair
// loop the review flagged as half-wired).
func TestReconnectForcesReconnectAndReconciles(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	fmarket := runtimetest.NewFakeMarket()
	fexec := runtimetest.NewFakeExec()
	facct := runtimetest.NewFakeAccount()

	// The venue reports one resting order the cache has never seen.
	fexec.SetOrderStatusReports(model.Order{
		Request:      model.OrderRequest{ClientID: "ext-1", InstrumentID: inst},
		VenueOrderID: "v-ext-1",
		Status:       enums.StatusNew,
	})

	node := runtime.NewNode(
		runtime.Clients{Market: fmarket, Execution: fexec, Account: facct},
		clk, "test",
	)

	rep, err := node.Reconnect(context.Background())
	if err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if fmarket.Reconnects != 1 || !fmarket.Connected() {
		t.Fatalf("market not reconnected: calls=%d connected=%v", fmarket.Reconnects, fmarket.Connected())
	}
	if rep.OrdersExternal != 1 {
		t.Fatalf("report=%+v, want OrdersExternal=1", rep)
	}
	if o, ok := node.Cache.Order("ext-1"); !ok || o.Status != enums.StatusNew {
		t.Fatalf("external order not adopted after reconnect: ok=%v", ok)
	}
}

// waitFill waits for one fill callback or fails after a short timeout. Wall-time
// is used only as a test safety net; the node logic itself is clock-driven.
func waitFill(t *testing.T, ch <-chan model.Fill) {
	t.Helper()
	select {
	case <-ch:
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for fill to be applied")
	}
}

func waitUntil(t *testing.T, cond func() bool, msg string) {
	t.Helper()
	deadline := time.After(time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if cond() {
			return
		}
		select {
		case <-deadline:
			t.Fatal(msg)
		case <-tick.C:
		}
	}
}

func waitNodeRunning(t *testing.T, node *runtime.TradingNode) {
	t.Helper()
	waitUntil(t, func() bool {
		return node.State().Node == lifecycle.NodeRunning
	}, "timed out waiting for node running")
}
