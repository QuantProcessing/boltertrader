package bybit

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	bybitsdk "github.com/QuantProcessing/boltertrader/sdk/bybit"
	"github.com/shopspring/decimal"
)

// Demo traffic may traverse an explicitly configured proxy. Keep the normal
// SDK default narrow while allowing this live acceptance path to tolerate the
// measured proxy round-trip time.
const bybitAcceptanceRecvWindowMillis int64 = 15000

func TestBybitDemoSpotAcceptance(t *testing.T) {
	cfg := testenv.RequireBybitDemoWrite(t)
	runBybitAcceptance(t, "Bybit Demo Spot", cfg.APIKey, cfg.APISecret, cfg.Profile, cfg.SpotSymbol, enums.KindSpot, "", cfg.MaxNotionalUSDT)
}

func TestBybitDemoUSDTPerpAcceptance(t *testing.T) {
	cfg := testenv.RequireBybitDemoWrite(t)
	runBybitAcceptance(t, "Bybit Demo USDT Perp", cfg.APIKey, cfg.APISecret, cfg.Profile, cfg.USDTPerpSymbol, enums.KindPerp, bybitsdk.SettleCoinUSDT, cfg.MaxNotionalUSDT)
}

func TestBybitDemoUSDCPerpAcceptance(t *testing.T) {
	cfg := testenv.RequireBybitDemoWrite(t)
	runBybitAcceptance(t, "Bybit Demo USDC Perp", cfg.APIKey, cfg.APISecret, cfg.Profile, cfg.USDCPerpSymbol, enums.KindPerp, bybitsdk.SettleCoinUSDC, cfg.MaxNotionalUSDC)
}

func TestBybitDemoSpotRuntimeAcceptance(t *testing.T) {
	cfg := testenv.RequireBybitDemoWrite(t)
	runBybitRuntimeAcceptance(t, "Bybit Demo Spot Runtime", cfg.APIKey, cfg.APISecret, cfg.Profile, cfg.SpotSymbol, enums.KindSpot, "", cfg.MaxNotionalUSDT)
}

func TestBybitDemoUSDTPerpRuntimeAcceptance(t *testing.T) {
	cfg := testenv.RequireBybitDemoWrite(t)
	runBybitRuntimeAcceptance(t, "Bybit Demo USDT Perp Runtime", cfg.APIKey, cfg.APISecret, cfg.Profile, cfg.USDTPerpSymbol, enums.KindPerp, bybitsdk.SettleCoinUSDT, cfg.MaxNotionalUSDT)
}

func TestBybitDemoUSDCPerpRuntimeAcceptance(t *testing.T) {
	cfg := testenv.RequireBybitDemoWrite(t)
	runBybitRuntimeAcceptance(t, "Bybit Demo USDC Perp Runtime", cfg.APIKey, cfg.APISecret, cfg.Profile, cfg.USDCPerpSymbol, enums.KindPerp, bybitsdk.SettleCoinUSDC, cfg.MaxNotionalUSDC)
}

