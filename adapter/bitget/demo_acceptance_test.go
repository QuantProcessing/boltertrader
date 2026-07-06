package bitget

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	btruntime "github.com/QuantProcessing/boltertrader/runtime"
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
	"github.com/shopspring/decimal"
)

func TestBitgetDemoSpotAcceptance(t *testing.T) {
	cfg := testenv.RequireBitgetDemoWrite(t)
	runBitgetAcceptance(t, "Bitget Demo Spot", cfg.APIKey, cfg.APISecret, cfg.Passphrase, cfg.Profile, cfg.SpotSymbol, enums.KindSpot, "", cfg.MaxNotionalUSDT)
}

func TestBitgetDemoUSDTPerpAcceptance(t *testing.T) {
	cfg := testenv.RequireBitgetDemoWrite(t)
	runBitgetAcceptance(t, "Bitget Demo USDT Perp", cfg.APIKey, cfg.APISecret, cfg.Passphrase, cfg.Profile, cfg.USDTPerpSymbol, enums.KindPerp, "USDT", cfg.MaxNotionalUSDT)
}

func TestBitgetDemoUSDCPerpAcceptance(t *testing.T) {
	cfg := testenv.RequireBitgetDemoWrite(t)
	runBitgetAcceptance(t, "Bitget Demo USDC Perp", cfg.APIKey, cfg.APISecret, cfg.Passphrase, cfg.Profile, cfg.USDCPerpSymbol, enums.KindPerp, "USDC", cfg.MaxNotionalUSDC)
}

func TestBitgetDemoSpotRuntimeAcceptance(t *testing.T) {
	cfg := testenv.RequireBitgetDemoWrite(t)
	runBitgetRuntimeAcceptance(t, "Bitget Demo Spot Runtime", cfg.APIKey, cfg.APISecret, cfg.Passphrase, cfg.Profile, cfg.SpotSymbol, enums.KindSpot, "", cfg.MaxNotionalUSDT)
}

func TestBitgetDemoUSDTPerpRuntimeAcceptance(t *testing.T) {
	cfg := testenv.RequireBitgetDemoWrite(t)
	runBitgetRuntimeAcceptance(t, "Bitget Demo USDT Perp Runtime", cfg.APIKey, cfg.APISecret, cfg.Passphrase, cfg.Profile, cfg.USDTPerpSymbol, enums.KindPerp, "USDT", cfg.MaxNotionalUSDT)
}

func TestBitgetDemoUSDCPerpRuntimeAcceptance(t *testing.T) {
	cfg := testenv.RequireBitgetDemoWrite(t)
	runBitgetRuntimeAcceptance(t, "Bitget Demo USDC Perp Runtime", cfg.APIKey, cfg.APISecret, cfg.Passphrase, cfg.Profile, cfg.USDCPerpSymbol, enums.KindPerp, "USDC", cfg.MaxNotionalUSDC)
}

func runBitgetAcceptance(t *testing.T, label, apiKey, apiSecret, passphrase string, profile testenv.BitgetEndpointProfile, symbol string, kind enums.InstrumentKind, settle string, maxNotional decimal.Decimal) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	adapter := newBitgetAcceptanceAdapter(t, ctx, apiKey, apiSecret, passphrase, profile)
	defer adapter.Close()
	id := requireBitgetAcceptanceInstrument(t, adapter, symbol, kind, settle)
	book, err := adapter.Market.OrderBook(ctx, id, 5)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label+" order book")
		t.Fatalf("%s order book: %v", label, err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("%s empty book for %s: %+v", label, symbol, book)
	}
	lifecycle := bitgetAcceptanceLifecycleSpec(t, adapter, label, id, book, maxNotional)
	state, err := adapter.acct.AccountState(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label+" account state")
		t.Fatalf("%s account state: %v", label, err)
	}
	if state.AccountID != AccountIDUnified {
		t.Fatalf("%s account id=%q, want %q", label, state.AccountID, AccountIDUnified)
	}
	ensureBitgetLifecycleFunds(t, label, adapter, state, lifecycle)
	if _, err := runtimeaccept.RunAdapterOrderLifecycle(ctx, adapter.Execution, lifecycle); err != nil {
		t.Fatalf("%s adapter order lifecycle: %v", label, err)
	}
}

