package nado

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
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	sdk "github.com/QuantProcessing/boltertrader/sdk/nado"
	"github.com/shopspring/decimal"
)

func TestNadoSpotTestnetReadAcceptance(t *testing.T) {
	cfg := testenv.RequireNadoTestnetRead(t)
	runNadoTestnetReadAcceptance(t, "Nado Spot Testnet", cfg, cfg.SpotSymbol, enums.KindSpot)
}

func TestNadoPerpTestnetReadAcceptance(t *testing.T) {
	cfg := testenv.RequireNadoTestnetRead(t)
	runNadoTestnetReadAcceptance(t, "Nado Perp Testnet", cfg, cfg.PerpSymbol, enums.KindPerp)
}

func TestNadoSpotTestnetAdapterAcceptance(t *testing.T) {
	cfg := testenv.RequireNadoTestnetWrite(t)
	runNadoTestnetAdapterAcceptance(t, "Nado Spot Testnet", cfg, cfg.SpotSymbol, enums.KindSpot)
}

func TestNadoSpotTestnetRuntimeAcceptance(t *testing.T) {
	cfg := testenv.RequireNadoTestnetWrite(t)
	runNadoTestnetRuntimeAcceptance(t, "Nado Spot Testnet Runtime", cfg, cfg.SpotSymbol, enums.KindSpot)
}

func TestNadoPerpTestnetAdapterAcceptance(t *testing.T) {
	cfg := testenv.RequireNadoTestnetWrite(t)
	runNadoTestnetAdapterAcceptance(t, "Nado Perp Testnet", cfg, cfg.PerpSymbol, enums.KindPerp)
}

func TestNadoPerpTestnetRuntimeAcceptance(t *testing.T) {
	cfg := testenv.RequireNadoTestnetWrite(t)
	runNadoTestnetRuntimeAcceptance(t, "Nado Perp Testnet Runtime", cfg, cfg.PerpSymbol, enums.KindPerp)
}

func runNadoTestnetReadAcceptance(t *testing.T, label string, cfg testenv.NadoTestnetConfig, symbol string, kind enums.InstrumentKind) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	requireOfficialNadoTestnetProfile(t, cfg)
	adapter := newNadoAcceptanceAdapter(t, ctx, cfg, kind)
	defer adapter.Close()
	id := requireNadoAcceptanceInstrument(t, adapter, symbol, kind)
	book := requireNadoAcceptanceBook(t, ctx, adapter, label, id)
	t.Logf("%s discovery instrument=%s venue_symbol=%s bid=%s ask=%s", label, id, nadoAcceptanceVenueSymbol(t, adapter, id), book.Bids[0].Price, book.Asks[0].Price)
	state := requireNadoAcceptanceAccountState(t, ctx, adapter, label)
	requireNadoAcceptanceAccountReady(t, label, state)
}

func runNadoTestnetAdapterAcceptance(t *testing.T, label string, cfg testenv.NadoTestnetConfig, symbol string, kind enums.InstrumentKind) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	requireOfficialNadoTestnetProfile(t, cfg)
	adapter := newNadoAcceptanceAdapter(t, ctx, cfg, kind)
	defer adapter.Close()
	id, _, lifecycle := requireNadoAcceptanceLifecycle(t, ctx, adapter, label, symbol, kind, cfg.MaxNotionalUSDT0)
	requireNoNadoAcceptanceOpenOrders(t, ctx, adapter, label, id)
	requireNoNadoAcceptancePosition(t, ctx, adapter, label, id)
	state := requireNadoAcceptanceAccountState(t, ctx, adapter, label)
	requireNadoLifecycleFunds(t, label, adapter, state, lifecycle)
	defer cancelAllNadoAcceptanceOrders(t, adapter, id)
	defer assertNoNadoPreparedAcceptanceEntries(t, label, adapter)
	preparedExec := nadoAcceptancePreparedExecution{
		ExecutionClient: adapter.Execution,
		validator:       adapter.exec,
		prepared:        adapter.exec,
		provider:        adapter.provider,
	}
	if err := adapter.Start(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label+" private stream")
		t.Fatalf("%s private stream: %v", label, err)
	}
	lifecycle.PrivateStreamTopics = []string{"orders", "fills", "positions"}
	t.Logf("%s private_stream_topics=%s", label, strings.Join(lifecycle.PrivateStreamTopics, ","))
	if _, err := runtimeaccept.RunAdapterOrderLifecycle(ctx, preparedExec, lifecycle); err != nil {
		t.Fatalf("%s adapter order lifecycle: %v", label, err)
	}
	evidenceCtx, evidenceCancel := context.WithTimeout(ctx, 30*time.Second)
	defer evidenceCancel()
	if _, err := runtimeaccept.WaitForPrivateFillEvidence(evidenceCtx, adapter.Execution.Events(), id, AccountIDUnified); err != nil {
		t.Fatalf("%s private execution evidence: %v", label, err)
	}
}

