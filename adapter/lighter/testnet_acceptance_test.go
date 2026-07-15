package lighter

import (
	"context"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	sdk "github.com/QuantProcessing/boltertrader/sdk/lighter"
	"github.com/shopspring/decimal"
)

func TestLighterTestnetReadAcceptance(t *testing.T) {
	cfg := testenv.RequireLighterTestnetRead(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	adapter := newLighterTestnetAdapter(t, ctx, cfg, false, 30*time.Second, "read")
	defer adapter.Close()

	perp := selectLighterTestnetInstrument(t, adapter, cfg.PerpSymbol, enums.KindPerp)
	spot := selectLighterTestnetInstrument(t, adapter, cfg.SpotSymbol, enums.KindSpot)
	for _, inst := range []*model.Instrument{perp, spot} {
		book, err := adapter.Market.OrderBook(ctx, inst.ID, 5)
		if err != nil {
			testenv.SkipIfTransientLiveNetworkError(t, err, "Lighter Testnet order book")
			t.Fatalf("order book %s: %v", inst.ID, err)
		}
		if len(book.Bids) == 0 || len(book.Asks) == 0 {
			t.Fatalf("empty Lighter Testnet book for %s", inst.ID)
		}
	}
	state, err := adapter.Account.(*accountClient).AccountState(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Lighter Testnet account state")
		t.Fatalf("account state: %v", err)
	}
	if state.AccountID != AccountIDDefault {
		t.Fatalf("account id=%q, want %q", state.AccountID, AccountIDDefault)
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("account state should validate: %v", err)
	}
	if !state.Reported || state.EventID == "" || state.TsEvent.IsZero() || state.TsInit.IsZero() {
		t.Fatalf("account state envelope incomplete: %+v", state)
	}
	if len(state.Balances) == 0 {
		t.Fatalf("account state has no balances")
	}
}

func TestLighterTestnetPerpWriteAcceptance(t *testing.T) {
	runLighterTestnetWriteAcceptance(t, enums.KindPerp, "Perp")
}

func TestLighterTestnetSpotWriteAcceptance(t *testing.T) {
	runLighterTestnetWriteAcceptance(t, enums.KindSpot, "Spot")
}

func runLighterTestnetWriteAcceptance(t *testing.T, kind enums.InstrumentKind, label string) {
	t.Helper()
	cfg := testenv.RequireLighterTestnetWrite(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	adapter := newLighterTestnetAdapter(t, ctx, cfg, true, 45*time.Second, label)
	defer adapter.Close()

	inst := selectLighterTestnetInstrument(t, adapter, symbolForLighterKind(cfg, kind), kind)
	ensureNoLighterTestnetOpenOrders(t, ctx, adapter, inst.ID, label)
	ensureNoLighterTestnetPositions(t, ctx, adapter, label)

	book, err := adapter.Market.OrderBook(ctx, inst.ID, 5)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Lighter "+label+" Testnet order book")
		t.Fatalf("order book: %v", err)
	}
	if len(book.Asks) == 0 {
		t.Fatalf("empty Lighter %s Testnet asks for %s", label, inst.VenueSymbol)
	}
	price := lighterRestingBuyPrice(inst, book)
	qty, err := selectLighterTestnetQuantity(inst, cfg.MaxNotionalUSDC, price)
	if err != nil {
		t.Fatalf("select Lighter %s Testnet quantity: %v", label, err)
	}
	ensureLighterTestnetCollateral(t, ctx, adapter, label, qty, price)

	exposureCleaner := newLighterAcceptanceExposureCleaner(adapter.Execution, adapter.acct, adapter.Market)
	exposureBaseline, err := exposureCleaner.CaptureBaseline(ctx, inst)
	if err != nil {
		t.Fatalf("capture Lighter %s Testnet exposure baseline: %v", label, err)
	}
	clientID := newLighterAcceptanceClientID(label)
	cleanup := newLighterRestingOrderCleanup(adapter.Execution, inst.ID, AccountIDDefault, clientID, qty)
	defer func() {
		if !cleanup.NeedsCleanup() && !cleanup.NeedsExposureCleanup() {
			return
		}
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), lighterAcceptanceDeferredCleanupTimeout)
		defer cleanupCancel()
		if cleanupErr := cleanup.CancelConfirmAndRecover(cleanupCtx, exposureCleaner, inst, exposureBaseline); cleanupErr != nil {
			t.Errorf("deferred Lighter %s Testnet cleanup: %v", label, cleanupErr)
		}
	}()
	order, err := adapter.Execution.Submit(ctx, model.OrderRequest{
		AccountID:    AccountIDDefault,
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
		t.Fatalf("observe Lighter %s Testnet resting submit: %v", label, cleanupErr)
	}
	if err != nil {
		t.Fatalf("submit Lighter %s Testnet resting order: %v", label, err)
	}
	if order.Status == enums.StatusFilled || !order.FilledQty.IsZero() {
		t.Fatalf("resting place/cancel order unexpectedly filled: %+v", order)
	}
	if err := adapter.Execution.Cancel(ctx, inst.ID, order.VenueOrderID); err != nil {
		t.Fatalf("cancel Lighter %s Testnet order %s: %v", label, order.VenueOrderID, err)
	}
	if err := cleanup.CancelAndConfirm(ctx); err != nil {
		t.Fatalf("cancel and confirm Lighter %s Testnet order %s: %v", label, order.VenueOrderID, err)
	}
	if err := waitForNoLighterTestnetOpenOrders(ctx, adapter, inst.ID); err != nil {
		t.Fatalf("wait for no Lighter %s Testnet open orders: %v", label, err)
	}
}

