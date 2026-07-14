package perp

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

func TestAsterPerpTestnetReadAcceptance(t *testing.T) {
	cfg := testenv.RequireAsterTestnetRead(t)
	security := requireAsterPerpAcceptanceSecurity(t, cfg)
	profile := requireAsterPerpTestnetProfile(t, cfg.PerpProfile)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	adapter := newAsterPerpTestnetAdapter(t, ctx, profile, security, cfg.ProxyURL, "read")
	defer adapter.Close()

	inst := requireAsterPerpAcceptanceInstrument(t, adapter, cfg.PerpSymbol)
	book := requireAsterPerpAcceptanceBook(t, ctx, adapter, inst.ID, "Aster Perp Testnet read")
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("Aster Perp Testnet read empty book for %s", inst.ID)
	}
	state := requireAsterPerpAcceptanceAccountState(t, ctx, adapter, "Aster Perp Testnet read")
	if state.Type != model.AccountMargin {
		t.Fatalf("Aster Perp Testnet read account type=%s, want %s", state.Type, model.AccountMargin)
	}
	t.Logf("Aster Perp Testnet read evidence profile=%s rest=%s chain_id=%d instrument=%s balances=%d margins=%d", profile, cfg.PerpProfile.RESTURL, cfg.PerpProfile.ChainID, inst.ID, len(state.Balances), len(state.Margins))
}

func TestAsterPerpTestnetAdapterAcceptance(t *testing.T) {
	cfg := testenv.RequireAsterTestnetWrite(t)
	security := requireAsterPerpAcceptanceSecurity(t, cfg)
	profile := requireAsterPerpTestnetProfile(t, cfg.PerpProfile)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	adapter := newAsterPerpTestnetAdapter(t, ctx, profile, security, cfg.ProxyURL, "adapter")
	defer adapter.Close()

	inst := requireAsterPerpAcceptanceInstrument(t, adapter, cfg.PerpSymbol)
	ensureNoAsterPerpTestnetOpenOrders(t, ctx, adapter, inst.ID, "Aster Perp Testnet adapter")
	ensureNoAsterPerpTestnetPositions(t, ctx, adapter, "Aster Perp Testnet adapter")
	book := requireAsterPerpAcceptanceBook(t, ctx, adapter, inst.ID, "Aster Perp Testnet adapter")
	lifecycle := asterPerpAcceptanceLifecycleSpec(t, "Aster Perp Testnet adapter", inst, book, cfg.MaxNotionalUSDT)
	state := requireAsterPerpAcceptanceAccountState(t, ctx, adapter, "Aster Perp Testnet adapter")
	ensureAsterPerpAcceptanceFunds(t, "Aster Perp Testnet adapter", state, lifecycle)

	if err := adapter.Start(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Aster Perp Testnet private stream")
		t.Fatalf("Aster Perp Testnet private stream: %v", err)
	}
	lifecycle.PrivateStreamTopics = asterPerpPrivateStreamTopics()
	t.Logf("Aster Perp Testnet adapter private_stream_topics=%s", strings.Join(lifecycle.PrivateStreamTopics, ","))
	if _, err := runtimeaccept.RunAdapterOrderLifecycle(ctx, adapter.Execution, lifecycle); err != nil {
		t.Fatalf("Aster Perp Testnet adapter order lifecycle: %v", err)
	}
	evidenceCtx, evidenceCancel := context.WithTimeout(ctx, 30*time.Second)
	defer evidenceCancel()
	if _, err := runtimeaccept.WaitForPrivateExecutionEvidence(evidenceCtx, adapter.Execution.Events(), inst.ID, AccountIDDefault); err != nil {
		t.Fatalf("Aster Perp Testnet private execution evidence: %v", err)
	}
}

