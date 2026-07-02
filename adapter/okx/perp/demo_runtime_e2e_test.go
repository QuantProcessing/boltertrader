package perp

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

func TestOKXPerpDemoRuntimeE2E(t *testing.T) {
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
	if _, err := node.Resync(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Perp Demo runtime initial reconcile")
		t.Fatalf("initial runtime reconcile: %v", err)
	}
	if err := adapter.Start(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Perp Demo runtime private stream")
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

	restingClientID := demoClientOrderID("runtime-rest")
	cleanup.Arm(restingClientID)
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
		t.Fatalf("runtime submit OKX Perp Demo resting order: %v", err)
	}
	cleanup.RecordVenueOrderID(resting.VenueOrderID)
	if err := node.Exec.Cancel(ctx, restingClientID); err != nil {
		t.Fatalf("runtime cancel OKX Perp Demo resting order: %v", err)
	}
	if _, err := waitForDemoOrderStatus(ctx, adapter.rest, spec.VenueSymbol, restingClientID, "canceled"); err != nil {
		t.Fatalf("runtime resting order did not cancel: %v", err)
	}

	fillClientID := demoClientOrderID("runtime-fill")
	cleanup.Arm(fillClientID)
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
		t.Fatalf("runtime submit OKX Perp Demo fill order: %v", err)
	}
	cleanup.RecordVenueOrderID(filled.VenueOrderID)
	filledResp, err := waitForDemoOrderStatus(ctx, adapter.rest, spec.VenueSymbol, fillClientID, "filled")
	if err != nil {
		t.Fatalf("wait for runtime fill order: %v", err)
	}
	filledQty := dec(filledResp.AccFillSz)
	if filledQty.IsZero() {
		t.Fatalf("runtime fill order reported zero executed quantity: %+v", filledResp)
	}
	if err := waitForRuntimeOrderFilled(ctx, node, fillClientID); err != nil {
		t.Fatalf("runtime cache did not observe OKX Perp Demo fill: %v", err)
	}
	if err := waitForRuntimePosition(ctx, node, instID, filledQty); err != nil {
		t.Fatalf("runtime cache did not observe OKX Perp Demo position: %v", err)
	}
	if err := waitForRuntimePortfolioNetQty(ctx, node, instID, filledQty); err != nil {
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

	closeClientID := demoClientOrderID("runtime-close")
	cleanup.Arm(closeClientID)
	closeBook, err := adapter.Market.OrderBook(ctx, instID, 5)
	if err != nil {
		t.Fatalf("load OKX Perp Demo runtime close book: %v", err)
	}
	if len(closeBook.Bids) == 0 {
		t.Fatalf("empty OKX Perp Demo runtime bid book before close for %s", spec.VenueSymbol)
	}
	closePrice := floorDecimalToStep(closeBook.Bids[0].Price.Mul(decimal.RequireFromString("0.99")), spec.PriceTick)
	closed, err := node.Exec.Submit(ctx, model.OrderRequest{
		InstrumentID: instID,
		ClientID:     closeClientID,
		Side:         enums.SideSell,
		Type:         enums.TypeLimit,
		TIF:          enums.TifIOC,
		Quantity:     exposure.Abs(),
		Price:        closePrice,
		PositionSide: enums.PosNet,
		ReduceOnly:   true,
	})
	if err != nil {
		t.Fatalf("runtime close OKX Perp Demo exposure: %v", err)
	}
	cleanup.RecordVenueOrderID(closed.VenueOrderID)
	if _, err := waitForDemoOrderStatus(ctx, adapter.rest, spec.VenueSymbol, closeClientID, "filled"); err != nil {
		t.Fatalf("wait for runtime close fill: %v", err)
	}
	if err := waitForDemoFlat(ctx, adapter, instID); err != nil {
		t.Fatalf("wait for OKX Perp Demo runtime flat: %v\n%s", err, cleanup.Remediation())
	}
	if err := waitForRuntimePortfolioFlat(ctx, node, instID); err != nil {
		t.Fatalf("runtime portfolio did not return flat after Perp close: %v\n%s", err, cleanup.Remediation())
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, instID); err != nil {
		t.Fatalf("wait for no OKX Perp Demo runtime open orders: %v\n%s", err, cleanup.Remediation())
	}
	if _, err := node.Resync(ctx); err != nil {
		t.Fatalf("final OKX Perp Demo runtime reconcile: %v", err)
	}
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
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Perp Demo runtime adapter initialization")
		t.Fatalf("new OKX Perp Demo runtime adapter: %v", err)
	}
	instID := model.InstrumentID{Venue: venueName, Symbol: instIDToNeutral(cfg.PerpSymbol), Kind: enums.KindPerp}
	if _, ok := adapter.provider.Instrument(instID); !ok {
		_ = adapter.Close()
		t.Fatalf("OKX Perp Demo runtime symbol %s not loaded", cfg.PerpSymbol)
	}
	insts, err := adapter.rest.GetInstruments(ctx, instTypeSwap)
	if err != nil {
		_ = adapter.Close()
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Perp Demo runtime instruments")
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
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Perp Demo runtime order book")
		t.Fatalf("order book: %v", err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		_ = adapter.Close()
		t.Fatalf("empty OKX Perp Demo runtime book for %s", spec.VenueSymbol)
	}
	qty, err := selectDemoPerpQuantity(spec, cfg.MaxNotionalUSDT, book.Asks[0].Price)
	if err != nil {
		_ = adapter.Close()
		t.Fatalf("select safe OKX Perp Demo runtime order quantity: %v", err)
	}
	restingPrice := floorDecimalToStep(book.Bids[0].Price.Mul(decimal.RequireFromString("0.80")), spec.PriceTick)
	fillPrice := ceilDecimalToStep(book.Asks[0].Price.Mul(decimal.RequireFromString("1.01")), spec.PriceTick)
	if open, err := adapter.Execution.OpenOrders(ctx, instID); err != nil {
		_ = adapter.Close()
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Perp Demo runtime open order preflight")
		t.Fatalf("open order preflight: %v", err)
	} else if len(open) > 0 {
		_ = adapter.Close()
		t.Skipf("skipping OKX Perp Demo runtime E2E: %s already has %d open order(s); clean the Demo account before running", spec.VenueSymbol, len(open))
	}
	if exposure, err := demoCurrentExposure(ctx, adapter, instID); err != nil {
		_ = adapter.Close()
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Perp Demo runtime position preflight")
		t.Fatalf("position preflight: %v", err)
	} else if !exposure.IsZero() {
		_ = adapter.Close()
		t.Skipf("skipping OKX Perp Demo runtime E2E: %s already has exposure %s; start from a flat Demo account", spec.VenueSymbol, exposure)
	}
	return adapter, spec, instID, qty, restingPrice, fillPrice
}