func runNadoTestnetRuntimeAcceptance(t *testing.T, label string, cfg testenv.NadoTestnetConfig, symbol string, kind enums.InstrumentKind) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Minute)
	defer cancel()
	requireOfficialNadoTestnetProfile(t, cfg)
	adapter := newNadoAcceptanceAdapter(t, ctx, cfg, kind)
	defer adapter.Close()
	id, _, lifecycle := requireNadoAcceptanceLifecycle(t, ctx, adapter, label, symbol, kind, cfg.MaxNotionalUSDT0)
	requireNoNadoAcceptanceOpenOrders(t, ctx, adapter, label, id)
	requireNoNadoAcceptancePosition(t, ctx, adapter, label, id)

	node := btruntime.NewNode(
		btruntime.Clients{Market: adapter.Market, Execution: adapter.Execution, Account: adapter.Account},
		clock.NewRealClock(),
		AccountIDUnified,
		btruntime.WithAccountID(AccountIDUnified),
		btruntime.WithAccountStaleAfter(2*time.Minute),
	)
	t.Logf("%s runtime account_stale_after=%s reason=testnet_full_reconcile_latency", label, 2*time.Minute)
	runtimeaccept.AttachAccountRequiredRiskWithAcceptanceLimit(node, adapter.Market.InstrumentProvider())
	if err := adapter.Start(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label+" private stream")
		t.Fatalf("%s private stream: %v", label, err)
	}
	lifecycle.PrivateStreamTopics = []string{"orders", "fills", "positions"}
	t.Logf("%s private_stream_topics=%s", label, strings.Join(lifecycle.PrivateStreamTopics, ","))
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer stopNadoRuntimeNode(t, stop, done)
	defer cancelAllNadoAcceptanceOrders(t, adapter, id)
	defer assertNoNadoPreparedAcceptanceEntries(t, label, adapter)
	if err := runtimeaccept.WaitForActive(ctx, node); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label+" startup reconcile")
		t.Fatalf("%s runtime active before risk probe: %v", label, err)
	}
	runtimeaccept.AssertAccountStateReady(t, node, AccountIDUnified, model.AccountMargin, kind)
	if state, ok := node.Cache.Account(AccountIDUnified); ok {
		requireNadoLifecycleFunds(t, label, adapter, state.LastEvent(), lifecycle)
	}
	runtimeaccept.AssertRuntimeOversizedOrderRejected(t, node, adapter.Market.InstrumentProvider(), id)
	if _, err := runtimeaccept.RunRuntimeOrderLifecycle(ctx, node, adapter.Execution, lifecycle); err != nil {
		t.Fatalf("%s runtime order lifecycle: %v", label, err)
	}
	finalReport, err := node.Resync(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label+" final reconcile")
		t.Fatalf("%s final reconcile: %v", label, err)
	}
	if finalReport.AccountStatesApplied != 1 {
		t.Fatalf("%s final account states applied=%d, want 1: %+v", label, finalReport.AccountStatesApplied, finalReport)
	}
	runtimeaccept.AssertAccountStateReady(t, node, AccountIDUnified, model.AccountMargin, kind)
}

