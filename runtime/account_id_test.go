package runtime

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime/lifecycle"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
	"github.com/QuantProcessing/boltertrader/runtime/strategy"
	"github.com/shopspring/decimal"
)

type accountIDExec struct {
	*runtimetest.FakeExec
	accountID string
}

func (e *accountIDExec) AccountID() string { return e.accountID }

type accountIDAccount struct {
	*runtimetest.FakeAccount
	accountID string
}

func (a *accountIDAccount) AccountID() string { return a.accountID }

type recordingExec struct {
	*accountIDExec
	submits              int
	massStatusAccountIDs []string
}

func (e *recordingExec) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	e.submits++
	return e.accountIDExec.Submit(ctx, req)
}

func (e *recordingExec) GenerateExecutionMassStatus(ctx context.Context, query model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	e.massStatusAccountIDs = append(e.massStatusAccountIDs, query.AccountID)
	return e.accountIDExec.FakeExec.GenerateExecutionMassStatus(ctx, query)
}

type recordingAccount struct {
	*accountIDAccount
	balances  int
	positions int
}

func (a *recordingAccount) Balances(ctx context.Context) ([]model.AccountBalance, error) {
	a.balances++
	return a.accountIDAccount.Balances(ctx)
}

func (a *recordingAccount) Positions(ctx context.Context) ([]model.Position, error) {
	a.positions++
	return a.accountIDAccount.Positions(ctx)
}

type productionVenueExecWithoutAccountID struct {
	*runtimetest.FakeExec
	venue string
}

func (e *productionVenueExecWithoutAccountID) Capabilities() contract.Capabilities {
	caps := e.FakeExec.Capabilities()
	caps.Venue = e.venue
	return caps
}

type startCounterStrategy struct {
	strategy.Base
	starts int
}

func (s *startCounterStrategy) OnStart(*strategy.Context) { s.starts++ }

func TestResolveRuntimeAccountIDPrefersAdapterAndGuardsExpected(t *testing.T) {
	exec := &accountIDExec{FakeExec: runtimetest.NewFakeExec(), accountID: model.AccountIDBinanceDefault}
	account := &accountIDAccount{FakeAccount: runtimetest.NewFakeAccount(), accountID: model.AccountIDBinanceDefault}

	got, adapterBacked, err := resolveRuntimeAccountID(Clients{Execution: exec, Account: account}, "", "strategy")
	if err != nil {
		t.Fatalf("adapter-backed account id rejected: %v", err)
	}
	if got != model.AccountIDBinanceDefault || !adapterBacked {
		t.Fatalf("resolved account id=%q adapterBacked=%v, want %q true", got, adapterBacked, model.AccountIDBinanceDefault)
	}

	got, adapterBacked, err = resolveRuntimeAccountID(Clients{Execution: exec, Account: account}, model.AccountIDBinanceDefault, "strategy")
	if err != nil || got != model.AccountIDBinanceDefault || !adapterBacked {
		t.Fatalf("matching expected account id should be accepted: got=%q adapterBacked=%v err=%v", got, adapterBacked, err)
	}

	if _, _, err := resolveRuntimeAccountID(Clients{Execution: exec, Account: account}, "OTHER-001", "strategy"); err == nil {
		t.Fatal("expected account id mismatch should fail")
	}

	otherAccount := &accountIDAccount{FakeAccount: runtimetest.NewFakeAccount(), accountID: model.AccountIDOKXDefault}
	if _, _, err := resolveRuntimeAccountID(Clients{Execution: exec, Account: otherAccount}, "", "strategy"); err == nil {
		t.Fatal("execution/account adapter id mismatch should fail")
	}

	emptyExec := &accountIDExec{FakeExec: runtimetest.NewFakeExec()}
	if _, _, err := resolveRuntimeAccountID(Clients{Execution: emptyExec}, "", "strategy"); err == nil {
		t.Fatal("empty adapter account id should fail")
	}

	got, adapterBacked, err = resolveRuntimeAccountID(
		Clients{Execution: runtimetest.NewFakeExec(), Account: runtimetest.NewFakeAccount()},
		"",
		"strategy",
	)
	if err != nil || got != "strategy" || adapterBacked {
		t.Fatalf("pure-test fallback=%q adapterBacked=%v err=%v, want strategy false nil", got, adapterBacked, err)
	}

	got, adapterBacked, err = resolveRuntimeAccountID(Clients{}, "", "")
	if err != nil || got != "bt" || adapterBacked {
		t.Fatalf("empty fallback=%q adapterBacked=%v err=%v, want bt false nil", got, adapterBacked, err)
	}
}

