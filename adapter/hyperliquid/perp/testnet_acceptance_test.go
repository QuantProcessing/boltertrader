package perp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/adapter/hyperliquid/internal/accepttest"
	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	sdk "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	"github.com/shopspring/decimal"
)

func TestHyperliquidPerpTestnetReadAcceptance(t *testing.T) {
	cfg := testenv.RequireHyperliquidTestnetRead(t)

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	httpClient, err := testenv.HyperliquidTestnetHTTPClient(30 * time.Second)
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
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Perp Testnet adapter initialization")
		t.Fatalf("new Hyperliquid Perp Testnet adapter: %v", err)
	}
	defer adapter.Close()

	inst := selectPerpTestnetInstrument(t, adapter, cfg.PerpSymbol)
	book, err := adapter.Market.OrderBook(ctx, inst.ID, 5)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Perp Testnet order book")
		t.Fatalf("order book: %v", err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("empty Hyperliquid Perp Testnet book for %s", inst.VenueSymbol)
	}
	if _, err := adapter.Market.Bars(ctx, inst.ID, "1m", 5); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Perp Testnet candles")
		t.Fatalf("candles: %v", err)
	}
	market := adapter.Market.(*marketDataClient)
	if funding, err := market.FundingRate(ctx, inst.ID); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Perp Testnet funding")
		t.Fatalf("funding: %v", err)
	} else if funding.InstrumentID != inst.ID {
		t.Fatalf("funding instrument=%s, want %s", funding.InstrumentID, inst.ID)
	}
}

func TestHyperliquidPerpTestnetHIP3ReadAcceptance(t *testing.T) {
	cfg := testenv.RequireHyperliquidTestnetRead(t)
	if cfg.HIP3Symbol == "" {
		t.Skipf("skipping Hyperliquid HIP-3 Testnet acceptance: set %s to a dex-qualified symbol such as dex:coin or dex:coin-USDC", testenv.HyperliquidTestnetHIP3SymbolEnv)
	}
	dex, _, ok := strings.Cut(cfg.HIP3Symbol, ":")
	if !ok || dex == "" {
		t.Fatalf("%s must include a dex qualifier, got %q", testenv.HyperliquidTestnetHIP3SymbolEnv, cfg.HIP3Symbol)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	httpClient, err := testenv.HyperliquidTestnetHTTPClient(30 * time.Second)
	if err != nil {
		t.Fatalf("Hyperliquid Testnet HTTP client: %v", err)
	}
	adapter, err := New(ctx, Config{
		PrivateKey:     cfg.PrivateKey,
		AccountAddress: cfg.AccountAddress,
		VaultAddress:   cfg.VaultAddress,
		Environment:    sdk.EnvironmentTestnet,
		HTTPClient:     httpClient,
		IncludeHIP3:    true,
		HIP3Dexes:      []string{dex},
	})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid HIP-3 Testnet adapter initialization")
		t.Fatalf("new Hyperliquid HIP-3 Testnet adapter: %v", err)
	}
	defer adapter.Close()

	inst := selectPerpTestnetInstrument(t, adapter, cfg.HIP3Symbol)
	book, err := adapter.Market.OrderBook(ctx, inst.ID, 5)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid HIP-3 Testnet order book")
		t.Fatalf("order book: %v", err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("empty Hyperliquid HIP-3 Testnet book for %s", inst.VenueSymbol)
	}
}

func TestHyperliquidPerpTestnetWriteAcceptance(t *testing.T) {
	cfg := testenv.RequireHyperliquidTestnetWrite(t)
	runHyperliquidPerpTestnetAdapterAcceptance(t, cfg, false, nil, cfg.PerpSymbol, "standard Perp")
}

func TestHyperliquidPerpTestnetHIP3WriteAcceptance(t *testing.T) {
	cfg := testenv.RequireHyperliquidTestnetWrite(t)
	if cfg.HIP3Symbol == "" {
		t.Fatalf("Hyperliquid HIP-3 Testnet write acceptance requires %s with a dex-qualified symbol", testenv.HyperliquidTestnetHIP3SymbolEnv)
	}
	dex, _, ok := strings.Cut(cfg.HIP3Symbol, ":")
	if !ok || strings.TrimSpace(dex) == "" {
		t.Fatalf("%s must include a dex qualifier, got %q", testenv.HyperliquidTestnetHIP3SymbolEnv, cfg.HIP3Symbol)
	}
	runHyperliquidPerpTestnetAdapterAcceptance(t, cfg, true, []string{dex}, cfg.HIP3Symbol, "HIP-3 Perp")
}

