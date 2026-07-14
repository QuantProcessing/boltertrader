package spot

import (
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

func TestAsterSpotAcceptanceProxyErrorRedactsCredentials(t *testing.T) {
	const secret = "proxy-super-secret"
	_, err := parseAsterSpotAcceptanceProxyURL("http://user:" + secret + "@%gh")
	if err == nil {
		t.Fatal("expected malformed proxy URL to fail")
	}
	if strings.Contains(err.Error(), secret) {
		t.Fatalf("proxy credential leaked in error: %v", err)
	}
}

func TestAsterSpotRuntimeAcceptanceBindsConfiguredMaxNotional(t *testing.T) {
	data, err := os.ReadFile("testnet_acceptance_test.go")
	if err != nil {
		t.Fatalf("read acceptance source: %v", err)
	}
	const want = "runtimeaccept.AttachAccountRequiredRiskWithMaxNotional(node, adapter.Market.InstrumentProvider(), cfg.MaxNotionalUSDT)"
	if !strings.Contains(string(data), want) {
		t.Fatalf("Aster Spot runtime acceptance must bind cfg.MaxNotionalUSDT through the runtime risk engine")
	}
}

func TestAsterSpotAcceptanceSelectInstrumentRejectsTestSymbols(t *testing.T) {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		asterSpotAcceptanceTestInstrument("BTC-USDT", "BTCUSDT"),
		asterSpotAcceptanceTestInstrument("TEST-USDT", "TESTUSDT"),
	})

	if _, err := selectAsterSpotAcceptanceInstrument(provider, "TESTUSDT"); err == nil || !strings.Contains(err.Error(), "TEST") {
		t.Fatalf("select TEST symbol err=%v, want TEST rejection", err)
	}
	got, err := selectAsterSpotAcceptanceInstrument(provider, "")
	if err != nil {
		t.Fatalf("select default symbol: %v", err)
	}
	if got.VenueSymbol != "BTCUSDT" {
		t.Fatalf("default symbol=%s, want BTCUSDT", got.VenueSymbol)
	}
}

func TestAsterSpotAcceptanceQuantityCoversMinNotionalAndStep(t *testing.T) {
	inst := asterSpotAcceptanceTestInstrument("BTC-USDT", "BTCUSDT")
	inst.MinQty = decimal.RequireFromString("0.0001")
	inst.SizeStep = decimal.RequireFromString("0.0001")
	inst.MinNotional = decimal.RequireFromString("5")

	qty, err := selectAsterSpotAcceptanceQuantity(inst, decimal.RequireFromString("20"), decimal.RequireFromString("30000"), decimal.RequireFromString("31000"))
	if err != nil {
		t.Fatalf("select quantity: %v", err)
	}
	if want := decimal.RequireFromString("0.0003"); !qty.Equal(want) {
		t.Fatalf("qty=%s, want %s", qty, want)
	}
}

func TestAsterSpotAcceptanceQuantityRejectsOverMaxNotional(t *testing.T) {
	inst := asterSpotAcceptanceTestInstrument("BTC-USDT", "BTCUSDT")
	inst.MinQty = decimal.RequireFromString("0.001")
	inst.SizeStep = decimal.RequireFromString("0.001")
	inst.MinNotional = decimal.RequireFromString("10")

	if _, err := selectAsterSpotAcceptanceQuantity(inst, decimal.RequireFromString("20"), decimal.RequireFromString("30000"), decimal.RequireFromString("31000")); err == nil {
		t.Fatalf("select quantity succeeded, want max-notional rejection")
	}
}

func TestAsterSpotAcceptancePricesRoundToTick(t *testing.T) {
	tick := decimal.RequireFromString("0.01")
	if got, want := floorAsterSpotAcceptanceDecimal(decimal.RequireFromString("100.009"), tick), decimal.RequireFromString("100"); !got.Equal(want) {
		t.Fatalf("floor price=%s, want %s", got, want)
	}
	if got, want := ceilAsterSpotAcceptanceDecimal(decimal.RequireFromString("100.001"), tick), decimal.RequireFromString("100.01"); !got.Equal(want) {
		t.Fatalf("ceil price=%s, want %s", got, want)
	}
}

func TestAsterSpotAcceptanceLifecycleUsesNearBookRestingPriceAndFeeBufferedClose(t *testing.T) {
	inst := asterSpotAcceptanceTestInstrument("BTC-USDT", "BTCUSDT")
	book := &model.OrderBook{
		InstrumentID: inst.ID,
		Bids:         []model.BookLevel{{Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1)}},
		Asks:         []model.BookLevel{{Price: decimal.NewFromInt(101), Quantity: decimal.NewFromInt(1)}},
		Timestamp:    time.Now(),
	}
	spec := asterSpotAcceptanceLifecycleSpec(t, nil, "Aster unit", inst, book, decimal.NewFromInt(100))
	if !spec.RestingPrice.Equal(decimal.NewFromInt(99)) {
		t.Fatalf("resting price=%s, want 99", spec.RestingPrice)
	}
	if !spec.CloseQuantity.IsPositive() || !spec.CloseQuantity.LessThan(spec.Quantity) || !spec.CloseQuantity.Mod(inst.SizeStep).IsZero() {
		t.Fatalf("close quantity=%s quantity=%s, want step-aligned fee buffer", spec.CloseQuantity, spec.Quantity)
	}
}

func TestAsterSpotAcceptanceResidualSellabilityUsesVenueMinimums(t *testing.T) {
	inst := asterSpotAcceptanceTestInstrument("ASTER-USDT", "ASTERUSDT")
	inst.SizeStep = decimal.RequireFromString("0.01")
	inst.MinQty = decimal.RequireFromString("0.01")
	inst.MinNotional = decimal.RequireFromString("5")
	price := decimal.RequireFromString("0.6")

	if asterSpotAcceptanceResidualSellable(inst, decimal.RequireFromString("0.042229"), price) {
		t.Fatal("fee dust below min notional must not be classified as sellable")
	}
	if !asterSpotAcceptanceResidualSellable(inst, decimal.RequireFromString("10"), price) {
		t.Fatal("step-aligned residual above min quantity/notional must be classified as sellable")
	}
}

func asterSpotAcceptanceTestInstrument(symbol, venueSymbol string) *model.Instrument {
	return &model.Instrument{
		ID:          model.InstrumentID{Venue: VenueName, Symbol: symbol, Kind: enums.KindSpot},
		Base:        strings.TrimSuffix(symbol, "-USDT"),
		Quote:       "USDT",
		Settle:      "USDT",
		VenueSymbol: venueSymbol,
		PriceTick:   decimal.RequireFromString("0.01"),
		SizeStep:    decimal.RequireFromString("0.0001"),
		MinQty:      decimal.RequireFromString("0.0001"),
		MinNotional: decimal.RequireFromString("5"),
	}
}
