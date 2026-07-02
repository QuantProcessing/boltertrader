package spot

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

func TestOKXSpotDemoRuntimeE2E(t *testing.T) {
	cfg := testenv.RequireOKXDemoWrite(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	adapter, spec, instID, qty, restingPrice, fillPrice := newOKXSpotDemoRuntimeFixture(t, ctx, cfg)
	defer adapter.Close()
	cleanup := newDemoSpotCleanupState(spec, qty)
	startBalances, err := demoSpotBalances(ctx, adapter)
	if err != nil {
		t.Fatalf("runtime balance preflight: %v", err)
	}
	startBaseAvailable := startBalances[spec.BaseCurrency].Available
	defer func() {
		if !cleanup.needed {
			return
		}
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancelCleanup()
		if err := cleanupOKXSpotDemo(cleanupCtx, adapter, instID, spec, startBaseAvailable, &cleanup); err != nil {
			t.Fatalf("%v\n%s", err, cleanup.Remediation())
		}
	}()

	node := btruntime.NewNode(
		btruntime.Clients{Market: adapter.Market, Execution: adapter.Execution, Account: adapter.Account},
		clock.NewRealClock(),
		"okx-spot-demo",
	)
	if _, err := node.Resync(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Spot Demo runtime initial reconcile")
		t.Fatalf("initial runtime reconcile: %v", err)
	}
	if got := len(node.Cache.Positions()); got != 0 {
		t.Fatalf("spot runtime cache positions=%d, want 0 before writes", got)
	}
	if err := adapter.Start(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Spot Demo runtime private stream")
		t.Fatalf("start OKX Spot Demo adapter stream: %v", err)
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
		t.Fatalf("runtime submit OKX Spot Demo resting order: %v", err)
	}
	cleanup.RecordVenueOrderID(resting.VenueOrderID)
	if err := node.Exec.Cancel(ctx, restingClientID); err != nil {
		t.Fatalf("runtime cancel OKX Spot Demo resting order: %v", err)
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
		t.Fatalf("runtime submit OKX Spot Demo fill order: %v", err)
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
		t.Fatalf("runtime cache did not observe OKX Spot Demo fill: %v", err)
	}
	if err := waitForRuntimePortfolioNetQty(ctx, node, instID, filledQty); err != nil {
		t.Fatalf("runtime portfolio did not observe OKX Spot Demo spot exposure: %v", err)
	}
	if got := node.Metrics(); got.OrdersSeen == 0 || got.FillsSeen == 0 {
		t.Fatalf("runtime metrics did not observe spot order/fill events: %+v", got)
	}

	closeClientID := demoClientOrderID("runtime-close")
	cleanup.Arm(closeClientID)
	closeBook, err := adapter.Market.OrderBook(ctx, instID, 5)
	if err != nil {
		t.Fatalf("load OKX Spot Demo runtime close book: %v", err)
	}
	if len(closeBook.Bids) == 0 {
		t.Fatalf("empty OKX Spot Demo runtime bid book before close for %s", spec.VenueSymbol)
	}
	closePrice := floorDecimalToStep(closeBook.Bids[0].Price.Mul(decimal.RequireFromString("0.99")), spec.PriceTick)
	closed, err := node.Exec.Submit(ctx, model.OrderRequest{
		InstrumentID: instID,
		ClientID:     closeClientID,
		Side:         enums.SideSell,
		Type:         enums.TypeLimit,
		TIF:          enums.TifIOC,
		Quantity:     floorDecimalToStep(filledQty, spec.SizeStep),
		Price:        closePrice,
		PositionSide: enums.PosNet,
	})
	if err != nil {
		t.Fatalf("runtime close OKX Spot Demo base delta: %v", err)
	}
	cleanup.RecordVenueOrderID(closed.VenueOrderID)
	if _, err := waitForDemoOrderStatus(ctx, adapter.rest, spec.VenueSymbol, closeClientID, "filled"); err != nil {
		t.Fatalf("wait for runtime close fill: %v", err)
	}
	if err := waitForRuntimePortfolioFlat(ctx, node, instID); err != nil {
		t.Fatalf("runtime portfolio did not return flat after Spot close: %v", err)
	}
	if _, err := node.Resync(ctx); err != nil {
		t.Fatalf("final OKX Spot Demo runtime reconcile: %v", err)
	}
	if got := len(node.Cache.Positions()); got != 0 {
		t.Fatalf("spot runtime cache positions=%d, want 0 after final reconcile", got)
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, instID); err != nil {
		t.Fatalf("wait for no OKX Spot Demo runtime open orders: %v", err)
	}
	if err := waitForDemoSpotBaseDeltaBelowStep(ctx, adapter, spec, startBaseAvailable, &cleanup); err != nil {
		t.Fatalf("wait for OKX Spot Demo runtime base delta cleanup: %v\n%s", err, cleanup.Remediation())
	}
	cleanup.MarkClean()
}

func newOKXSpotDemoRuntimeFixture(t *testing.T, ctx context.Context, cfg testenv.OKXDemoConfig) (*Adapter, demoSpotSpec, model.InstrumentID, decimal.Decimal, decimal.Decimal, decimal.Decimal) {
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
		Environment:     okx.Simulated,
		DemoHostProfile: okx.DemoHostProfile(cfg.HostProfile),
		RESTBaseURL:     endpoints.REST,
		WSPublicURL:     endpoints.WSPublic,
		WSPrivateURL:    endpoints.WSPrivate,
		HTTPClient:      httpClient,
	})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Spot Demo runtime adapter initialization")
		t.Fatalf("new OKX Spot Demo runtime adapter: %v", err)
	}
	instID := model.InstrumentID{Venue: venueName, Symbol: cfg.SpotSymbol, Kind: enums.KindSpot}
	inst, ok := adapter.provider.Instrument(instID)
	if !ok {
		_ = adapter.Close()
		t.Fatalf("OKX Spot Demo runtime symbol %s not loaded", cfg.SpotSymbol)
	}
	spec, err := demoSpotSpecFromInstrument(inst)
	if err != nil {
		_ = adapter.Close()
		t.Fatalf("resolve OKX Spot Demo runtime symbol: %v", err)
	}
	book, err := adapter.Market.OrderBook(ctx, instID, 5)
	if err != nil {
		_ = adapter.Close()
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Spot Demo runtime order book")
		t.Fatalf("order book: %v", err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		_ = adapter.Close()
		t.Fatalf("empty OKX Spot Demo runtime book for %s", spec.VenueSymbol)
	}
	qty, err := selectDemoSpotQuantity(spec, cfg.MaxNotionalUSDT, book.Asks[0].Price)
	if err != nil {
		_ = adapter.Close()
		t.Fatalf("select safe OKX Spot Demo runtime order quantity: %v", err)
	}
	restingPrice := floorDecimalToStep(book.Bids[0].Price.Mul(decimal.RequireFromString("0.80")), spec.PriceTick)
	fillPrice := ceilDecimalToStep(book.Asks[0].Price.Mul(decimal.RequireFromString("1.01")), spec.PriceTick)
	if open, err := adapter.Execution.OpenOrders(ctx, instID); err != nil {
		_ = adapter.Close()
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Spot Demo runtime open order preflight")
		t.Fatalf("open order preflight: %v", err)
	} else if len(open) > 0 {
		_ = adapter.Close()
		t.Skipf("skipping OKX Spot Demo runtime E2E: %s already has %d open order(s); clean the Demo account before running", spec.VenueSymbol, len(open))
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
