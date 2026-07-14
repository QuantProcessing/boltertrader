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
		t.Fatalf("new OKX Perp Demo adapter: %v", err)
	}
	defer adapter.Close()
	if err := validateDemoPerpAccountMode(ctx, adapter.rest); err != nil {
		t.Fatalf("OKX Perp Demo account mode preflight: %v", err)
	}

	if err := adapter.Start(ctx); err != nil {
		t.Fatalf("start OKX Perp Demo adapter stream: %v", err)
	}
	execEvents := collectDemoExecEvents(adapter.Execution.Events())

	instID := model.InstrumentID{Venue: venueName, Symbol: instIDToNeutral(cfg.PerpSymbol), Kind: enums.KindPerp}
	if _, ok := adapter.provider.Instrument(instID); !ok {
		t.Fatalf("OKX Perp Demo symbol %s not loaded", cfg.PerpSymbol)
	}
	insts, err := adapter.rest.GetInstruments(ctx, instTypeSwap)
	if err != nil {
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
		t.Fatalf("order book: %v", err)
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		t.Fatalf("empty OKX Perp Demo book for %s", spec.VenueSymbol)
	}
	bid := book.Bids[0].Price
	ask := book.Asks[0].Price
	restingPrice := floorDecimalToStep(bid.Mul(decimal.RequireFromString("0.80")), spec.PriceTick)
	fillPrice := ceilDecimalToStep(ask.Mul(decimal.RequireFromString("1.01")), spec.PriceTick)
	qty, err := selectDemoPerpQuantity(spec, cfg.MaxNotionalUSDT, fillPrice)
	if err != nil {
		t.Fatalf("select safe OKX Perp Demo order quantity: %v", err)
	}

	if open, err := adapter.Execution.OpenOrders(ctx, instID); err != nil {
		t.Fatalf("open order preflight: %v", err)
	} else if len(open) > 0 {
		t.Fatalf("OKX Perp Demo acceptance requires a clean account: %s already has %d open order(s); clean the Demo account before running", spec.VenueSymbol, len(open))
	}
	if exposure, err := demoCurrentExposure(ctx, adapter, instID); err != nil {
		t.Fatalf("position preflight: %v", err)
	} else if !exposure.IsZero() {
		t.Fatalf("OKX Perp Demo acceptance requires a flat account: %s already has exposure %s; flatten the Demo account before running", spec.VenueSymbol, exposure)
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
	cleanup.TrackOrder(demoOrderRoleResting, restingClientID)
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
		recoveryErr := recoverAmbiguousOKXPerpDemoOrder(ctx, adapter, spec, &cleanup, restingClientID)
		t.Fatalf("submit OKX Perp Demo resting order returned an ambiguous error: %v; client-ID recovery: %v\n%s", err, recoveryErr, cleanup.Remediation())
	}
	cleanup.RecordVenueOrderID(restingClientID, resting.VenueOrderID)
	if resting.VenueOrderID == "" {
		if err := recoverAmbiguousOKXPerpDemoOrder(ctx, adapter, spec, &cleanup, restingClientID); err != nil {
			t.Fatalf("recover OKX Perp Demo resting order identity: %v\n%s", err, cleanup.Remediation())
		}
	}
	if err := cancelAndConfirmOKXPerpDemoOrder(ctx, adapter, instID, spec, &cleanup, restingClientID); err != nil {
		t.Fatalf("cancel and confirm OKX Perp Demo resting order: %v\n%s", err, cleanup.Remediation())
	}
	if cleanup.RestingFillQuantity().IsPositive() {
		t.Fatalf("OKX Perp Demo resting order partially filled %s; IOC opening is blocked and bounded cleanup will run\n%s", cleanup.RestingFillQuantity(), cleanup.Remediation())
	}
	if !cleanup.OpeningAllowed() {
		t.Fatalf("OKX Perp Demo resting order is not authoritatively canceled without fills\n%s", cleanup.Remediation())
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, instID); err != nil {
		t.Fatalf("prove stable no-open state after resting cancel: %v\n%s", err, cleanup.Remediation())
	}
	if err := waitForDemoFlat(ctx, adapter, instID); err != nil {
		t.Fatalf("prove stable flat state after resting cancel: %v\n%s", err, cleanup.Remediation())
	}

	fillClientID := demoClientOrderID("fill")
	cleanup.TrackOrder(demoOrderRoleOpening, fillClientID)
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
		recoveryErr := recoverAmbiguousOKXPerpDemoOrder(ctx, adapter, spec, &cleanup, fillClientID)
		t.Fatalf("submit OKX Perp Demo fill order returned an ambiguous error: %v; client-ID recovery: %v\n%s", err, recoveryErr, cleanup.Remediation())
	}
	cleanup.RecordVenueOrderID(fillClientID, filled.VenueOrderID)
	filledResp, err := confirmOKXPerpDemoOrderTerminal(ctx, adapter, spec, &cleanup, fillClientID)
	if err != nil {
		t.Fatalf("wait for fill order terminal state: %v", err)
	}
	filledQty, err := validateOKXPerpDemoFill(filledResp, spec, cfg.MaxNotionalUSDT)
	if err != nil {
		t.Fatalf("validate bounded OKX Perp Demo fill: %v\n%s", err, cleanup.Remediation())
	}
	if err := waitForDemoExecObservation(ctx, execEvents, fillClientID, filled.VenueOrderID); err != nil {
		t.Fatalf("adapter execution stream did not observe OKX Perp Demo fill: %v", err)
	}
	exposure, err := waitForDemoExposure(ctx, adapter, instID, filledQty)
	if err != nil {
		t.Fatalf("wait for opened OKX Perp Demo position: %v", err)
	}
	cleanup.SetExposure(exposure)

	closed, err := closeOKXPerpDemoExposure(ctx, adapter, instID, spec, &cleanup)
	if err != nil {
		t.Fatalf("close OKX Perp Demo exposure: %v\n%s", err, cleanup.Remediation())
	}
	if closed != nil {
		if _, err := confirmOKXPerpDemoOrderTerminal(ctx, adapter, spec, &cleanup, closed.Request.ClientID); err != nil {
			t.Fatalf("confirm OKX Perp Demo close terminal state: %v\n%s", err, cleanup.Remediation())
		}
	}
	if err := waitForNoDemoOpenOrders(ctx, adapter, instID); err != nil {
		t.Fatalf("wait for no OKX Perp Demo open orders: %v\n%s", err, cleanup.Remediation())
	}
	if err := waitForDemoFlat(ctx, adapter, instID); err != nil {
		t.Fatalf("wait for flat OKX Perp Demo position: %v\n%s", err, cleanup.Remediation())
	}
	cleanup.MarkClean()
}