func requireOfficialNadoTestnetProfile(t *testing.T, cfg testenv.NadoTestnetConfig) {
	t.Helper()
	profile := nadoSDKTestnetProfile(t)
	if profile.Environment() != sdk.EnvironmentTestnet {
		t.Fatalf("Nado profile environment=%q, want %q", profile.Environment(), sdk.EnvironmentTestnet)
	}
	if cfg.Profile.ChainID != profile.ChainID() {
		t.Fatalf("Nado Testnet chain id=%d, want %d", cfg.Profile.ChainID, profile.ChainID())
	}
	if err := profile.Validate(); err != nil {
		t.Fatalf("Nado Testnet profile is not official: %v", err)
	}
	for _, endpoint := range []struct {
		name string
		got  string
		want string
	}{
		{"gateway_v1", cfg.Profile.GatewayV1URL, profile.GatewayV1URL()},
		{"gateway_v2", cfg.Profile.GatewayV2URL, profile.GatewayV2URL()},
		{"archive_v1", cfg.Profile.ArchiveV1URL, profile.ArchiveV1URL()},
		{"archive_v2", cfg.Profile.ArchiveV2URL, profile.ArchiveV2URL()},
		{"gateway_ws", cfg.Profile.GatewayWSURL, profile.GatewayWSURL()},
		{"subscriptions_ws", cfg.Profile.SubscriptionsWSURL, profile.SubscriptionsWSURL()},
		{"trigger", cfg.Profile.TriggerURL, profile.TriggerURL()},
	} {
		if endpoint.got != endpoint.want {
			t.Fatalf("Nado Testnet %s endpoint=%q, want official %q", endpoint.name, endpoint.got, endpoint.want)
		}
	}
}

func newNadoAcceptanceAdapter(t *testing.T, ctx context.Context, cfg testenv.NadoTestnetConfig, kind enums.InstrumentKind) *Adapter {
	t.Helper()
	profile := nadoSDKTestnetProfile(t)
	adapter, err := New(ctx, Config{
		PrivateKey:  cfg.PrivateKey,
		Subaccount:  cfg.Subaccount,
		Profile:     &profile,
		ProductKind: kind,
		AccountID:   AccountIDUnified,
		HTTPClient:  nadoAcceptanceHTTPClient(t, cfg.ProxyURL),
	})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Nado Testnet adapter initialization")
		t.Fatalf("new Nado Testnet adapter: %v", err)
	}
	if adapter.rest == nil || adapter.rest.Profile().Environment() != sdk.EnvironmentTestnet {
		t.Fatalf("Nado adapter did not use Testnet profile")
	}
	if err := adapter.rest.Profile().Validate(); err != nil {
		t.Fatalf("Nado adapter profile is not official Testnet: %v", err)
	}
	return adapter
}

func nadoAcceptanceHTTPClient(t *testing.T, proxyURL string) *http.Client {
	t.Helper()
	client := &http.Client{Timeout: 45 * time.Second}
	if strings.TrimSpace(proxyURL) == "" {
		return client
	}
	parsed, err := url.Parse(proxyURL)
	if err != nil {
		t.Fatalf("Nado Testnet proxy URL: %v", err)
	}
	client.Transport = &http.Transport{Proxy: http.ProxyURL(parsed)}
	return client
}

func nadoSDKTestnetProfile(t *testing.T) sdk.Profile {
	t.Helper()
	profile, err := sdk.NewProfile(sdk.EnvironmentTestnet)
	if err != nil {
		t.Fatalf("Nado Testnet SDK profile: %v", err)
	}
	return profile
}

func requireNadoAcceptanceInstrument(t *testing.T, adapter *Adapter, venueSymbol string, kind enums.InstrumentKind) model.InstrumentID {
	t.Helper()
	id, ok := selectNadoAcceptanceInstrument(adapter.provider, venueSymbol, kind)
	if !ok {
		if strings.TrimSpace(venueSymbol) == "" {
			t.Fatalf("Nado Testnet returned no supported %s instruments", kind)
		}
		t.Fatalf("Nado Testnet symbol %q kind=%s was not loaded", venueSymbol, kind)
	}
	inst, ok := adapter.provider.Instrument(id)
	if !ok {
		t.Fatalf("Nado Testnet instrument %s not available", id)
	}
	if inst.ID.Kind != kind {
		t.Fatalf("Nado Testnet instrument %s kind=%s, want %s", id, inst.ID.Kind, kind)
	}
	if inst.Settle != "USDT0" {
		t.Fatalf("Nado Testnet instrument %s settle=%q, want USDT0", id, inst.Settle)
	}
	return id
}