func TestResolveRuntimeAccountIDFailsClosedForVenueClientWithoutProvider(t *testing.T) {
	exec := &productionVenueExecWithoutAccountID{FakeExec: runtimetest.NewFakeExec(), venue: "BINANCE"}
	if _, _, err := resolveRuntimeAccountID(Clients{Execution: exec}, model.AccountIDBinanceDefault, "strategy"); err == nil || !strings.Contains(err.Error(), "must expose AccountIDProvider") {
		t.Fatalf("missing provider error=%v, want fail-closed AccountIDProvider guard", err)
	}

	exec.venue = ""
	if _, _, err := resolveRuntimeAccountID(Clients{Execution: exec}, model.AccountIDBinanceDefault, "strategy"); err == nil || !strings.Contains(err.Error(), "empty venue") {
		t.Fatalf("empty venue missing-provider error=%v, want fail-closed empty venue guard", err)
	}
}

func TestNodeUsesAdapterAccountIDForSubmitAndReconcile(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	exec := &recordingExec{accountIDExec: &accountIDExec{
		FakeExec:  runtimetest.NewFakeExec(),
		accountID: model.AccountIDBinanceDefault,
	}}
	account := &recordingAccount{accountIDAccount: &accountIDAccount{
		FakeAccount: runtimetest.NewFakeAccount(),
		accountID:   model.AccountIDBinanceDefault,
	}}

	node := NewNode(
		Clients{Execution: exec, Account: account},
		clk,
		"strategy",
		WithAccountID(model.AccountIDBinanceDefault),
	)
	if node.accountIDConfigErr != nil {
		t.Fatalf("node account id config error: %v", node.accountIDConfigErr)
	}
	if node.accountID != model.AccountIDBinanceDefault || !node.adapterBackedAccountID {
		t.Fatalf("node account id=%q adapterBacked=%v, want %q true", node.accountID, node.adapterBackedAccountID, model.AccountIDBinanceDefault)
	}

	if _, err := node.Resync(context.Background()); err != nil {
		t.Fatalf("resync: %v", err)
	}
	if len(exec.massStatusAccountIDs) == 0 || exec.massStatusAccountIDs[len(exec.massStatusAccountIDs)-1] != model.AccountIDBinanceDefault {
		t.Fatalf("reconcile account ids=%v, want last %q", exec.massStatusAccountIDs, model.AccountIDBinanceDefault)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeState(t, node, lifecycle.NodeRunning)

	order, err := node.Exec.Submit(ctx, model.OrderRequest{
		InstrumentID: model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindSpot},
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     decimal.RequireFromString("1"),
		Price:        decimal.RequireFromString("100"),
	})
	if err != nil {
		t.Fatalf("submit: %v", err)
	}
	if order.Request.AccountID != model.AccountIDBinanceDefault {
		t.Fatalf("submitted account id=%q, want %q", order.Request.AccountID, model.AccountIDBinanceDefault)
	}
}

func TestNodeAccountIDMismatchFailsBeforeLifecycleSideEffects(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	exec := &recordingExec{accountIDExec: &accountIDExec{
		FakeExec:  runtimetest.NewFakeExec(),
		accountID: model.AccountIDBinanceDefault,
	}}
	account := &recordingAccount{accountIDAccount: &accountIDAccount{
		FakeAccount: runtimetest.NewFakeAccount(),
		accountID:   model.AccountIDOKXDefault,
	}}
	strat := &startCounterStrategy{}
	node := NewNode(Clients{Execution: exec, Account: account}, clk, "strategy", WithStrategy(strat))
	if node.accountIDConfigErr == nil {
		t.Fatal("node should record an account id configuration error")
	}

	if _, err := node.Resync(context.Background()); err == nil {
		t.Fatal("resync should fail before touching adapter snapshots")
	}
	if account.balances != 0 || account.positions != 0 || len(exec.massStatusAccountIDs) != 0 {
		t.Fatalf("resync crossed adapter boundary: balances=%d positions=%d mass=%v", account.balances, account.positions, exec.massStatusAccountIDs)
	}

	node.Run(context.Background())
	if state := node.State(); state.Node != lifecycle.NodeFailed || !strings.Contains(state.Reason, "does not match") {
		t.Fatalf("state=%+v, want failed mismatch", state)
	}
	if strat.starts != 0 {
		t.Fatalf("strategy started despite account id config error: starts=%d", strat.starts)
	}

	_, err := node.Exec.Submit(context.Background(), model.OrderRequest{
		InstrumentID: model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindSpot},
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     decimal.RequireFromString("1"),
		Price:        decimal.RequireFromString("100"),
	})
	if !errors.Is(err, lifecycle.ErrTradingBlocked) {
		t.Fatalf("submit err=%v, want lifecycle trading block", err)
	}
	if exec.submits != 0 {
		t.Fatalf("submit crossed adapter boundary %d time(s)", exec.submits)
	}
}

