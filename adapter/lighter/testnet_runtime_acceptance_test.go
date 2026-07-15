package lighter

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/risk"
)

func TestLighterTestnetPerpRuntimeAcceptance(t *testing.T) {
	runLighterTestnetRuntimeAcceptance(t, enums.KindPerp, "Perp")
}

func TestLighterTestnetSpotRuntimeAcceptance(t *testing.T) {
	runLighterTestnetRuntimeAcceptance(t, enums.KindSpot, "Spot")
}

func runLighterTestnetRuntimeAcceptance(t *testing.T, kind enums.InstrumentKind, label string) {
	t.Helper()
	cfg := testenv.RequireLighterTestnetWrite(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	adapter := newLighterTestnetAdapter(t, ctx, cfg, true, 45*time.Second, "runtime "+label)
	defer adapter.Close()

	inst := selectLighterTestnetInstrument(t, adapter, symbolForLighterKind(cfg, kind), kind)
	ensureNoLighterTestnetOpenOrders(t, ctx, adapter, inst.ID, "runtime "+label)
	ensureNoLighterTestnetPositions(t, ctx, adapter, "runtime "+label)

	book, err := adapter.Market.OrderBook(ctx, inst.ID, 5)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Lighter "+label+" Testnet runtime order book")
		t.Fatalf("order book: %v", err)
	}
	if len(book.Asks) == 0 {
		t.Fatalf("empty Lighter %s Testnet runtime asks for %s", label, inst.VenueSymbol)
	}
	price := lighterRestingBuyPrice(inst, book)
	qty, err := selectLighterTestnetQuantity(inst, cfg.MaxNotionalUSDC, price)
	if err != nil {
		t.Fatalf("select Lighter %s Testnet runtime quantity: %v", label, err)
	}
	ensureLighterTestnetCollateral(t, ctx, adapter, "runtime "+label, qty, price)

	accountID := AccountIDDefault
	node := btruntime.NewNode(
		btruntime.Clients{Market: adapter.Market, Execution: adapter.Execution, Account: adapter.Account},
		clock.NewRealClock(),
		"lighter-"+strings.ToLower(label)+"-testnet",
		btruntime.WithAccountID(accountID),
	)
	provider := adapter.Market.InstrumentProvider()
	riskEngine := risk.New(risk.Limits{MaxOrderNotional: cfg.MaxNotionalUSDC}, node.Cache).
		WithInstrumentProvider(provider).
		RequireAccountState()
	riskEngine.SetRuntimeCapabilities(node.ExecutionCapabilities(), node.AccountCapabilities())
	btruntime.WithRisk(riskEngine, provider)(node)

	if _, err := node.Resync(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Lighter "+label+" Testnet runtime initial reconcile")
		t.Fatalf("initial runtime reconcile: %v", err)
	}
	runtimeaccept.AssertAccountStateReady(t, node, accountID, model.AccountMargin, kind)
	if got := len(node.Cache.Positions()); got != 0 {
		t.Fatalf("runtime cache has %d pre-existing positions after clean preflight", got)
	}
	if err := adapter.Start(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Lighter "+label+" Testnet runtime adapter start")
		t.Fatalf("start adapter: %v", err)
	}

	runCtx, stopNode := context.WithCancel(ctx)
	nodeDone := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(nodeDone)
	}()
	defer stopLighterRuntimeNode(t, stopNode, nodeDone)
	if err := runtimeaccept.WaitForActive(ctx, node); err != nil {
		t.Fatalf("runtime did not become active before Lighter %s Testnet writes: %v", label, err)
	}
	runtimeaccept.AssertOversizedOrderRejected(t, node, provider, inst.ID, cfg.MaxNotionalUSDC)
	book, err = adapter.Market.OrderBook(ctx, inst.ID, 5)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Lighter "+label+" Testnet runtime refreshed order book")
		t.Fatalf("refreshed order book: %v", err)
	}
	if len(book.Asks) == 0 {
		t.Fatalf("empty refreshed Lighter %s Testnet runtime asks for %s", label, inst.VenueSymbol)
	}
	price = lighterRestingBuyPrice(inst, book)
	qty, err = selectLighterTestnetQuantity(inst, cfg.MaxNotionalUSDC, price)
	if err != nil {
		t.Fatalf("select refreshed Lighter %s Testnet runtime quantity: %v", label, err)
	}
	ensureLighterTestnetCollateral(t, ctx, adapter, "runtime "+label, qty, price)

	exposureCleaner := newLighterAcceptanceExposureCleaner(adapter.Execution, adapter.acct, adapter.Market)
	exposureBaseline, err := exposureCleaner.CaptureBaseline(ctx, inst)
	if err != nil {
		t.Fatalf("capture runtime Lighter %s Testnet exposure baseline: %v", label, err)
	}
	clientID := newLighterAcceptanceClientID("runtime-" + label)
	cleanup := newLighterRestingOrderCleanup(adapter.Execution, inst.ID, accountID, clientID, qty)
	defer func() {
		if !cleanup.NeedsCleanup() && !cleanup.NeedsExposureCleanup() {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), lighterAcceptanceDeferredCleanupTimeout)
		defer cleanupCancel()
		if cleanupErr := cleanup.CancelConfirmAndRecover(cleanupCtx, exposureCleaner, inst, exposureBaseline); cleanupErr != nil {
			t.Errorf("deferred Lighter %s Testnet runtime cleanup: %v", label, cleanupErr)
		}
	}()
	order, err := node.Exec.Submit(ctx, model.OrderRequest{
		InstrumentID: inst.ID,
		ClientID:     clientID,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTX,
		Quantity:     qty,
		Price:        price,
		PositionSide: enums.PosNet,
	})
	if cleanupErr := cleanup.ObserveSubmitResult(order); cleanupErr != nil {
		t.Fatalf("observe runtime Lighter %s Testnet resting submit: %v", label, cleanupErr)
	}
	if err != nil {
		t.Fatalf("runtime submit Lighter %s Testnet resting order: %v", label, err)
	}
	if order.Request.AccountID != accountID {
		t.Fatalf("runtime order account id=%q, want %q", order.Request.AccountID, accountID)
	}
	if order.Status == enums.StatusFilled || !order.FilledQty.IsZero() {
		t.Fatalf("resting runtime order unexpectedly filled: %+v", order)
	}
	if err := node.Exec.Cancel(ctx, order.Request.ClientID); err != nil {
		t.Fatalf("runtime cancel Lighter %s Testnet resting order: %v", label, err)
	}
	if err := cleanup.CancelAndConfirm(ctx); err != nil {
		t.Fatalf("runtime cancel and confirm Lighter %s Testnet resting order: %v", label, err)
	}
	if err := waitForLighterRuntimeOrderStatus(ctx, node, order.Request.ClientID, enums.StatusCanceled); err != nil {
		t.Fatalf("runtime cache did not observe canceled Lighter %s order: %v", label, err)
	}
	if err := waitForNoLighterTestnetOpenOrders(ctx, adapter, inst.ID); err != nil {
		t.Fatalf("wait for no Lighter %s Testnet open orders: %v", label, err)
	}
	if _, err := node.Resync(ctx); err != nil {
		t.Fatalf("final Lighter %s Testnet runtime reconcile: %v", label, err)
	}
	if open := node.Cache.OpenOrders(); len(open) != 0 {
		t.Fatalf("runtime cache open orders=%d, want 0 after final reconcile: %+v", len(open), open)
	}
	if got := len(node.Cache.Positions()); got != 0 {
		t.Fatalf("runtime cache positions=%d, want 0 after final reconcile", got)
	}
	if got := node.Portfolio.NetQty(inst.ID, enums.PosNet); !got.IsZero() {
		t.Fatalf("runtime portfolio net qty=%s, want flat", got)
	}
	runtimeaccept.AssertAccountStateReady(t, node, accountID, model.AccountMargin, kind)
}

func stopLighterRuntimeNode(t *testing.T, stop context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("runtime node did not stop")
	}
}

func waitForLighterRuntimeOrderStatus(ctx context.Context, node *btruntime.TradingNode, clientID string, status enums.OrderStatus) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if order, ok := node.Cache.Order(clientID); ok && order.Status == status {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