func selectNadoAcceptanceInstrument(provider *instrumentProvider, desired string, kind enums.InstrumentKind) (model.InstrumentID, bool) {
	if provider == nil {
		return model.InstrumentID{}, false
	}
	desired = strings.TrimSpace(desired)
	for _, inst := range provider.All() {
		if inst == nil || inst.ID.Kind != kind {
			continue
		}
		if desired == "" || strings.EqualFold(inst.VenueSymbol, desired) || strings.EqualFold(inst.ID.Symbol, desired) {
			return inst.ID, true
		}
	}
	return model.InstrumentID{}, false
}

func requireNadoAcceptanceBook(t *testing.T, ctx context.Context, adapter *Adapter, label string, id model.InstrumentID) *model.OrderBook {
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

type nadoAcceptanceLifecycleCandidate struct {
	id        model.InstrumentID
	book      *model.OrderBook
	lifecycle runtimeaccept.OrderLifecycleSpec
}

func requireNadoAcceptanceLifecycle(
	t *testing.T,
	ctx context.Context,
	adapter *Adapter,
	label, desired string,
	kind enums.InstrumentKind,
	maxNotional decimal.Decimal,
) (model.InstrumentID, *model.OrderBook, runtimeaccept.OrderLifecycleSpec) {
	t.Helper()
	candidate, err := selectNadoAcceptanceLifecycleCandidate(
		ctx,
		adapter.provider,
		desired,
		kind,
		maxNotional,
		func(ctx context.Context, id model.InstrumentID) (*model.OrderBook, error) {
			return adapter.Market.OrderBook(ctx, id, 5)
		},
	)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label+" lifecycle selection")
		t.Fatalf("%s lifecycle selection: %v", label, err)
	}
	candidate.lifecycle.Label = label
	candidate.lifecycle.Logf = t.Logf
	t.Logf("%s selected instrument=%s venue_symbol=%s", label, candidate.id, nadoAcceptanceVenueSymbol(t, adapter, candidate.id))
	return candidate.id, candidate.book, candidate.lifecycle
}

func selectNadoAcceptanceLifecycleCandidate(
	ctx context.Context,
	provider *instrumentProvider,
	desired string,
	kind enums.InstrumentKind,
	maxNotional decimal.Decimal,
	loadBook func(context.Context, model.InstrumentID) (*model.OrderBook, error),
) (nadoAcceptanceLifecycleCandidate, error) {
	if provider == nil {
		return nadoAcceptanceLifecycleCandidate{}, fmt.Errorf("nado acceptance: instrument provider is required")
	}
	if loadBook == nil {
		return nadoAcceptanceLifecycleCandidate{}, fmt.Errorf("nado acceptance: order book loader is required")
	}
	desired = strings.TrimSpace(desired)
	rejections := make([]string, 0)
	matched := false
	for _, inst := range provider.All() {
		if inst == nil || inst.ID.Kind != kind {
			continue
		}
		if desired != "" && !strings.EqualFold(inst.VenueSymbol, desired) && !strings.EqualFold(inst.ID.Symbol, desired) {
			continue
		}
		matched = true
		if inst.Settle != "USDT0" {
			rejections = append(rejections, fmt.Sprintf("%s settle=%q", inst.ID, inst.Settle))
			if desired != "" {
				break
			}
			continue
		}
		book, err := loadBook(ctx, inst.ID)
		if err != nil {
			return nadoAcceptanceLifecycleCandidate{}, fmt.Errorf("nado acceptance: load %s order book: %w", inst.ID, err)
		}
		lifecycle, err := buildNadoAcceptanceLifecycleSpec(inst, book, maxNotional)
		if err == nil {
			return nadoAcceptanceLifecycleCandidate{id: inst.ID, book: book, lifecycle: lifecycle}, nil
		}
		rejections = append(rejections, fmt.Sprintf("%s: %v", inst.ID, err))
		if desired != "" {
			break
		}
	}
	if !matched {
		if desired == "" {
			return nadoAcceptanceLifecycleCandidate{}, fmt.Errorf("nado acceptance: no supported %s instruments", kind)
		}
		return nadoAcceptanceLifecycleCandidate{}, fmt.Errorf("nado acceptance: symbol %q kind=%s was not loaded", desired, kind)
	}
	return nadoAcceptanceLifecycleCandidate{}, fmt.Errorf("nado acceptance: no safe %s lifecycle under max notional %s (%s)", kind, maxNotional, strings.Join(rejections, "; "))
}