func newLighterTestnetAdapter(t *testing.T, ctx context.Context, cfg testenv.LighterTestnetConfig, withCredentials bool, timeout time.Duration, label string) *Adapter {
	t.Helper()
	httpClient, err := testenv.LighterTestnetHTTPClient(timeout)
	if err != nil {
		t.Fatalf("Lighter Testnet HTTP client: %v", err)
	}
	privateKey := ""
	if withCredentials {
		privateKey = cfg.PrivateKey
	}
	adapter, err := New(ctx, Config{
		PrivateKey:   privateKey,
		AccountIndex: cfg.AccountIndex,
		APIKeyIndex:  cfg.APIKeyIndex,
		Environment:  sdk.EnvironmentTestnet,
		HTTPClient:   httpClient,
	})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Lighter "+label+" Testnet adapter initialization")
		t.Fatalf("new Lighter %s Testnet adapter: %v", label, err)
	}
	return adapter
}

func selectLighterTestnetInstrument(t *testing.T, adapter *Adapter, desired string, kind enums.InstrumentKind) *model.Instrument {
	t.Helper()
	all := adapter.Market.InstrumentProvider().All()
	if len(all) == 0 {
		t.Skip("Lighter Testnet returned no instruments")
	}
	for _, inst := range all {
		if inst.ID.Kind != kind {
			continue
		}
		if desired == "" || matchesLighterSymbol(inst, desired) {
			return inst
		}
	}
	t.Fatalf("configured Lighter Testnet %s symbol %q not loaded", kind, desired)
	return nil
}

func matchesLighterSymbol(inst *model.Instrument, desired string) bool {
	if inst == nil {
		return false
	}
	desired = strings.TrimSpace(desired)
	if desired == "" {
		return false
	}
	if strings.EqualFold(inst.VenueSymbol, desired) || strings.EqualFold(inst.ID.Symbol, desired) {
		return true
	}
	neutral := strings.ReplaceAll(desired, "/", "-")
	return strings.EqualFold(inst.ID.Symbol, neutral)
}

func symbolForLighterKind(cfg testenv.LighterTestnetConfig, kind enums.InstrumentKind) string {
	if kind == enums.KindSpot {
		return cfg.SpotSymbol
	}
	return cfg.PerpSymbol
}

