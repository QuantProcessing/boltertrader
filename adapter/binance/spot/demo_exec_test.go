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

func TestBinanceSpotDemoExecE2E(t *testing.T) {
	testenv.RequireBinanceDemoWrite(t)
	runBinanceSpotDemoExecE2E(t)
}

func runBinanceSpotDemoExecE2E(t *testing.T) {
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
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance Spot Demo adapter initialization")
		t.Fatalf("new Binance Spot Demo adapter: %v", err)
	}
	defer adapter.Close()

	if err := adapter.Start(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance Spot Demo user-data stream")
		t.Fatalf("start Binance Spot Demo adapter stream: %v", err)
	}
	execEvents := collectDemoExecEvents(adapter.Execution.Events())
	accountEvents := collectDemoAccountEvents(adapter.Account.Events())

	info, err := adapter.rest.ExchangeInfo(ctx)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance Spot Demo exchangeInfo")
		t.Fatalf("exchange info: %v", err)
	}
	spec, err := demoE2ESymbolSpecFromExchangeInfo(info, symbolInput)
	if err != nil {
		t.Fatalf("resolve Spot Demo symbol: %v", err)
	}
	instID := adapter.provider.resolveVenueSymbol(spec.VenueSymbol)

	ticker, err := adapter.rest.BookTicker(ctx, spec.VenueSymbol)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance Spot Demo bookTicker")
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
	qty, err := selectDemoE2EOrderQuantityForPriceBand(spec, configuredQty, maxNotional, restingPrice, ask)
	if err != nil {
		t.Fatalf("select safe Spot Demo order quantity: %v", err)
	}

	if open, err := adapter.Execution.OpenOrders(ctx, instID); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance Spot Demo open order preflight")
		t.Fatalf("open order preflight: %v", err)
	} else if len(open) > 0 {
		t.Skipf("skipping Binance Spot Demo E2E: %s already has %d open order(s); clean the Demo account before running", spec.VenueSymbol, len(open))
	}

	startBalances, err := demoSpotBalances(ctx, adapter)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "Binance Spot Demo balance preflight")
		t.Fatalf("balance preflight: %v", err)
	}
	startBaseAvailable := startBalances[spec.BaseCurrency].Available
	startBaseTotal := startBalances[spec.BaseCurrency].Total
	quoteAvailable := startBalances[spec.QuoteCurrency].Available
	requiredQuote := qty.Mul(ask).Mul(decimal.RequireFromString("1.05"))
	if quoteAvailable.LessThan(requiredQuote) {
		t.Skipf("skipping Binance Spot Demo E2E: %s available %s below required %s for %s quantity %s at ask %s", spec.QuoteCurrency, quoteAvailable, requiredQuote, spec.VenueSymbol, qty, ask)
	}

	cleanup := newDemoE2ECleanupState(spec, qty)
	defer func() {
		if !cleanup.Needed() {
			return
		}
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancelCleanup()
		meta := cleanup.Metadata()
		if err := cleanupBinanceSpotDemoE2E(cleanupCtx, adapter, instID, spec, startBaseAvailable, &meta); err != nil {
			t.Fatalf("%v\n%s", err, meta.Remediation())
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
		t.Fatalf("submit Spot Demo resting order: %v", err)
	}
	cleanup.RecordVenueOrderID(resting.VenueOrderID)
	if resting.Status == enums.StatusFilled || !resting.FilledQty.IsZero() {
		t.Fatalf("resting place/cancel order unexpectedly filled: %+v", resting)
	}
	if err := adapter.Execution.Cancel(ctx, instID, resting.VenueOrderID); err != nil {
		t.Fatalf("cancel Spot Demo resting order %s: %v", resting.VenueOrderID, err)
	}
	if _, err := waitForDemoSpotOrderStatus(ctx, adapter.rest, spec.VenueSymbol, restingClientID, "CANCELED"); err != nil {
		t.Fatalf("wait for resting order cancel: %v", err)
	}

	fillClientID := demoClientOrderID("fill")
	cleanup.Arm(enums.SideBuy, fillClientID)
	filled, err := adapter.Execution.Submit(ctx, model.OrderRequest{
		InstrumentID: instID,
		ClientID:     fillClientID,
		Side:         enums.SideBuy,
		Type:         enums.TypeMarket,
		Quantity:     qty,
		PositionSide: enums.PosNet,
	})
	if err != nil {
		t.Fatalf("submit Spot Demo fill order: %v", err)
	}
	cleanup.RecordVenueOrderID(filled.VenueOrderID)
	filledResp, err := waitForDemoSpotOrderStatus(ctx, adapter.rest, spec.VenueSymbol, fillClientID, "FILLED")
	if err != nil {
		t.Fatalf("wait for fill order: %v", err)
	}
	filledQty := dec(filledResp.ExecutedQty)
	if filledQty.IsZero() {
		t.Fatalf("filled order reported zero executed quantity: %+v", filledResp)
	}
	if err := waitForDemoExecObservation(ctx, execEvents, fillClientID, filled.VenueOrderID); err != nil {
		t.Fatalf("adapter execution stream did not observe Spot Demo fill: %v", err)
	}
	if err := waitForDemoSpotBalanceObservation(ctx, accountEvents, spec.BaseCurrency, startBaseTotal, spec.SizeStep); err != nil {
		t.Fatalf("adapter account stream did not observe Spot Demo base balance update: %v", err)
	}
	baseDelta, err := waitForDemoSpotBalanceDelta(ctx, adapter, spec.BaseCurrency, startBaseTotal, spec.SizeStep)
	if err != nil {
		t.Fatalf("wait for opened Spot Demo base balance: %v", err)
	}
	cleanup.SetBaseDelta(baseDelta)

	meta := cleanup.Metadata()
	if err := closeBinanceSpotDemoBaseDelta(ctx, adapter, instID, spec, startBaseAvailable); err != nil {
		t.Fatalf("close Spot Demo base delta: %v\n%s", err, meta.Remediation())
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, instID); err != nil {
		t.Fatalf("wait for no Spot Demo open orders: %v\n%s", err, meta.Remediation())
	}
	if err := waitForDemoSpotBaseDeltaBelowStep(ctx, adapter, spec, startBaseAvailable); err != nil {
		t.Fatalf("wait for Spot Demo base delta cleanup: %v\n%s", err, meta.Remediation())
	}
	cleanup.MarkClean()
}
