package runtime_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/lifecycle"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
	"github.com/QuantProcessing/boltertrader/runtime/strategy"
)

type startSubmitStrategy struct {
	strategy.Base
	started chan struct{}
	errs    chan error
}

func (s startSubmitStrategy) OnStart(c *strategy.Context) {
	_, err := c.Buy(inst, d("1"), d("100"))
	s.errs <- err
	close(s.started)
}

func TestRunStartsWithReconciliation(t *testing.T) {
	fexec := runtimetest.NewFakeExec()
	strat := startSubmitStrategy{started: make(chan struct{}), errs: make(chan error, 1)}
	node := runtime.NewNode(runtime.Clients{Execution: fexec}, nil, "life", runtime.WithStrategy(strat))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)

	select {
	case <-strat.started:
	case <-time.After(time.Second):
		t.Fatal("strategy OnStart not called")
	}
	if err := <-strat.errs; err != nil {
		t.Fatalf("OnStart submit should be accepted after startup reconciliation: %v", err)
	}
	if got := node.State(); got.Node != lifecycle.NodeRunning || got.Trading != lifecycle.TradingActive {
		t.Fatalf("state=%+v, want running/active", got)
	}
}

func TestStartupReconciliationFailureHalts(t *testing.T) {
	fexec := runtimetest.NewFakeExec()
	fexec.SetReportError(errors.New("report failed"))
	strat := startSubmitStrategy{started: make(chan struct{}), errs: make(chan error, 1)}
	node := runtime.NewNode(runtime.Clients{Execution: fexec}, nil, "life", runtime.WithStrategy(strat))
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)

	waitUntil(t, func() bool {
		return node.State().Node == lifecycle.NodeFailed
	}, "timed out waiting for failed startup")
	select {
	case <-strat.started:
		t.Fatal("strategy OnStart must not run after startup reconciliation failure")
	default:
	}
	_, err := node.Exec.Submit(context.Background(), model.OrderRequest{InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeLimit, Quantity: d("1"), Price: d("100")})
	if !errors.Is(err, lifecycle.ErrTradingBlocked) {
		t.Fatalf("submit err=%v, want lifecycle block", err)
	}
	if node.State().LastReconciliationError == "" {
		t.Fatalf("state missing reconciliation error: %+v", node.State())
	}
}

func TestPreRunResyncLeavesNodeStartable(t *testing.T) {
	fexec := runtimetest.NewFakeExec()
	strat := startSubmitStrategy{started: make(chan struct{}), errs: make(chan error, 1)}
	node := runtime.NewNode(runtime.Clients{Execution: fexec}, nil, "life", runtime.WithStrategy(strat))
	if _, err := node.Resync(context.Background()); err != nil {
		t.Fatalf("resync before run: %v", err)
	}
	if got := node.State(); got.Node != lifecycle.NodeCreated || got.Trading != lifecycle.TradingDisabled {
		t.Fatalf("state after pre-run resync=%+v, want created/disabled", got)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	select {
	case <-strat.started:
	case <-time.After(time.Second):
		t.Fatal("strategy OnStart not called after pre-run resync")
	}
	if err := <-strat.errs; err != nil {
		t.Fatalf("OnStart submit should be accepted after pre-run resync and Run: %v", err)
	}
	if got := node.State(); got.Node != lifecycle.NodeRunning || got.Trading != lifecycle.TradingActive {
		t.Fatalf("state=%+v, want running/active", got)
	}
}

func TestReconnectPausesAndReconcilesBeforeRunning(t *testing.T) {
	fmarket := runtimetest.NewFakeMarket()
	fexec := runtimetest.NewFakeExec()
	node := runtime.NewNode(runtime.Clients{Market: fmarket, Execution: fexec}, nil, "life")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)

	if _, err := node.Reconnect(context.Background()); err != nil {
		t.Fatalf("reconnect: %v", err)
	}
	if fmarket.Reconnects != 1 {
		t.Fatalf("reconnect calls=%d, want 1", fmarket.Reconnects)
	}
	if got := node.State(); got.Node != lifecycle.NodeRunning || got.Trading != lifecycle.TradingActive {
		t.Fatalf("state=%+v, want running/active", got)
	}
}

func TestReduceOnlyGate(t *testing.T) {
	fexec := runtimetest.NewFakeExec()
	node := runtime.NewNode(runtime.Clients{Execution: fexec}, nil, "life")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)

	node.ReduceOnly("operator")
	_, err := node.Exec.Submit(context.Background(), model.OrderRequest{InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeLimit, Quantity: d("1"), Price: d("100")})
	if !errors.Is(err, lifecycle.ErrTradingBlocked) {
		t.Fatalf("new exposure err=%v, want lifecycle block", err)
	}
	if _, err := node.Exec.Submit(context.Background(), model.OrderRequest{InstrumentID: inst, Side: enums.SideSell, Type: enums.TypeLimit, Quantity: d("1"), Price: d("100"), ReduceOnly: true}); err != nil {
		t.Fatalf("reduce-only submit should pass: %v", err)
	}
}

func TestHaltBlocksVenueMutatingCommands(t *testing.T) {
	fexec := runtimetest.NewFakeExec()
	node := runtime.NewNode(runtime.Clients{Execution: fexec}, nil, "life")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitNodeRunning(t, node)

	order, err := node.Exec.Submit(context.Background(), model.OrderRequest{InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeLimit, Quantity: d("1"), Price: d("100")})
	if err != nil {
		t.Fatalf("submit before halt: %v", err)
	}
	node.Halt("operator")
	_, err = node.Exec.Submit(context.Background(), model.OrderRequest{InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeLimit, Quantity: d("1"), Price: d("100")})
	if !errors.Is(err, lifecycle.ErrTradingBlocked) {
		t.Fatalf("submit err=%v, want lifecycle block", err)
	}
	if err := node.Exec.Cancel(context.Background(), order.Request.ClientID); !errors.Is(err, lifecycle.ErrTradingBlocked) {
		t.Fatalf("cancel err=%v, want lifecycle block", err)
	}
}
