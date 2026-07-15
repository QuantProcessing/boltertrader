package spot

import (
	"context"
	"fmt"
	"os"
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

func TestBinanceSpotDemoRuntimeAcceptance(t *testing.T) {
	testenv.RequireBinanceDemoWrite(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	adapter, spec, instID, qty, restingPrice, fillPrice, maxNotional := newBinanceSpotDemoRuntimeFixture(t, ctx)
	defer adapter.Close()

	startBalances, err := demoSpotBalances(ctx, adapter)
	if err != nil {
		t.Fatalf("runtime balance preflight: %v", err)
	}
	startBaseAvailable := startBalances[spec.BaseCurrency].Free
	startBaseTotal := startBalances[spec.BaseCurrency].Total
	quoteAvailable := startBalances[spec.QuoteCurrency].Free
	requiredQuote := qty.Mul(fillPrice).Mul(decimal.RequireFromString("1.05"))
	if quoteAvailable.LessThan(requiredQuote) {
		t.Fatalf("Binance Spot Demo runtime acceptance has insufficient funds: %s available %s below required %s for %s quantity %s at fill price %s", spec.QuoteCurrency, quoteAvailable, requiredQuote, spec.VenueSymbol, qty, fillPrice)
	}

	cleanup := newDemoAcceptanceCleanupState(spec, qty)
	defer func() {
		if !cleanup.Needed() {
			return
		}
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancelCleanup()
		if err := cleanupBinanceSpotDemoAcceptance(cleanupCtx, adapter, instID, spec, startBaseAvailable, startBaseTotal, maxNotional, &cleanup); err != nil {
			t.Fatalf("%v\n%s", err, cleanup.Metadata().Remediation())
		}
	}()

	node := btruntime.NewNode(
		btruntime.Clients{Market: adapter.Market, Execution: adapter.Execution, Account: adapter.Account},
		clock.NewRealClock(),
		"binance-spot-demo",
	)
	runtimeaccept.AttachAccountRequiredRiskWithMaxNotional(node, adapter.Market.InstrumentProvider(), maxNotional)
	initialReconcile, err := node.Resync(ctx)
	if err != nil {
		t.Fatalf("initial runtime reconcile: %v", err)
	}
	if initialReconcile.AccountStatesApplied != 1 {
		t.Fatalf("initial runtime reconcile account states=%d, want 1: %+v", initialReconcile.AccountStatesApplied, initialReconcile)
	}
	runtimeaccept.AssertAccountStateReady(t, node, AccountIDDefault, model.AccountCash, enums.KindSpot)
	if got := len(node.Cache.Positions()); got != 0 {
		t.Fatalf("spot runtime cache positions=%d, want 0 before writes", got)
	}
	if err := adapter.Start(ctx); err != nil {
		t.Fatalf("start Binance Spot Demo adapter stream: %v", err)
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
		t.Fatalf("runtime node did not become active before Binance Spot Demo writes: %v", err)
	}
	runtimeaccept.AssertOversizedOrderRejected(t, node, adapter.Market.InstrumentProvider(), instID, maxNotional)

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
		t.Fatalf("runtime submit Binance Spot Demo resting order (outcome ambiguous; only a known venue order can be canceled): %v\n%s", err, cleanup.Metadata().Remediation())
	}
	cleanup.RecordVenueOrderID(resting.VenueOrderID)
	if err := node.Exec.Cancel(ctx, restingClientID); err != nil {
		t.Fatalf("runtime cancel Binance Spot Demo resting order: %v", err)
	}
	restingFinal, err := waitForDemoSpotOrderStatus(ctx, adapter.rest, spec.VenueSymbol, restingClientID, "CANCELED")
	if err != nil {
		t.Fatalf("runtime resting order did not cancel: %v", err)
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
	portfolioBeforeFill := node.Portfolio.NetQty(instID, enums.PosNet)

	fillClientID := demoClientOrderID("runtime-fill")
	cleanup.Arm(enums.SideBuy, fillClientID)
	filled, err := node.Exec.Submit(ctx, model.OrderRequest{
		InstrumentID: instID,
		ClientID:     fillClientID,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifIOC,
		Quantity:     qty,
		Price:        fillPrice,
		PositionSide: enums.PosNet,
	})
	if err != nil {
		t.Fatalf("runtime submit Binance Spot Demo fill order (outcome ambiguous; no automatic close attempted): %v\n%s", err, cleanup.Metadata().Remediation())
	}
	cleanup.RecordVenueOrderID(filled.VenueOrderID)
	filledResp, err := waitForDemoSpotOrderStatus(ctx, adapter.rest, spec.VenueSymbol, fillClientID, binanceSpotDemoTerminalStatuses...)
	if err != nil {
		t.Fatalf("wait for authoritative runtime fill order terminal status: %v\n%s", err, cleanup.Metadata().Remediation())
	}
	filledQty, err := validateBinanceSpotDemoFill(filledResp, maxNotional)
	cleanup.MarkOrderTerminal(filled.VenueOrderID)
	cleanup.ConfirmFill(filledQty)
	if err != nil {
		t.Fatalf("validate bounded runtime Spot Demo fill: %v\n%s", err, cleanup.Metadata().Remediation())
	}
	if err := runtimeaccept.WaitForOrderFilled(ctx, node, fillClientID); err != nil {
		t.Fatalf("runtime cache did not observe Binance Spot Demo fill: %v", err)
	}
	if got := node.Metrics(); got.OrdersSeen == 0 || got.FillsSeen == 0 {
		t.Fatalf("runtime metrics did not observe spot order/fill events: %+v", got)
	}
	observationThreshold := demoSpotFillObservationThreshold(filledQty, spec.SizeStep)
	baseDelta, err := waitForDemoSpotBalanceDelta(ctx, adapter, spec.BaseCurrency, startBaseTotal, observationThreshold)
	if err != nil {
		t.Fatalf("wait for opened Binance Spot Demo runtime base balance: %v", err)
	}
	cleanup.SetBaseDelta(baseDelta)
	portfolioTolerance := spec.SizeStep.Mul(decimal.RequireFromString("0.000001"))
	if err := waitForDemoSpotPortfolioDelta(ctx, node, instID, portfolioBeforeFill, baseDelta, portfolioTolerance); err != nil {
		t.Fatalf("runtime portfolio did not observe Binance Spot Demo exposure: %v", err)
	}

	postBuyReconcile, err := node.Resync(ctx)
	if err != nil {
		t.Fatalf("post-buy Binance Spot Demo runtime reconcile: %v", err)
	}
	if postBuyReconcile.AccountStatesApplied != 1 {
		t.Fatalf("post-buy Binance Spot Demo runtime reconcile account states=%d, want 1: %+v", postBuyReconcile.AccountStatesApplied, postBuyReconcile)
	}
	runtimeaccept.AssertAccountStateReady(t, node, AccountIDDefault, model.AccountCash, enums.KindSpot)

	closeQty := floorDecimalToStep(baseDelta, spec.SizeStep)
	if closeQty.GreaterThan(cleanup.CloseLimit()) {
		t.Fatalf("refusing runtime automatic close: quantity %s exceeds authoritative lifecycle fill %s\n%s", closeQty, cleanup.CloseLimit(), cleanup.Metadata().Remediation())
	}
	if closeQty.GreaterThanOrEqual(spec.MinQty) {
		closeClientID := demoClientOrderID("runtime-close")
		closeTicker, err := adapter.rest.BookTicker(ctx, spec.VenueSymbol)
		if err != nil {
			t.Fatalf("load Binance Spot Demo runtime close ticker: %v", err)
		}
		closePrice := floorDecimalToStep(dec(closeTicker.BidPrice).Mul(decimal.RequireFromString("0.99")), spec.PriceTick)
		cleanup.Arm(enums.SideSell, closeClientID)
		cleanup.BeginCloseAttempt()
		closed, err := node.Exec.Submit(ctx, model.OrderRequest{
			InstrumentID: instID,
			ClientID:     closeClientID,
			Side:         enums.SideSell,
			Type:         enums.TypeLimit,
			TIF:          enums.TifIOC,
			Quantity:     closeQty,
			Price:        closePrice,
			PositionSide: enums.PosNet,
		})
		if err != nil {
			t.Fatalf("runtime close Binance Spot Demo base delta (outcome ambiguous; not retried): %v\n%s", err, cleanup.Metadata().Remediation())
		}
		cleanup.RecordVenueOrderID(closed.VenueOrderID)
		if _, err := waitForDemoSpotOrderStatus(ctx, adapter.rest, spec.VenueSymbol, closeClientID, binanceSpotDemoTerminalStatuses...); err != nil {
			t.Fatalf("wait for runtime close terminal status: %v", err)
		}
		cleanup.MarkOrderTerminal(closed.VenueOrderID)
	}
	portfolioDustTolerance := spec.SizeStep.Mul(decimal.RequireFromString("1.000001"))
	if err := waitForDemoSpotPortfolioDelta(ctx, node, instID, portfolioBeforeFill, decimal.Zero, portfolioDustTolerance); err != nil {
		t.Fatalf("runtime portfolio did not return to its pre-fill baseline after Binance Spot close: %v", err)
	}
	finalReconcile, err := node.Resync(ctx)
	if err != nil {
		t.Fatalf("final Binance Spot Demo runtime reconcile: %v", err)
	}
	if finalReconcile.AccountStatesApplied != 1 {
		t.Fatalf("final Binance Spot Demo runtime reconcile account states=%d, want 1: %+v", finalReconcile.AccountStatesApplied, finalReconcile)
	}
	runtimeaccept.AssertAccountStateReady(t, node, AccountIDDefault, model.AccountCash, enums.KindSpot)
	if got := len(node.Cache.Positions()); got != 0 {
		t.Fatalf("spot runtime cache positions=%d, want 0 after final reconcile", got)
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, instID); err != nil {
		t.Fatalf("wait for no Binance Spot Demo runtime open orders: %v", err)
	}
	if err := waitForDemoSpotBaseDeltaBelowStep(ctx, adapter, spec, startBaseAvailable); err != nil {
		t.Fatalf("wait for Binance Spot Demo runtime base delta cleanup: %v\n%s", err, cleanup.Metadata().Remediation())
	}
	cleanup.MarkClean()
}

func waitForDemoSpotPortfolioDelta(
	ctx context.Context,
	node *btruntime.TradingNode,
	id model.InstrumentID,
	baseline decimal.Decimal,
	wantDelta decimal.Decimal,
	tolerance decimal.Decimal,
) error {
	var last decimal.Decimal
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		last = node.Portfolio.NetQty(id, enums.PosNet)
		if demoSpotPortfolioDeltaWithin(last, baseline, wantDelta, tolerance) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf(
				"timed out waiting for runtime portfolio delta %s within %s; baseline=%s current=%s observed delta=%s: %w",
				wantDelta, tolerance.Abs(), baseline, last, last.Sub(baseline), ctx.Err(),
			)
		case <-ticker.C:
		}
	}
}