func TestValidateBybitAcceptanceAPIKeyInfo(t *testing.T) {
	t.Parallel()

	valid := bybitsdk.APIKeyInfo{
		UTA: 1,
		Permissions: bybitsdk.APIKeyPermissions{
			Spot:          []string{"SpotTrade"},
			ContractTrade: []string{"Order", "Position"},
		},
	}
	if err := validateBybitAcceptanceAPIKeyInfo("Bybit Demo Spot", enums.KindSpot, valid); err != nil {
		t.Fatalf("spot valid permissions: %v", err)
	}
	if err := validateBybitAcceptanceAPIKeyInfo("Bybit Demo Perp", enums.KindPerp, valid); err != nil {
		t.Fatalf("perp valid permissions: %v", err)
	}

	readOnly := valid
	readOnly.ReadOnly = 1
	if err := validateBybitAcceptanceAPIKeyInfo("Bybit Demo Spot", enums.KindSpot, readOnly); err == nil || !strings.Contains(err.Error(), "read-only") {
		t.Fatalf("expected read-only error, got %v", err)
	}

	notUnified := valid
	notUnified.UTA = 0
	if err := validateBybitAcceptanceAPIKeyInfo("Bybit Demo Spot", enums.KindSpot, notUnified); err == nil || !strings.Contains(err.Error(), "unified account") {
		t.Fatalf("expected unified-account error, got %v", err)
	}

	noSpot := valid
	noSpot.Permissions.Spot = nil
	if err := validateBybitAcceptanceAPIKeyInfo("Bybit Demo Spot", enums.KindSpot, noSpot); err == nil || !strings.Contains(err.Error(), "SpotTrade") {
		t.Fatalf("expected missing SpotTrade error, got %v", err)
	}

	spotOnly := valid
	spotOnly.Permissions.ContractTrade = nil
	if err := validateBybitAcceptanceAPIKeyInfo("Bybit Demo Spot", enums.KindSpot, spotOnly); err == nil || !strings.Contains(err.Error(), "ContractTrade Position") {
		t.Fatalf("expected missing ContractTrade Position error for spot acceptance, got %v", err)
	}

	noContractOrder := valid
	noContractOrder.Permissions.ContractTrade = []string{"Position"}
	if err := validateBybitAcceptanceAPIKeyInfo("Bybit Demo Perp", enums.KindPerp, noContractOrder); err == nil || !strings.Contains(err.Error(), "Order") {
		t.Fatalf("expected missing Order error, got %v", err)
	}

	noContractPosition := valid
	noContractPosition.Permissions.ContractTrade = []string{"Order"}
	if err := validateBybitAcceptanceAPIKeyInfo("Bybit Demo Perp", enums.KindPerp, noContractPosition); err == nil || !strings.Contains(err.Error(), "Position") {
		t.Fatalf("expected missing Position error, got %v", err)
	}
}

func TestFormatBybitAcceptanceAPIKeyPreflightErrorAddsDemoKeyGuidance(t *testing.T) {
	t.Parallel()

	got := formatBybitAcceptanceAPIKeyPreflightError("Bybit Demo Spot", fmt.Errorf("bybit sdk: query api key failed: 10003 API key is invalid"))
	for _, want := range []string{
		"Bybit Demo Spot API key preflight",
		"bybit sdk: query api key failed: 10003 API key is invalid",
		"Bybit Demo Trading",
		"not Bybit Testnet",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("expected %q in %q", want, got)
		}
	}
	if strings.Contains(got, ".;") {
		t.Fatalf("unexpected punctuation in %q", got)
	}
}

func runBybitAcceptance(t *testing.T, label, apiKey, apiSecret string, profile testenv.BybitEndpointProfile, symbol string, kind enums.InstrumentKind, settle string, maxNotional decimal.Decimal) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	adapter := newBybitAcceptanceAdapter(t, ctx, label, apiKey, apiSecret, profile, kind)
	defer adapter.Close()

	id := requireBybitAcceptanceInstrument(t, adapter, symbol, kind, settle)
	book, err := adapter.Market.OrderBook(ctx, id, 5)
	if err != nil {
		t.Fatalf("%s order book: %v", label, err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("%s empty book for %s: %+v", label, symbol, book)
	}
	lifecycle := bybitAcceptanceLifecycleSpec(t, adapter, label, id, book, maxNotional)
	state, err := adapter.acct.AccountState(ctx)
	if err != nil {
		t.Fatalf("%s account state: %v", label, err)
	}
	if state.AccountID != AccountIDUnified {
		t.Fatalf("%s account id=%q, want %q", label, state.AccountID, AccountIDUnified)
	}
	if err := state.Validate(); err != nil {
		t.Fatalf("%s account state invalid: %v", label, err)
	}
	if !state.Reported || state.EventID == "" || state.TsEvent.IsZero() || state.TsInit.IsZero() {
		t.Fatalf("%s account state envelope incomplete: %+v", label, state)
	}
	ensureBybitLifecycleFunds(t, label, adapter, state, lifecycle)
	if _, err := runtimeaccept.RunAdapterOrderLifecycle(ctx, adapter.Execution, lifecycle); err != nil {
		t.Fatalf("%s adapter order lifecycle: %v", label, err)
	}
}

