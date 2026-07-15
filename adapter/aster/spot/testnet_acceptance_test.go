package spot

import (
	"context"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
	"github.com/shopspring/decimal"
)

func TestAsterSpotTestnetReadAcceptance(t *testing.T) {
	cfg := testenv.RequireAsterTestnetRead(t)
	security := requireAsterSpotAcceptanceSecurity(t, cfg)
	profile := requireAsterSpotTestnetProfile(t, cfg.SpotProfile)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	adapter := newAsterSpotTestnetAdapter(t, ctx, profile, security, cfg.ProxyURL, "read")
	defer adapter.Close()

	inst := requireAsterSpotAcceptanceInstrument(t, adapter, cfg.SpotSymbol)
	book := requireAsterSpotAcceptanceBook(t, ctx, adapter, inst.ID, "Aster Spot Testnet read")
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("Aster Spot Testnet read empty book for %s", inst.ID)
	}
	state := requireAsterSpotAcceptanceAccountState(t, ctx, adapter, "Aster Spot Testnet read")
	if state.Type != model.AccountCash {
		t.Fatalf("Aster Spot Testnet read account type=%s, want %s", state.Type, model.AccountCash)
	}
	t.Logf("Aster Spot Testnet read evidence profile=%s rest=%s chain_id=%d instrument=%s balances=%d", profile, cfg.SpotProfile.RESTURL, cfg.SpotProfile.ChainID, inst.ID, len(state.Balances))
}

func TestAsterSpotTestnetAdapterAcceptance(t *testing.T) {
	cfg := testenv.RequireAsterTestnetWrite(t)
	security := requireAsterSpotAcceptanceSecurity(t, cfg)
	profile := requireAsterSpotTestnetProfile(t, cfg.SpotProfile)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	adapter := newAsterSpotTestnetAdapter(t, ctx, profile, security, cfg.ProxyURL, "adapter")
	defer adapter.Close()

	inst := requireAsterSpotAcceptanceInstrument(t, adapter, cfg.SpotSymbol)
	ensureNoAsterSpotTestnetOpenOrders(t, ctx, adapter, inst.ID, "Aster Spot Testnet adapter")
	book := requireAsterSpotAcceptanceBook(t, ctx, adapter, inst.ID, "Aster Spot Testnet adapter")
	lifecycle := asterSpotAcceptanceLifecycleSpec(t, adapter, "Aster Spot Testnet adapter", inst, book, cfg.MaxNotionalUSDT)
	state := requireAsterSpotAcceptanceAccountState(t, ctx, adapter, "Aster Spot Testnet adapter")
	ensureAsterSpotAcceptanceFunds(t, "Aster Spot Testnet adapter", state, lifecycle)
	initialBaseTotal := requireAsterSpotAcceptanceBalanceTotal(t, "Aster Spot Testnet adapter preflight", state, inst.Base)

	if err := adapter.Start(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Aster Spot Testnet private stream")
		t.Fatalf("Aster Spot Testnet private stream: %v", err)
	}
	lifecycle.PrivateStreamTopics = asterSpotPrivateStreamTopics()
	t.Logf("Aster Spot Testnet adapter private_stream_topics=%s", strings.Join(lifecycle.PrivateStreamTopics, ","))
	result, err := runtimeaccept.RunAdapterOrderLifecycle(ctx, adapter.Execution, lifecycle)
	if err != nil {
		t.Fatalf("Aster Spot Testnet adapter order lifecycle: %v", err)
	}
	evidenceCtx, evidenceCancel := context.WithTimeout(ctx, 30*time.Second)
	defer evidenceCancel()
	if _, err := runtimeaccept.WaitForPrivateExecutionEvidence(evidenceCtx, adapter.Execution.Events(), inst.ID, AccountIDDefault); err != nil {
		t.Fatalf("Aster Spot Testnet private execution evidence: %v", err)
	}
	finalState := requireAsterSpotAcceptanceAccountState(t, ctx, adapter, "Aster Spot Testnet adapter final")
	assertAsterSpotAcceptanceBalanceDelta(t, "Aster Spot Testnet adapter", inst, initialBaseTotal, finalState, result)
}