func requireNadoAcceptanceAccountState(t *testing.T, ctx context.Context, adapter *Adapter, label string) model.AccountState {
	t.Helper()
	state, err := adapter.acct.AccountState(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label+" account state")
		t.Fatalf("%s account state: %v", label, err)
	}
	requireNadoAcceptanceAccountReady(t, label, state)
	return state
}

func requireNadoAcceptanceAccountReady(t *testing.T, label string, state model.AccountState) {
	t.Helper()
	if state.AccountID != AccountIDUnified || state.Venue != VenueName {
		t.Fatalf("%s account identity mismatch: %+v", label, state)
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("%s account state invalid: %v", label, err)
	}
	if !state.Reported || state.EventID == "" || state.TsEvent.IsZero() || state.TsInit.IsZero() {
		t.Fatalf("%s account state envelope incomplete: %+v", label, state)
	}
	if state.Summary == nil || state.Summary.SettlementCurrency != "USDT0" {
		t.Fatalf("%s account summary missing USDT0 settlement: %+v", label, state.Summary)
	}
	if len(state.Balances) == 0 {
		t.Fatalf("%s account state has no balances", label)
	}
}

func buildNadoAcceptanceLifecycleSpec(inst *model.Instrument, book *model.OrderBook, maxNotional decimal.Decimal) (runtimeaccept.OrderLifecycleSpec, error) {
	prices, err := buildNadoAcceptanceLifecyclePrices(inst, book)
	if err != nil {
		return runtimeaccept.OrderLifecycleSpec{}, err
	}
	qty, err := nadoAcceptanceQuantity(inst, maxNotional, minPositiveNadoDecimal(prices.resting, prices.fill, prices.close), maxPositiveNadoDecimal(prices.resting, prices.fill, prices.close))
	if err != nil {
		return runtimeaccept.OrderLifecycleSpec{}, err
	}
	closeQty := decimal.Zero
	if inst.ID.Kind == enums.KindSpot {
		closeQty, err = nadoAcceptanceSpotCloseQuantity(inst, qty)
		if err != nil {
			return runtimeaccept.OrderLifecycleSpec{}, err
		}
	}
	return runtimeaccept.OrderLifecycleSpec{
		Venue:          VenueName,
		Environment:    "Testnet",
		Product:        nadoAcceptanceProduct(inst.ID.Kind),
		AccountID:      AccountIDUnified,
		InstrumentID:   inst.ID,
		Quantity:       qty,
		CloseQuantity:  closeQty,
		RestingPrice:   prices.resting,
		FillPrice:      prices.fill,
		ClosePrice:     prices.close,
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
	}, nil
}

type nadoAcceptancePrices struct {
	resting decimal.Decimal
	fill    decimal.Decimal
	close   decimal.Decimal
}

func nadoAcceptanceLifecyclePrices(t *testing.T, label string, inst *model.Instrument, book *model.OrderBook) nadoAcceptancePrices {
	t.Helper()
	prices, err := buildNadoAcceptanceLifecyclePrices(inst, book)
	if err != nil {
		t.Fatalf("%s lifecycle prices: %v", label, err)
	}
	return prices
}

func buildNadoAcceptanceLifecyclePrices(inst *model.Instrument, book *model.OrderBook) (nadoAcceptancePrices, error) {
	if inst == nil {
		return nadoAcceptancePrices{}, fmt.Errorf("instrument is required")
	}
	if book == nil || len(book.Bids) == 0 || len(book.Asks) == 0 {
		return nadoAcceptancePrices{}, fmt.Errorf("empty order book")
	}
	resting := floorNadoAcceptanceDecimal(book.Bids[0].Price.Mul(decimal.RequireFromString("0.99")), inst.PriceTick)
	fill := ceilNadoAcceptanceDecimal(book.Asks[0].Price.Mul(decimal.RequireFromString("1.01")), inst.PriceTick)
	closePrice := floorNadoAcceptanceDecimal(book.Bids[0].Price.Mul(decimal.RequireFromString("0.99")), inst.PriceTick)
	if !resting.IsPositive() || !fill.IsPositive() || !closePrice.IsPositive() {
		return nadoAcceptancePrices{}, fmt.Errorf("lifecycle prices must be positive: resting=%s fill=%s close=%s", resting, fill, closePrice)
	}
	return nadoAcceptancePrices{resting: resting, fill: fill, close: closePrice}, nil
}

