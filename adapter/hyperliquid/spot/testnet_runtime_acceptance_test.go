package spot

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	"github.com/QuantProcessing/boltertrader/runtime/observ"
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
		t.Fatalf("new Hyperliquid Spot Testnet runtime adapter: %v", err)
	}
	defer adapter.Close()
	accountID := adapter.acct.accountID
	if accountID == "" || adapter.exec.accountID != accountID {
		t.Fatalf("adapter account ids acct=%q exec=%q, want shared canonical account id", adapter.acct.accountID, adapter.exec.accountID)
	}

	inst := requireSpotTestnetWriteInstrument(t, adapter, cfg.SpotSymbol)
	if open, err := adapter.Execution.OpenOrders(ctx, inst.ID); err != nil {
		t.Fatalf("open order preflight: %v", err)
	} else if len(open) > 0 {
		t.Fatalf("unsafe pre-existing state: Hyperliquid Spot Testnet %s already has %d open order(s); clean the testnet account first", inst.VenueSymbol, len(open))
	}
	book, err := adapter.Market.OrderBook(ctx, inst.ID, 5)
	if err != nil {
		t.Fatalf("order book: %v", err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("empty Hyperliquid Spot Testnet runtime book for %s", inst.VenueSymbol)
	}
	lifecycleSpec := hyperliquidSpotTestnetLifecycleSpec(t, "Hyperliquid Spot Testnet runtime", accountID, inst, book, cfg.MaxNotionalUSDC, adapter.acct)
	requireSpotTestnetCash(t, ctx, adapter, inst, lifecycleSpec.Quantity, lifecycleSpec.FillPrice)
	evidence := &hyperliquidRuntimePrivateEvidence{}

	node := btruntime.NewNode(
		btruntime.Clients{Market: adapter.Market, Execution: adapter.Execution, Account: adapter.Account},
		clock.NewRealClock(),
		"hyperliquid-spot-testnet",
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
	assertHyperliquidSpotRuntimeAccountReady(t, node, accountID, inst.Quote)
	if err := adapter.Start(ctx); err != nil {
		t.Fatalf("start adapter: %v", err)
	}

	runCtx, stopNode := context.WithCancel(ctx)
	nodeDone := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(nodeDone)
	}()
	defer stopRuntimeNode(t, stopNode, nodeDone)
	result, err := runtimeaccept.RunRuntimeOrderLifecycle(ctx, node, adapter.Execution, lifecycleSpec)
	if err != nil {
		t.Fatalf("Hyperliquid Spot Testnet runtime lifecycle: %v", err)
	}
	requireHyperliquidRuntimePrivateLifecycleEvidence(t, ctx, evidence, result)
	finalRep, err := node.Resync(ctx)
	if err != nil {
		t.Fatalf("final Hyperliquid Spot Testnet runtime reconcile: %v", err)
	}
	if finalRep.AccountStatesApplied != 1 {
		t.Fatalf("final runtime reconcile account states=%d, want 1 (report=%+v)", finalRep.AccountStatesApplied, finalRep)
	}
	assertHyperliquidSpotRuntimeAccountReady(t, node, accountID, inst.Quote)
	if got := len(node.Cache.Positions()); got != 0 {
		t.Fatalf("spot runtime cache positions=%d, want 0 after final reconcile", got)
	}
	// Hyperliquid replays historical userFills snapshots at subscription time,
	// and this cash lifecycle intentionally retains an unsellable fee buffer.
	// The authoritative final account snapshot above, rather than an absolute
	// fill-derived Portfolio quantity, proves Spot cleanup.
	if got := len(node.Cache.OpenOrders()); got != 0 {
		t.Fatalf("spot runtime cache open orders=%d, want 0 after final reconcile", got)
	}
}

func assertHyperliquidSpotRuntimeAccountReady(t *testing.T, node *btruntime.TradingNode, accountID, currency string) decimal.Decimal {
	t.Helper()
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
		t.Fatalf("runtime account %s missing free balance for %s", accountID, currency)
	}
	if _, ok := node.Cache.BalanceForAccount(accountID, currency); !ok {
		t.Fatalf("runtime cache missing account-scoped balance %s/%s", accountID, currency)
	}
	if metrics := node.Metrics(); metrics.Accounts != 1 || metrics.AccountStateAgeNs < 0 {
		t.Fatalf("runtime metrics accounts=%d accountAgeNs=%d, want one fresh account", metrics.Accounts, metrics.AccountStateAgeNs)
	}
	return free
}

func requireSpotTestnetCash(t *testing.T, ctx context.Context, adapter *Adapter, inst *model.Instrument, qty, price decimal.Decimal) {
	t.Helper()
	state, err := adapter.acct.AccountState(ctx)
	if err != nil {
		t.Fatalf("account state: %v", err)
	}
	required := qty.Mul(price)
	for _, balance := range state.Balances {
		if balance.Currency == inst.Quote {
			free := balance.Free
			if free.LessThan(required) {
				t.Fatalf("insufficient Hyperliquid Spot Testnet funds: account-state free %s %s below required %s", inst.Quote, free, required)
			}
			return
		}
	}
	t.Fatalf("insufficient Hyperliquid Spot Testnet funds: no %s balance found in account state", inst.Quote)
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

type hyperliquidRuntimePrivateEvidence struct {
	observ.Base
	mu     sync.RWMutex
	orders []model.Order
	fills  []model.Fill
	events []contract.ExecEnvelope
}

func (e *hyperliquidRuntimePrivateEvidence) OnOrder(order model.Order) {
	e.mu.Lock()
	e.orders = append(e.orders, order)
	e.mu.Unlock()
}

func (e *hyperliquidRuntimePrivateEvidence) OnFill(fill model.Fill) {
	e.mu.Lock()
	e.fills = append(e.fills, fill)
	e.mu.Unlock()
}

func (e *hyperliquidRuntimePrivateEvidence) OnExecEnvelope(env contract.ExecEnvelope) {
	e.mu.Lock()
	e.events = append(e.events, env)
	e.mu.Unlock()
}

func requireHyperliquidRuntimePrivateLifecycleEvidence(t *testing.T, ctx context.Context, evidence *hyperliquidRuntimePrivateEvidence, result *runtimeaccept.OrderLifecycleResult) {
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

func (e *hyperliquidRuntimePrivateEvidence) hasOrderAndFill(target model.Order) bool {
	e.mu.RLock()
	defer e.mu.RUnlock()
	var orderSeen, fillSeen bool
	for _, event := range e.events {
		if event.Source != contract.SourceAdapterStream || !event.Flags.Has(contract.EventFlagFromStream) {
			continue
		}
		switch payload := event.Payload.(type) {
		case contract.OrderEvent:
			if hyperliquidLifecycleIdentityMatches(payload.Order.Request.ClientID, payload.Order.VenueOrderID, target) {
				orderSeen = true
			}
		case contract.FillEvent:
			if hyperliquidLifecycleIdentityMatches(payload.Fill.ClientID, payload.Fill.VenueOrderID, target) {
				fillSeen = true
			}
		}
	}
	return orderSeen && fillSeen
}
