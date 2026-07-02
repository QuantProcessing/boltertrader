package perp

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

func TestOKXPerpDemoExecAcceptance(t *testing.T) {
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
		TdMode:          "cross",
		Environment:     okx.Simulated,
		DemoHostProfile: okx.DemoHostProfile(cfg.HostProfile),
		RESTBaseURL:     endpoints.REST,
		WSPublicURL:     endpoints.WSPublic,
		WSPrivateURL:    endpoints.WSPrivate,
		HTTPClient:      httpClient,
	})
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Perp Demo adapter initialization")
		t.Fatalf("new OKX Perp Demo adapter: %v", err)
	}
	defer adapter.Close()

	if err := adapter.Start(ctx); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Perp Demo private stream")
		t.Fatalf("start OKX Perp Demo adapter stream: %v", err)
	}
	execEvents := collectDemoExecEvents(adapter.Execution.Events())

	instID := model.InstrumentID{Venue: venueName, Symbol: instIDToNeutral(cfg.PerpSymbol), Kind: enums.KindPerp}
	if _, ok := adapter.provider.Instrument(instID); !ok {
		t.Fatalf("OKX Perp Demo symbol %s not loaded", cfg.PerpSymbol)
	}
	insts, err := adapter.rest.GetInstruments(ctx, instTypeSwap)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Perp Demo instruments")
		t.Fatalf("instrument metadata: %v", err)
	}
	var native *okx.Instrument
	for i := range insts {
		if insts[i].InstId == cfg.PerpSymbol {
			native = &insts[i]
			break
		}
	}
	spec, err := demoPerpSpecFromOKX(native)
	if err != nil {
		t.Fatalf("resolve OKX Perp Demo symbol: %v", err)
	}
	book, err := adapter.Market.OrderBook(ctx, instID, 5)
	if err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Perp Demo order book")
		t.Fatalf("order book: %v", err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("empty OKX Perp Demo book for %s", spec.VenueSymbol)
	}
	bid := book.Bids[0].Price
	ask := book.Asks[0].Price
	qty, err := selectDemoPerpQuantity(spec, cfg.MaxNotionalUSDT, ask)
	if err != nil {
		t.Fatalf("select safe OKX Perp Demo order quantity: %v", err)
	}
	restingPrice := floorDecimalToStep(bid.Mul(decimal.RequireFromString("0.80")), spec.PriceTick)
	fillPrice := ceilDecimalToStep(ask.Mul(decimal.RequireFromString("1.01")), spec.PriceTick)

	if open, err := adapter.Execution.OpenOrders(ctx, instID); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Perp Demo open order preflight")
		t.Fatalf("open order preflight: %v", err)
	} else if len(open) > 0 {
		t.Skipf("skipping OKX Perp Demo acceptance: %s already has %d open order(s); clean the Demo account before running", spec.VenueSymbol, len(open))
	}
	if exposure, err := demoCurrentExposure(ctx, adapter, instID); err != nil {
		testenv.SkipIfTransientLiveNetworkError(t, err, "OKX Perp Demo position preflight")
		t.Fatalf("position preflight: %v", err)
	} else if !exposure.IsZero() {
		t.Skipf("skipping OKX Perp Demo acceptance: %s already has exposure %s; start from a flat Demo account", spec.VenueSymbol, exposure)
	}

	cleanup := newDemoPerpCleanupState(spec, qty)
	defer func() {
		if !cleanup.needed {
			return
		}
		cleanupCtx, cancelCleanup := context.WithTimeout(context.Background(), 45*time.Second)
		defer cancelCleanup()
		if err := cleanupOKXPerpDemo(cleanupCtx, adapter, instID, spec, &cleanup); err != nil {
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
		t.Fatalf("submit OKX Perp Demo resting order: %v", err)
	}
	cleanup.RecordVenueOrderID(resting.VenueOrderID)
	if err := adapter.Execution.Cancel(ctx, instID, resting.VenueOrderID); err != nil {
		t.Fatalf("cancel OKX Perp Demo resting order %s: %v", resting.VenueOrderID, err)
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
		t.Fatalf("submit OKX Perp Demo fill order: %v", err)
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
		t.Fatalf("adapter execution stream did not observe OKX Perp Demo fill: %v", err)
	}
	exposure, err := waitForDemoExposure(ctx, adapter, instID, filledQty)
	if err != nil {
		t.Fatalf("wait for opened OKX Perp Demo position: %v", err)
	}
	cleanup.SetExposure(exposure)

	if err := closeOKXPerpDemoExposure(ctx, adapter, instID, spec); err != nil {
		t.Fatalf("close OKX Perp Demo exposure: %v\n%s", err, cleanup.Remediation())
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, instID); err != nil {
		t.Fatalf("wait for no OKX Perp Demo open orders: %v\n%s", err, cleanup.Remediation())
	}
	if err := waitForDemoFlat(ctx, adapter, instID); err != nil {
		t.Fatalf("wait for flat OKX Perp Demo position: %v\n%s", err, cleanup.Remediation())
	}
	cleanup.MarkClean()
}