func TestAsterSpotTestnetRuntimeAcceptance(t *testing.T) {
	cfg := testenv.RequireAsterTestnetWrite(t)
	security := requireAsterSpotAcceptanceSecurity(t, cfg)
	profile := requireAsterSpotTestnetProfile(t, cfg.SpotProfile)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	adapter := newAsterSpotTestnetAdapter(t, ctx, profile, security, cfg.ProxyURL, "runtime")
	defer adapter.Close()

	inst := requireAsterSpotAcceptanceInstrument(t, adapter, cfg.SpotSymbol)
	ensureNoAsterSpotTestnetOpenOrders(t, ctx, adapter, inst.ID, "Aster Spot Testnet runtime")
	book := requireAsterSpotAcceptanceBook(t, ctx, adapter, inst.ID, "Aster Spot Testnet runtime")
	lifecycle := asterSpotAcceptanceLifecycleSpec(t, adapter, "Aster Spot Testnet runtime", inst, book, cfg.MaxNotionalUSDT)

	node := btruntime.NewNode(
		btruntime.Clients{Market: adapter.Market, Execution: adapter.Execution, Account: adapter.Account},
		clock.NewRealClock(),
		AccountIDDefault,
		btruntime.WithAccountID(AccountIDDefault),
	)
	runtimeaccept.AttachAccountRequiredRiskWithMaxNotional(node, adapter.Market.InstrumentProvider(), cfg.MaxNotionalUSDT)
	report, err := node.Resync(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Aster Spot Testnet initial reconcile")
		t.Fatalf("Aster Spot Testnet initial reconcile: %v", err)
	}
	if report.AccountStatesApplied != 1 {
		t.Fatalf("Aster Spot Testnet account states applied=%d, want 1: %+v", report.AccountStatesApplied, report)
	}
	runtimeaccept.AssertAccountStateReady(t, node, AccountIDDefault, model.AccountCash, enums.KindSpot)
	initialBaseTotal := requireAsterSpotRuntimeBalanceTotal(t, "Aster Spot Testnet runtime preflight", node, inst.Base)
	if acct, ok := node.Cache.Account(AccountIDDefault); ok {
		ensureAsterSpotAcceptanceFunds(t, "Aster Spot Testnet runtime", acct.LastEvent(), lifecycle)
	}

	if err := adapter.Start(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Aster Spot Testnet private stream")
		t.Fatalf("Aster Spot Testnet private stream: %v", err)
	}
	lifecycle.PrivateStreamTopics = asterSpotPrivateStreamTopics()
	lifecycle.BeforeRuntimeClose = func(closeCtx context.Context, closeQty decimal.Decimal) error {
		return waitForAsterSpotRuntimeCloseReady(closeCtx, node, inst.Base, closeQty, t.Logf)
	}
	t.Logf("Aster Spot Testnet runtime private_stream_topics=%s", strings.Join(lifecycle.PrivateStreamTopics, ","))
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		node.Run(runCtx)
	}()
	defer stopAsterSpotRuntimeNode(t, stop, done)
	if err := runtimeaccept.WaitForActive(ctx, node); err != nil {
		t.Fatalf("Aster Spot Testnet runtime active before lifecycle: %v", err)
	}

	result, err := runtimeaccept.RunRuntimeOrderLifecycle(ctx, node, adapter.Execution, lifecycle)
	if err != nil {
		t.Fatalf("Aster Spot Testnet runtime order lifecycle: %v", err)
	}
	finalReport, err := node.Resync(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Aster Spot Testnet final reconcile")
		t.Fatalf("Aster Spot Testnet final reconcile: %v", err)
	}
	if finalReport.AccountStatesApplied != 1 {
		t.Fatalf("Aster Spot Testnet final account states applied=%d, want 1: %+v", finalReport.AccountStatesApplied, finalReport)
	}
	runtimeaccept.AssertAccountStateReady(t, node, AccountIDDefault, model.AccountCash, enums.KindSpot)
	finalState, ok := node.Cache.Account(AccountIDDefault)
	if !ok {
		t.Fatalf("Aster Spot Testnet runtime final account missing")
	}
	assertAsterSpotAcceptanceBalanceDelta(t, "Aster Spot Testnet runtime", inst, initialBaseTotal, finalState.LastEvent(), result)
}