func TestAsterPerpTestnetRuntimeAcceptance(t *testing.T) {
	cfg := testenv.RequireAsterTestnetWrite(t)
	security := requireAsterPerpAcceptanceSecurity(t, cfg)
	profile := requireAsterPerpTestnetProfile(t, cfg.PerpProfile)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()
	adapter := newAsterPerpTestnetAdapter(t, ctx, profile, security, cfg.ProxyURL, "runtime")
	defer adapter.Close()

	inst := requireAsterPerpAcceptanceInstrument(t, adapter, cfg.PerpSymbol)
	ensureNoAsterPerpTestnetOpenOrders(t, ctx, adapter, inst.ID, "Aster Perp Testnet runtime")
	ensureNoAsterPerpTestnetPositions(t, ctx, adapter, "Aster Perp Testnet runtime")
	book := requireAsterPerpAcceptanceBook(t, ctx, adapter, inst.ID, "Aster Perp Testnet runtime")
	lifecycle := asterPerpAcceptanceLifecycleSpec(t, "Aster Perp Testnet runtime", inst, book, cfg.MaxNotionalUSDT)

	node := btruntime.NewNode(
		btruntime.Clients{Market: adapter.Market, Execution: adapter.Execution, Account: adapter.Account},
		clock.NewRealClock(),
		AccountIDDefault,
		btruntime.WithAccountID(AccountIDDefault),
	)
	runtimeaccept.AttachAccountRequiredRiskWithMaxNotional(node, adapter.Market.InstrumentProvider(), cfg.MaxNotionalUSDT)
	report, err := node.Resync(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Aster Perp Testnet initial reconcile")
		t.Fatalf("Aster Perp Testnet initial reconcile: %v", err)
	}
	if report.AccountStatesApplied != 1 {
		t.Fatalf("Aster Perp Testnet account states applied=%d, want 1: %+v", report.AccountStatesApplied, report)
	}
	runtimeaccept.AssertAccountStateReady(t, node, AccountIDDefault, model.AccountMargin, enums.KindPerp)
	if acct, ok := node.Cache.Account(AccountIDDefault); ok {
		ensureAsterPerpAcceptanceFunds(t, "Aster Perp Testnet runtime", acct.LastEvent(), lifecycle)
	}

	if err := adapter.Start(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Aster Perp Testnet private stream")
		t.Fatalf("Aster Perp Testnet private stream: %v", err)
	}
	lifecycle.PrivateStreamTopics = asterPerpPrivateStreamTopics()
	t.Logf("Aster Perp Testnet runtime private_stream_topics=%s", strings.Join(lifecycle.PrivateStreamTopics, ","))
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		defer close(done)
		node.Run(runCtx)
	}()
	defer stopAsterPerpRuntimeNode(t, stop, done)
	if err := runtimeaccept.WaitForActive(ctx, node); err != nil {
		t.Fatalf("Aster Perp Testnet runtime active before lifecycle: %v", err)
	}

	if _, err := runtimeaccept.RunRuntimeOrderLifecycle(ctx, node, adapter.Execution, lifecycle); err != nil {
		t.Fatalf("Aster Perp Testnet runtime order lifecycle: %v", err)
	}
	finalReport, err := node.Resync(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Aster Perp Testnet final reconcile")
		t.Fatalf("Aster Perp Testnet final reconcile: %v", err)
	}
	if finalReport.AccountStatesApplied != 1 {
		t.Fatalf("Aster Perp Testnet final account states applied=%d, want 1: %+v", finalReport.AccountStatesApplied, finalReport)
	}
	runtimeaccept.AssertAccountStateReady(t, node, AccountIDDefault, model.AccountMargin, enums.KindPerp)
}

func newAsterPerpTestnetAdapter(t *testing.T, ctx context.Context, profile astercommon.Profile, security *astercommon.SecurityContext, proxyURL, label string) *Adapter {
	t.Helper()
	adapter, err := New(ctx, Config{
		Profile:    profile,
		Security:   security,
		AccountID:  AccountIDDefault,
		HTTPClient: asterPerpAcceptanceHTTPClient(t, proxyURL),
	})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Aster Perp Testnet "+label+" adapter initialization")
		t.Fatalf("new Aster Perp Testnet %s adapter: %v", label, err)
	}
	return adapter
}

