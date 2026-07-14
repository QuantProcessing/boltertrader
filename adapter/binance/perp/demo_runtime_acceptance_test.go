package perp

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	"github.com/shopspring/decimal"
)

func TestBinanceDemoRuntimeAcceptance(t *testing.T) {
	testenv.RequireBinanceDemoWrite(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	adapter, spec, instID, qty, restingPrice, fillPrice, maxNotional := newBinanceDemoRuntimeAcceptanceFixture(t, ctx)
	defer adapter.Close()
	cleanup := newDemoAcceptanceCleanupState(spec.VenueSymbol, qty)
	defer func() {
		if !cleanup.Needed() {
			return
		}
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancelCleanup()
		if err := cleanupBinanceDemoAcceptance(cleanupCtx, adapter, instID, spec, maxNotional, &cleanup); err != nil {
			t.Fatalf("%v\n%s", err, cleanup.Metadata().Remediation())
		}
	}()

	node := btruntime.NewNode(
		btruntime.Clients{Market: adapter.Market, Execution: adapter.Execution, Account: adapter.Account},
		clock.NewRealClock(),
		"btdr",
	)
	runtimeaccept.AttachAccountRequiredRiskWithMaxNotional(node, adapter.Market.InstrumentProvider(), maxNotional)

	initialReconcile, err := node.Resync(ctx)
	if err != nil {
		t.Fatalf("initial runtime reconcile: %v", err)
	}
	if initialReconcile.AccountStatesApplied != 1 {
		t.Fatalf("initial runtime reconcile account states=%d, want 1: %+v", initialReconcile.AccountStatesApplied, initialReconcile)
	}
	runtimeaccept.AssertAccountStateReady(t, node, model.AccountIDBinanceDefault, model.AccountMargin, enums.KindPerp)
	runtimeaccept.AssertOversizedOrderRejected(t, node, adapter.Market.InstrumentProvider(), instID)
	if err := adapter.Start(ctx); err != nil {
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

	if err := runtimeaccept.WaitForActive(ctx, node); err != nil {
		t.Fatalf("runtime node did not become active before Binance Demo writes: %v", err)
	}

	restingClientID := demoClientOrderID("runtime-rest")
	cleanup.Arm(enums.SideBuy, restingClientID)
	resting, err := node.Exec.Submit(ctx, model.OrderRequest{
		InstrumentID: instID,
		ClientID:     restingClientID,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTX,
		Quantity:     qty,
		Price:        restingPrice,
		PositionSide: enums.PosNet,
	})
	if err != nil {
		t.Fatalf("runtime submit Binance Demo resting order (outcome ambiguous; only a known venue order can be canceled): %v\n%s", err, cleanup.Metadata().Remediation())
	}
	cleanup.RecordVenueOrderID(resting.VenueOrderID)
	if resting.FilledQty.IsPositive() {
		cleanup.ConfirmFill(resting.FilledQty)
		t.Fatalf("runtime resting order unexpectedly filled on submit: %+v\n%s", resting, cleanup.Metadata().Remediation())
	}
	if err := node.Exec.Cancel(ctx, restingClientID); err != nil {
		t.Fatalf("runtime cancel Binance Demo resting order (outcome ambiguous; cleanup remains scoped to the recorded order): %v\n%s", err, cleanup.Metadata().Remediation())
	}
	restingFinal, err := waitForDemoOrderStatus(ctx, adapter.rest, spec.VenueSymbol, restingClientID, "CANCELED")
	if err != nil {
		t.Fatalf("runtime resting order did not reach authoritative CANCELED: %v\n%s", err, cleanup.Metadata().Remediation())
	}
	cleanup.MarkOrderTerminal(resting.VenueOrderID)
	if partialQty := dec(restingFinal.ExecutedQty); partialQty.IsPositive() {
		cleanup.ConfirmFill(partialQty)
		t.Fatalf("runtime resting cancellation reported unexpected executed quantity %s\n%s", partialQty, cleanup.Metadata().Remediation())
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, instID); err != nil {
		t.Fatalf("runtime resting cancellation did not produce authoritative no-open state: %v\n%s", err, cleanup.Metadata().Remediation())
	}
	if err := runtimeaccept.WaitForOrderStatus(ctx, node, restingClientID, enums.StatusCanceled); err != nil {
		t.Fatalf("runtime cache did not observe resting order cancellation: %v", err)
	}

	fillClientID := demoClientOrderID("runtime-fill")
	cleanup.Arm(enums.SideBuy, fillClientID)
	filled, err := node.Exec.Submit(ctx, demoFillOrderRequest(instID, fillClientID, qty, fillPrice))
	if err != nil {
		t.Fatalf("runtime submit Binance Demo IOC fill order (outcome ambiguous; no automatic close attempted): %v\n%s", err, cleanup.Metadata().Remediation())
	}
	cleanup.RecordVenueOrderID(filled.VenueOrderID)
	filledResp, err := waitForDemoOrderStatus(ctx, adapter.rest, spec.VenueSymbol, fillClientID, binanceDemoTerminalStatuses...)
	if err != nil {
		t.Fatalf("wait for authoritative runtime IOC terminal status: %v\n%s", err, cleanup.Metadata().Remediation())
	}
	confirmedQty, err := validateBinanceDemoFill(filledResp, maxNotional)
	cleanup.MarkOrderTerminal(filled.VenueOrderID)
	cleanup.ConfirmFill(confirmedQty)
	if err != nil {
		t.Fatalf("validate bounded runtime Demo fill: %v\n%s", err, cleanup.Metadata().Remediation())
	}
	if err := runtimeaccept.WaitForOrderFilled(ctx, node, fillClientID); err != nil {
		t.Fatalf("runtime cache did not observe Binance Demo IOC fill: %v", err)
	}
	if err := waitForDemoRuntimePosition(ctx, node, instID, confirmedQty); err != nil {
		t.Fatalf("runtime cache did not observe Demo position: %v", err)
	}
	if err := waitForDemoRuntimePortfolioNetQty(ctx, node, instID, confirmedQty); err != nil {
		t.Fatalf("runtime portfolio did not observe Demo fill: %v", err)
	}
	if got := node.Metrics(); got.OrdersSeen == 0 || got.FillsSeen == 0 {
		t.Fatalf("runtime metrics did not observe order/fill events: %+v", got)
	}

	exposure, err := waitForDemoExposure(ctx, adapter, instID, confirmedQty)
	if err != nil {
		t.Fatalf("wait for Demo runtime account exposure: %v", err)
	}
	cleanup.SetExposure(exposure)
	if exposure.IsNegative() {
		t.Fatalf("refusing runtime automatic close of unexpected short exposure %s after a buy lifecycle\n%s", exposure, cleanup.Metadata().Remediation())
	}
	if exposure.GreaterThan(cleanup.CloseLimit()) {
		t.Fatalf("refusing runtime automatic close: exposure %s exceeds authoritative IOC fill %s\n%s", exposure, cleanup.CloseLimit(), cleanup.Metadata().Remediation())
	}
	closeBook, err := adapter.Market.OrderBook(ctx, instID, 5)
	if err != nil {
		t.Fatalf("load Binance Demo runtime close book: %v", err)
	}
	if len(closeBook.Bids) == 0 {
		t.Fatalf("empty Binance Demo runtime bid book before close for %s", spec.VenueSymbol)
	}
	closePrice := floorDecimalToStep(closeBook.Bids[0].Price.Mul(decimal.RequireFromString("0.99")), spec.PriceTick)
	closeClientID := demoClientOrderID("runtime-close")
	cleanup.Arm(enums.SideSell, closeClientID)
	cleanup.BeginCloseAttempt()
	closed, err := node.Exec.Submit(ctx, model.OrderRequest{
		InstrumentID: instID,
		ClientID:     closeClientID,
		Side:         enums.SideSell,
		Type:         enums.TypeLimit,
		TIF:          enums.TifIOC,
		Quantity:     exposure,
		Price:        closePrice,
		PositionSide: enums.PosNet,
		ReduceOnly:   true,
	})
	if err != nil {
		t.Fatalf("runtime submit single bounded reduce-only close (outcome ambiguous; not retried): %v\n%s", err, cleanup.Metadata().Remediation())
	}
	cleanup.RecordVenueOrderID(closed.VenueOrderID)
	closedResp, err := waitForDemoOrderStatus(ctx, adapter.rest, spec.VenueSymbol, closeClientID, binanceDemoTerminalStatuses...)
	if err != nil {
		t.Fatalf("wait for runtime close terminal status: %v\n%s", err, cleanup.Metadata().Remediation())
	}
	cleanup.MarkOrderTerminal(closed.VenueOrderID)
	if closedQty := dec(closedResp.ExecutedQty); !closedQty.IsPositive() {
		t.Fatalf("runtime close reached %s with zero executed quantity\n%s", closedResp.Status, cleanup.Metadata().Remediation())
	}
	if err := waitForDemoFlat(ctx, adapter, instID); err != nil {
		t.Fatalf("wait for Demo runtime flat: %v\n%s", err, cleanup.Metadata().Remediation())
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, instID); err != nil {
		t.Fatalf("wait for no Demo runtime open orders: %v\n%s", err, cleanup.Metadata().Remediation())
	}
	if err := waitForDemoRuntimePortfolioFlat(ctx, node, instID); err != nil {
		t.Fatalf("runtime portfolio did not observe Demo close fill: %v\n%s", err, cleanup.Metadata().Remediation())
	}
	finalReconcile, err := node.Resync(ctx)
	if err != nil {
		t.Fatalf("final runtime reconcile: %v", err)
	}
	if finalReconcile.AccountStatesApplied != 1 {
		t.Fatalf("final runtime reconcile account states=%d, want 1: %+v", finalReconcile.AccountStatesApplied, finalReconcile)
	}
	runtimeaccept.AssertAccountStateReady(t, node, model.AccountIDBinanceDefault, model.AccountMargin, enums.KindPerp)
	if _, ok := node.Cache.Position(instID, enums.PosNet); ok {
		t.Fatalf("runtime cache still has Demo position after final reconcile")
	}
	cleanup.MarkClean()
}
