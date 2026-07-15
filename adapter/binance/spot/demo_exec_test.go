package spot

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	"github.com/shopspring/decimal"
)

func TestBinanceSpotDemoExecAcceptance(t *testing.T) {
	testenv.RequireBinanceDemoWrite(t)
	runBinanceSpotDemoExecAcceptance(t)
}

func runBinanceSpotDemoExecAcceptance(t *testing.T) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	apiKey := os.Getenv("BINANCE_DEMO_API_KEY")
	apiSecret := os.Getenv("BINANCE_DEMO_API_SECRET")
	symbolInput := demoEnvOrDefault("BINANCE_DEMO_SYMBOL", "ETH-USDT")
	maxNotional := demoDecimalEnvOrDefault(t, "BINANCE_DEMO_MAX_NOTIONAL_USDT", demoDefaultMaxNotionalUSDT)
	configuredQty := demoDecimalEnvOrDefault(t, "BINANCE_DEMO_ORDER_QTY", "0")

	httpClient, err := demoHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("Demo HTTP client: %v", err)
	}
	adapter, err := New(ctx, Config{
		Demo:          true,
		DemoAPIKey:    apiKey,
		DemoAPISecret: apiSecret,
		HTTPClient:    httpClient,
	})
	if err != nil {
		t.Fatalf("new Binance Spot Demo adapter: %v", err)
	}
	defer adapter.Close()

	if err := adapter.Start(ctx); err != nil {
		t.Fatalf("start Binance Spot Demo adapter stream: %v", err)
	}
	execEvents := collectDemoExecEvents(adapter.Execution.Events())
	accountEvents := collectDemoAccountEvents(adapter.Account.Events())

	info, err := adapter.rest.ExchangeInfo(ctx)
	if err != nil {
		t.Fatalf("exchange info: %v", err)
	}
	spec, err := demoAcceptanceSymbolSpecFromExchangeInfo(info, symbolInput)
	if err != nil {
		t.Fatalf("resolve Spot Demo symbol: %v", err)
	}
	instID := adapter.provider.resolveVenueSymbol(spec.VenueSymbol)

	ticker, err := adapter.rest.BookTicker(ctx, spec.VenueSymbol)
	if err != nil {
		t.Fatalf("bookTicker: %v", err)
	}
	bid := dec(ticker.BidPrice)
	ask := dec(ticker.AskPrice)
	if bid.LessThanOrEqual(decimal.Zero) || ask.LessThanOrEqual(decimal.Zero) {
		t.Fatalf("non-positive Spot Demo bookTicker for %s: %+v", spec.VenueSymbol, ticker)
	}
	restingPrice := floorDecimalToStep(bid.Mul(decimal.RequireFromString("0.80")), spec.PriceTick)
	if restingPrice.LessThanOrEqual(decimal.Zero) {
		t.Fatalf("computed non-positive resting price %s from bid %s", restingPrice, bid)
	}
	fillPrice := ceilDecimalToStep(ask.Mul(decimal.RequireFromString("1.01")), spec.PriceTick)
	if fillPrice.LessThanOrEqual(decimal.Zero) {
		t.Fatalf("computed non-positive IOC fill price %s from ask %s", fillPrice, ask)
	}
	qty, err := selectDemoAcceptanceOrderQuantityForPriceBand(spec, configuredQty, maxNotional, restingPrice, fillPrice)
	if err != nil {
		t.Fatalf("select safe Spot Demo order quantity: %v", err)
	}

	if open, err := adapter.Execution.OpenOrders(ctx, instID); err != nil {
		t.Fatalf("open order preflight: %v", err)
	} else if len(open) > 0 {
		t.Fatalf("Binance Spot Demo acceptance requires a clean account: %s already has %d open order(s); clean the Demo account before running", spec.VenueSymbol, len(open))
	}

	startBalances, err := demoSpotBalances(ctx, adapter)
	if err != nil {
		t.Fatalf("balance preflight: %v", err)
	}
	startBaseAvailable := startBalances[spec.BaseCurrency].Free
	startBaseTotal := startBalances[spec.BaseCurrency].Total
	quoteAvailable := startBalances[spec.QuoteCurrency].Free
	requiredQuote := qty.Mul(fillPrice).Mul(decimal.RequireFromString("1.05"))
	if quoteAvailable.LessThan(requiredQuote) {
		t.Fatalf("Binance Spot Demo acceptance has insufficient funds: %s available %s below required %s for %s quantity %s at bounded fill price %s", spec.QuoteCurrency, quoteAvailable, requiredQuote, spec.VenueSymbol, qty, fillPrice)
	}

	cleanup := newDemoAcceptanceCleanupState(spec, qty)
	defer func() {
		if !cleanup.Needed() {
			return
		}
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancelCleanup()
		if err := cleanupBinanceSpotDemoAcceptance(cleanupCtx, adapter, instID, spec, startBaseAvailable, startBaseTotal, maxNotional, &cleanup); err != nil {
			t.Fatalf("%v\n%s", err, cleanup.Metadata().Remediation())
		}
	}()

	restingClientID := demoClientOrderID("rest")
	cleanup.Arm(enums.SideBuy, restingClientID)
	resting, err := adapter.Execution.Submit(ctx, model.OrderRequest{
		InstrumentID: instID,
		ClientID:     restingClientID,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifGTX,
		Quantity:     qty,
		Price:        restingPrice,
		PositionSide: enums.PosNet,
	})
	if err != nil {
		t.Fatalf("submit Spot Demo resting order (outcome ambiguous; only a known venue order can be canceled): %v\n%s", err, cleanup.Metadata().Remediation())
	}
	cleanup.RecordVenueOrderID(resting.VenueOrderID)
	if resting.Status == enums.StatusFilled || !resting.FilledQty.IsZero() {
		cleanup.ConfirmFill(resting.FilledQty)
		t.Fatalf("resting place/cancel order unexpectedly filled: %+v\n%s", resting, cleanup.Metadata().Remediation())
	}
	if err := adapter.Execution.Cancel(ctx, instID, resting.VenueOrderID); err != nil {
		t.Fatalf("cancel Spot Demo resting order %s: %v\n%s", resting.VenueOrderID, err, cleanup.Metadata().Remediation())
	}
	restingFinal, err := waitForDemoSpotOrderStatus(ctx, adapter.rest, spec.VenueSymbol, restingClientID, "CANCELED")
	if err != nil {
		t.Fatalf("wait for resting order cancel: %v\n%s", err, cleanup.Metadata().Remediation())
	}
	cleanup.MarkOrderTerminal(resting.VenueOrderID)
	if partialQty := dec(restingFinal.ExecutedQty); partialQty.IsPositive() {
		cleanup.ConfirmFill(partialQty)
		t.Fatalf("resting order cancellation reported unexpected executed quantity %s\n%s", partialQty, cleanup.Metadata().Remediation())
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, instID); err != nil {
		t.Fatalf("resting cancellation did not produce authoritative no-open state: %v\n%s", err, cleanup.Metadata().Remediation())
	}

	fillClientID := demoClientOrderID("fill")
	cleanup.Arm(enums.SideBuy, fillClientID)
	filled, err := adapter.Execution.Submit(ctx, demoFillOrderRequest(instID, fillClientID, qty, fillPrice))
	if err != nil {
		t.Fatalf("submit Spot Demo fill order (outcome ambiguous; no automatic close attempted): %v\n%s", err, cleanup.Metadata().Remediation())
	}
	cleanup.RecordVenueOrderID(filled.VenueOrderID)
	filledResp, err := waitForDemoSpotOrderStatus(ctx, adapter.rest, spec.VenueSymbol, fillClientID, binanceSpotDemoTerminalStatuses...)
	if err != nil {
		t.Fatalf("wait for authoritative fill order terminal status: %v\n%s", err, cleanup.Metadata().Remediation())
	}
	filledQty, err := validateBinanceSpotDemoFill(filledResp, maxNotional)
	cleanup.MarkOrderTerminal(filled.VenueOrderID)
	cleanup.ConfirmFill(filledQty)
	if err != nil {
		t.Fatalf("validate bounded Spot Demo fill: %v\n%s", err, cleanup.Metadata().Remediation())
	}
	if err := waitForDemoExecObservation(ctx, execEvents, fillClientID, filled.VenueOrderID); err != nil {
		t.Fatalf("adapter execution stream did not observe Spot Demo fill: %v", err)
	}
	observationThreshold := demoSpotFillObservationThreshold(filledQty, spec.SizeStep)
	if err := waitForDemoSpotBalanceObservation(ctx, accountEvents, spec.BaseCurrency, startBaseTotal, observationThreshold); err != nil {
		t.Fatalf("adapter account stream did not observe Spot Demo base balance update: %v", err)
	}
	baseDelta, err := waitForDemoSpotBalanceDelta(ctx, adapter, spec.BaseCurrency, startBaseTotal, observationThreshold)
	if err != nil {
		t.Fatalf("wait for opened Spot Demo base balance: %v", err)
	}
	cleanup.SetBaseDelta(baseDelta)

	closeClientID := demoClientOrderID("close")
	closed, err := closeBinanceSpotDemoBaseDelta(ctx, adapter, instID, spec, startBaseAvailable, &cleanup, closeClientID)
	if err != nil {
		t.Fatalf("close Spot Demo base delta (not retried because outcome may be ambiguous): %v\n%s", err, cleanup.Metadata().Remediation())
	}
	if closed != nil {
		cleanup.RecordVenueOrderID(closed.VenueOrderID)
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, instID); err != nil {
		t.Fatalf("wait for no Spot Demo open orders: %v\n%s", err, cleanup.Metadata().Remediation())
	}
	if err := waitForDemoSpotBaseDeltaBelowStep(ctx, adapter, spec, startBaseAvailable); err != nil {
		t.Fatalf("wait for Spot Demo base delta cleanup: %v\n%s", err, cleanup.Metadata().Remediation())
	}
	if closed != nil {
		cleanup.MarkOrderTerminal(closed.VenueOrderID)
	}
	cleanup.MarkClean()
}