func waitForRuntimeOrderFilled(ctx context.Context, node *btruntime.TradingNode, clientID string) error {
	var last enums.OrderStatus
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		if order, ok := node.Cache.Order(clientID); ok {
			last = order.Status
			if order.Status == enums.StatusFilled || !order.FilledQty.IsZero() {
				return nil
			}
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for runtime order %s filled; last=%v: %w", clientID, last, ctx.Err())
		case <-ticker.C:
		}
	}
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

func waitForRuntimePortfolioNetQty(ctx context.Context, node *btruntime.TradingNode, id model.InstrumentID, minAbs decimal.Decimal) error {
	var last decimal.Decimal
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		last = node.Portfolio.NetQty(id, enums.PosNet)
		if last.Abs().GreaterThanOrEqual(minAbs.Abs()) {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for runtime portfolio net qty >= %s; last=%s: %w", minAbs.Abs(), last, ctx.Err())
		case <-ticker.C:
		}
	}
}

func waitForRuntimePortfolioFlat(ctx context.Context, node *btruntime.TradingNode, id model.InstrumentID) error {
	var last decimal.Decimal
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		last = node.Portfolio.NetQty(id, enums.PosNet)
		if last.IsZero() {
			return nil
		}
		select {
		case <-ctx.Done():
			return fmt.Errorf("timed out waiting for runtime portfolio flat; last=%s: %w", last, ctx.Err())
		case <-ticker.C:
		}
	}
}