func TestNodeAccountIDFallbacksRemainExplicitForPureTests(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	node := NewNode(
		Clients{Execution: runtimetest.NewFakeExec(), Account: runtimetest.NewFakeAccount()},
		clk,
		"legacy-test",
	)
	if node.accountID != "legacy-test" || node.adapterBackedAccountID {
		t.Fatalf("fallback account id=%q adapterBacked=%v, want legacy-test false", node.accountID, node.adapterBackedAccountID)
	}

	node = NewNode(Clients{}, clk, "")
	if node.accountID != "bt" || node.adapterBackedAccountID {
		t.Fatalf("empty fallback account id=%q adapterBacked=%v, want bt false", node.accountID, node.adapterBackedAccountID)
	}

	node = NewNode(
		Clients{Execution: runtimetest.NewFakeExec()},
		clk,
		"strategy",
		WithAccountID("TEST-001"),
	)
	if node.accountID != "TEST-001" || node.adapterBackedAccountID {
		t.Fatalf("explicit pure-test account id=%q adapterBacked=%v, want TEST-001 false", node.accountID, node.adapterBackedAccountID)
	}
}

func TestNodeStampsResolvedAccountIDOnAccountEventsAndExternalFills(t *testing.T) {
	clk := clock.NewSimulatedClock(time.Date(2026, 1, 1, 0, 0, 0, 0, time.UTC))
	node := NewNode(Clients{}, clk, "acct-1")
	positionID := model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindPerp}
	fillID := model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindSpot}

	node.onAccount(contract.NewAccountEnvelopeWithMeta(contract.BalanceEvent{
		Balance: model.AccountBalance{Currency: "USDT", Total: decimal.RequireFromString("100"), Free: decimal.RequireFromString("90")},
	}, contract.EventMeta{}))
	if b, ok := node.Cache.BalanceForAccount("acct-1", "USDT"); !ok || !b.Free.Equal(decimal.RequireFromString("90")) {
		t.Fatalf("stamped balance=%+v ok=%v, want acct-1 free 90", b, ok)
	}

	node.onAccount(contract.NewAccountEnvelopeWithMeta(contract.PositionEvent{
		Position: model.Position{InstrumentID: positionID, Side: enums.PosNet, Quantity: decimal.RequireFromString("1")},
	}, contract.EventMeta{}))
	if p, ok := node.Cache.PositionForAccount("acct-1", positionID, enums.PosNet); !ok || !p.Quantity.Equal(decimal.RequireFromString("1")) {
		t.Fatalf("stamped position=%+v ok=%v, want acct-1 qty 1", p, ok)
	}

	fill := model.Fill{
		InstrumentID: fillID,
		VenueOrderID: "venue-external",
		TradeID:      "trade-external",
		Side:         enums.SideBuy,
		Price:        decimal.RequireFromString("100"),
		Quantity:     decimal.RequireFromString("2"),
		Timestamp:    clk.Now(),
	}
	node.applyOrBufferFill(fill, contract.NewExecEnvelopeWithMeta(contract.FillEvent{Fill: fill}, contract.EventMeta{}))
	order, ok := node.Cache.Order("external-acct-1-venue-external-trade-external")
	if !ok {
		t.Fatal("materialized account-scoped external order missing")
	}
	if order.Request.AccountID != "acct-1" {
		t.Fatalf("materialized order account id=%q, want acct-1", order.Request.AccountID)
	}
	if got := node.Portfolio.NetQtyForAccount("acct-1", fillID, enums.PosNet); !got.Equal(decimal.RequireFromString("2")) {
		t.Fatalf("portfolio acct-1 net qty=%s, want 2", got)
	}
}

func waitNodeState(t *testing.T, node *TradingNode, want lifecycle.NodeState) {
	t.Helper()
	deadline := time.After(time.Second)
	tick := time.NewTicker(5 * time.Millisecond)
	defer tick.Stop()
	for {
		if node.State().Node == want {
			return
		}
		select {
		case <-deadline:
			t.Fatalf("timed out waiting for node state %s: got %+v", want, node.State())
		case <-tick.C:
		}
	}
}