func runBybitRuntimeAcceptance(t *testing.T, label, apiKey, apiSecret string, profile testenv.BybitEndpointProfile, symbol string, kind enums.InstrumentKind, settle string, maxNotional decimal.Decimal) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	adapter := newBybitAcceptanceAdapter(t, ctx, label, apiKey, apiSecret, profile, kind)
	defer adapter.Close()
	id := requireBybitAcceptanceInstrument(t, adapter, symbol, kind, settle)
	book, err := adapter.Market.OrderBook(ctx, id, 5)
	if err != nil {
		t.Fatalf("%s order book: %v", label, err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("%s empty book for %s: %+v", label, symbol, book)
	}
	lifecycle := bybitAcceptanceLifecycleSpec(t, adapter, label, id, book, maxNotional)

	node := btruntime.NewNode(
		btruntime.Clients{Market: adapter.Market, Execution: adapter.Execution, Account: adapter.Account},
		clock.NewRealClock(),
		AccountIDUnified,
		btruntime.WithAccountID(AccountIDUnified),
	)
	runtimeaccept.AttachAccountRequiredRiskWithMaxNotional(node, adapter.Market.InstrumentProvider(), maxNotional)
	report, err := node.Resync(ctx)
	if err != nil {
		t.Fatalf("%s initial reconcile: %v", label, err)
	}
	if report.AccountStatesApplied != 1 {
		t.Fatalf("%s account states applied=%d, want 1: %+v", label, report.AccountStatesApplied, report)
	}
	if state, ok := node.Cache.Account(AccountIDUnified); ok {
		ensureBybitLifecycleFunds(t, label, adapter, state.LastEvent(), lifecycle)
	}
	runtimeaccept.AssertAccountStateReady(t, node, AccountIDUnified, model.AccountMargin, kind)
	runtimeaccept.AssertOversizedOrderRejected(t, node, adapter.Market.InstrumentProvider(), id)
	if err := adapter.Start(ctx); err != nil {
		t.Fatalf("%s private stream: %v", label, err)
	}
	lifecycle.PrivateStreamTopics = bybitPrivateStreamTopics()
	t.Logf("%s private_stream_topics=%s", label, strings.Join(lifecycle.PrivateStreamTopics, ","))
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer stopBybitRuntimeNode(t, stop, done)
	if _, err := runtimeaccept.RunRuntimeOrderLifecycle(ctx, node, adapter.Execution, lifecycle); err != nil {
		t.Fatalf("%s runtime order lifecycle: %v", label, err)
	}
	finalReport, err := node.Resync(ctx)
	if err != nil {
		t.Fatalf("%s final reconcile: %v", label, err)
	}
	if finalReport.AccountStatesApplied != 1 {
		t.Fatalf("%s final account states applied=%d, want 1: %+v", label, finalReport.AccountStatesApplied, finalReport)
	}
	runtimeaccept.AssertAccountStateReady(t, node, AccountIDUnified, model.AccountMargin, kind)
}

func newBybitAcceptanceAdapter(t *testing.T, ctx context.Context, label, apiKey, apiSecret string, profile testenv.BybitEndpointProfile, kind enums.InstrumentKind) *Adapter {
	t.Helper()
	httpClient, err := testenv.BybitDemoHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Bybit HTTP client: %v", err)
	}
	requireBybitAcceptanceAPIKey(t, ctx, label, apiKey, apiSecret, profile, httpClient, kind)
	categories := []string{"linear"}
	if kind == enums.KindSpot {
		categories = []string{"spot"}
	}
	adapter, err := newWithRESTRecvWindow(ctx, Config{
		APIKey:      apiKey,
		APISecret:   apiSecret,
		Environment: bybitSDKProfile(profile),
		HTTPClient:  httpClient,
		Categories:  categories,
	}, bybitAcceptanceRecvWindowMillis)
	if err != nil {
		t.Fatalf("new Bybit adapter: %v", err)
	}
	return adapter
}