func selectLighterTestnetQuantity(inst *model.Instrument, maxNotional, price decimal.Decimal) (decimal.Decimal, error) {
	if inst == nil {
		return decimal.Zero, fmt.Errorf("instrument is required")
	}
	if !maxNotional.IsPositive() {
		return decimal.Zero, fmt.Errorf("max notional must be positive, got %s", maxNotional)
	}
	if !price.IsPositive() {
		return decimal.Zero, fmt.Errorf("price must be positive, got %s", price)
	}
	step := inst.SizeStep
	if !step.IsPositive() {
		step = decimal.RequireFromString("0.0001")
	}
	qty := maxNotional.Div(decimal.NewFromInt(4)).Div(price)
	minQty := inst.MinQty
	if !minQty.IsPositive() {
		minQty = step
	}
	if inst.MinNotional.IsPositive() {
		minByNotional := inst.MinNotional.Div(price)
		if minByNotional.GreaterThan(minQty) {
			minQty = minByNotional
		}
	}
	if minQty.GreaterThan(qty) {
		qty = minQty
	}
	qty = qty.Div(step).Ceil().Mul(step)
	if notional := qty.Mul(price); notional.GreaterThan(maxNotional) {
		return decimal.Zero, fmt.Errorf("minimum tradable notional %s exceeds max notional %s", notional, maxNotional)
	}
	return qty, nil
}

func lighterRestingBuyPrice(inst *model.Instrument, book *model.OrderBook) decimal.Decimal {
	tick := inst.PriceTick
	if !tick.IsPositive() {
		tick = decimal.RequireFromString("0.01")
	}
	price := tick
	if book != nil && len(book.Bids) > 0 {
		price = book.Bids[0].Price.Mul(decimal.RequireFromString("0.99"))
	} else if book != nil && len(book.Asks) > 0 {
		price = book.Asks[0].Price.Mul(decimal.RequireFromString("0.98"))
	}
	if !price.IsPositive() {
		price = tick
	}
	aligned := price.Div(tick).Floor().Mul(tick)
	if !aligned.IsPositive() && book != nil && len(book.Bids) > 0 {
		aligned = book.Bids[0].Price.Div(tick).Floor().Mul(tick)
	}
	if book != nil && len(book.Asks) > 0 && !aligned.LessThan(book.Asks[0].Price) {
		aligned = book.Asks[0].Price.Sub(tick).Div(tick).Floor().Mul(tick)
	}
	if !aligned.IsPositive() {
		return decimal.Zero
	}
	return aligned
}

func ensureNoLighterTestnetOpenOrders(t *testing.T, ctx context.Context, adapter *Adapter, id model.InstrumentID, label string) {
	t.Helper()
	if open, err := adapter.Execution.OpenOrders(ctx, id); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Lighter "+label+" Testnet open order preflight")
		t.Fatalf("open order preflight: %v", err)
	} else if len(open) > 0 {
		t.Skipf("skipping Lighter %s Testnet acceptance: %s already has %d open order(s); clean the testnet account first", label, id, len(open))
	}
}

func ensureNoLighterTestnetPositions(t *testing.T, ctx context.Context, adapter *Adapter, label string) {
	t.Helper()
	positions, err := adapter.Account.Positions(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Lighter "+label+" Testnet position preflight")
		t.Fatalf("position preflight: %v", err)
	}
	if len(positions) > 0 {
		t.Skipf("skipping Lighter %s Testnet acceptance: account already has %d open position(s); clean the testnet account first", label, len(positions))
	}
}

func ensureLighterTestnetCollateral(t *testing.T, ctx context.Context, adapter *Adapter, label string, qty, price decimal.Decimal) {
	t.Helper()
	state, err := adapter.Account.(*accountClient).AccountState(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Lighter "+label+" Testnet balances")
		t.Fatalf("account state: %v", err)
	}
	required := qty.Mul(price)
	for _, balance := range state.Balances {
		if balance.Currency == "USDC" {
			if balance.Free.LessThan(required) {
				t.Skipf("skipping Lighter %s Testnet acceptance: available USDC %s below required notional %s", label, balance.Free, required)
			}
			return
		}
	}
	t.Skipf("skipping Lighter %s Testnet acceptance: no USDC balance found", label)
}

func waitForNoLighterTestnetOpenOrders(ctx context.Context, adapter *Adapter, id model.InstrumentID) error {
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

func scopeContains(scope []enums.InstrumentKind, kind enums.InstrumentKind) bool {
	for _, got := range scope {
		if got == kind {
			return true
		}
	}
	return false
}