func waitForAsterSpotRuntimeCloseReady(ctx context.Context, node *btruntime.TradingNode, currency string, closeQty decimal.Decimal, logf func(string, ...any)) error {
	streamCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		if acct, ok := node.Cache.Account(AccountIDDefault); ok {
			if free, found := acct.BalanceFree(currency); found && free.GreaterThanOrEqual(closeQty) {
				logf("runtime_close_ready source=account_stream currency=%s free=%s required=%s", currency, free, closeQty)
				return nil
			}
		}
		select {
		case <-streamCtx.Done():
			goto resync
		case <-ticker.C:
		}
	}

resync:
	report, err := node.Resync(ctx)
	if err != nil {
		return fmt.Errorf("account stream lagged and resync failed: %w", err)
	}
	if report.AccountStatesApplied != 1 {
		return fmt.Errorf("account stream lagged and resync applied %d account states", report.AccountStatesApplied)
	}
	acct, ok := node.Cache.Account(AccountIDDefault)
	if !ok {
		return fmt.Errorf("account missing after resync")
	}
	free, ok := acct.BalanceFree(currency)
	if !ok || free.LessThan(closeQty) {
		return fmt.Errorf("insufficient %s after resync: free=%s required=%s", currency, free, closeQty)
	}
	logf("runtime_close_ready source=resync currency=%s free=%s required=%s", currency, free, closeQty)
	return nil
}

func newAsterSpotTestnetAdapter(t *testing.T, ctx context.Context, profile astercommon.Profile, security *astercommon.SecurityContext, proxyURL, label string) *Adapter {
	t.Helper()
	adapter, err := New(ctx, Config{
		Profile:    profile,
		Security:   security,
		AccountID:  AccountIDDefault,
		HTTPClient: asterSpotAcceptanceHTTPClient(t, proxyURL),
	})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Aster Spot Testnet "+label+" adapter initialization")
		t.Fatalf("new Aster Spot Testnet %s adapter: %v", label, err)
	}
	return adapter
}

func requireAsterSpotAcceptanceSecurity(t *testing.T, cfg testenv.AsterTestnetConfig) *astercommon.SecurityContext {
	t.Helper()
	if strings.TrimSpace(cfg.UserAddress) == "" || strings.TrimSpace(cfg.SignerPrivateKey) == "" {
		t.Skip("skipping Aster Testnet acceptance: ASTER_TESTNET_USER_ADDRESS and ASTER_TESTNET_SIGNER_PRIVATE_KEY are required")
	}
	security, err := astercommon.NewSecurityContext(astercommon.CredentialConfig{
		User:           cfg.UserAddress,
		PrivateKey:     cfg.SignerPrivateKey,
		ExpectedSigner: cfg.ExpectedSignerAddress,
	})
	if err != nil {
		t.Fatalf("Aster Testnet security context: %v", err)
	}
	return security
}

