package nado

import (
	"context"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

func TestNadoAcceptanceProxyErrorRedactsCredentials(t *testing.T) {
	const secret = "proxy-super-secret"
	_, err := parseNadoAcceptanceProxyURL("http://user:" + secret + "@%gh")
	if err == nil {
		t.Fatal("expected malformed proxy URL to fail")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("proxy credential leaked in error: %v", err)
	}
}

func TestNadoAcceptanceDoesNotInstallAccountWideCancelAllCleanup(t *testing.T) {
	data, err := os.ReadFile("testnet_acceptance_test.go")
	if err != nil {
		t.Fatalf("read acceptance source: %v", err)
	}
	source := string(data)
	for _, forbidden := range []string{"cancelAllNadoAcceptanceOrders", "Execution.CancelAll("} {
		if strings.Contains(source, forbidden) {
			t.Errorf("Nado acceptance must rely on lifecycle-owned order cleanup; found %q", forbidden)
		}
	}
}

func TestNadoRuntimeAcceptanceBindsConfiguredMaxNotional(t *testing.T) {
	data, err := os.ReadFile("testnet_acceptance_test.go")
	if err != nil {
		t.Fatalf("read acceptance source: %v", err)
	}
	const want = "runtimeaccept.AttachAccountRequiredRiskWithMaxNotional(node, adapter.Market.InstrumentProvider(), cfg.MaxNotionalUSDT0)"
	if !strings.Contains(string(data), want) {
		t.Fatalf("Nado runtime acceptance must bind cfg.MaxNotionalUSDT0 through the runtime risk engine")
	}
}

func TestNadoAcceptanceSelectsConfiguredSymbolAndProductKind(t *testing.T) {
	provider := nadoTestProvider()
	spotID, ok := selectNadoAcceptanceInstrument(provider, "ETH-USDT0", enums.KindSpot)
	if !ok {
		t.Fatal("select spot instrument failed")
	}
	if spotID.Kind != enums.KindSpot || spotID.Symbol != "ETH-USDT0" {
		t.Fatalf("spot id=%+v", spotID)
	}
	perpID, ok := selectNadoAcceptanceInstrument(provider, "BTC-USDT0", enums.KindPerp)
	if !ok {
		t.Fatal("select perp instrument failed")
	}
	if perpID.Kind != enums.KindPerp || perpID.Symbol != "BTC-USDT0" {
		t.Fatalf("perp id=%+v", perpID)
	}
	if _, ok := selectNadoAcceptanceInstrument(provider, "BTC-USDT0", enums.KindSpot); ok {
		t.Fatal("perp symbol must not satisfy spot selection")
	}
}

func TestNadoAcceptanceLifecyclePricesUsePassiveRestingAndTightIOCSlippage(t *testing.T) {
	inst := &model.Instrument{
		ID:        model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT0", Kind: enums.KindPerp},
		PriceTick: decimal.RequireFromString("0.5"),
	}
	book := &model.OrderBook{
		InstrumentID: inst.ID,
		Bids:         []model.BookLevel{{Price: decimal.RequireFromString("100.2"), Quantity: decimal.NewFromInt(1)}},
		Asks:         []model.BookLevel{{Price: decimal.RequireFromString("101.2"), Quantity: decimal.NewFromInt(1)}},
		Timestamp:    time.Now(),
	}
	prices := nadoAcceptanceLifecyclePrices(t, "Nado unit", inst, book)
	if !prices.resting.Equal(decimal.RequireFromString("99.0")) {
		t.Fatalf("resting=%s, want 99.0", prices.resting)
	}
	if !prices.fill.Equal(decimal.RequireFromString("102.5")) {
		t.Fatalf("fill=%s, want 102.5", prices.fill)
	}
	if !prices.close.Equal(decimal.RequireFromString("99.0")) {
		t.Fatalf("close=%s, want 99.0", prices.close)
	}
}

func TestNadoAcceptanceQuantityCoversMinNotionalAndMaxNotional(t *testing.T) {
	inst := &model.Instrument{
		ID:          model.InstrumentID{Venue: VenueName, Symbol: "ETH-USDT0", Kind: enums.KindSpot},
		MinQty:      decimal.RequireFromString("0.001"),
		SizeStep:    decimal.RequireFromString("0.001"),
		MinNotional: decimal.RequireFromString("10"),
	}
	qty, err := nadoAcceptanceQuantity(inst, decimal.NewFromInt(100), decimal.NewFromInt(2000), decimal.NewFromInt(2000))
	if err != nil {
		t.Fatalf("quantity: %v", err)
	}
	if !qty.Equal(decimal.RequireFromString("0.006")) {
		t.Fatalf("qty=%s, want 0.006", qty)
	}
	closeQty, err := nadoAcceptanceSpotCloseQuantity(inst, qty)
	if err != nil {
		t.Fatalf("close quantity: %v", err)
	}
	if !closeQty.Equal(decimal.RequireFromString("0.005")) {
		t.Fatalf("closeQty=%s, want 0.005", closeQty)
	}
}

func TestNadoAcceptanceLifecycleSelectionFallsBackOnlyWhenSymbolIsImplicit(t *testing.T) {
	provider := &instrumentProvider{}
	usdc := &model.Instrument{
		ID:          model.InstrumentID{Venue: VenueName, Symbol: "USDC-USDT0", Kind: enums.KindSpot},
		VenueSymbol: "USDC_USDT0",
		Base:        "USDC",
		Quote:       "USDT0",
		Settle:      "USDT0",
		PriceTick:   decimal.RequireFromString("0.0001"),
		SizeStep:    decimal.NewFromInt(1),
		MinQty:      decimal.NewFromInt(100),
	}
	weth := &model.Instrument{
		ID:          model.InstrumentID{Venue: VenueName, Symbol: "WETH-USDT0", Kind: enums.KindSpot},
		VenueSymbol: "WETH_USDT0",
		Base:        "WETH",
		Quote:       "USDT0",
		Settle:      "USDT0",
		PriceTick:   decimal.RequireFromString("0.01"),
		SizeStep:    decimal.RequireFromString("0.0001"),
		MinQty:      decimal.RequireFromString("0.0001"),
	}
	provider.loadDiscovery(
		[]discoveredInstrument{{instrument: usdc, productID: 1}, {instrument: weth, productID: 2}},
		map[int64]string{0: "USDT0", 1: "USDC", 2: "WETH"},
		"USDT0",
	)
	books := func(_ context.Context, id model.InstrumentID) (*model.OrderBook, error) {
		price := decimal.RequireFromString("1.01")
		if id == weth.ID {
			price = decimal.NewFromInt(2000)
		}
		return &model.OrderBook{
			InstrumentID: id,
			Bids:         []model.BookLevel{{Price: price, Quantity: decimal.NewFromInt(1)}},
			Asks:         []model.BookLevel{{Price: price, Quantity: decimal.NewFromInt(1)}},
		}, nil
	}

	candidate, err := selectNadoAcceptanceLifecycleCandidate(context.Background(), provider, "", enums.KindSpot, decimal.NewFromInt(100), books)
	if err != nil {
		t.Fatalf("implicit selection: %v", err)
	}
	if candidate.id != weth.ID {
		t.Fatalf("selected=%s, want %s", candidate.id, weth.ID)
	}

	if _, err := selectNadoAcceptanceLifecycleCandidate(context.Background(), provider, usdc.VenueSymbol, enums.KindSpot, decimal.NewFromInt(100), books); err == nil {
		t.Fatal("explicit unsafe symbol must fail instead of falling back")
	}
}

func TestNadoAcceptanceRoundingHelpers(t *testing.T) {
	step := decimal.RequireFromString("0.05")
	if got := ceilNadoAcceptanceDecimal(decimal.RequireFromString("1.01"), step); !got.Equal(decimal.RequireFromString("1.05")) {
		t.Fatalf("ceil=%s, want 1.05", got)
	}
	if got := floorNadoAcceptanceDecimal(decimal.RequireFromString("1.09"), step); !got.Equal(decimal.RequireFromString("1.05")) {
		t.Fatalf("floor=%s, want 1.05", got)
	}
}
