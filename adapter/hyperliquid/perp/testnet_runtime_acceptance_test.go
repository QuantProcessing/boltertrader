package perp

import (
	"context"
	"errors"
	"strings"
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

func TestHyperliquidPerpTestnetRuntimeAcceptance(t *testing.T) {
	cfg := testenv.RequireHyperliquidTestnetWrite(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	adapter := newHyperliquidPerpTestnetRuntimeAdapter(t, ctx, cfg, false, nil, "Perp")
	defer adapter.Close()

	inst := selectPerpTestnetInstrument(t, adapter, cfg.PerpSymbol)
	runHyperliquidPerpTestnetRuntimeAcceptance(t, ctx, adapter, cfg, inst, "Perp")
}

func TestHyperliquidPerpTestnetHIP3RuntimeAcceptance(t *testing.T) {
	cfg := testenv.RequireHyperliquidTestnetWrite(t)
	if cfg.HIP3Symbol == "" {
		t.Skipf("skipping Hyperliquid HIP-3 Testnet runtime acceptance: set %s to a dex-qualified symbol such as dex:coin or dex:coin-USDC", testenv.HyperliquidTestnetHIP3SymbolEnv)
	}
	dex, _, ok := strings.Cut(cfg.HIP3Symbol, ":")
	if !ok || dex == "" {
		t.Fatalf("%s must include a dex qualifier, got %q", testenv.HyperliquidTestnetHIP3SymbolEnv, cfg.HIP3Symbol)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	adapter := newHyperliquidPerpTestnetRuntimeAdapter(t, ctx, cfg, true, []string{dex}, "HIP-3")
	defer adapter.Close()

	inst := selectPerpTestnetInstrument(t, adapter, cfg.HIP3Symbol)
	runHyperliquidPerpTestnetRuntimeAcceptance(t, ctx, adapter, cfg, inst, "HIP-3")
}

func newHyperliquidPerpTestnetRuntimeAdapter(t *testing.T, ctx context.Context, cfg testenv.HyperliquidTestnetConfig, includeHIP3 bool, hip3Dexes []string, label string) *Adapter {
	t.Helper()
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
		IncludeHIP3:    includeHIP3,
		HIP3Dexes:      hip3Dexes,
	})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid "+label+" Testnet runtime adapter initialization")
		t.Fatalf("new Hyperliquid %s Testnet runtime adapter: %v", label, err)
	}
	return adapter
}