func requireAsterSpotTestnetProfile(t *testing.T, endpoints testenv.AsterEndpointProfile) astercommon.Profile {
	t.Helper()
	profile, err := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductSpot)
	if err != nil {
		t.Fatalf("Aster Spot Testnet profile: %v", err)
	}
	if profile.Environment() != astercommon.EnvironmentTestnet || profile.Product() != astercommon.ProductSpot {
		t.Fatalf("Aster Spot acceptance profile=%s, want official testnet/spot", profile)
	}
	if err = profile.Validate(); err != nil {
		t.Fatalf("Aster Spot acceptance profile is not official Testnet: %v", err)
	}
	if endpoints.RESTURL != profile.RESTURL() || endpoints.PublicWSURL != profile.PublicWSURL() || endpoints.UserWSURL != profile.UserWSURL() || endpoints.ChainID != profile.ChainID() {
		t.Fatalf("Aster Spot testenv endpoints do not match SDK official Testnet profile: endpoints=%+v sdk={rest:%s public_ws:%s user_ws:%s chain:%d}", endpoints, profile.RESTURL(), profile.PublicWSURL(), profile.UserWSURL(), profile.ChainID())
	}
	return profile
}

func asterSpotAcceptanceHTTPClient(t *testing.T, proxyURL string) *http.Client {
	t.Helper()
	client := &http.Client{Timeout: 45 * time.Second}
	if strings.TrimSpace(proxyURL) == "" {
		return client
	}
	parsed, err := parseAsterSpotAcceptanceProxyURL(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	client.Transport = &http.Transport{Proxy: http.ProxyURL(parsed)}
	return client
}

func parseAsterSpotAcceptanceProxyURL(proxyURL string) (*url.URL, error) {
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("Aster Testnet proxy URL is invalid")
	}
	return parsed, nil
}

func requireAsterSpotAcceptanceInstrument(t *testing.T, adapter *Adapter, desired string) *model.Instrument {
	t.Helper()
	inst, err := selectAsterSpotAcceptanceInstrument(adapter.provider, desired)
	if err != nil {
		t.Fatal(err)
	}
	return inst
}

func selectAsterSpotAcceptanceInstrument(provider *instrumentProvider, desired string) (*model.Instrument, error) {
	if provider == nil {
		return nil, fmt.Errorf("Aster Spot Testnet instrument provider is nil")
	}
	desired = strings.TrimSpace(desired)
	if isAsterSpotTestSymbol(desired) {
		return nil, fmt.Errorf("Aster Spot Testnet symbol %q is rejected because TEST* symbols are non-tradable fixtures", desired)
	}
	for _, inst := range provider.All() {
		if inst == nil || isAsterSpotTestSymbol(inst.VenueSymbol) || isAsterSpotTestSymbol(inst.ID.Symbol) {
			continue
		}
		if desired == "" || asterSpotSymbolMatches(inst, desired) {
			return inst, nil
		}
	}
	if desired == "" {
		return nil, fmt.Errorf("Aster Spot Testnet returned no supported non-TEST instruments")
	}
	return nil, fmt.Errorf("Aster Spot Testnet symbol %q was not loaded", desired)
}

func asterSpotSymbolMatches(inst *model.Instrument, desired string) bool {
	if inst == nil {
		return false
	}
	desired = strings.TrimSpace(desired)
	neutral := strings.ReplaceAll(desired, "/", "-")
	return strings.EqualFold(inst.VenueSymbol, desired) ||
		strings.EqualFold(inst.ID.Symbol, desired) ||
		strings.EqualFold(inst.ID.Symbol, neutral)
}

func isAsterSpotTestSymbol(symbol string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(symbol)), "TEST")
}

func requireAsterSpotAcceptanceBook(t *testing.T, ctx context.Context, adapter *Adapter, id model.InstrumentID, label string) *model.OrderBook {
	t.Helper()
	book, err := adapter.Market.OrderBook(ctx, id, 5)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label+" order book")
		t.Fatalf("%s order book: %v", label, err)
	}
	if book == nil || len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("%s empty order book for %s: %+v", label, id, book)
	}
	return book
}

