package runtime_test

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/risk"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
	"github.com/QuantProcessing/boltertrader/runtime/strategy"
)

type mirrorStrategy struct {
	strategy.Base
	starts atomic.Int64
	fills  atomic.Int64
	stops  atomic.Int64
}

func (s *mirrorStrategy) OnStart(*strategy.Context) { s.starts.Add(1) }
func (s *mirrorStrategy) OnFill(*strategy.Context, model.Fill) {
	s.fills.Add(1)
}
func (s *mirrorStrategy) OnStop(*strategy.Context) { s.stops.Add(1) }

func TestOfflineRuntimeMirrorAndBoundedReconcile(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	fexec := runtimetest.NewFakeExec().WithClock(clk)
	fexec.SetAccountID("mirror")
	fexec.SetInstruments(inst)
	facct := runtimetest.NewFakeAccount()
	facct.SetAccountID("mirror")
	facct.SetAccountStateSnapshot(authoritativeAccountState("mirror", model.AccountMargin, start))
	obs := &recordingObserver{}
	strat := &mirrorStrategy{}

	node := runtime.NewNode(
		runtime.Clients{Execution: fexec, Account: facct},
		clk,
		"mirror",
		runtime.WithStrategy(strat),
		runtime.WithObserver(obs),
	)

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() {
		node.Run(ctx)
		close(done)
	}()
	waitUntil(t, func() bool {
		return strat.starts.Load() == 1 && obs.starts.Load() == 1
	}, "timed out waiting for node start")

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

	filled := *order
	filled.Status = enums.StatusFilled
	filled.FilledQty = d("2")
	filled.AvgFillPrice = d("100")
	filled.UpdatedAt = start.Add(time.Second)
	fexec.EmitOrder(filled)
	fexec.EmitFill(model.Fill{
		InstrumentID: inst,
		VenueOrderID: order.VenueOrderID,
		ClientID:     order.Request.ClientID,
		TradeID:      "t-1",
		Side:         enums.SideBuy,
		Liquidity:    enums.LiqMaker,
		Price:        d("100"),
		Quantity:     d("2"),
		Fee:          d("0.1"),
		FeeCurrency:  "USDT",
		Timestamp:    start.Add(time.Second),
	})
	facct.EmitBalance(model.AccountBalance{Currency: "USDT", Total: d("9999.9"), Free: d("9000"), UpdatedAt: start.Add(time.Second)})
	facct.EmitPosition(model.Position{InstrumentID: inst, Side: enums.PosNet, Quantity: d("2"), EntryPrice: d("100"), UpdatedAt: start.Add(time.Second)})
	waitUntil(t, func() bool {
		m := node.Metrics()
		return m.OrdersSeen == 1 && m.FillsSeen == 1 && m.Positions == 1
	}, "timed out waiting for fake venue events")

	if o, ok := node.Cache.Order(order.Request.ClientID); !ok || o.Status != enums.StatusFilled {
		t.Fatalf("order not filled in cache: ok=%v status=%v", ok, o.Status)
	}
	if got := node.Portfolio.NetQty(inst, enums.PosNet); !got.Equal(d("2")) {
		t.Fatalf("netQty=%s, want 2", got)
	}
	if got := node.Portfolio.UnrealizedPnL(inst, enums.PosNet, d("110")); !got.Equal(d("20")) {
		t.Fatalf("unrealized@110=%s, want 20", got)
	}
	if got := node.Portfolio.Fees(); !got.Equal(d("0.1")) {
		t.Fatalf("fees=%s, want 0.1", got)
	}
	if b, ok := node.Cache.Balance("USDT"); !ok || !b.Total.Equal(d("9999.9")) || !b.Free.Equal(d("9000")) {
		t.Fatalf("balance not mirrored: ok=%v balance=%+v", ok, b)
	}
	if p, ok := node.Cache.Position(inst, enums.PosNet); !ok || !p.Quantity.Equal(d("2")) || !p.EntryPrice.Equal(d("100")) {
		t.Fatalf("position not mirrored: ok=%v position=%+v", ok, p)
	}
	if strat.starts.Load() != 1 || strat.fills.Load() != 1 {
		t.Fatalf("strategy callbacks starts=%d fills=%d, want 1/1", strat.starts.Load(), strat.fills.Load())
	}
	if obs.starts.Load() != 1 || obs.orders.Load() != 1 || obs.fills.Load() != 1 {
		t.Fatalf("observer callbacks starts=%d orders=%d fills=%d, want 1/1/1", obs.starts.Load(), obs.orders.Load(), obs.fills.Load())
	}
	before := node.Metrics()
	if before.OpenOrders != 0 || before.Positions != 1 || before.OrdersSeen != 1 || before.FillsSeen != 1 {
		t.Fatalf("metrics before reconcile=%+v, want open=0 positions=1 orders=1 fills=1", before)
	}

	gapOrder := model.Order{
		Request: model.OrderRequest{
			ClientID:     "gap-1",
			InstrumentID: inst,
			Side:         enums.SideSell,
			Type:         enums.TypeLimit,
			TIF:          enums.TifGTC,
			Quantity:     d("1"),
			Price:        d("120"),
		},
		VenueOrderID: "v-gap-1",
		Status:       enums.StatusNew,
	}
	node.Cache.UpsertOrder(gapOrder)
	facct.SetAccountStateSnapshot(authoritativeAccountState(
		"mirror",
		model.AccountMargin,
		start.Add(2*time.Second),
		model.AccountBalance{Currency: "USDT", Total: d("9999.9"), Free: d("9000"), UpdatedAt: start.Add(2 * time.Second)},
	))
	facct.SetSnapshots(
		nil,
		[]model.Position{{InstrumentID: inst, Side: enums.PosNet, Quantity: d("2"), EntryPrice: d("100"), UpdatedAt: start.Add(2 * time.Second)}},
	)
	fexec.SetOrderStatusReports()

	rep, err := node.Resync(ctx)
	if err != nil {
		t.Fatalf("resync: %v", err)
	}
	if rep.BalancesUpdated != 1 || rep.PositionsUpdated != 0 || rep.OrdersClosedUnknown != 1 || rep.OrdersCleared != 0 {
		t.Fatalf("reconcile report=%+v, want balances=1 directPositions=0 closedUnknown=1 cleared=0", rep)
	}
	if o, ok := node.Cache.Order("gap-1"); !ok || o.Status != enums.StatusUnknown {
		t.Fatalf("gap order status=%v ok=%v, want unknown closed", o.Status, ok)
	}
	foundUnknown := false
	for _, o := range node.Cache.OpenOrders() {
		if o.Request.ClientID == "gap-1" {
			foundUnknown = o.Status == enums.StatusUnknown
		}
	}
	if !foundUnknown {
		t.Fatal("unknown gap order must remain in the unresolved working-order set")
	}
	after := node.Metrics()
	if after.FillsSeen != before.FillsSeen || !after.RealizedPnL.Equal(before.RealizedPnL) || !after.Fees.Equal(before.Fees) {
		t.Fatalf("reconcile invented execution impact: before=%+v after=%+v", before, after)
	}

	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("node did not stop")
	}
	if strat.stops.Load() != 1 || obs.stops.Load() != 1 {
		t.Fatalf("stop callbacks strategy=%d observer=%d, want 1/1", strat.stops.Load(), obs.stops.Load())
	}
}

