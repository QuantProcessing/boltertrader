package perp

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

func TestOKXPerpDemoRuntimeAcceptance(t *testing.T) {
	cfg := testenv.RequireOKXDemoWrite(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	adapter, spec, instID, qty, restingPrice, fillPrice := newOKXPerpDemoRuntimeFixture(t, ctx, cfg)
	defer adapter.Close()
	cleanup := newDemoPerpCleanupState(spec, qty)
	defer func() {
		if !cleanup.needed {
			return
		}
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancelCleanup()
		if err := cleanupOKXPerpDemo(cleanupCtx, adapter, instID, spec, &cleanup); err != nil {
			t.Fatalf("%v\n%s", err, cleanup.Remediation())
		}
	}()

	node := btruntime.NewNode(
		btruntime.Clients{Market: adapter.Market, Execution: adapter.Execution, Account: adapter.Account},
		clock.NewRealClock(),
		"okx-perp-demo",
	)
	runtimeaccept.AttachAccountRequiredRiskWithMaxNotional(node, adapter.Market.InstrumentProvider(), cfg.MaxNotionalUSDT)
	initialReconcile, err := node.Resync(ctx)
	if err != nil {
		t.Fatalf("initial runtime reconcile: %v", err)
	}
	if initialReconcile.AccountStatesApplied != 1 {
		t.Fatalf("initial runtime reconcile account states=%d, want 1: %+v", initialReconcile.AccountStatesApplied, initialReconcile)
	}
	runtimeaccept.AssertAccountStateReady(t, node, AccountIDDefault, model.AccountMargin, enums.KindPerp)
	if err := adapter.Start(ctx); err != nil {
		t.Fatalf("start OKX Perp Demo adapter stream: %v", err)
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
		t.Fatalf("runtime node did not become active before OKX Perp Demo writes: %v", err)
	}
	runtimeaccept.AssertOversizedOrderRejected(t, node, adapter.Market.InstrumentProvider(), instID, cfg.MaxNotionalUSDT)

	restingClientID := demoClientOrderID("runtime-rest")
	cleanup.TrackOrder(demoOrderRoleResting, restingClientID)
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
		recoveryErr := recoverAmbiguousOKXPerpDemoOrder(ctx, adapter, spec, &cleanup, restingClientID)
		t.Fatalf("runtime submit OKX Perp Demo resting order returned an ambiguous error: %v; client-ID recovery: %v\n%s", err, recoveryErr, cleanup.Remediation())
	}
	cleanup.RecordVenueOrderID(restingClientID, resting.VenueOrderID)
	cancelErr := node.Exec.Cancel(ctx, restingClientID)
	if _, err := confirmOKXPerpDemoOrderTerminal(ctx, adapter, spec, &cleanup, restingClientID); err != nil {
		t.Fatalf("runtime cancel/terminal confirmation failed: cancelErr=%v terminalErr=%v\n%s", cancelErr, err, cleanup.Remediation())
	}
	if cleanup.RestingFillQuantity().IsPositive() {
		t.Fatalf("OKX Perp Demo runtime resting order partially filled %s; IOC opening is blocked and bounded cleanup will run\n%s", cleanup.RestingFillQuantity(), cleanup.Remediation())
	}
	if !cleanup.OpeningAllowed() {
		t.Fatalf("OKX Perp Demo runtime resting order is not authoritatively canceled without fills\n%s", cleanup.Remediation())
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, instID); err != nil {
		t.Fatalf("prove stable no-open runtime state after resting cancel: %v\n%s", err, cleanup.Remediation())
	}
	if err := waitForDemoFlat(ctx, adapter, instID); err != nil {
		t.Fatalf("prove stable flat runtime state after resting cancel: %v\n%s", err, cleanup.Remediation())
	}

	fillClientID := demoClientOrderID("runtime-fill")
	cleanup.TrackOrder(demoOrderRoleOpening, fillClientID)
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
		recoveryErr := recoverAmbiguousOKXPerpDemoOrder(ctx, adapter, spec, &cleanup, fillClientID)
		t.Fatalf("runtime submit OKX Perp Demo fill order returned an ambiguous error: %v; client-ID recovery: %v\n%s", err, recoveryErr, cleanup.Remediation())
	}
	cleanup.RecordVenueOrderID(fillClientID, filled.VenueOrderID)
	filledResp, err := confirmOKXPerpDemoOrderTerminal(ctx, adapter, spec, &cleanup, fillClientID)
	if err != nil {
		t.Fatalf("wait for runtime fill order terminal state: %v", err)
	}
	filledQty, err := validateOKXPerpDemoFill(filledResp, spec, cfg.MaxNotionalUSDT)
	if err != nil {
		t.Fatalf("validate bounded runtime OKX Perp Demo fill: %v\n%s", err, cleanup.Remediation())
	}
	if err := runtimeaccept.WaitForOrderFilled(ctx, node, fillClientID); err != nil {
		t.Fatalf("runtime cache did not observe OKX Perp Demo fill: %v", err)
	}
	if err := waitForRuntimePosition(ctx, node, instID, filledQty); err != nil {
		t.Fatalf("runtime cache did not observe OKX Perp Demo position: %v", err)
	}
	if err := runtimeaccept.WaitForPortfolioNetQty(ctx, node, instID, filledQty); err != nil {
		t.Fatalf("runtime portfolio did not observe OKX Perp Demo fill: %v", err)
	}
	if got := node.Metrics(); got.OrdersSeen == 0 || got.FillsSeen == 0 {
		t.Fatalf("runtime metrics did not observe perp order/fill events: %+v", got)
	}

	exposure, err := waitForDemoExposure(ctx, adapter, instID, filledQty)
	if err != nil {
		t.Fatalf("wait for OKX Perp Demo runtime account exposure: %v", err)
	}
	cleanup.SetExposure(exposure)

	if !cleanup.CloseAuthorized() {
		t.Fatalf("OKX Perp Demo runtime close was not authorized by the confirmed fill\n%s", cleanup.Remediation())
	}
	maxCloseQty := cleanup.CloseLimit()
	closeQty, err := demoPerpCloseQuantity(exposure, maxCloseQty)
	if err != nil {
		t.Fatalf("select bounded OKX Perp Demo runtime close quantity: %v\n%s", err, cleanup.Remediation())
	}
	if closeQty.IsZero() {
		t.Fatalf("OKX Perp Demo runtime close quantity is zero\n%s", cleanup.Remediation())
	}
	closeBook, err := adapter.Market.OrderBook(ctx, instID, 5)
	if err != nil {
		t.Fatalf("load OKX Perp Demo runtime close book: %v", err)
	}
	if len(closeBook.Bids) == 0 {
		t.Fatalf("empty OKX Perp Demo runtime bid book before close for %s", spec.VenueSymbol)
	}
	closePrice := floorDecimalToStep(closeBook.Bids[0].Price.Mul(decimal.RequireFromString("0.99")), spec.PriceTick)
	closeClientID := demoClientOrderID("runtime-close")
	cleanup.TrackOrder(demoOrderRoleClose, closeClientID)
	cleanup.MarkCloseAttempted()
	closed, err := node.Exec.Submit(ctx, model.OrderRequest{
		InstrumentID: instID,
		ClientID:     closeClientID,
		Side:         enums.SideSell,
		Type:         enums.TypeLimit,
		TIF:          enums.TifIOC,
		Quantity:     closeQty,
		Price:        closePrice,
		PositionSide: enums.PosNet,
		ReduceOnly:   true,
	})
	if err != nil {
		recoveryErr := recoverAmbiguousOKXPerpDemoOrder(ctx, adapter, spec, &cleanup, closeClientID)
		t.Fatalf("runtime close OKX Perp Demo exposure returned an ambiguous error and will not be retried: %v; client-ID recovery: %v\n%s", err, recoveryErr, cleanup.Remediation())
	}
	cleanup.RecordVenueOrderID(closeClientID, closed.VenueOrderID)
	if _, err := confirmOKXPerpDemoOrderTerminal(ctx, adapter, spec, &cleanup, closeClientID); err != nil {
		t.Fatalf("wait for runtime close terminal state: %v", err)
	}
	if err := waitForDemoFlat(ctx, adapter, instID); err != nil {
		t.Fatalf("wait for OKX Perp Demo runtime flat: %v\n%s", err, cleanup.Remediation())
	}
	if err := runtimeaccept.WaitForPortfolioFlat(ctx, node, instID, decimal.Zero); err != nil {
		t.Fatalf("runtime portfolio did not return flat after Perp close: %v\n%s", err, cleanup.Remediation())
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, instID); err != nil {
		t.Fatalf("wait for no OKX Perp Demo runtime open orders: %v\n%s", err, cleanup.Remediation())
	}
	finalReconcile, err := node.Resync(ctx)
	if err != nil {
		t.Fatalf("final OKX Perp Demo runtime reconcile: %v", err)
	}
	if finalReconcile.AccountStatesApplied != 1 {
		t.Fatalf("final OKX Perp Demo runtime reconcile account states=%d, want 1: %+v", finalReconcile.AccountStatesApplied, finalReconcile)
	}
	runtimeaccept.AssertAccountStateReady(t, node, AccountIDDefault, model.AccountMargin, enums.KindPerp)
	if _, ok := node.Cache.Position(instID, enums.PosNet); ok {
		t.Fatalf("runtime cache still has OKX Perp Demo position after final reconcile")
	}
	cleanup.MarkClean()
}