func requireAsterSpotAcceptanceAccountState(t *testing.T, ctx context.Context, adapter *Adapter, label string) model.AccountState {
	t.Helper()
	state, err := adapter.acct.AccountState(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label+" account state")
		t.Fatalf("%s account state: %v", label, err)
	}
	if state.AccountID != AccountIDDefault {
		t.Fatalf("%s account id=%q, want %q", label, state.AccountID, AccountIDDefault)
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("%s account state invalid: %v", label, err)
	}
	if !state.Reported || state.EventID == "" || state.TsEvent.IsZero() || state.TsInit.IsZero() {
		t.Fatalf("%s account state envelope incomplete: %+v", label, state)
	}
	if len(state.Balances) == 0 {
		t.Fatalf("%s account state has no balances", label)
	}
	return state
}

func ensureNoAsterSpotTestnetOpenOrders(t *testing.T, ctx context.Context, adapter *Adapter, id model.InstrumentID, label string) {
	t.Helper()
	open, err := adapter.Execution.OpenOrders(ctx, id)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label+" open-order preflight")
		t.Fatalf("%s open-order preflight: %v", label, err)
	}
	if len(open) > 0 {
		t.Skipf("skipping %s acceptance: %s already has %d open order(s); clean the testnet account first", label, id, len(open))
	}
}

func asterSpotAcceptanceLifecycleSpec(t *testing.T, adapter *Adapter, label string, inst *model.Instrument, book *model.OrderBook, maxNotional decimal.Decimal) runtimeaccept.OrderLifecycleSpec {
	t.Helper()
	if inst == nil {
		t.Fatalf("%s instrument is required", label)
	}
	restingPrice := floorAsterSpotAcceptanceDecimal(book.Bids[0].Price.Mul(decimal.RequireFromString("0.99")), inst.PriceTick)
	fillPrice := ceilAsterSpotAcceptanceDecimal(book.Asks[0].Price.Mul(decimal.RequireFromString("1.01")), inst.PriceTick)
	closePrice := floorAsterSpotAcceptanceDecimal(book.Bids[0].Price.Mul(decimal.RequireFromString("0.99")), inst.PriceTick)
	qty := asterSpotAcceptanceQuantity(t, label, inst, maxNotional, minAsterSpotPositiveDecimal(restingPrice, fillPrice, closePrice), maxAsterSpotPositiveDecimal(restingPrice, fillPrice, closePrice))
	closeQty := asterSpotAcceptanceCloseQuantity(t, label, inst, qty)
	spec := runtimeaccept.OrderLifecycleSpec{
		Label:          label,
		Venue:          VenueName,
		Environment:    string(astercommon.EnvironmentTestnet),
		Product:        "Spot cash",
		AccountID:      AccountIDDefault,
		InstrumentID:   inst.ID,
		Quantity:       qty,
		CloseQuantity:  closeQty,
		RestingPrice:   restingPrice,
		FillPrice:      fillPrice,
		ClosePrice:     closePrice,
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		Logf:           t.Logf,
	}
	step := inst.SizeStep
	if !step.IsPositive() {
		step = decimal.NewFromInt(1)
	}
	minQty := inst.MinQty
	if !minQty.IsPositive() {
		minQty = step
	}
	if adapter == nil {
		return runtimeaccept.ConfigureSpotBalanceGuard(spec, nil, inst.Base, step, minQty, inst.MinNotional, qty.Sub(closeQty))
	}
	return runtimeaccept.ConfigureSpotBalanceGuard(spec, adapter.acct, inst.Base, step, minQty, inst.MinNotional, qty.Sub(closeQty))
}

func asterSpotAcceptanceQuantity(t *testing.T, label string, inst *model.Instrument, maxNotional, minNotionalPrice, maxNotionalPrice decimal.Decimal) decimal.Decimal {
	t.Helper()
	qty, err := selectAsterSpotAcceptanceQuantity(inst, maxNotional, minNotionalPrice, maxNotionalPrice)
	if err != nil {
		t.Fatalf("%s quantity selection: %v", label, err)
	}
	return qty
}