func nadoAcceptanceQuantity(inst *model.Instrument, maxNotional, minNotionalPrice, maxNotionalPrice decimal.Decimal) (decimal.Decimal, error) {
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
	multiplier := nadoContractMultiplier(inst)
	qty := inst.MinQty
	if !qty.IsPositive() {
		qty = step
	}
	if inst.ID.Kind == enums.KindSpot {
		minBufferedQty := qty.Div(nadoSpotCloseQuantityBuffer())
		if minBufferedQty.GreaterThan(qty) {
			qty = minBufferedQty
		}
	}
	if inst.MinNotional.IsPositive() && minNotionalPrice.IsPositive() {
		minByNotional := inst.MinNotional.Div(minNotionalPrice.Mul(multiplier))
		if minByNotional.GreaterThan(qty) {
			qty = minByNotional
		}
		if inst.ID.Kind == enums.KindSpot {
			minCloseQty := ceilNadoAcceptanceDecimal(minByNotional, step)
			minBufferedQty := minCloseQty.Div(nadoSpotCloseQuantityBuffer())
			if minBufferedQty.GreaterThan(qty) {
				qty = minBufferedQty
			}
		}
	}
	qty = ceilNadoAcceptanceDecimal(qty, step)
	if maxNotionalPrice.IsPositive() && qty.Mul(maxNotionalPrice).Mul(multiplier).GreaterThan(maxNotional) {
		return decimal.Zero, fmt.Errorf("min lifecycle quantity %s notional %s exceeds max notional %s", qty, qty.Mul(maxNotionalPrice).Mul(multiplier), maxNotional)
	}
	return qty, nil
}

func nadoAcceptanceSpotCloseQuantity(inst *model.Instrument, qty decimal.Decimal) (decimal.Decimal, error) {
	if inst == nil {
		return decimal.Zero, fmt.Errorf("instrument is required")
	}
	step := inst.SizeStep
	if !step.IsPositive() {
		step = decimal.NewFromInt(1)
	}
	minQty := inst.MinQty
	if !minQty.IsPositive() {
		minQty = step
	}
	closeQty := floorNadoAcceptanceDecimal(qty.Mul(nadoSpotCloseQuantityBuffer()), step)
	if closeQty.LessThan(minQty) {
		return decimal.Zero, fmt.Errorf("spot close quantity %s is below min quantity %s after fee buffer", closeQty, minQty)
	}
	return closeQty, nil
}

func nadoSpotCloseQuantityBuffer() decimal.Decimal {
	return decimal.RequireFromString("0.995")
}

func requireNadoLifecycleFunds(t *testing.T, label string, adapter *Adapter, state model.AccountState, spec runtimeaccept.OrderLifecycleSpec) {
	t.Helper()
	inst, ok := adapter.provider.Instrument(spec.InstrumentID)
	if !ok {
		t.Fatalf("%s instrument %s not available", label, spec.InstrumentID)
	}
	if state.Summary == nil {
		t.Fatalf("%s unified account summary is unavailable", label)
	}
	if state.Summary.SettlementCurrency != inst.Settle {
		t.Fatalf("%s account settlement=%s, want %s", label, state.Summary.SettlementCurrency, inst.Settle)
	}
	if !state.Summary.AvailableCollateral.IsPositive() {
		t.Fatalf("%s unified account has no positive available collateral", label)
	}
	if spec.InstrumentID.Kind == enums.KindSpot {
		requireNoNadoSpotBorrow(t, label, state)
		for _, balance := range state.Balances {
			if balance.Currency == inst.Quote && balance.Total.IsPositive() {
				return
			}
		}
		t.Fatalf("%s funded-only Spot lifecycle has no positive raw %s inventory", label, inst.Quote)
	}
}

func requireNoNadoSpotBorrow(t *testing.T, label string, state model.AccountState) {
	t.Helper()
	for _, balance := range state.Balances {
		if balance.Borrowed.IsPositive() || balance.Interest.IsPositive() {
			t.Fatalf("%s Spot funded-only pretrade requires no borrowed balance, got currency=%s borrowed=%s interest=%s", label, balance.Currency, balance.Borrowed, balance.Interest)
		}
	}
}