func requireBybitAcceptanceAPIKey(t *testing.T, ctx context.Context, label, apiKey, apiSecret string, profile testenv.BybitEndpointProfile, httpClient *http.Client, kind enums.InstrumentKind) {
	t.Helper()
	info, err := bybitsdk.NewClient().
		WithCredentials(apiKey, apiSecret).
		WithEnvironmentProfile(bybitSDKProfile(profile)).
		WithHTTPClient(httpClient).
		WithRecvWindowMillis(bybitAcceptanceRecvWindowMillis).
		GetAPIKeyInfo(ctx)
	if err != nil {
		t.Fatal(formatBybitAcceptanceAPIKeyPreflightError(label, err))
	}
	if info == nil {
		t.Fatalf("%s API key preflight: empty query-api result", label)
	}
	if err := validateBybitAcceptanceAPIKeyInfo(label, kind, *info); err != nil {
		t.Fatalf("%s API key preflight: %v", label, err)
	}
}

func formatBybitAcceptanceAPIKeyPreflightError(label string, err error) string {
	return fmt.Sprintf("%s API key preflight: %s; ensure BYBIT_DEMO_API_KEY and BYBIT_DEMO_API_SECRET were created from Bybit Demo Trading on a mainnet account, not Bybit Testnet or Testnet demo.", label, strings.TrimRight(err.Error(), ". "))
}

func validateBybitAcceptanceAPIKeyInfo(label string, kind enums.InstrumentKind, info bybitsdk.APIKeyInfo) error {
	if info.ReadOnly != 0 {
		return fmt.Errorf("%s API key is read-only, write acceptance requires order permission", label)
	}
	if info.UTA == 0 {
		return fmt.Errorf("%s API key is not attached to a Bybit unified account", label)
	}
	if kind == enums.KindSpot && !info.Permissions.HasSpotTrade() {
		return fmt.Errorf("%s API key missing Spot SpotTrade permission", label)
	}
	if !info.Permissions.HasContractPosition() {
		return fmt.Errorf("%s API key missing ContractTrade Position permission required for unified account-state reconciliation", label)
	}
	if kind != enums.KindSpot {
		if !info.Permissions.HasContractOrder() {
			return fmt.Errorf("%s API key missing ContractTrade Order permission", label)
		}
	}
	return nil
}

func bybitSDKProfile(profile testenv.BybitEndpointProfile) bybitsdk.EnvironmentProfile {
	return bybitsdk.EnvironmentProfile{
		RESTBaseURL:       profile.RESTBaseURL,
		PublicSpotWSURL:   profile.PublicSpotWSURL,
		PublicLinearWSURL: profile.PublicLinearWSURL,
		PrivateWSURL:      profile.PrivateWSURL,
		TradeWSURL:        profile.TradeWSURL,
		SupportsWSTrade:   profile.SupportsWSTrade,
	}
}

func requireBybitAcceptanceInstrument(t *testing.T, adapter *Adapter, venueSymbol string, kind enums.InstrumentKind, settle string) model.InstrumentID {
	t.Helper()
	id, ok := adapter.provider.ResolveVenueInstrument(venueSymbol, kind, settle)
	if !ok {
		t.Fatalf("Bybit symbol %s not loaded", venueSymbol)
	}
	inst, ok := adapter.provider.Instrument(id)
	if !ok {
		t.Fatalf("Bybit instrument %s not available", id)
	}
	if inst.ID.Kind != kind {
		t.Fatalf("Bybit instrument %s kind=%s, want %s", id, inst.ID.Kind, kind)
	}
	if settle != "" && inst.Settle != settle {
		t.Fatalf("Bybit instrument %s settle=%q, want %q", id, inst.Settle, settle)
	}
	return id
}