func selectAsterSpotAcceptanceQuantity(inst *model.Instrument, maxNotional, minNotionalPrice, maxNotionalPrice decimal.Decimal) (decimal.Decimal, error) {
	if inst == nil {
		return decimal.Zero, fmt.Errorf("instrument is required")
	}
	if !maxNotional.IsPositive() {
		return decimal.Zero, fmt.Errorf("max notional must be positive, got %s", maxNotional)
	}
	step := inst.SizeStep
	if !step.IsPositive() {
		step = decimal.NewFromInt(1)
	}
	qty := inst.MinQty
	if !qty.IsPositive() {
		qty = step
	}
	minBufferedQty := qty.Div(asterSpotAcceptanceCloseBuffer())
	if minBufferedQty.GreaterThan(qty) {
		qty = minBufferedQty
	}
	if inst.MinNotional.IsPositive() && minNotionalPrice.IsPositive() {
		minByNotional := inst.MinNotional.Div(minNotionalPrice)
		if minByNotional.GreaterThan(qty) {
			qty = minByNotional
		}
		minCloseQty := ceilAsterSpotAcceptanceDecimal(minByNotional, step)
		minBufferedQty := minCloseQty.Div(asterSpotAcceptanceCloseBuffer())
		if minBufferedQty.GreaterThan(qty) {
			qty = minBufferedQty
		}
	}
	qty = ceilAsterSpotAcceptanceDecimal(qty, step)
	if maxNotionalPrice.IsPositive() && qty.Mul(maxNotionalPrice).GreaterThan(maxNotional) {
		return decimal.Zero, fmt.Errorf("minimum tradable notional %s exceeds max notional %s", qty.Mul(maxNotionalPrice), maxNotional)
	}
	return qty, nil
}

func asterSpotAcceptanceCloseQuantity(t *testing.T, label string, inst *model.Instrument, qty decimal.Decimal) decimal.Decimal {
	t.Helper()
	step := inst.SizeStep
	if !step.IsPositive() {
		step = decimal.NewFromInt(1)
	}
	minQty := inst.MinQty
	if !minQty.IsPositive() {
		minQty = step
	}
	closeQty := floorAsterSpotAcceptanceDecimal(qty.Mul(asterSpotAcceptanceCloseBuffer()), step)
	if closeQty.LessThan(minQty) {
		t.Skipf("skipping %s: spot close quantity %s is below min quantity %s after fee buffer", label, closeQty, minQty)
	}
	return closeQty
}

func asterSpotAcceptanceCloseBuffer() decimal.Decimal {
	return decimal.RequireFromString("0.995")
}

func ensureAsterSpotAcceptanceFunds(t *testing.T, label string, state model.AccountState, lifecycle runtimeaccept.OrderLifecycleSpec) {
	t.Helper()
	required := lifecycle.Quantity.Mul(lifecycle.FillPrice)
	for _, balance := range state.Balances {
		if strings.EqualFold(balance.Currency, "USDT") {
			if balance.Free.LessThan(required) {
				t.Skipf("skipping %s acceptance: available USDT %s below required notional %s", label, balance.Free, required)
			}
			return
		}
	}
	t.Skipf("skipping %s acceptance: no USDT balance found", label)
}

func requireAsterSpotAcceptanceBalanceTotal(t *testing.T, label string, state model.AccountState, currency string) decimal.Decimal {
	t.Helper()
	for _, balance := range state.Balances {
		if strings.EqualFold(balance.Currency, currency) {
			return balance.Total
		}
	}
	t.Fatalf("%s account state has no %s balance", label, currency)
	return decimal.Zero
}

func requireAsterSpotRuntimeBalanceTotal(t *testing.T, label string, node *btruntime.TradingNode, currency string) decimal.Decimal {
	t.Helper()
	acct, ok := node.Cache.Account(AccountIDDefault)
	if !ok {
		t.Fatalf("%s account missing", label)
	}
	total, ok := acct.BalanceTotal(currency)
	if !ok {
		t.Fatalf("%s account has no %s balance", label, currency)
	}
	return total
}