func runBitgetRuntimeAcceptance(t *testing.T, label, apiKey, apiSecret, passphrase string, profile testenv.BitgetEndpointProfile, symbol string, kind enums.InstrumentKind, settle string, maxNotional decimal.Decimal) {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	adapter := newBitgetAcceptanceAdapter(t, ctx, apiKey, apiSecret, passphrase, profile)
	defer adapter.Close()
	id := requireBitgetAcceptanceInstrument(t, adapter, symbol, kind, settle)
	book, err := adapter.Market.OrderBook(ctx, id, 5)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label+" order book")
		t.Fatalf("%s order book: %v", label, err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("%s empty book for %s: %+v", label, symbol, book)
	}
	lifecycle := bitgetAcceptanceLifecycleSpec(t, adapter, label, id, book, maxNotional)
	node := btruntime.NewNode(
		btruntime.Clients{Market: adapter.Market, Execution: adapter.Execution, Account: adapter.Account},
		clock.NewRealClock(),
		AccountIDUnified,
		btruntime.WithAccountID(AccountIDUnified),
	)
	runtimeaccept.AttachAccountRequiredRisk(node, adapter.Market.InstrumentProvider())
	report, err := node.Resync(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label+" initial reconcile")
		t.Fatalf("%s initial reconcile: %v", label, err)
	}
	if report.AccountStatesApplied != 1 {
		t.Fatalf("%s account states applied=%d, want 1: %+v", label, report.AccountStatesApplied, report)
	}
	runtimeaccept.AssertAccountStateReady(t, node, AccountIDUnified, model.AccountMargin, kind)
	runtimeaccept.AssertOversizedOrderRejected(t, node, adapter.Market.InstrumentProvider(), id)
	if state, ok := node.Cache.Account(AccountIDUnified); ok {
		ensureBitgetLifecycleFunds(t, label, adapter, state.LastEvent(), lifecycle)
	}
	if err := adapter.Start(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, label+" private stream")
		t.Fatalf("%s private stream: %v", label, err)
	}
	lifecycle.PrivateStreamTopics = bitgetPrivateStreamTopics()
	t.Logf("%s private_stream_topics=%s", label, strings.Join(lifecycle.PrivateStreamTopics, ","))
	runCtx, stop := context.WithCancel(ctx)
	done := make(chan struct{})
	go func() {
		node.Run(runCtx)
		close(done)
	}()
	defer stopBitgetRuntimeNode(t, stop, done)
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

func newBitgetAcceptanceAdapter(t *testing.T, ctx context.Context, apiKey, apiSecret, passphrase string, profile testenv.BitgetEndpointProfile) *Adapter {
	t.Helper()
	httpClient, err := testenv.BitgetDemoHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Bitget HTTP client: %v", err)
	}
	adapter, err := New(ctx, Config{
		APIKey:     apiKey,
		APISecret:  apiSecret,
		Passphrase: passphrase,
		Environment: bitgetsdk.EnvironmentProfile{
			RESTBaseURL:     profile.RESTBaseURL,
			PublicWSURL:     profile.PublicWSURL,
			PrivateWSURL:    profile.PrivateWSURL,
			PAPTrading:      profile.PAPTrading,
			OfficialTestnet: profile.OfficialTestnet,
		},
		HTTPClient: httpClient,
		Categories: []string{"SPOT", bitgetsdk.ProductTypeUSDTFutures, bitgetsdk.ProductTypeUSDCFutures},
	})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Bitget adapter initialization")
		t.Fatalf("new Bitget adapter: %v", err)
	}
	return adapter
}

func requireBitgetAcceptanceInstrument(t *testing.T, adapter *Adapter, venueSymbol string, kind enums.InstrumentKind, settle string) model.InstrumentID {
	t.Helper()
	id, ok := adapter.provider.ResolveVenueInstrument(venueSymbol, kind, settle)
	if !ok {
		t.Fatalf("Bitget symbol %s kind=%s settle=%q not loaded", venueSymbol, kind, settle)
	}
	inst, ok := adapter.provider.Instrument(id)
	if !ok {
		t.Fatalf("Bitget instrument %s not available", id)
	}
	if inst.ID.Kind != kind {
		t.Fatalf("Bitget instrument %s kind=%s, want %s", id, inst.ID.Kind, kind)
	}
	if settle != "" && inst.Settle != settle {
		t.Fatalf("Bitget instrument %s settle=%q, want %q", id, inst.Settle, settle)
	}
	return id
}

