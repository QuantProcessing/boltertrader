package spot

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

func TestHyperliquidSpotTestnetReadAcceptance(t *testing.T) {
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
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Spot Testnet adapter initialization")
		t.Fatalf("new Hyperliquid Spot Testnet adapter: %v", err)
	}
	defer adapter.Close()

	inst := selectSpotTestnetInstrument(t, adapter, cfg.SpotSymbol)
	book, err := adapter.Market.OrderBook(ctx, inst.ID, 5)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Spot Testnet order book")
		t.Fatalf("order book: %v", err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("empty Hyperliquid Spot Testnet book for %s", inst.VenueSymbol)
	}
	if _, err := adapter.Market.Bars(ctx, inst.ID, "1m", 5); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Hyperliquid Spot Testnet candles")
		t.Fatalf("candles: %v", err)
	}
}

func TestHyperliquidSpotTestnetWriteAcceptance(t *testing.T) {
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
		t.Fatalf("new Hyperliquid Spot Testnet adapter: %v", err)
	}
	defer adapter.Close()

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
		t.Fatalf("empty Hyperliquid Spot Testnet book for %s", inst.VenueSymbol)
	}
	lifecycle := hyperliquidSpotTestnetLifecycleSpec(
		t,
		"Hyperliquid Spot Testnet adapter",
		adapter.acct.accountID,
		inst,
		book,
		cfg.MaxNotionalUSDC,
		adapter.acct,
	)
	requireSpotTestnetCash(t, ctx, adapter, inst, lifecycle.Quantity, lifecycle.FillPrice)
	if err := adapter.Start(ctx); err != nil {
		t.Fatalf("start Hyperliquid Spot Testnet private stream: %v", err)
	}
	evidenceCtx, stopEvidence := context.WithCancel(ctx)
	defer stopEvidence()
	evidence := observeHyperliquidPrivateExec(evidenceCtx, adapter.Execution.Events())
	result, err := runtimeaccept.RunAdapterOrderLifecycle(ctx, adapter.Execution, lifecycle)
	if err != nil {
		t.Fatalf("Hyperliquid Spot Testnet adapter lifecycle: %v", err)
	}
	requireHyperliquidPrivateLifecycleEvidence(t, ctx, evidence, result)
}

func selectSpotTestnetInstrument(t *testing.T, adapter *Adapter, desired string) *model.Instrument {
	t.Helper()
	all := adapter.Market.InstrumentProvider().All()
	if len(all) == 0 {
		t.Fatal("Hyperliquid Spot Testnet returned no spot instruments")
	}
	if desired != "" {
		for _, inst := range all {
			if strings.EqualFold(inst.VenueSymbol, desired) || strings.EqualFold(inst.ID.Symbol, strings.ReplaceAll(desired, "/", "-")) {
				return inst
			}
		}
		t.Fatalf("configured Hyperliquid Spot Testnet symbol %q not loaded", desired)
	}
	return all[0]
}

func requireSpotTestnetWriteInstrument(t *testing.T, adapter *Adapter, desired string) *model.Instrument {
	t.Helper()
	return selectSpotTestnetInstrument(t, adapter, desired)
}

func selectHyperliquidTestnetQuantity(inst *model.Instrument, maxNotional, price decimal.Decimal) (decimal.Decimal, error) {
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
		step = decimal.NewFromInt(1)
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

var (
	hyperliquidAcceptanceMinNotional = decimal.NewFromInt(10)
	hyperliquidSpotCloseBuffer       = decimal.RequireFromString("0.995")
)

type accountStateSource interface {
	AccountState(context.Context) (model.AccountState, error)
}

func hyperliquidSpotTestnetLifecycleSpec(
	t *testing.T,
	label, accountID string,
	inst *model.Instrument,
	book *model.OrderBook,
	maxNotional decimal.Decimal,
	reporter accountStateSource,
) runtimeaccept.OrderLifecycleSpec {
	t.Helper()
	if inst == nil || book == nil || len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("%s requires an instrument and two-sided order book", label)
	}
	restingPrice := accepttest.RestingBuyPrice(inst, book.Bids[0].Price, true)
	fillPrice := accepttest.RoundDownOrderPrice(book.Asks[0].Price.Mul(decimal.RequireFromString("1.01")), true, hyperliquidSizeDecimals(inst))
	closePrice := accepttest.RoundDownOrderPrice(book.Bids[0].Price.Mul(decimal.RequireFromString("0.99")), true, hyperliquidSizeDecimals(inst))
	if !restingPrice.IsPositive() || fillPrice.LessThan(book.Asks[0].Price) || !closePrice.IsPositive() || closePrice.GreaterThan(book.Bids[0].Price) {
		t.Fatalf("%s unsafe lifecycle prices resting=%s fill=%s ask=%s close=%s bid=%s", label, restingPrice, fillPrice, book.Asks[0].Price, closePrice, book.Bids[0].Price)
	}
	qty, closeQty, minNotional, err := selectHyperliquidSpotLifecycleQuantity(inst, maxNotional, fillPrice, closePrice)
	if err != nil {
		t.Fatalf("%s quantity selection: %v", label, err)
	}
	step := hyperliquidSizeStep(inst)
	minQty := inst.MinQty
	if !minQty.IsPositive() {
		minQty = step
	}
	spec := runtimeaccept.OrderLifecycleSpec{
		Label:               label,
		Venue:               venueName,
		Environment:         "Testnet",
		Product:             "Spot cash",
		AccountID:           accountID,
		InstrumentID:        inst.ID,
		Quantity:            qty,
		CloseQuantity:       closeQty,
		RestingPrice:        restingPrice,
		FillPrice:           fillPrice,
		ClosePrice:          closePrice,
		PositionSide:        enums.PosNet,
		CloseAfterFill:      true,
		PrivateStreamTopics: []string{"orderUpdates", "userFills", "spotState"},
		PollRequestTimeout:  8 * time.Second,
		CleanupTimeout:      60 * time.Second,
		Logf:                t.Logf,
	}
	return runtimeaccept.ConfigureSpotBalanceGuard(spec, reporter, inst.Base, step, minQty, minNotional, qty.Sub(closeQty))
}