func runHyperliquidPerpTestnetAdapterAcceptance(
	t *testing.T,
	cfg testenv.HyperliquidTestnetConfig,
	includeHIP3 bool,
	hip3Dexes []string,
	desired, product string,
) {
	t.Helper()
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
		IncludeHIP3:    includeHIP3,
		HIP3Dexes:      hip3Dexes,
	})
	if err != nil {
		t.Fatalf("new Hyperliquid %s Testnet adapter: %v", product, err)
	}
	defer adapter.Close()

	inst := selectPerpTestnetInstrument(t, adapter, desired)
	if open, err := adapter.Execution.OpenOrders(ctx, inst.ID); err != nil {
		t.Fatalf("open order preflight: %v", err)
	} else if len(open) > 0 {
		t.Fatalf("unsafe pre-existing state: Hyperliquid %s Testnet %s already has %d open order(s); clean the testnet account first", product, inst.VenueSymbol, len(open))
	}
	book, err := adapter.Market.OrderBook(ctx, inst.ID, 5)
	if err != nil {
		t.Fatalf("order book: %v", err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("empty Hyperliquid %s Testnet book for %s", product, inst.VenueSymbol)
	}
	lifecycle := hyperliquidPerpTestnetLifecycleSpec(t, "Hyperliquid "+product+" Testnet adapter", product, adapter.acct.accountID, inst, book, cfg.MaxNotionalUSDC, adapter.acct)
	requirePerpTestnetCollateral(t, ctx, adapter, product, inst, lifecycle.Quantity, lifecycle.FillPrice)
	if err := adapter.Start(ctx); err != nil {
		t.Fatalf("start Hyperliquid %s Testnet private stream: %v", product, err)
	}
	evidenceCtx, stopEvidence := context.WithCancel(ctx)
	defer stopEvidence()
	evidence := observeHyperliquidPerpPrivateExec(evidenceCtx, adapter.Execution.Events())
	result, err := runtimeaccept.RunAdapterOrderLifecycle(ctx, adapter.Execution, lifecycle)
	if err != nil {
		t.Fatalf("Hyperliquid %s Testnet adapter lifecycle: %v", product, err)
	}
	requireHyperliquidPrivateLifecycleEvidence(t, ctx, evidence, result)
}

func selectPerpTestnetInstrument(t *testing.T, adapter *Adapter, desired string) *model.Instrument {
	t.Helper()
	all := adapter.Market.InstrumentProvider().All()
	if len(all) == 0 {
		t.Fatal("Hyperliquid Perp Testnet returned no perp instruments")
	}
	if desired != "" {
		for _, inst := range all {
			if matchesPerpTestnetSymbol(inst, desired) {
				return inst
			}
		}
		t.Fatalf("configured Hyperliquid Perp Testnet symbol %q not loaded", desired)
	}
	return all[0]
}

func matchesPerpTestnetSymbol(inst *model.Instrument, desired string) bool {
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
	withoutKind := strings.TrimSuffix(desired, "-PERP")
	if strings.EqualFold(inst.ID.Symbol, withoutKind) {
		return true
	}
	if inst.Settle != "" {
		rawFromNeutral := strings.TrimSuffix(withoutKind, "-"+inst.Settle)
		if rawFromNeutral != withoutKind && strings.EqualFold(inst.VenueSymbol, rawFromNeutral) {
			return true
		}
	}
	legacyNeutral := strings.ReplaceAll(desired, ":", "-")
	return strings.EqualFold(inst.ID.Symbol, legacyNeutral)
}

func TestMatchesPerpTestnetSymbolAcceptsHIP3RawAndNeutralForms(t *testing.T) {
	inst := &model.Instrument{
		ID:          model.InstrumentID{Venue: venueName, Symbol: "xyz:TSLA-USDC", Kind: enums.KindPerp},
		VenueSymbol: "xyz:TSLA",
		Settle:      "USDC",
	}
	for _, desired := range []string{"xyz:TSLA", "xyz:TSLA-USDC", "xyz:TSLA-USDC-PERP"} {
		if !matchesPerpTestnetSymbol(inst, desired) {
			t.Fatalf("desired %q did not match %+v", desired, inst)
		}
	}
	if matchesPerpTestnetSymbol(inst, "stocks:TSLA-USDC") {
		t.Fatalf("different HIP-3 dex must not match")
	}
}

