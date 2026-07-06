package spot

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
	accountID := adapter.acct.accountID
	if accountID == "" || adapter.exec.accountID != accountID {
		t.Fatalf("adapter account ids acct=%q exec=%q, want shared canonical account id", adapter.acct.accountID, adapter.exec.accountID)
	}

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
		btruntime.WithAccountID(accountID),
		btruntime.WithAccountStaleAfter(2*time.Minute),
	)
	riskEngine := risk.New(risk.Limits{}, node.Cache).
		RequireAccountState()
	btruntime.WithRisk(riskEngine, adapter.Market.InstrumentProvider())(node)

	rep, err := node.Resync(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Spot Testnet runtime initial reconcile")
		t.Fatalf("initial runtime reconcile: %v", err)
	}
	if rep.AccountStatesApplied != 1 {
		t.Fatalf("initial runtime reconcile account states=%d, want 1 (report=%+v)", rep.AccountStatesApplied, rep)
	}
	assertHyperliquidSpotRuntimeAccountReady(t, node, accountID, inst.Quote)
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
	oversizedQty := hyperliquidSpotAccountBackedRejectQuantity(t, node, accountID, inst, qty, price)
	if _, err := node.Exec.Submit(ctx, model.OrderRequest{
		InstrumentID: inst.ID,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTC,
		Quantity:     oversizedQty,
		Price:        price,
		PositionSide: enums.PosNet,
	}); !errors.Is(err, risk.ErrRiskRejected) || !strings.Contains(err.Error(), "account") {
		t.Fatalf("account-state risk probe err=%v, want account-backed ErrRiskRejected", err)
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
	if order.Request.AccountID != accountID {
		t.Fatalf("submitted order account id=%q, want %q", order.Request.AccountID, accountID)
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
	if got := node.Portfolio.NetQty(inst.ID, enums.PosNet); !got.IsZero() {
		t.Fatalf("spot runtime portfolio net qty=%s, want flat", got)
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

func hyperliquidSpotAccountBackedRejectQuantity(t *testing.T, node *btruntime.TradingNode, accountID string, inst *model.Instrument, validQty, price decimal.Decimal) decimal.Decimal {
	t.Helper()
	free := assertHyperliquidSpotRuntimeAccountReady(t, node, accountID, inst.Quote)
	denom := price.Mul(hyperliquidSpotContractMultiplier(inst))
	if !denom.IsPositive() {
		t.Fatalf("invalid account-backed risk denominator price=%s multiplier=%s", price, hyperliquidSpotContractMultiplier(inst))
	}
	return free.Div(denom).Add(validQty)
}

func hyperliquidSpotContractMultiplier(inst *model.Instrument) decimal.Decimal {
	if inst != nil && inst.ContractMultiplier.IsPositive() {
		return inst.ContractMultiplier
	}
	return decimal.NewFromInt(1)
}

func ensureSpotTestnetCash(t *testing.T, ctx context.Context, adapter *Adapter, inst *model.Instrument, qty, price decimal.Decimal) {
	t.Helper()
	state, err := adapter.acct.AccountState(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Spot Testnet runtime account state")
		t.Fatalf("account state: %v", err)
	}
	required := qty.Mul(price)
	for _, balance := range state.Balances {
		if balance.Currency == inst.Quote {
			free := balance.FreeOrAvailable()
			if free.LessThan(required) {
				t.Skipf("skipping Hyperliquid Spot Testnet acceptance: account-state free %s %s below required %s", inst.Quote, free, required)
			}
			return
		}
	}
	t.Skipf("skipping Hyperliquid Spot Testnet acceptance: no %s balance found in account state", inst.Quote)
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