func selectHyperliquidSpotLifecycleQuantity(inst *model.Instrument, maxNotional, fillPrice, closePrice decimal.Decimal) (decimal.Decimal, decimal.Decimal, decimal.Decimal, error) {
	if inst == nil || !closePrice.IsPositive() {
		return decimal.Zero, decimal.Zero, decimal.Zero, fmt.Errorf("instrument and positive close price are required")
	}
	minNotional := inst.MinNotional
	if minNotional.LessThan(hyperliquidAcceptanceMinNotional) {
		minNotional = hyperliquidAcceptanceMinNotional
	}
	withMinimum := *inst
	withMinimum.MinNotional = minNotional
	qty, err := selectHyperliquidTestnetQuantity(&withMinimum, maxNotional, fillPrice)
	if err != nil {
		return decimal.Zero, decimal.Zero, decimal.Zero, err
	}
	step := hyperliquidSizeStep(inst)
	minCloseQty := inst.MinQty
	if !minCloseQty.IsPositive() {
		minCloseQty = step
	}
	if byNotional := minNotional.Div(closePrice); byNotional.GreaterThan(minCloseQty) {
		minCloseQty = byNotional
	}
	minCloseQty = ceilHyperliquidQuantity(minCloseQty, step)
	minBuyQty := ceilHyperliquidQuantity(minCloseQty.Div(hyperliquidSpotCloseBuffer), step)
	if minBuyQty.GreaterThan(qty) {
		qty = minBuyQty
	}
	if notional := qty.Mul(fillPrice); notional.GreaterThan(maxNotional) {
		return decimal.Zero, decimal.Zero, decimal.Zero, fmt.Errorf("minimum buffered Spot lifecycle notional %s exceeds max notional %s", notional, maxNotional)
	}
	closeQty := floorHyperliquidQuantity(qty.Mul(hyperliquidSpotCloseBuffer), step)
	if closeQty.LessThan(minCloseQty) || closeQty.Mul(closePrice).LessThan(minNotional) {
		return decimal.Zero, decimal.Zero, decimal.Zero, fmt.Errorf("fee-buffered close quantity %s is below venue minimum at price %s", closeQty, closePrice)
	}
	return qty, closeQty, minNotional, nil
}

func hyperliquidSizeStep(inst *model.Instrument) decimal.Decimal {
	if inst != nil && inst.SizeStep.IsPositive() {
		return inst.SizeStep
	}
	return decimal.NewFromInt(1)
}

func hyperliquidSizeDecimals(inst *model.Instrument) int {
	step := hyperliquidSizeStep(inst)
	if step.Exponent() < 0 {
		return int(-step.Exponent())
	}
	return 0
}

func ceilHyperliquidQuantity(value, step decimal.Decimal) decimal.Decimal {
	return value.Div(step).Ceil().Mul(step)
}

func floorHyperliquidQuantity(value, step decimal.Decimal) decimal.Decimal {
	return value.Div(step).Floor().Mul(step)
}

type hyperliquidPrivateExecEvidence struct {
	mu     sync.RWMutex
	events []contract.ExecEnvelope
}

func observeHyperliquidPrivateExec(ctx context.Context, events <-chan contract.ExecEnvelope) *hyperliquidPrivateExecEvidence {
	evidence := &hyperliquidPrivateExecEvidence{}
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

func requireHyperliquidPrivateLifecycleEvidence(t *testing.T, ctx context.Context, evidence *hyperliquidPrivateExecEvidence, result *runtimeaccept.OrderLifecycleResult) {
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

func (e *hyperliquidPrivateExecEvidence) hasOrderAndFill(target model.Order) bool {
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

func hyperliquidLifecycleIdentityMatches(clientID, venueOrderID string, target model.Order) bool {
	return (clientID != "" && clientID == target.Request.ClientID) || (venueOrderID != "" && venueOrderID == target.VenueOrderID)
}