func bybitAcceptanceLifecycleSpec(t *testing.T, adapter *Adapter, label string, id model.InstrumentID, book *model.OrderBook, maxNotional decimal.Decimal) runtimeaccept.OrderLifecycleSpec {
	t.Helper()
	if book == nil || len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("%s empty order book for lifecycle: %+v", label, book)
	}
	inst, ok := adapter.provider.Instrument(id)
	if !ok {
		t.Fatalf("%s instrument %s not available", label, id)
	}
	fillMultiplier := decimal.RequireFromString("1.001")
	closeMultiplier := decimal.RequireFromString("0.999")
	if inst.ID.Kind != enums.KindSpot {
		fillMultiplier = decimal.RequireFromString("1.01")
		closeMultiplier = decimal.RequireFromString("0.99")
	}
	restingPrice := floorBybitAcceptanceDecimal(book.Bids[0].Price.Mul(decimal.RequireFromString("0.80")), inst.PriceTick)
	fillPrice := ceilBybitAcceptanceDecimal(book.Asks[0].Price.Mul(fillMultiplier), inst.PriceTick)
	closePrice := floorBybitAcceptanceDecimal(book.Bids[0].Price.Mul(closeMultiplier), inst.PriceTick)
	qty := bybitAcceptanceQuantity(t, label, inst, maxNotional, minPositiveBybitDecimal(restingPrice, fillPrice, closePrice), maxPositiveBybitDecimal(restingPrice, fillPrice, closePrice))
	closeQty := decimal.Zero
	if inst.ID.Kind == enums.KindSpot {
		closeQty = bybitAcceptanceSpotCloseQuantity(t, label, inst, qty, closePrice)
	}
	spec := runtimeaccept.OrderLifecycleSpec{
		Label:          label,
		Venue:          VenueName,
		Environment:    acceptanceEnvironment(label),
		Product:        acceptanceProduct(inst.ID.Kind, inst.Settle),
		AccountID:      AccountIDUnified,
		InstrumentID:   id,
		Quantity:       qty,
		CloseQuantity:  closeQty,
		RestingPrice:   restingPrice,
		FillPrice:      fillPrice,
		ClosePrice:     closePrice,
		PositionSide:   enums.PosNet,
		CloseAfterFill: true,
		Logf:           t.Logf,
	}
	if inst.ID.Kind == enums.KindSpot {
		step, minQty := bybitAcceptanceSpotQuantityRules(inst)
		spec = runtimeaccept.ConfigureSpotBalanceGuard(spec, adapter.acct, inst.Base, step, minQty, inst.MinNotional, qty.Sub(closeQty))
	} else {
		// Bybit Perp accounts default to lpaPerp=false, which clamps an
		// out-of-band limit to the nearest allowed price. The lifecycle still
		// rejects any change that worsens the caller's limit.
		spec.AllowVenuePriceImprovement = true
	}
	return spec
}

func bybitAcceptanceSpotCloseQuantity(t *testing.T, label string, inst *model.Instrument, qty, closePrice decimal.Decimal) decimal.Decimal {
	t.Helper()
	step, minQty := bybitAcceptanceSpotQuantityRules(inst)
	closeQty := floorBybitAcceptanceDecimal(qty.Mul(bybitSpotCloseQuantityBuffer()), step)
	if closeQty.LessThan(minQty) {
		t.Fatalf("%s: spot close quantity %s is below min quantity %s after fee buffer", label, closeQty, minQty)
	}
	if inst.MinNotional.IsPositive() && closeQty.Mul(closePrice).LessThan(inst.MinNotional) {
		t.Fatalf("%s: spot close notional %s is below min notional %s after fee buffer", label, closeQty.Mul(closePrice), inst.MinNotional)
	}
	return closeQty
}

func bybitAcceptanceSpotQuantityRules(inst *model.Instrument) (decimal.Decimal, decimal.Decimal) {
	step := inst.SizeStep
	if !step.IsPositive() {
		step = decimal.NewFromInt(1)
	}
	minQty := inst.MinQty
	if !minQty.IsPositive() {
		minQty = step
	}
	return step, minQty
}

func bybitSpotCloseQuantityBuffer() decimal.Decimal {
	return decimal.RequireFromString("0.995")
}

func acceptanceEnvironment(label string) string {
	switch {
	case strings.Contains(label, "Testnet"):
		return "Testnet"
	case strings.Contains(label, "Demo"):
		return "Demo"
	default:
		return ""
	}
}

func acceptanceProduct(kind enums.InstrumentKind, settle string) string {
	if kind == enums.KindSpot {
		return "Spot cash"
	}
	switch settle {
	case bybitsdk.SettleCoinUSDT:
		return "USDT-linear Perp/SWAP"
	case bybitsdk.SettleCoinUSDC:
		return "USDC-linear Perp/SWAP"
	default:
		return "Linear Perp/SWAP"
	}
}

func bybitPrivateStreamTopics() []string {
	return []string{"order", "execution", "position", "wallet"}
}