func requireNoNadoAcceptanceOpenOrders(t *testing.T, ctx context.Context, adapter *Adapter, label string, id model.InstrumentID) {
	t.Helper()
	orders, err := adapter.Execution.OpenOrders(ctx, id)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label+" open order preflight")
		t.Fatalf("%s open order preflight: %v", label, err)
	}
	if len(orders) != 0 {
		t.Fatalf("%s has %d pre-existing open orders for %s: %+v", label, len(orders), id, orders)
	}
}

func requireNoNadoAcceptancePosition(t *testing.T, ctx context.Context, adapter *Adapter, label string, id model.InstrumentID) {
	t.Helper()
	if id.Kind == enums.KindSpot {
		return
	}
	positions, err := adapter.Account.Positions(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label+" position preflight")
		t.Fatalf("%s position preflight: %v", label, err)
	}
	for _, pos := range positions {
		if pos.InstrumentID == id && !pos.Quantity.IsZero() {
			t.Fatalf("%s has pre-existing position for %s: %+v", label, id, pos)
		}
	}
}

func cancelAllNadoAcceptanceOrders(t *testing.T, adapter *Adapter, id model.InstrumentID) {
	t.Helper()
	if adapter == nil || adapter.Execution == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	if err := adapter.Execution.CancelAll(ctx, id); err != nil {
		t.Logf("Nado acceptance cleanup cancel-all for %s failed: %v", id, err)
	}
}

func assertNoNadoPreparedAcceptanceEntries(t *testing.T, label string, adapter *Adapter) {
	t.Helper()
	if adapter == nil || adapter.exec == nil {
		t.Errorf("%s prepared-state cleanup cannot inspect execution client", label)
		return
	}
	if got := adapter.exec.preparedLen(); got != 0 {
		t.Errorf("%s left %d active prepared-order entries", label, got)
		return
	}
	t.Logf("%s cleanup=prepared_entries_zero", label)
}

func nadoAcceptanceVenueSymbol(t *testing.T, adapter *Adapter, id model.InstrumentID) string {
	t.Helper()
	inst, ok := adapter.provider.Instrument(id)
	if !ok {
		t.Fatalf("Nado instrument %s not available", id)
	}
	return inst.VenueSymbol
}

func nadoAcceptanceProduct(kind enums.InstrumentKind) string {
	if kind == enums.KindSpot {
		return "Spot cash USDT0"
	}
	return "USDT0-linear Perp"
}

func nadoContractMultiplier(inst *model.Instrument) decimal.Decimal {
	if inst != nil && inst.ContractMultiplier.IsPositive() {
		return inst.ContractMultiplier
	}
	return decimal.NewFromInt(1)
}

func minPositiveNadoDecimal(values ...decimal.Decimal) decimal.Decimal {
	out := decimal.Zero
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

func maxPositiveNadoDecimal(values ...decimal.Decimal) decimal.Decimal {
	out := decimal.Zero
	for _, value := range values {
		if value.GreaterThan(out) {
			out = value
		}
	}
	return out
}

func ceilNadoAcceptanceDecimal(value, step decimal.Decimal) decimal.Decimal {
	if value.IsZero() || step.IsZero() {
		return value
	}
	return value.Div(step).Ceil().Mul(step)
}

func floorNadoAcceptanceDecimal(value, step decimal.Decimal) decimal.Decimal {
	if value.IsZero() || step.IsZero() {
		return value
	}
	return value.Div(step).Floor().Mul(step)
}

type nadoAcceptancePreparedExecution struct {
	contract.ExecutionClient
	validator contract.VenuePreTradeValidator
	prepared  contract.PreparedExecutionClient
	provider  model.InstrumentProvider
}

func (e nadoAcceptancePreparedExecution) Submit(ctx context.Context, req model.OrderRequest) (*model.Order, error) {
	if e.validator == nil || e.prepared == nil {
		return nil, fmt.Errorf("nado acceptance: prepared pre-trade surfaces are required")
	}
	var inst *model.Instrument
	if e.provider != nil {
		inst, _ = e.provider.Instrument(req.InstrumentID)
	}
	lease, err := e.validator.ValidatePreTrade(ctx, req, inst)
	if err != nil {
		return nil, err
	}
	if lease != nil {
		defer lease.Release()
	}
	return e.prepared.SubmitPrepared(ctx, req)
}

func stopNadoRuntimeNode(t *testing.T, stop context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Nado runtime node did not stop")
	}
}