func runHyperliquidPerpTestnetRuntimeAcceptance(t *testing.T, ctx context.Context, adapter *Adapter, cfg testenv.HyperliquidTestnetConfig, inst *model.Instrument, label string) {
	t.Helper()
	if open, err := adapter.Execution.OpenOrders(ctx, inst.ID); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid "+label+" Testnet runtime open order preflight")
		t.Fatalf("open order preflight: %v", err)
	} else if len(open) > 0 {
		t.Skipf("skipping Hyperliquid %s Testnet runtime acceptance: %s already has %d open order(s); clean the testnet account first", label, inst.VenueSymbol, len(open))
	}
	ensureNoHyperliquidPerpTestnetPositions(t, ctx, adapter, label)

	book, err := adapter.Market.OrderBook(ctx, inst.ID, 5)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid "+label+" Testnet runtime order book")
		t.Fatalf("order book: %v", err)
	}
	if len(book.Bids) == 0 {
		t.Fatalf("empty Hyperliquid %s Testnet runtime bids for %s", label, inst.VenueSymbol)
	}
	price := accepttest.RestingBuyPrice(inst, book.Bids[0].Price, false)
	qty := selectHyperliquidPerpTestnetQuantity(inst, cfg.MaxNotionalUSDC, price)
	ensurePerpTestnetCollateral(t, ctx, adapter, label, qty, price)

	node := btruntime.NewNode(
		btruntime.Clients{Market: adapter.Market, Execution: adapter.Execution, Account: adapter.Account},
		clock.NewRealClock(),
		"hyperliquid-"+strings.ToLower(strings.ReplaceAll(label, "-", ""))+"-testnet",
	)
	riskEngine := risk.New(risk.Limits{MaxOrderNotional: cfg.MaxNotionalUSDC}, node.Cache).
		AllowLegacyBalanceFallback()
	btruntime.WithRisk(riskEngine, adapter.Market.InstrumentProvider())(node)

	if _, err := node.Resync(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid "+label+" Testnet runtime initial reconcile")
		t.Fatalf("initial runtime reconcile: %v", err)
	}
	if got := node.Cache.Balances(); len(got) == 0 {
		t.Fatalf("runtime cache has no balances after initial reconcile")
	}
	if got := len(node.Cache.Positions()); got != 0 {
		t.Fatalf("runtime cache has %d pre-existing positions after clean preflight", got)
	}
	if err := adapter.Start(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid "+label+" Testnet runtime adapter start")
		t.Fatalf("start adapter: %v", err)
	}

	runCtx, stopNode := context.WithCancel(ctx)
	nodeDone := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(nodeDone)
	}()
	defer stopHyperliquidPerpRuntimeNode(t, stopNode, nodeDone)
	if err := waitForHyperliquidPerpRuntimeActive(ctx, node); err != nil {
		t.Fatalf("runtime did not become active before Hyperliquid %s Testnet writes: %v", label, err)
	}
	if _, err := node.Exec.Submit(ctx, model.OrderRequest{
		InstrumentID: inst.ID,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTX,
		Quantity:     qty,
		Price:        price.Mul(decimal.NewFromInt(10)),
		PositionSide: enums.PosNet,
	}); !errors.Is(err, risk.ErrRiskRejected) {
		t.Fatalf("risk probe err=%v, want ErrRiskRejected", err)
	}

	var venueOrderID string
	defer func() {
		if venueOrderID != "" {
			_ = adapter.Execution.Cancel(context.Background(), inst.ID, venueOrderID)
		}
	}()
	order, err := node.Exec.Submit(ctx, model.OrderRequest{
		InstrumentID: inst.ID,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTX,
		Quantity:     qty,
		Price:        price,
		PositionSide: enums.PosNet,
	})
	if err != nil {
		t.Fatalf("runtime submit Hyperliquid %s Testnet resting order: %v", label, err)
	}
	venueOrderID = order.VenueOrderID
	if order.Status == enums.StatusFilled || !order.FilledQty.IsZero() {
		t.Fatalf("resting runtime order unexpectedly filled: %+v", order)
	}
	if err := node.Exec.Cancel(ctx, order.Request.ClientID); err != nil {
		t.Fatalf("runtime cancel Hyperliquid %s Testnet resting order: %v", label, err)
	}
	venueOrderID = ""
	if err := waitForHyperliquidPerpRuntimeOrderStatus(ctx, node, order.Request.ClientID, enums.StatusCanceled); err != nil {
		t.Fatalf("runtime cache did not observe canceled %s order: %v", label, err)
	}
	if err := waitForNoHyperliquidPerpOpenOrders(ctx, adapter, inst.ID); err != nil {
		t.Fatalf("wait for no Hyperliquid %s Testnet open orders: %v", label, err)
	}
	if _, err := node.Resync(ctx); err != nil {
		t.Fatalf("final Hyperliquid %s Testnet runtime reconcile: %v", label, err)
	}
	if got := len(node.Cache.OpenOrders()); got != 0 {
		t.Fatalf("runtime cache open orders=%d, want 0 after final reconcile", got)
	}
	if got := len(node.Cache.Positions()); got != 0 {
		t.Fatalf("runtime cache positions=%d, want 0 after final reconcile", got)
	}
	if got := node.Portfolio.NetQty(inst.ID, enums.PosNet); !got.IsZero() {
		t.Fatalf("runtime portfolio net qty=%s, want flat", got)
	}
}

func ensureNoHyperliquidPerpTestnetPositions(t *testing.T, ctx context.Context, adapter *Adapter, label string) {
	t.Helper()
	positions, err := adapter.Account.Positions(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid "+label+" Testnet runtime position preflight")
		t.Fatalf("position preflight: %v", err)
	}
	if len(positions) > 0 {
		t.Skipf("skipping Hyperliquid %s Testnet runtime acceptance: account already has %d open position(s); clean the testnet account first", label, len(positions))
	}
}

func ensurePerpTestnetCollateral(t *testing.T, ctx context.Context, adapter *Adapter, label string, qty, price decimal.Decimal) {
	t.Helper()
	balances, err := adapter.Account.Balances(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid "+label+" Testnet runtime balances")
		t.Fatalf("balances: %v", err)
	}
	required := qty.Mul(price)
	for _, balance := range balances {
		if balance.Currency == "USDC" {
			if balance.Available.LessThan(required) {
				t.Skipf("skipping Hyperliquid %s Testnet runtime acceptance: available USDC %s below required notional %s", label, balance.Available, required)
			}
			return
		}
	}
	t.Skipf("skipping Hyperliquid %s Testnet runtime acceptance: no USDC balance found", label)
}

func waitForHyperliquidPerpRuntimeActive(ctx context.Context, node *btruntime.TradingNode) error {
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

func stopHyperliquidPerpRuntimeNode(t *testing.T, stop context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("runtime node did not stop")
	}
}

func waitForHyperliquidPerpRuntimeOrderStatus(ctx context.Context, node *btruntime.TradingNode, clientID string, status enums.OrderStatus) error {
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

func waitForNoHyperliquidPerpOpenOrders(ctx context.Context, adapter *Adapter, id model.InstrumentID) error {
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