func bitgetAcceptanceLifecycleSpec(t *testing.T, adapter *Adapter, label string, id model.InstrumentID, book *model.OrderBook, maxNotional decimal.Decimal) runtimeaccept.OrderLifecycleSpec {
	t.Helper()
	if book == nil || len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("%s empty order book for lifecycle: %+v", label, book)
	}
	inst, ok := adapter.provider.Instrument(id)
	if !ok {
		t.Fatalf("%s instrument %s not available", label, id)
	}
	restingPrice := floorBitgetAcceptanceDecimal(book.Bids[0].Price.Mul(decimal.RequireFromString("0.80")), inst.PriceTick)
	fillPrice := ceilBitgetAcceptanceDecimal(book.Asks[0].Price.Mul(decimal.RequireFromString("1.01")), inst.PriceTick)
	closePrice := floorBitgetAcceptanceDecimal(book.Bids[0].Price.Mul(decimal.RequireFromString("0.99")), inst.PriceTick)
	qty := bitgetAcceptanceQuantity(t, label, inst, maxNotional, minPositiveDecimal(restingPrice, fillPrice, closePrice), maxPositiveDecimal(restingPrice, fillPrice, closePrice))
	return runtimeaccept.OrderLifecycleSpec{
		Label:                 label,
		Venue:                 VenueName,
		Environment:           acceptanceEnvironment(label),
		Product:               acceptanceProduct(inst.ID.Kind, inst.Settle),
		AccountID:             AccountIDUnified,
		InstrumentID:          id,
		Quantity:              qty,
		RestingPrice:          restingPrice,
		FillPrice:             fillPrice,
		ClosePrice:            closePrice,
		PositionSide:          enums.PosNet,
		CloseAfterFill:        true,
		CleanExistingPosition: id.Kind != enums.KindSpot,
		Logf:                  t.Logf,
	}
}

func acceptanceEnvironment(label string) string {
	switch {
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
	case "USDT":
		return "USDT-linear Perp/SWAP"
	case "USDC":
		return "USDC-linear Perp/SWAP"
	default:
		return "Linear Perp/SWAP"
	}
}

func bitgetPrivateStreamTopics() []string {
	return []string{"UTA/order", "UTA/fill", "UTA/position", "UTA/account"}
}

func bitgetAcceptanceQuantity(t *testing.T, label string, inst *model.Instrument, maxNotional, minNotionalPrice, maxNotionalPrice decimal.Decimal) decimal.Decimal {
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
	if inst.MinNotional.IsPositive() && minNotionalPrice.IsPositive() {
		minByNotional := inst.MinNotional.Div(minNotionalPrice)
		if minByNotional.GreaterThan(qty) {
			qty = minByNotional
		}
	}
	qty = ceilBitgetAcceptanceDecimal(qty, step)
	if qty.Mul(maxNotionalPrice).GreaterThan(maxNotional) {
		t.Skipf("skipping %s: min lifecycle quantity %s notional %s exceeds max notional %s", label, qty, qty.Mul(maxNotionalPrice), maxNotional)
	}
	return qty
}

func minPositiveDecimal(values ...decimal.Decimal) decimal.Decimal {
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

func maxPositiveDecimal(values ...decimal.Decimal) decimal.Decimal {
	out := decimal.Zero
	for _, value := range values {
		if value.GreaterThan(out) {
			out = value
		}
	}
	return out
}

func ensureBitgetLifecycleFunds(t *testing.T, label string, adapter *Adapter, state model.AccountState, spec runtimeaccept.OrderLifecycleSpec) {
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
	for _, balance := range state.Balances {
		if balance.Currency == currency && balance.FreeOrAvailable().GreaterThanOrEqual(required) {
			return
		}
	}
	t.Skipf("skipping %s: no %s balance with available >= %s for lifecycle", label, currency, required)
}

func ceilBitgetAcceptanceDecimal(value, step decimal.Decimal) decimal.Decimal {
	if value.IsZero() || step.IsZero() {
		return value
	}
	return value.Div(step).Ceil().Mul(step)
}

func floorBitgetAcceptanceDecimal(value, step decimal.Decimal) decimal.Decimal {
	if value.IsZero() || step.IsZero() {
		return value
	}
	return value.Div(step).Floor().Mul(step)
}

func stopBitgetRuntimeNode(t *testing.T, stop context.CancelFunc, done <-chan struct{}) {
	t.Helper()
	stop()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatalf("Bitget runtime node did not stop")
	}
}