func selectHyperliquidPerpTestnetQuantity(inst *model.Instrument, maxNotional, price decimal.Decimal) (decimal.Decimal, error) {
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

var hyperliquidPerpAcceptanceMinNotional = decimal.NewFromInt(10)

func hyperliquidPerpTestnetLifecycleSpec(
	t *testing.T,
	label, product, accountID string,
	inst *model.Instrument,
	book *model.OrderBook,
	maxNotional decimal.Decimal,
	reporter runtimeaccept.PerpPositionReporter,
) runtimeaccept.OrderLifecycleSpec {
	t.Helper()
	if inst == nil || book == nil || len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("%s requires an instrument and two-sided order book", label)
	}
	restingPrice := accepttest.RestingBuyPrice(inst, book.Bids[0].Price, false)
	fillPrice := accepttest.RoundDownOrderPrice(book.Asks[0].Price.Mul(decimal.RequireFromString("1.01")), false, hyperliquidPerpSizeDecimals(inst))
	closePrice := accepttest.RoundDownOrderPrice(book.Bids[0].Price.Mul(decimal.RequireFromString("0.99")), false, hyperliquidPerpSizeDecimals(inst))
	if !restingPrice.IsPositive() || fillPrice.LessThan(book.Asks[0].Price) || !closePrice.IsPositive() || closePrice.GreaterThan(book.Bids[0].Price) {
		t.Fatalf("%s unsafe lifecycle prices resting=%s fill=%s ask=%s close=%s bid=%s", label, restingPrice, fillPrice, book.Asks[0].Price, closePrice, book.Bids[0].Price)
	}
	withMinimum := *inst
	if withMinimum.MinNotional.LessThan(hyperliquidPerpAcceptanceMinNotional) {
		withMinimum.MinNotional = hyperliquidPerpAcceptanceMinNotional
	}
	qty, err := selectHyperliquidPerpTestnetQuantity(&withMinimum, maxNotional, fillPrice)
	if err != nil {
		t.Fatalf("%s quantity selection: %v", label, err)
	}
	spec := runtimeaccept.OrderLifecycleSpec{
		Label:                label,
		Venue:                venueName,
		Environment:          "Testnet",
		Product:              product + " settle=" + strings.TrimSpace(inst.Settle),
		AccountID:            accountID,
		InstrumentID:         inst.ID,
		Quantity:             qty,
		CleanupPositionLimit: qty,
		RestingPrice:         restingPrice,
		FillPrice:            fillPrice,
		ClosePrice:           closePrice,
		PositionSide:         enums.PosNet,
		CloseAfterFill:       true,
		PrivateStreamTopics:  hyperliquidPerpPrivateStreamTopics(reporter),
		PollRequestTimeout:   8 * time.Second,
		CleanupTimeout:       60 * time.Second,
		Logf:                 t.Logf,
	}
	return runtimeaccept.ConfigurePerpPositionReporter(spec, reporter)
}

func hyperliquidPerpPrivateStreamTopics(reporter runtimeaccept.PerpPositionReporter) []string {
	topics := []string{"orderUpdates", "userFills", "clearinghouseState"}
	if acct, ok := reporter.(*accountClient); ok && acct != nil && acct.accountMode.UsesSpotClearinghouseState() {
		topics = append(topics, "spotState")
	}
	return topics
}

func hyperliquidPerpSizeDecimals(inst *model.Instrument) int {
	if inst == nil || !inst.SizeStep.IsPositive() || inst.SizeStep.Exponent() >= 0 {
		return 0
	}
	return int(-inst.SizeStep.Exponent())
}

type hyperliquidPerpPrivateExecEvidence struct {
	mu     sync.RWMutex
	events []contract.ExecEnvelope
}

func observeHyperliquidPerpPrivateExec(ctx context.Context, events <-chan contract.ExecEnvelope) *hyperliquidPerpPrivateExecEvidence {
	evidence := &hyperliquidPerpPrivateExecEvidence{}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case event, ok := <-events:
				if !ok {
					return
				}
				evidence.mu.Lock()
				evidence.events = append(evidence.events, event)
				evidence.mu.Unlock()
			}
		}
	}()
	return evidence
}

func requireHyperliquidPrivateLifecycleEvidence(t *testing.T, ctx context.Context, evidence *hyperliquidPerpPrivateExecEvidence, result *runtimeaccept.OrderLifecycleResult) {
	t.Helper()
	if evidence == nil || result == nil {
		t.Fatal("Hyperliquid private lifecycle evidence and result are required")
	}
	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()
	for {
		if evidence.hasOrderAndFill(result.Filled) && evidence.hasOrderAndFill(result.Closed) {
			return
		}
		select {
		case <-ctx.Done():
			t.Fatalf("private order/fill evidence incomplete for fill=%s close=%s: %v", result.Filled.VenueOrderID, result.Closed.VenueOrderID, ctx.Err())
		case <-ticker.C:
		}
	}
}

func (e *hyperliquidPerpPrivateExecEvidence) hasOrderAndFill(target model.Order) bool {
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

func hyperliquidPerpLifecycleIdentityMatches(clientID, venueOrderID string, target model.Order) bool {
	return (clientID != "" && clientID == target.Request.ClientID) || (venueOrderID != "" && venueOrderID == target.VenueOrderID)
}
