package perp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/observ"
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
		t.Fatalf("Hyperliquid HIP-3 Testnet runtime acceptance requires %s with a dex-qualified symbol", testenv.HyperliquidTestnetHIP3SymbolEnv)
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
		t.Fatalf("new Hyperliquid %s Testnet runtime adapter: %v", label, err)
	}
	return adapter
}

func runHyperliquidPerpTestnetRuntimeAcceptance(t *testing.T, ctx context.Context, adapter *Adapter, cfg testenv.HyperliquidTestnetConfig, inst *model.Instrument, label string) {
	t.Helper()
	accountID := adapter.acct.accountID
	if accountID == "" || adapter.exec.accountID != accountID {
		t.Fatalf("adapter account ids acct=%q exec=%q, want shared canonical account id", adapter.acct.accountID, adapter.exec.accountID)
	}
	if open, err := adapter.Execution.OpenOrders(ctx, inst.ID); err != nil {
		t.Fatalf("open order preflight: %v", err)
	} else if len(open) > 0 {
		t.Fatalf("unsafe pre-existing state: Hyperliquid %s Testnet %s already has %d open order(s); clean the testnet account first", label, inst.VenueSymbol, len(open))
	}

	book, err := adapter.Market.OrderBook(ctx, inst.ID, 5)
	if err != nil {
		t.Fatalf("order book: %v", err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("empty Hyperliquid %s Testnet runtime book for %s", label, inst.VenueSymbol)
	}
	lifecycleSpec := hyperliquidPerpTestnetLifecycleSpec(t, "Hyperliquid "+label+" Testnet runtime", label, accountID, inst, book, cfg.MaxNotionalUSDC, adapter.acct)
	requirePerpTestnetCollateral(t, ctx, adapter, label, inst, lifecycleSpec.Quantity, lifecycleSpec.FillPrice)
	evidence := &hyperliquidPerpRuntimePrivateEvidence{}

	node := btruntime.NewNode(
		btruntime.Clients{Market: adapter.Market, Execution: adapter.Execution, Account: adapter.Account},
		clock.NewRealClock(),
		"hyperliquid-"+strings.ToLower(strings.ReplaceAll(label, "-", ""))+"-testnet",
		btruntime.WithAccountID(accountID),
		btruntime.WithAccountStaleAfter(2*time.Minute),
		btruntime.WithObserver(evidence),
		btruntime.WithOnExecEnvelope(evidence.OnExecEnvelope),
	)
	runtimeaccept.AttachAccountRequiredRiskWithMaxNotional(node, adapter.Market.InstrumentProvider(), cfg.MaxNotionalUSDC)

	rep, err := node.Resync(ctx)
	if err != nil {
		t.Fatalf("initial runtime reconcile: %v", err)
	}
	if rep.AccountStatesApplied != 1 {
		t.Fatalf("initial runtime reconcile account states=%d, want 1 (report=%+v)", rep.AccountStatesApplied, rep)
	}
	assertHyperliquidPerpRuntimeAccountReady(t, node, accountID, inst)
	assertNoHyperliquidRuntimePosition(t, node, inst.ID, "initial reconcile")
	if err := adapter.Start(ctx); err != nil {
		t.Fatalf("start adapter: %v", err)
	}

	runCtx, stopNode := context.WithCancel(ctx)
	nodeDone := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(nodeDone)
	}()
	defer stopHyperliquidPerpRuntimeNode(t, stopNode, nodeDone)
	lifecycleSpec.BeforeRuntimeClose = func(closeCtx context.Context, qty decimal.Decimal) error {
		return runtimeaccept.WaitForPortfolioNetQty(closeCtx, node, inst.ID, qty)
	}
	result, err := runtimeaccept.RunRuntimeOrderLifecycle(ctx, node, adapter.Execution, lifecycleSpec)
	if err != nil {
		t.Fatalf("Hyperliquid %s Testnet runtime lifecycle: %v", label, err)
	}
	requireHyperliquidPerpRuntimePrivateLifecycleEvidence(t, ctx, evidence, result)
	finalRep, err := node.Resync(ctx)
	if err != nil {
		t.Fatalf("final Hyperliquid %s Testnet runtime reconcile: %v", label, err)
	}
	if finalRep.AccountStatesApplied != 1 {
		t.Fatalf("final runtime reconcile account states=%d, want 1 (report=%+v)", finalRep.AccountStatesApplied, finalRep)
	}
	assertHyperliquidPerpRuntimeAccountReady(t, node, accountID, inst)
	if got := len(node.Cache.OpenOrders()); got != 0 {
		t.Fatalf("runtime cache open orders=%d, want 0 after final reconcile", got)
	}
	assertNoHyperliquidRuntimePosition(t, node, inst.ID, "final reconcile")
	if got := node.Portfolio.NetQty(inst.ID, enums.PosNet); !got.IsZero() {
		t.Fatalf("runtime portfolio net qty=%s, want flat", got)
	}
}