func requireAsterPerpAcceptanceSecurity(t *testing.T, cfg testenv.AsterTestnetConfig) *astercommon.SecurityContext {
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

func requireAsterPerpTestnetProfile(t *testing.T, endpoints testenv.AsterEndpointProfile) astercommon.Profile {
	t.Helper()
	profile, err := astercommon.NewProfile(astercommon.EnvironmentTestnet, astercommon.ProductPerp)
	if err != nil {
		t.Fatalf("Aster Perp Testnet profile: %v", err)
	}
	if profile.Environment() != astercommon.EnvironmentTestnet || profile.Product() != astercommon.ProductPerp {
		t.Fatalf("Aster Perp acceptance profile=%s, want official testnet/perp", profile)
	}
	if err = profile.Validate(); err != nil {
		t.Fatalf("Aster Perp acceptance profile is not official Testnet: %v", err)
	}
	if endpoints.RESTURL != profile.RESTURL() || endpoints.PublicWSURL != profile.PublicWSURL() || endpoints.UserWSURL != profile.UserWSURL() || endpoints.ChainID != profile.ChainID() {
		t.Fatalf("Aster Perp testenv endpoints do not match SDK official Testnet profile: endpoints=%+v sdk={rest:%s public_ws:%s user_ws:%s chain:%d}", endpoints, profile.RESTURL(), profile.PublicWSURL(), profile.UserWSURL(), profile.ChainID())
	}
	return profile
}

func asterPerpAcceptanceHTTPClient(t *testing.T, proxyURL string) *http.Client {
	t.Helper()
	client := &http.Client{Timeout: 45 * time.Second}
	if strings.TrimSpace(proxyURL) == "" {
		return client
	}
	parsed, err := parseAsterPerpAcceptanceProxyURL(proxyURL)
	if err != nil {
		t.Fatal(err)
	}
	client.Transport = &http.Transport{Proxy: http.ProxyURL(parsed)}
	return client
}

func parseAsterPerpAcceptanceProxyURL(proxyURL string) (*url.URL, error) {
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		return nil, fmt.Errorf("Aster Testnet proxy URL is invalid")
	}
	return parsed, nil
}

func requireAsterPerpAcceptanceInstrument(t *testing.T, adapter *Adapter, desired string) *model.Instrument {
	t.Helper()
	inst, err := selectAsterPerpAcceptanceInstrument(adapter.provider, desired)
	if err != nil {
		t.Fatal(err)
	}
	return inst
}

func selectAsterPerpAcceptanceInstrument(provider *instrumentProvider, desired string) (*model.Instrument, error) {
	if provider == nil {
		return nil, fmt.Errorf("Aster Perp Testnet instrument provider is nil")
	}
	desired = strings.TrimSpace(desired)
	if isAsterPerpTestSymbol(desired) {
		return nil, fmt.Errorf("Aster Perp Testnet symbol %q is rejected because TEST* symbols are non-tradable fixtures", desired)
	}
	for _, inst := range provider.All() {
		if inst == nil || isAsterPerpTestSymbol(inst.VenueSymbol) || isAsterPerpTestSymbol(inst.ID.Symbol) {
			continue
		}
		if desired == "" || asterPerpSymbolMatches(inst, desired) {
			return inst, nil
		}
	}
	if desired == "" {
		return nil, fmt.Errorf("Aster Perp Testnet returned no supported non-TEST instruments")
	}
	return nil, fmt.Errorf("Aster Perp Testnet symbol %q was not loaded", desired)
}

func asterPerpSymbolMatches(inst *model.Instrument, desired string) bool {
	if inst == nil {
		return false
	}
	desired = strings.TrimSpace(desired)
	neutral := strings.ReplaceAll(desired, "/", "-")
	return strings.EqualFold(inst.VenueSymbol, desired) ||
		strings.EqualFold(inst.ID.Symbol, desired) ||
		strings.EqualFold(inst.ID.Symbol, neutral)
}

func isAsterPerpTestSymbol(symbol string) bool {
	return strings.HasPrefix(strings.ToUpper(strings.TrimSpace(symbol)), "TEST")
}