func demoSpotPortfolioDeltaWithin(current, baseline, wantDelta, tolerance decimal.Decimal) bool {
	return current.Sub(baseline).Sub(wantDelta).Abs().LessThanOrEqual(tolerance.Abs())
}

func newBinanceSpotDemoRuntimeFixture(t *testing.T, ctx context.Context) (*Adapter, demoAcceptanceSymbolSpec, model.InstrumentID, decimal.Decimal, decimal.Decimal, decimal.Decimal, decimal.Decimal) {
	t.Helper()
	httpClient, err := demoHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Demo HTTP client: %v", err)
	}
	adapter, err := New(ctx, Config{
		Demo:          true,
		DemoAPIKey:    os.Getenv("BINANCE_DEMO_API_KEY"),
		DemoAPISecret: os.Getenv("BINANCE_DEMO_API_SECRET"),
		HTTPClient:    httpClient,
	})
	if err != nil {
		t.Fatalf("new Binance Spot Demo runtime adapter: %v", err)
	}

	symbolInput := demoEnvOrDefault("BINANCE_DEMO_SYMBOL", "ETH-USDT")
	maxNotional := demoDecimalEnvOrDefault(t, "BINANCE_DEMO_MAX_NOTIONAL_USDT", demoDefaultMaxNotionalUSDT)
	configuredQty := demoDecimalEnvOrDefault(t, "BINANCE_DEMO_ORDER_QTY", "0")
	info, err := adapter.rest.ExchangeInfo(ctx)
	if err != nil {
		_ = adapter.Close()
		t.Fatalf("exchange info: %v", err)
	}
	spec, err := demoAcceptanceSymbolSpecFromExchangeInfo(info, symbolInput)
	if err != nil {
		_ = adapter.Close()
		t.Fatalf("resolve Spot Demo runtime symbol: %v", err)
	}
	instID := adapter.provider.resolveVenueSymbol(spec.VenueSymbol)
	ticker, err := adapter.rest.BookTicker(ctx, spec.VenueSymbol)
	if err != nil {
		_ = adapter.Close()
		t.Fatalf("bookTicker: %v", err)
	}
	bid := dec(ticker.BidPrice)
	ask := dec(ticker.AskPrice)
	if bid.LessThanOrEqual(decimal.Zero) || ask.LessThanOrEqual(decimal.Zero) {
		_ = adapter.Close()
		t.Fatalf("non-positive Spot Demo runtime bookTicker for %s: %+v", spec.VenueSymbol, ticker)
	}
	restingPrice := floorDecimalToStep(bid.Mul(decimal.RequireFromString("0.80")), spec.PriceTick)
	fillPrice := ceilDecimalToStep(ask.Mul(decimal.RequireFromString("1.01")), spec.PriceTick)
	qty, err := selectDemoAcceptanceOrderQuantityForPriceBand(spec, configuredQty, maxNotional, restingPrice, fillPrice)
	if err != nil {
		_ = adapter.Close()
		t.Fatalf("select safe Spot Demo runtime order quantity: %v", err)
	}
	if open, err := adapter.Execution.OpenOrders(ctx, instID); err != nil {
		_ = adapter.Close()
		t.Fatalf("open order preflight: %v", err)
	} else if len(open) > 0 {
		_ = adapter.Close()
		t.Fatalf("Binance Spot Demo runtime acceptance requires a clean account: %s already has %d open order(s); clean the Demo account before running", spec.VenueSymbol, len(open))
	}
	return adapter, spec, instID, qty, restingPrice, fillPrice, maxNotional
}