func TestOfflineAccountStateSnapshotReconcilesPortfolioAndRisk(t *testing.T) {
	start := time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC)
	clk := clock.NewSimulatedClock(start)
	fexec := runtimetest.NewFakeExec().WithClock(clk)
	fexec.SetInstruments(inst)
	facct := runtimetest.NewFakeAccount()
	accountID := "FAKE:perp"
	instMeta := &model.Instrument{
		ID:                 inst,
		Base:               "BTC",
		Quote:              "USDT",
		Settle:             "USDT",
		ContractMultiplier: d("1"),
	}
	marginInst := inst
	state := model.AccountState{
		AccountID:    accountID,
		Venue:        "FAKE",
		Type:         model.AccountMargin,
		BaseCurrency: "USDT",
		Balances: []model.AccountBalance{{
			Currency: "USDT",
			Total:    d("1000"),
			Free:     d("900"),
		}},
		Margins: []model.MarginBalance{{
			Currency:     "USDT",
			InstrumentID: &marginInst,
			Initial:      d("50"),
			Maintenance:  d("25"),
			UpdatedAt:    start,
		}},
		Reported: true,
		EventID:  model.AccountStateEventID("FAKE", accountID, start),
		TsEvent:  start,
		TsInit:   start,
	}
	facct.SetAccountStateSnapshot(state)
	fexec.SetAccountID(accountID)
	facct.SetAccountID(accountID)
	facct.SetSnapshots(nil, []model.Position{{
		AccountID:     accountID,
		InstrumentID:  inst,
		Side:          enums.PosNet,
		Quantity:      d("1"),
		EntryPrice:    d("100"),
		MarkPrice:     d("110"),
		UnrealizedPnL: d("10"),
		UpdatedAt:     start,
	}})
	fexec.SetOrderStatusReports()

	node := runtime.NewNode(
		runtime.Clients{Execution: fexec, Account: facct},
		clk,
		"account-state",
		runtime.WithAccountID(accountID),
		runtime.WithAccountStaleAfter(10*time.Second),
	)
	// Position reconciliation is evidence-only: seed the stream-derived cache
	// projection so the authoritative report can confirm it without bypassing
	// the cache/portfolio/event path.
	node.Cache.UpsertPosition(model.Position{
		AccountID:     accountID,
		InstrumentID:  inst,
		Side:          enums.PosNet,
		Quantity:      d("1"),
		EntryPrice:    d("100"),
		MarkPrice:     d("110"),
		UnrealizedPnL: d("10"),
		UpdatedAt:     start,
	})

	rep, err := node.Resync(context.Background())
	if err != nil {
		t.Fatalf("resync account state: %v", err)
	}
	if rep.AccountStatesApplied != 1 || rep.BalancesUpdated != 1 || rep.PositionsUpdated != 0 || !rep.ActivationVerdict().Safe {
		t.Fatalf("reconcile report=%+v, want account=1 balances=1 directPositions=0 safe", rep)
	}

	acct, ok := node.Cache.Account(accountID)
	if !ok {
		t.Fatalf("cache account %s missing", accountID)
	}
	if acct.Type() != model.AccountMargin || !acct.IsFresh(start.Add(2*time.Second)) {
		t.Fatalf("account not trading-ready enough for offline gate: type=%s freshness=%+v", acct.Type(), acct.Freshness())
	}
	if acct.Freshness().LastReconciledAt.IsZero() {
		t.Fatalf("account freshness missing reconciliation timestamp: %+v", acct.Freshness())
	}
	if b, ok := node.Cache.Balance("USDT"); !ok || !b.Total.Equal(d("1000")) || !b.Free.Equal(d("900")) {
		t.Fatalf("legacy balance mirror=%+v ok=%v, want total=1000 free=900", b, ok)
	}
	if p, ok := node.Cache.Position(inst, enums.PosNet); !ok || !p.Quantity.Equal(d("1")) || !p.UnrealizedPnL.Equal(d("10")) {
		t.Fatalf("position mirror=%+v ok=%v, want qty=1 upnl=10", p, ok)
	}

	if got, ok := node.Portfolio.Equity(accountID); !ok || !got["USDT"].Equal(d("1010")) {
		t.Fatalf("portfolio equity=%v ok=%v, want USDT=1010", got, ok)
	}
	if got, ok := node.Portfolio.MarginInitial(accountID); !ok || !got["USDT"].Equal(d("50")) {
		t.Fatalf("portfolio initial margin=%v ok=%v, want USDT=50", got, ok)
	}
	if got, ok := node.Portfolio.MarginMaintenance(accountID); !ok || !got["USDT"].Equal(d("25")) {
		t.Fatalf("portfolio maintenance margin=%v ok=%v, want USDT=25", got, ok)
	}
	if got, ok := node.Portfolio.NetExposure(accountID); !ok || !got[inst].Equal(d("110")) {
		t.Fatalf("portfolio net exposure=%v ok=%v, want %s=110", got, ok, inst)
	}

	engine := risk.New(risk.Limits{MaxOrderNotional: d("900")}, node.Cache).
		WithClock(func() time.Time { return start.Add(2 * time.Second) }).
		RequireAccountState()
	executionCaps := fexec.Capabilities()
	accountCaps := facct.Capabilities()
	engine.SetRuntimeCapabilities(&executionCaps, &accountCaps)
	if err := checkSubmissionRisk(engine, model.OrderRequest{
		ClientID:     "risk-pass",
		InstrumentID: inst,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("1"),
		Price:        d("100"),
		PositionSide: enums.PosNet,
	}, instMeta); err != nil {
		t.Fatalf("fresh account risk check rejected valid order: %v", err)
	}
	err = checkSubmissionRisk(engine, model.OrderRequest{
		ClientID:     "risk-reject",
		InstrumentID: inst,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     d("10"),
		Price:        d("100"),
		PositionSide: enums.PosNet,
	}, instMeta)
	if !errors.Is(err, risk.ErrRiskRejected) {
		t.Fatalf("oversized account risk check err=%v, want risk rejection", err)
	}
}