func requireAsterPerpAcceptanceBook(t *testing.T, ctx context.Context, adapter *Adapter, id model.InstrumentID, label string) *model.OrderBook {
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

func requireAsterPerpAcceptanceAccountState(t *testing.T, ctx context.Context, adapter *Adapter, label string) model.AccountState {
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

func ensureNoAsterPerpTestnetOpenOrders(t *testing.T, ctx context.Context, adapter *Adapter, id model.InstrumentID, label string) {
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

func ensureNoAsterPerpTestnetPositions(t *testing.T, ctx context.Context, adapter *Adapter, label string) {
	t.Helper()
	positions, err := adapter.Account.Positions(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label+" position preflight")
		t.Fatalf("%s position preflight: %v", label, err)
	}
	if len(positions) > 0 {
		t.Skipf("skipping %s acceptance: account already has %d open position(s); clean the testnet account first", label, len(positions))
	}
}

func asterPerpAcceptanceLifecycleSpec(t *testing.T, label string, inst *model.Instrument, book *model.OrderBook, maxNotional decimal.Decimal) runtimeaccept.OrderLifecycleSpec {
	t.Helper()
	if inst == nil {
		t.Fatalf("%s instrument is required", label)
	}
	restingPrice := floorAsterPerpAcceptanceDecimal(book.Bids[0].Price.Mul(decimal.RequireFromString("0.80")), inst.PriceTick)
	fillPrice := ceilAsterPerpAcceptanceDecimal(book.Asks[0].Price.Mul(decimal.RequireFromString("1.01")), inst.PriceTick)
	closePrice := floorAsterPerpAcceptanceDecimal(book.Bids[0].Price.Mul(decimal.RequireFromString("0.99")), inst.PriceTick)
	qty := asterPerpAcceptanceQuantity(t, label, inst, maxNotional, minAsterPerpPositiveDecimal(restingPrice, fillPrice, closePrice), maxAsterPerpPositiveDecimal(restingPrice, fillPrice, closePrice))
	return runtimeaccept.OrderLifecycleSpec{
		Label:          label,
		Venue:          VenueName,
		Environment:    string(astercommon.EnvironmentTestnet),
		Product:        "USDT-linear Perp",
		AccountID:      AccountIDDefault,
		InstrumentID:   inst.ID,
		Quantity:       qty,
		RestingPrice:   restingPrice,
		FillPrice:      fillPrice,
		ClosePrice:     closePrice,
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		Logf:           t.Logf,
	}
}

func asterPerpAcceptanceQuantity(t *testing.T, label string, inst *model.Instrument, maxNotional, minNotionalPrice, maxNotionalPrice decimal.Decimal) decimal.Decimal {
	t.Helper()
	qty, err := selectAsterPerpAcceptanceQuantity(inst, maxNotional, minNotionalPrice, maxNotionalPrice)
	if err != nil {
		t.Fatalf("%s quantity selection: %v", label, err)
	}
	return qty
}

func selectAsterPerpAcceptanceQuantity(inst *model.Instrument, maxNotional, minNotionalPrice, maxNotionalPrice decimal.Decimal) (decimal.Decimal, error) {
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
	if inst.MinNotional.IsPositive() && minNotionalPrice.IsPositive() {
		minByNotional := inst.MinNotional.Div(minNotionalPrice)
		if minByNotional.GreaterThan(qty) {
			qty = minByNotional
		}
	}
	qty = ceilAsterPerpAcceptanceDecimal(qty, step)
	if maxNotionalPrice.IsPositive() && qty.Mul(maxNotionalPrice).GreaterThan(maxNotional) {
		return decimal.Zero, fmt.Errorf("minimum tradable notional %s exceeds max notional %s", qty.Mul(maxNotionalPrice), maxNotional)
	}
	return qty, nil
}

func ensureAsterPerpAcceptanceFunds(t *testing.T, label string, state model.AccountState, lifecycle runtimeaccept.OrderLifecycleSpec) {
	t.Helper()
	required := lifecycle.Quantity.Mul(lifecycle.FillPrice)
	for _, balance := range state.Balances {
		if strings.EqualFold(balance.Currency, "USDT") {
			if balance.Available.LessThan(required) {
				t.Skipf("skipping %s acceptance: available USDT %s below required notional %s", label, balance.Available, required)
			}
			return
		}
	}
	t.Skipf("skipping %s acceptance: no USDT balance found", label)
}

func asterPerpPrivateStreamTopics() []string {
	return []string{"ORDER_TRADE_UPDATE", "ACCOUNT_UPDATE"}
}

func stopAsterPerpRuntimeNode(t *testing.T, stop context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	stop()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatalf("Aster Perp Testnet runtime node did not stop")
	}
}

func ceilAsterPerpAcceptanceDecimal(value, step decimal.Decimal) decimal.Decimal {
	if !step.IsPositive() {
		return value
	}
	return value.Div(step).Ceil().Mul(step)
}

func floorAsterPerpAcceptanceDecimal(value, step decimal.Decimal) decimal.Decimal {
	if !step.IsPositive() {
		return value
	}
	out := value.Div(step).Floor().Mul(step)
	if out.IsPositive() {
		return out
	}
	return step
}

func minAsterPerpPositiveDecimal(values ...decimal.Decimal) decimal.Decimal {
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

func maxAsterPerpPositiveDecimal(values ...decimal.Decimal) decimal.Decimal {
	var out decimal.Decimal
	for _, value := range values {
		if value.IsPositive() && value.GreaterThan(out) {
			out = value
		}
	}
	return out
}
