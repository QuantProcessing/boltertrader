package runtime_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/lifecycle"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
	"github.com/shopspring/decimal"
)

type gapAwareExec struct {
	*runtimetest.FakeExec
	events          chan contract.ExecEnvelope
	fillHistory     bool
	reconcileCalls  atomic.Int64
	blockRecovery   atomic.Bool
	recoveryStarted chan struct{}
	releaseRecovery chan struct{}
	startOnce       sync.Once
}

func newGapAwareExec(fillHistory bool) *gapAwareExec {
	return &gapAwareExec{
		FakeExec:        runtimetest.NewFakeExec(),
		events:          make(chan contract.ExecEnvelope, 32),
		fillHistory:     fillHistory,
		recoveryStarted: make(chan struct{}),
		releaseRecovery: make(chan struct{}),
	}
}

func (e *gapAwareExec) Capabilities() contract.Capabilities {
	caps := e.FakeExec.Capabilities()
	caps.Reports.FillHistory = e.fillHistory
	return caps
}

func (e *gapAwareExec) Events() <-chan contract.ExecEnvelope { return e.events }

func (e *gapAwareExec) Close() error {
	close(e.events)
	return nil
}

func (e *gapAwareExec) GenerateExecutionMassStatus(ctx context.Context, query model.MassStatusQuery) (*model.ExecutionMassStatus, error) {
	call := e.reconcileCalls.Add(1)
	if call > 1 && e.blockRecovery.Load() {
		e.startOnce.Do(func() { close(e.recoveryStarted) })
		select {
		case <-e.releaseRecovery:
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	return e.FakeExec.GenerateExecutionMassStatus(ctx, query)
}

func (e *gapAwareExec) emitGap(streamID string, generation uint64, phase contract.StreamGapPhase) {
	e.events <- contract.NewExecEnvelope(contract.StreamGapEvent{
		Venue:      "FAKE",
		AccountID:  "gap",
		StreamID:   streamID,
		Generation: generation,
		Phase:      phase,
	})
}

func TestAutomaticPrivateStreamGapBlocksUntilReconciliationCompletes(t *testing.T) {
	exec := newGapAwareExec(true)
	exec.blockRecovery.Store(true)
	var submits atomic.Int64
	exec.OnSubmit(func(model.OrderRequest) { submits.Add(1) })
	node := runtime.NewNode(runtime.Clients{Execution: exec}, nil, "gap")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitTradingState(t, node, lifecycle.TradingActive)

	exec.emitGap("private", 1, contract.StreamGapStarted)
	waitTradingState(t, node, lifecycle.TradingReconciling)

	_, err := node.Exec.Submit(context.Background(), model.OrderRequest{
		InstrumentID: model.InstrumentID{Venue: "FAKE", Symbol: "BTC-USDT", Kind: enums.KindPerp},
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     decimal.NewFromInt(1),
		Price:        decimal.NewFromInt(100),
	})
	if !errors.Is(err, lifecycle.ErrTradingBlocked) {
		t.Fatalf("submit during gap err=%v, want trading blocked", err)
	}
	if got := submits.Load(); got != 0 {
		t.Fatalf("venue submits during gap=%d, want 0", got)
	}

	exec.emitGap("private", 1, contract.StreamGapRecovered)
	select {
	case <-exec.recoveryStarted:
	case <-time.After(time.Second):
		t.Fatal("recovery reconciliation did not start")
	}
	if got := node.State().Trading; got == lifecycle.TradingActive {
		t.Fatalf("trading became active before reconciliation completed: %s", got)
	}
	close(exec.releaseRecovery)
	waitTradingState(t, node, lifecycle.TradingActive)
	if got := exec.reconcileCalls.Load(); got != 2 {
		t.Fatalf("reconciliation calls=%d, want startup + one recovery", got)
	}
}

func TestOverlappingAndStalePrivateStreamGapsReconcileOnce(t *testing.T) {
	exec := newGapAwareExec(true)
	node := runtime.NewNode(runtime.Clients{Execution: exec}, nil, "gap")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitTradingState(t, node, lifecycle.TradingActive)

	exec.emitGap("spot-private", 4, contract.StreamGapStarted)
	exec.emitGap("futures-private", 9, contract.StreamGapStarted)
	waitTradingState(t, node, lifecycle.TradingReconciling)
	exec.emitGap("spot-private", 4, contract.StreamGapRecovered)
	assertReconcileCallsRemain(t, exec, 1)
	exec.emitGap("futures-private", 8, contract.StreamGapRecovered) // stale
	assertReconcileCallsRemain(t, exec, 1)
	exec.emitGap("futures-private", 9, contract.StreamGapRecovered)
	waitTradingState(t, node, lifecycle.TradingActive)
	waitReconcileCalls(t, exec, 2)

	exec.emitGap("futures-private", 9, contract.StreamGapRecovered) // duplicate
	exec.emitGap("spot-private", 3, contract.StreamGapStarted)      // stale
	assertReconcileCallsRemain(t, exec, 2)
}

func TestAutomaticGapPreservesOperatorHalt(t *testing.T) {
	exec := newGapAwareExec(true)
	node := runtime.NewNode(runtime.Clients{Execution: exec}, nil, "gap")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitTradingState(t, node, lifecycle.TradingActive)

	node.Halt("operator")
	exec.emitGap("private", 1, contract.StreamGapStarted)
	exec.emitGap("private", 1, contract.StreamGapRecovered)
	waitReconcileCalls(t, exec, 2)
	waitTradingState(t, node, lifecycle.TradingHalted)
}

func TestAutomaticGapPreservesOperatorHaltSetDuringGap(t *testing.T) {
	exec := newGapAwareExec(true)
	node := runtime.NewNode(runtime.Clients{Execution: exec}, nil, "gap")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitTradingState(t, node, lifecycle.TradingActive)

	exec.emitGap("private", 1, contract.StreamGapStarted)
	waitTradingState(t, node, lifecycle.TradingReconciling)
	node.Halt("operator during gap")
	exec.emitGap("private", 1, contract.StreamGapRecovered)
	waitReconcileCalls(t, exec, 2)
	waitTradingState(t, node, lifecycle.TradingHalted)
}

func TestAutomaticGapWithoutFillHistoryStaysRestricted(t *testing.T) {
	exec := newGapAwareExec(false)
	node := runtime.NewNode(runtime.Clients{Execution: exec}, nil, "gap")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitTradingState(t, node, lifecycle.TradingActive)

	exec.emitGap("private", 1, contract.StreamGapStarted)
	exec.emitGap("private", 1, contract.StreamGapRecovered)
	waitReconcileCalls(t, exec, 2)
	waitTradingState(t, node, lifecycle.TradingReconciling)
}

func TestMissingFillHistoryGapCannotBeClearedByManualResync(t *testing.T) {
	exec := newGapAwareExec(false)
	node := runtime.NewNode(runtime.Clients{Execution: exec}, nil, "gap")
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go node.Run(ctx)
	waitTradingState(t, node, lifecycle.TradingActive)

	exec.emitGap("private", 1, contract.StreamGapStarted)
	exec.emitGap("private", 1, contract.StreamGapRecovered)
	waitReconcileCalls(t, exec, 2)
	waitTradingState(t, node, lifecycle.TradingReconciling)

	if _, err := node.Resync(context.Background()); err != nil {
		t.Fatalf("manual resync: %v", err)
	}
	if got := node.State(); got.Trading != lifecycle.TradingReconciling {
		t.Fatalf("state after manual resync=%+v, missing gap fills must remain restricted", got)
	}
}

func waitTradingState(t *testing.T, node *runtime.TradingNode, want lifecycle.TradingState) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if node.State().Trading == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("trading state=%+v, want %s", node.State(), want)
}

func waitReconcileCalls(t *testing.T, exec *gapAwareExec, want int64) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if exec.reconcileCalls.Load() == want {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatalf("reconciliation calls=%d, want %d", exec.reconcileCalls.Load(), want)
}

func assertReconcileCallsRemain(t *testing.T, exec *gapAwareExec, want int64) {
	t.Helper()
	timer := time.NewTimer(50 * time.Millisecond)
	defer timer.Stop()
	<-timer.C
	if got := exec.reconcileCalls.Load(); got != want {
		t.Fatalf("reconciliation calls=%d, want %d", got, want)
	}
}
