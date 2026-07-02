package spot

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/testenv"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
	"github.com/shopspring/decimal"
)

func TestOKXSpotDemoExecAcceptance(t *testing.T) {
	cfg := testenv.RequireOKXDemoWrite(t)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Minute)
	defer cancel()

	httpClient, err := testenv.OKXDemoHTTPClient(45 * time.Second)
	if err != nil {
		t.Fatalf("OKX Demo HTTP client: %v", err)
	}
	endpoints := okxDemoEndpoints(t, cfg)
	adapter, err := New(ctx, Config{
		APIKey:          cfg.APIKey,
		APISecret:       cfg.APISecret,
		Passphrase:      cfg.Passphrase,
		Environment:     okx.Simulated,
		DemoHostProfile: okx.DemoHostProfile(cfg.HostProfile),
		RESTBaseURL:     endpoints.REST,
		WSPublicURL:     endpoints.WSPublic,
		WSPrivateURL:    endpoints.WSPrivate,
		HTTPClient:      httpClient,
	})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Spot Demo adapter initialization")
		t.Fatalf("new OKX Spot Demo adapter: %v", err)
	}
	defer adapter.Close()

	if err := adapter.Start(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Spot Demo private stream")
		t.Fatalf("start OKX Spot Demo adapter stream: %v", err)
	}
	execEvents := collectDemoExecEvents(adapter.Execution.Events())

	instID := model.InstrumentID{Venue: venueName, Symbol: cfg.SpotSymbol, Kind: enums.KindSpot}
	inst, ok := adapter.provider.Instrument(instID)
	if !ok {
		t.Fatalf("OKX Spot Demo symbol %s not loaded", cfg.SpotSymbol)
	}
	spec, err := demoSpotSpecFromInstrument(inst)
	if err != nil {
		t.Fatalf("resolve OKX Spot Demo symbol: %v", err)
	}
	book, err := adapter.Market.OrderBook(ctx, instID, 5)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Spot Demo order book")
		t.Fatalf("order book: %v", err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("empty OKX Spot Demo book for %s", spec.VenueSymbol)
	}
	bid := book.Bids[0].Price
	ask := book.Asks[0].Price
	if bid.LessThanOrEqual(decimal.Zero) || ask.LessThanOrEqual(decimal.Zero) {
		t.Fatalf("non-positive OKX Spot Demo book for %s: %+v", spec.VenueSymbol, book)
	}
	qty, err := selectDemoSpotQuantity(spec, cfg.MaxNotionalUSDT, ask)
	if err != nil {
		t.Fatalf("select safe OKX Spot Demo order quantity: %v", err)
	}
	restingPrice := floorDecimalToStep(bid.Mul(decimal.RequireFromString("0.80")), spec.PriceTick)
	fillPrice := ceilDecimalToStep(ask.Mul(decimal.RequireFromString("1.01")), spec.PriceTick)

	if open, err := adapter.Execution.OpenOrders(ctx, instID); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Spot Demo open order preflight")
		t.Fatalf("open order preflight: %v", err)
	} else if len(open) > 0 {
		t.Skipf("skipping OKX Spot Demo acceptance: %s already has %d open order(s); clean the Demo account before running", spec.VenueSymbol, len(open))
	}

	startBalances, err := demoSpotBalances(ctx, adapter)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Spot Demo balance preflight")
		t.Fatalf("balance preflight: %v", err)
	}
	startBaseAvailable := startBalances[spec.BaseCurrency].Available
	startBaseTotal := startBalances[spec.BaseCurrency].Total
	quoteAvailable := startBalances[spec.QuoteCurrency].Available
	requiredQuote := qty.Mul(fillPrice).Mul(decimal.RequireFromString("1.05"))
	if quoteAvailable.LessThan(requiredQuote) {
		t.Skipf("skipping OKX Spot Demo acceptance: %s available %s below required %s for %s quantity %s at fill price %s", spec.QuoteCurrency, quoteAvailable, requiredQuote, spec.VenueSymbol, qty, fillPrice)
	}

	cleanup := newDemoSpotCleanupState(spec, qty)
	defer func() {
		if !cleanup.needed {
			return
		}
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancelCleanup()
		if err := cleanupOKXSpotDemo(cleanupCtx, adapter, instID, spec, startBaseAvailable, &cleanup); err != nil {
			t.Fatalf("%v\n%s", err, cleanup.Remediation())
		}
	}()

	restingClientID := demoClientOrderID("rest")
	cleanup.Arm(restingClientID)
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
		t.Fatalf("submit OKX Spot Demo resting order: %v", err)
	}
	cleanup.RecordVenueOrderID(resting.VenueOrderID)
	if err := adapter.Execution.Cancel(ctx, instID, resting.VenueOrderID); err != nil {
		t.Fatalf("cancel OKX Spot Demo resting order %s: %v", resting.VenueOrderID, err)
	}
	if _, err := waitForDemoOrderStatus(ctx, adapter.rest, spec.VenueSymbol, restingClientID, "canceled"); err != nil {
		t.Fatalf("wait for resting order cancel: %v", err)
	}

	fillClientID := demoClientOrderID("fill")
	cleanup.Arm(fillClientID)
	filled, err := adapter.Execution.Submit(ctx, model.OrderRequest{
		InstrumentID: instID,
		ClientID:     fillClientID,
		Side:         enums.SideBuy,
		Type:         enums.TypeLimit,
		TIF:          enums.TifIOC,
		Quantity:     qty,
		Price:        fillPrice,
		PositionSide: enums.PosNet,
	})
	if err != nil {
		t.Fatalf("submit OKX Spot Demo fill order: %v", err)
	}
	cleanup.RecordVenueOrderID(filled.VenueOrderID)
	filledResp, err := waitForDemoOrderStatus(ctx, adapter.rest, spec.VenueSymbol, fillClientID, "filled")
	if err != nil {
		t.Fatalf("wait for fill order: %v", err)
	}
	filledQty := dec(filledResp.AccFillSz)
	if filledQty.IsZero() {
		t.Fatalf("filled order reported zero executed quantity: %+v", filledResp)
	}
	if err := waitForDemoExecObservation(ctx, execEvents, fillClientID, filled.VenueOrderID); err != nil {
		t.Fatalf("adapter execution stream did not observe OKX Spot Demo fill: %v", err)
	}
	baseDelta, err := waitForDemoSpotBaseDelta(ctx, adapter, spec.BaseCurrency, startBaseTotal, spec.SizeStep)
	if err != nil {
		t.Fatalf("wait for opened OKX Spot Demo base balance: %v", err)
	}
	cleanup.SetBaseDelta(baseDelta)

	if err := closeOKXSpotDemoBaseDelta(ctx, adapter, instID, spec, startBaseAvailable); err != nil {
		t.Fatalf("close OKX Spot Demo base delta: %v\n%s", err, cleanup.Remediation())
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, instID); err != nil {
		t.Fatalf("wait for no OKX Spot Demo open orders: %v\n%s", err, cleanup.Remediation())
	}
	if err := waitForDemoSpotBaseDeltaBelowStep(ctx, adapter, spec, startBaseAvailable, &cleanup); err != nil {
		t.Fatalf("wait for OKX Spot Demo base delta cleanup: %v\n%s", err, cleanup.Remediation())
	}
	cleanup.MarkClean()
}