func assertAsterSpotAcceptanceBalanceDelta(t *testing.T, label string, inst *model.Instrument, initialTotal decimal.Decimal, finalState model.AccountState, result *runtimeaccept.OrderLifecycleResult) {
	t.Helper()
	if result == nil || !result.FilledQty.IsPositive() || !result.ClosedQty.IsPositive() {
		t.Fatalf("%s missing Spot fill/close evidence: %+v", label, result)
	}
	if result.ClosedQty.GreaterThan(result.FilledQty) {
		t.Fatalf("%s closed qty %s exceeds test-created qty %s", label, result.ClosedQty, result.FilledQty)
	}
	finalTotal := requireAsterSpotAcceptanceBalanceTotal(t, label+" final", finalState, inst.Base)
	delta := finalTotal.Sub(initialTotal)
	step := inst.SizeStep
	if !step.IsPositive() {
		step = decimal.NewFromInt(1)
	}
	maxResidual := result.FilledQty.Sub(result.ClosedQty).Add(step)
	if delta.LessThan(step.Neg()) {
		t.Fatalf("%s sold pre-existing %s: initial=%s final=%s delta=%s tolerance=%s", label, inst.Base, initialTotal, finalTotal, delta, step)
	}
	if delta.GreaterThan(maxResidual) {
		t.Fatalf("%s left uncleaned test-created %s: initial=%s final=%s delta=%s max_residual=%s", label, inst.Base, initialTotal, finalTotal, delta, maxResidual)
	}
	if asterSpotAcceptanceResidualSellable(inst, delta, result.Closed.Request.Price) {
		t.Fatalf("%s left a sellable test-created %s delta: initial=%s final=%s delta=%s close_price=%s", label, inst.Base, initialTotal, finalTotal, delta, result.Closed.Request.Price)
	}
	t.Logf("spot_balance_cleanup label=%q currency=%s initial_total=%s bought_qty=%s sold_qty=%s final_total=%s residual_delta=%s max_residual=%s", label, inst.Base, initialTotal, result.FilledQty, result.ClosedQty, finalTotal, delta, maxResidual)
}

func asterSpotAcceptanceResidualSellable(inst *model.Instrument, delta, price decimal.Decimal) bool {
	if inst == nil || !delta.IsPositive() || !price.IsPositive() {
		return false
	}
	step := inst.SizeStep
	if !step.IsPositive() {
		step = decimal.NewFromInt(1)
	}
	qty := delta.Div(step).Floor().Mul(step)
	minQty := inst.MinQty
	if !minQty.IsPositive() {
		minQty = step
	}
	if qty.LessThan(minQty) {
		return false
	}
	return !inst.MinNotional.IsPositive() || qty.Mul(price).GreaterThanOrEqual(inst.MinNotional)
}

func asterSpotPrivateStreamTopics() []string {
	return []string{"executionReport", "outboundAccountPosition"}
}

func stopAsterSpotRuntimeNode(t *testing.T, stop context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Aster Spot Testnet runtime node did not stop")
	}
}

func ceilAsterSpotAcceptanceDecimal(value, step decimal.Decimal) decimal.Decimal {
	if !step.IsPositive() {
		return value
	}
	return value.Div(step).Ceil().Mul(step)
}

func floorAsterSpotAcceptanceDecimal(value, step decimal.Decimal) decimal.Decimal {
	if !step.IsPositive() {
		return value
	}
	out := value.Div(step).Floor().Mul(step)
	if out.IsPositive() {
		return out
	}
	return step
}

func minAsterSpotPositiveDecimal(values ...decimal.Decimal) decimal.Decimal {
	var out decimal.Decimal
	for _, value := range values {
		if !value.IsPositive() {
			continue
		}
		if out.IsZero() || value.LessThan(out) {
			out = value
		}
	}
	return out
}

func maxAsterSpotPositiveDecimal(values ...decimal.Decimal) decimal.Decimal {
	var out decimal.Decimal
	for _, value := range values {
		if value.IsPositive() && value.GreaterThan(out) {
			out = value
		}
	}
	return out
}