func newOKXPerpDemoRuntimeFixture(t *testing.T, ctx context.Context, cfg testenv.OKXDemoConfig) (*Adapter, demoPerpSpec, model.InstrumentID, decimal.Decimal, decimal.Decimal, decimal.Decimal) {
	t.Helper()
	httpClient, err := testenv.OKXDemoHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("OKX Demo HTTP client: %v", err)
	}
	endpoints := okxDemoEndpoints(t, cfg)
	adapter, err := New(ctx, Config{
		APIKey:          cfg.APIKey,
		APISecret:       cfg.APISecret,
		Passphrase:      cfg.Passphrase,
		TdMode:          "cross",
		Environment:     okx.Simulated,
		DemoHostProfile: okx.DemoHostProfile(cfg.HostProfile),
		RESTBaseURL:     endpoints.REST,
		WSPublicURL:     endpoints.WSPublic,
		WSPrivateURL:    endpoints.WSPrivate,
		HTTPClient:      httpClient,
	})
	if err != nil {
		t.Fatalf("new OKX Perp Demo runtime adapter: %v", err)
	}
	if err := validateDemoPerpAccountMode(ctx, adapter.rest); err != nil {
		_ = adapter.Close()
		t.Fatalf("OKX Perp Demo runtime account mode preflight: %v", err)
	}
	instID := model.InstrumentID{Venue: venueName, Symbol: instIDToNeutral(cfg.PerpSymbol), Kind: enums.KindPerp}
	if _, ok := adapter.provider.Instrument(instID); !ok {
		_ = adapter.Close()
		t.Fatalf("OKX Perp Demo runtime symbol %s not loaded", cfg.PerpSymbol)
	}
	insts, err := adapter.rest.GetInstruments(ctx, instTypeSwap)
	if err != nil {
		_ = adapter.Close()
		t.Fatalf("instrument metadata: %v", err)
	}
	var native *okx.Instrument
	for i := range insts {
		if insts[i].InstId == cfg.PerpSymbol {
			native = &insts[i]
			break
		}
	}
	spec, err := demoPerpSpecFromOKX(native)
	if err != nil {
		_ = adapter.Close()
		t.Fatalf("resolve OKX Perp Demo runtime symbol: %v", err)
	}
	book, err := adapter.Market.OrderBook(ctx, instID, 5)
	if err != nil {
		_ = adapter.Close()
		t.Fatalf("order book: %v", err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		_ = adapter.Close()
		t.Fatalf("empty OKX Perp Demo runtime book for %s", spec.VenueSymbol)
	}
	restingPrice := floorDecimalToStep(book.Bids[0].Price.Mul(decimal.RequireFromString("0.80")), spec.PriceTick)
	fillPrice := ceilDecimalToStep(book.Asks[0].Price.Mul(decimal.RequireFromString("1.01")), spec.PriceTick)
	qty, err := selectDemoPerpQuantity(spec, cfg.MaxNotionalUSDT, fillPrice)
	if err != nil {
		_ = adapter.Close()
		t.Fatalf("select safe OKX Perp Demo runtime order quantity: %v", err)
	}
	if open, err := adapter.Execution.OpenOrders(ctx, instID); err != nil {
		_ = adapter.Close()
		t.Fatalf("open order preflight: %v", err)
	} else if len(open) > 0 {
		_ = adapter.Close()
		t.Fatalf("OKX Perp Demo runtime acceptance requires a clean account: %s already has %d open order(s); clean the Demo account before running", spec.VenueSymbol, len(open))
	}
	if exposure, err := demoCurrentExposure(ctx, adapter, instID); err != nil {
		_ = adapter.Close()
		t.Fatalf("position preflight: %v", err)
	} else if !exposure.IsZero() {
		_ = adapter.Close()
		t.Fatalf("OKX Perp Demo runtime acceptance requires a flat account: %s already has exposure %s; flatten the Demo account before running", spec.VenueSymbol, exposure)
	}
	return adapter, spec, instID, qty, restingPrice, fillPrice
}

func waitForRuntimePosition(ctx context.Context, node *btruntime.TradingNode, id model.InstrumentID, minAbs decimal.Decimal) error {
	var last decimal.Decimal
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if position, ok := node.Cache.Position(id, enums.PosNet); ok {
			last = position.Quantity
			if position.Quantity.Abs().GreaterThanOrEqual(minAbs.Abs()) {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for runtime position >= %s; last=%s: %w", minAbs.Abs(), last, ctx.Err())
		case <-ticker.C:
		}
	}
}
