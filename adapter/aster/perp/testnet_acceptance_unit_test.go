package perp

import (
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

func TestAsterPerpAcceptanceSelectInstrumentRejectsTestSymbols(t *testing.T) {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		asterPerpAcceptanceTestInstrument("BTC-USDT", "BTCUSDT"),
		asterPerpAcceptanceTestInstrument("TEST-USDT", "TESTUSDT"),
	})

	if _, err := selectAsterPerpAcceptanceInstrument(provider, "TESTUSDT"); err == nil || !strings.Contains(err.Error(), "TEST") {
		t.Fatalf("select TEST symbol err=%v, want TEST rejection", err)
	}
	got, err := selectAsterPerpAcceptanceInstrument(provider, "")
	if err != nil {
		t.Fatalf("select default symbol: %v", err)
	}
	if got.VenueSymbol != "BTCUSDT" {
		t.Fatalf("default symbol=%s, want BTCUSDT", got.VenueSymbol)
	}
}

func TestAsterPerpAcceptanceQuantityCoversMinNotionalAndStep(t *testing.T) {
	inst := asterPerpAcceptanceTestInstrument("BTC-USDT", "BTCUSDT")
	inst.MinQty = decimal.RequireFromString("0.001")
	inst.SizeStep = decimal.RequireFromString("0.001")
	inst.MinNotional = decimal.RequireFromString("5")

	qty, err := selectAsterPerpAcceptanceQuantity(inst, decimal.RequireFromString("20"), decimal.RequireFromString("3000"), decimal.RequireFromString("3100"))
	if err != nil {
		t.Fatalf("select quantity: %v", err)
	}
	if want := decimal.RequireFromString("0.002"); !qty.Equal(want) {
		t.Fatalf("qty=%s, want %s", qty, want)
	}
}

func TestAsterPerpAcceptanceQuantityRejectsOverMaxNotional(t *testing.T) {
	inst := asterPerpAcceptanceTestInstrument("BTC-USDT", "BTCUSDT")
	inst.MinQty = decimal.RequireFromString("0.01")
	inst.SizeStep = decimal.RequireFromString("0.001")
	inst.MinNotional = decimal.RequireFromString("10")

	if _, err := selectAsterPerpAcceptanceQuantity(inst, decimal.RequireFromString("20"), decimal.RequireFromString("3000"), decimal.RequireFromString("3100")); err == nil {
		t.Fatalf("select quantity succeeded, want max-notional rejection")
	}
}

func TestAsterPerpAcceptancePricesRoundToTick(t *testing.T) {
	tick := decimal.RequireFromString("0.1")
	if got, want := floorAsterPerpAcceptanceDecimal(decimal.RequireFromString("100.09"), tick), decimal.RequireFromString("100"); !got.Equal(want) {
		t.Fatalf("floor price=%s, want %s", got, want)
	}
	if got, want := ceilAsterPerpAcceptanceDecimal(decimal.RequireFromString("100.01"), tick), decimal.RequireFromString("100.1"); !got.Equal(want) {
		t.Fatalf("ceil price=%s, want %s", got, want)
	}
}

func asterPerpAcceptanceTestInstrument(symbol, venueSymbol string) *model.Instrument {
	return &model.Instrument{
		ID:                 model.InstrumentID{Venue: VenueName, Symbol: symbol, Kind: enums.KindPerp},
		Base:               strings.TrimSuffix(symbol, "-USDT"),
		Quote:              "USDT",
		Settle:             "USDT",
		VenueSymbol:        venueSymbol,
		PriceTick:          decimal.RequireFromString("0.1"),
		SizeStep:           decimal.RequireFromString("0.001"),
		MinQty:             decimal.RequireFromString("0.001"),
		MinNotional:        decimal.RequireFromString("5"),
		ContractMultiplier: decimal.NewFromInt(1),
	}
}