func bybitAcceptanceQuantity(t *testing.T, label string, inst *model.Instrument, maxNotional, minNotionalPrice, maxNotionalPrice decimal.Decimal) decimal.Decimal {
	t.Helper()
	if !maxNotional.IsPositive() {
		t.Fatalf("%s max notional must be positive, got %s", label, maxNotional)
	}
	step := inst.SizeStep
	if !step.IsPositive() {
		step = decimal.NewFromInt(1)
	}
	qty := inst.MinQty
	if !qty.IsPositive() {
		qty = step
	}
	if inst.ID.Kind == enums.KindSpot {
		minBufferedQty := qty.Div(bybitSpotCloseQuantityBuffer())
		if minBufferedQty.GreaterThan(qty) {
			qty = minBufferedQty
		}
	}
	if inst.MinNotional.IsPositive() && minNotionalPrice.IsPositive() {
		minByNotional := inst.MinNotional.Div(minNotionalPrice)
		if minByNotional.GreaterThan(qty) {
			qty = minByNotional
		}
		if inst.ID.Kind == enums.KindSpot {
			minCloseQty := ceilBybitAcceptanceDecimal(minByNotional, step)
			minBufferedQty := minCloseQty.Div(bybitSpotCloseQuantityBuffer())
			if minBufferedQty.GreaterThan(qty) {
				qty = minBufferedQty
			}
		}
	}
	qty = ceilBybitAcceptanceDecimal(qty, step)
	if maxNotionalPrice.IsPositive() && qty.Mul(maxNotionalPrice).GreaterThan(maxNotional) {
		t.Fatalf("%s: min lifecycle quantity %s notional %s exceeds max notional %s", label, qty, qty.Mul(maxNotionalPrice), maxNotional)
	}
	return qty
}

func ensureBybitLifecycleFunds(t *testing.T, label string, adapter *Adapter, state model.AccountState, spec runtimeaccept.OrderLifecycleSpec) {
	t.Helper()
	required := spec.Quantity.Mul(spec.FillPrice)
	currency := "USDT"
	if inst, ok := adapter.provider.Instrument(spec.InstrumentID); ok {
		if spec.InstrumentID.Kind == enums.KindSpot {
			currency = inst.Quote
		} else if inst.Settle != "" {
			currency = inst.Settle
		}
	}
	available := bybitAvailableBalance(state, currency)
	if spec.InstrumentID.Kind != enums.KindSpot {
		if usdAvailable := bybitAvailableBalance(state, state.BaseCurrency); usdAvailable.GreaterThan(available) {
			available = usdAvailable
			currency = state.BaseCurrency
		}
	}
	if available.GreaterThanOrEqual(required) {
		return
	}
	t.Fatalf("%s: %s available %s below required %s for lifecycle", label, currency, available, required)
}

func bybitAvailableBalance(state model.AccountState, currency string) decimal.Decimal {
	available := decimal.Zero
	for _, balance := range state.Balances {
		if balance.Currency != currency {
			continue
		}
		if candidate := balance.FreeOrAvailable(); candidate.GreaterThan(available) {
			available = candidate
		}
	}
	return available
}

func ceilBybitAcceptanceDecimal(value, step decimal.Decimal) decimal.Decimal {
	if value.IsZero() || step.IsZero() {
		return value
	}
	return value.Div(step).Ceil().Mul(step)
}

func floorBybitAcceptanceDecimal(value, step decimal.Decimal) decimal.Decimal {
	if value.IsZero() || step.IsZero() {
		return value
	}
	return value.Div(step).Floor().Mul(step)
}

func minPositiveBybitDecimal(values ...decimal.Decimal) decimal.Decimal {
	min := decimal.Zero
	for _, value := range values {
		if !value.IsPositive() {
			continue
		}
		if min.IsZero() || value.LessThan(min) {
			min = value
		}
	}
	return min
}

func maxPositiveBybitDecimal(values ...decimal.Decimal) decimal.Decimal {
	max := decimal.Zero
	for _, value := range values {
		if value.IsPositive() && value.GreaterThan(max) {
			max = value
		}
	}
	return max
}

func stopBybitRuntimeNode(t *testing.T, stop context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Bybit runtime node did not stop")
	}
}
