package spot

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/accepttest"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/lifecycle"
	"github.com/QuantProcessing/boltertrader/runtime/risk"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	"github.com/shopspring/decimal"
)

func TestHyperliquidSpotTestnetRuntimeAcceptance(t *testing.T) {
	cfg := testenv.RequireHyperliquidTestnetWrite(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	httpClient, err := testenv.HyperliquidTestnetHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Hyperliquid Testnet HTTP client: %v", err)
	}
	adapter, err := New(ctx, Config{
		PrivateKey:     cfg.PrivateKey,
		AccountAddress: cfg.AccountAddress,
		VaultAddress:   cfg.VaultAddress,
		Environment:    sdk.EnvironmentTestnet,
		HTTPClient:     httpClient,
	})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Spot Testnet runtime adapter initialization")
		t.Fatalf("new Hyperliquid Spot Testnet runtime adapter: %v", err)
	}
	defer adapter.Close()

	inst := selectSpotTestnetInstrument(t, adapter, cfg.SpotSymbol)
	if open, err := adapter.Execution.OpenOrders(ctx, inst.ID); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Spot Testnet runtime open order preflight")
		t.Fatalf("open order preflight: %v", err)
	} else if len(open) > 0 {
		t.Skipf("skipping Hyperliquid Spot Testnet runtime acceptance: %s already has %d open order(s); clean the testnet account first", inst.VenueSymbol, len(open))
	}
	book, err := adapter.Market.OrderBook(ctx, inst.ID, 5)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Spot Testnet runtime order book")
		t.Fatalf("order book: %v", err)
	}
	if len(book.Bids) == 0 {
		t.Fatalf("empty Hyperliquid Spot Testnet runtime bids for %s", inst.VenueSymbol)
	}
	price := accepttest.RestingBuyPrice(inst, book.Bids[0].Price, true)
	qty := selectHyperliquidTestnetQuantity(inst, cfg.MaxNotionalUSDC, price)
	ensureSpotTestnetCash(t, ctx, adapter, inst, qty, price)

	node := btruntime.NewNode(
		btruntime.Clients{Market: adapter.Market, Execution: adapter.Execution, Account: adapter.Account},
		clock.NewRealClock(),
		"hyperliquid-spot-testnet",
	)
	riskEngine := risk.New(risk.Limits{MaxOrderNotional: cfg.MaxNotionalUSDC}, node.Cache)
	btruntime.WithRisk(riskEngine, adapter.Market.InstrumentProvider())(node)

	if _, err := node.Resync(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Spot Testnet runtime initial reconcile")
		t.Fatalf("initial runtime reconcile: %v", err)
	}
	if got := node.Cache.Balances(); len(got) == 0 {
		t.Fatalf("runtime cache has no balances after initial reconcile")
	}
	if err := adapter.Start(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Spot Testnet runtime adapter start")
		t.Fatalf("start adapter: %v", err)
	}

	runCtx, stopNode := context.WithCancel(ctx)
	nodeDone := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(nodeDone)
	}()
	defer stopRuntimeNode(t, stopNode, nodeDone)
	if err := waitForHyperliquidRuntimeActive(ctx, node); err != nil {
		t.Fatalf("runtime did not become active before Hyperliquid Spot Testnet writes: %v", err)
	}
	if _, err := node.Exec.Submit(ctx, model.OrderRequest{
		InstrumentID: inst.ID,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     qty,
		Price:        price.Mul(decimal.NewFromInt(10)),
		PositionSide: enums.PosNet,
	}); !errors.Is(err, risk.ErrRiskRejected) {
		t.Fatalf("risk probe err=%v, want ErrRiskRejected", err)
	}

	order, err := node.Exec.Submit(ctx, model.OrderRequest{
		InstrumentID: inst.ID,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     qty,
		Price:        price,
		PositionSide: enums.PosNet,
	})
	if err != nil {
		t.Fatalf("runtime submit Hyperliquid Spot Testnet resting order: %v", err)
	}
	if err := node.Exec.Cancel(ctx, order.Request.ClientID); err != nil {
		t.Fatalf("runtime cancel Hyperliquid Spot Testnet resting order: %v", err)
	}
	if err := waitForRuntimeOrderStatus(ctx, node, order.Request.ClientID, enums.StatusCanceled); err != nil {
		t.Fatalf("runtime cache did not observe canceled Spot order: %v", err)
	}
	if err := waitForNoHyperliquidSpotOpenOrders(ctx, adapter, inst.ID); err != nil {
		t.Fatalf("wait for no Hyperliquid Spot Testnet open orders: %v", err)
	}
	if _, err := node.Resync(ctx); err != nil {
		t.Fatalf("final Hyperliquid Spot Testnet runtime reconcile: %v", err)
	}
	if got := len(node.Cache.Positions()); got != 0 {
		t.Fatalf("spot runtime cache positions=%d, want 0 after final reconcile", got)
	}
	if got := node.Portfolio.NetQty(inst.ID, enums.PosNet); !got.IsZero() {
		t.Fatalf("spot runtime portfolio net qty=%s, want flat", got)
	}
}

func ensureSpotTestnetCash(t *testing.T, ctx context.Context, adapter *Adapter, inst *model.Instrument, qty, price decimal.Decimal) {
	t.Helper()
	balances, err := adapter.Account.Balances(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Spot Testnet runtime balances")
		t.Fatalf("balances: %v", err)
	}
	required := qty.Mul(price)
	for _, balance := range balances {
		if balance.Currency == inst.Quote {
			if balance.Available.LessThan(required) {
				t.Skipf("skipping Hyperliquid Spot Testnet acceptance: available %s %s below required %s", inst.Quote, balance.Available, required)
			}
			return
		}
	}
	t.Skipf("skipping Hyperliquid Spot Testnet acceptance: no %s balance found", inst.Quote)
}

func waitForHyperliquidRuntimeActive(ctx context.Context, node *btruntime.TradingNode) error {
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		last := node.State()
		if last.Node == lifecycle.NodeRunning && last.Trading == lifecycle.TradingActive {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}

func stopRuntimeNode(t *testing.T, stop context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("runtime node did not stop")
	}
}

func waitForRuntimeOrderStatus(ctx context.Context, node *btruntime.TradingNode, clientID string, status enums.OrderStatus) error {
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

func waitForNoHyperliquidSpotOpenOrders(ctx context.Context, adapter *Adapter, id model.InstrumentID) error {
	ticker := time.NewTicker(500 * time.Millisecond)
	defer ticker.Stop()
	for {
		open, err := adapter.Execution.OpenOrders(ctx, id)
		if err == nil && len(open) == 0 {
			return nil
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
		}
	}
}
