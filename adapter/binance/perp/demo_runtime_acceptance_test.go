package perp

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/runtimetest"
)

func TestBinanceDemoRuntimeAcceptance(t *testing.T) {
	testenv.RequireBinanceDemoWrite(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	adapter, spec, instID, qty, restingPrice := newBinanceDemoRuntimeAcceptanceFixture(t, ctx)
	defer adapter.Close()
	defer func() {
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelCleanup()
		meta := demoAcceptanceCleanupMetadata{Symbol: spec.VenueSymbol, Side: "BUY", Quantity: qty}
		if err := cleanupBinanceDemoAcceptance(cleanupCtx, adapter, instID, &meta); err != nil {
			t.Fatalf("%v\n%s", err, meta.Remediation())
		}
	}()

	tester := runtimetest.NewExecTester(runtimetest.ExecTesterConfig{
		InstrumentID:   instID,
		OrderQty:       qty,
		RestingPrice:   restingPrice,
		PositionSide:   enums.PosNet,
		ClientIDPrefix: "btdr",
	})
	node := btruntime.NewNode(
		btruntime.Clients{Market: adapter.Market, Execution: adapter.Execution, Account: adapter.Account},
		clock.NewRealClock(),
		"btdr",
		btruntime.WithStrategy(tester),
	)

	if _, err := node.Resync(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance Demo runtime initial reconcile")
		t.Fatalf("initial runtime reconcile: %v", err)
	}
	if err := adapter.Start(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance Demo runtime user-data stream")
		t.Fatalf("start Binance Demo adapter stream: %v", err)
	}

	runCtx, stopNode := context.WithCancel(ctx)
	nodeDone := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(nodeDone)
	}()
	defer func() {
		stopNode()
		select {
		case <-nodeDone:
		case <-time.After(5 * time.Second):
			t.Fatalf("runtime node did not stop")
		}
	}()

	filled, err := tester.WaitForFill(ctx)
	if err != nil {
		t.Fatalf("wait for runtime Demo fill: %v", err)
	}
	if filled.Quantity.IsZero() {
		t.Fatalf("runtime tester observed zero fill: %+v", filled)
	}
	if _, err := waitForDemoOrderStatus(ctx, adapter.rest, spec.VenueSymbol, tester.RestingClientID(), "CANCELED"); err != nil {
		t.Fatalf("runtime resting order did not cancel: %v", err)
	}
	if err := waitForDemoRuntimePosition(ctx, node, instID, qty); err != nil {
		t.Fatalf("runtime cache did not observe Demo position: %v", err)
	}
	if err := waitForDemoRuntimePortfolioNetQty(ctx, node, instID, qty); err != nil {
		t.Fatalf("runtime portfolio did not observe Demo fill: %v", err)
	}
	if got := node.Metrics(); got.OrdersSeen == 0 || got.FillsSeen == 0 {
		t.Fatalf("runtime metrics did not observe order/fill events: %+v", got)
	}

	exposure, err := waitForDemoExposure(ctx, adapter, instID, qty)
	if err != nil {
		t.Fatalf("wait for Demo runtime account exposure: %v", err)
	}
	meta := demoAcceptanceCleanupMetadata{Symbol: spec.VenueSymbol, Side: "BUY", Quantity: qty, Exposure: exposure}
	if err := closeBinanceDemoExposure(ctx, adapter, instID, exposure); err != nil {
		t.Fatalf("close Demo runtime exposure: %v\n%s", err, meta.Remediation())
	}
	if err := waitForDemoFlat(ctx, adapter, instID); err != nil {
		t.Fatalf("wait for Demo runtime flat: %v\n%s", err, meta.Remediation())
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, instID); err != nil {
		t.Fatalf("wait for no Demo runtime open orders: %v\n%s", err, meta.Remediation())
	}
	if err := waitForDemoRuntimePortfolioFlat(ctx, node, instID); err != nil {
		t.Fatalf("runtime portfolio did not observe Demo close fill: %v\n%s", err, meta.Remediation())
	}
	if _, err := node.Resync(ctx); err != nil {
		t.Fatalf("final runtime reconcile: %v", err)
	}
	if _, ok := node.Cache.Position(instID, enums.PosNet); ok {
		t.Fatalf("runtime cache still has Demo position after final reconcile")
	}
}