func assertHyperliquidPerpRuntimeAccountReady(t *testing.T, node *btruntime.TradingNode, accountID string, inst *model.Instrument) decimal.Decimal {
	t.Helper()
	currency, err := hyperliquidPerpTestnetSettlementCurrency(inst)
	if err != nil {
		t.Fatal(err)
	}
	acct, ok := node.Cache.Account(accountID)
	if !ok {
		t.Fatalf("runtime cache missing account state for %s", accountID)
	}
	if !acct.IsFresh(time.Now()) {
		t.Fatalf("runtime account %s is stale: %+v", accountID, acct.Freshness())
	}
	if _, ok := node.Portfolio.Account(accountID); !ok {
		t.Fatalf("runtime portfolio missing account %s", accountID)
	}
	if equity, ok := node.Portfolio.Equity(accountID); !ok || len(equity) == 0 {
		t.Fatalf("runtime portfolio equity=%v ok=%v, want non-empty account equity", equity, ok)
	}
	free, ok := acct.BalanceFree(currency)
	if !ok {
		t.Fatalf("runtime account %s missing free %s", accountID, currency)
	}
	if _, ok := node.Cache.BalanceForAccount(accountID, currency); !ok {
		t.Fatalf("runtime cache missing account-scoped %s balance for %s", currency, accountID)
	}
	if metrics := node.Metrics(); metrics.Accounts != 1 || metrics.AccountStateAgeNs < 0 {
		t.Fatalf("runtime metrics accounts=%d accountAgeNs=%d, want one fresh account", metrics.Accounts, metrics.AccountStateAgeNs)
	}
	return free
}

func requirePerpTestnetCollateral(t *testing.T, ctx context.Context, adapter *Adapter, label string, inst *model.Instrument, qty, price decimal.Decimal) {
	t.Helper()
	currency, err := hyperliquidPerpTestnetSettlementCurrency(inst)
	if err != nil {
		t.Fatal(err)
	}
	state, err := adapter.acct.AccountState(ctx)
	if err != nil {
		t.Fatalf("account state: %v", err)
	}
	required := qty.Mul(price)
	for _, balance := range state.Balances {
		if strings.EqualFold(balance.Currency, currency) {
			free := balance.Free
			if free.LessThan(required) {
				t.Fatalf("insufficient Hyperliquid %s Testnet collateral: account-state free %s %s below required notional %s", label, currency, free, required)
			}
			return
		}
	}
	t.Fatalf("insufficient Hyperliquid %s Testnet collateral: no %s balance found in account state", label, currency)
}

func hyperliquidPerpTestnetSettlementCurrency(inst *model.Instrument) (string, error) {
	if inst == nil {
		return "", fmt.Errorf("hyperliquid perp testnet: instrument required for collateral check")
	}
	currency := strings.TrimSpace(inst.Settle)
	if currency == "" {
		return "", fmt.Errorf("hyperliquid perp testnet: instrument %s has no settlement currency", inst.ID)
	}
	return currency, nil
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

func assertNoHyperliquidRuntimePosition(t *testing.T, node *btruntime.TradingNode, id model.InstrumentID, stage string) {
	t.Helper()
	for _, position := range node.Cache.Positions() {
		if position.InstrumentID == id && !position.Quantity.IsZero() {
			t.Fatalf("%s runtime position for %s remains non-zero: %+v", stage, id, position)
		}
	}
}

type hyperliquidPerpRuntimePrivateEvidence struct {
	observ.Base
	mu     sync.RWMutex
	orders []model.Order
	fills  []model.Fill
	events []contract.ExecEnvelope
}

func (e *hyperliquidPerpRuntimePrivateEvidence) OnOrder(order model.Order) {
	e.mu.Lock()
	e.orders = append(e.orders, order)
	e.mu.Unlock()
}

func (e *hyperliquidPerpRuntimePrivateEvidence) OnFill(fill model.Fill) {
	e.mu.Lock()
	e.fills = append(e.fills, fill)
	e.mu.Unlock()
}

func (e *hyperliquidPerpRuntimePrivateEvidence) OnExecEnvelope(env contract.ExecEnvelope) {
	e.mu.Lock()
	e.events = append(e.events, env)
	e.mu.Unlock()
}

func requireHyperliquidPerpRuntimePrivateLifecycleEvidence(t *testing.T, ctx context.Context, evidence *hyperliquidPerpRuntimePrivateEvidence, result *runtimeaccept.OrderLifecycleResult) {
	t.Helper()
	if evidence == nil || result == nil {
		t.Fatal("Hyperliquid runtime private lifecycle evidence and result are required")
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if evidence.hasOrderAndFill(result.Filled) && evidence.hasOrderAndFill(result.Closed) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("runtime private order/fill evidence incomplete for fill=%s close=%s: %v", result.Filled.VenueOrderID, result.Closed.VenueOrderID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (e *hyperliquidPerpRuntimePrivateEvidence) hasOrderAndFill(target model.Order) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var orderSeen, fillSeen bool
	for _, event := range e.events {
		if event.Source != contract.SourceAdapterStream || !event.Flags.Has(contract.EventFlagFromStream) {
			continue
		}
		switch payload := event.Payload.(type) {
		case contract.OrderEvent:
			if hyperliquidPerpLifecycleIdentityMatches(payload.Order.Request.ClientID, payload.Order.VenueOrderID, target) {
				orderSeen = true
			}
		case contract.FillEvent:
			if hyperliquidPerpLifecycleIdentityMatches(payload.Fill.ClientID, payload.Fill.VenueOrderID, target) {
				fillSeen = true
			}
		}
	}
	return orderSeen && fillSeen
}
