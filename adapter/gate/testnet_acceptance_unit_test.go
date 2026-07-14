package gate

import (
	"os"
	"strings"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

func TestGateRuntimeAcceptanceBindsConfiguredMaxNotional(t *testing.T) {
	data, err := os.ReadFile("testnet_acceptance_test.go")
	if err != nil {
		t.Fatalf("read acceptance source: %v", err)
	}
	const want = "runtimeaccept.AttachAccountRequiredRiskWithMaxNotional(node, adapter.Market.InstrumentProvider(), cfg.MaxNotionalUSDT)"
	if !strings.Contains(string(data), want) {
		t.Fatalf("Gate runtime acceptance must bind cfg.MaxNotionalUSDT through the runtime risk engine")
	}
}

func TestGateAcceptanceQuantityUsesContractMultiplierForPerpNotional(t *testing.T) {
	inst := &model.Instrument{
		ID:                 model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp},
		Settle:             "USDT",
		PriceTick:          decimal.RequireFromString("0.1"),
		SizeStep:           decimal.NewFromInt(1),
		MinQty:             decimal.NewFromInt(1),
		ContractMultiplier: decimal.RequireFromString("0.0001"),
	}
	qty := gateAcceptanceQuantity(t, "Gate Testnet Perp", inst, decimal.NewFromInt(100), decimal.NewFromInt(100000), decimal.NewFromInt(100000))
	if !qty.Equal(decimal.NewFromInt(1)) {
		t.Fatalf("qty=%s, want 1", qty)
	}
	notional := qty.Mul(decimal.NewFromInt(100000)).Mul(inst.ContractMultiplier)
	if !notional.Equal(decimal.NewFromInt(10)) {
		t.Fatalf("notional=%s, want 10", notional)
	}
}

func TestGateAcceptanceQuantityCoversSpotMinNotional(t *testing.T) {
	inst := &model.Instrument{
		ID:          model.InstrumentID{Venue: VenueName, Symbol: "ETH-USDT", Kind: enums.KindSpot},
		Base:        "ETH",
		Quote:       "USDT",
		PriceTick:   decimal.RequireFromString("0.01"),
		SizeStep:    decimal.RequireFromString("0.001"),
		MinQty:      decimal.RequireFromString("0.001"),
		MinNotional: decimal.NewFromInt(5),
	}
	qty := gateAcceptanceQuantity(t, "Gate Testnet Spot", inst, decimal.NewFromInt(100), decimal.NewFromInt(2000), decimal.NewFromInt(2000))
	if qty.Mul(decimal.NewFromInt(2000)).LessThan(inst.MinNotional) {
		t.Fatalf("qty=%s notional=%s below min %s", qty, qty.Mul(decimal.NewFromInt(2000)), inst.MinNotional)
	}
}

func TestGateAcceptanceQuantityCoversBufferedSpotCloseMinNotional(t *testing.T) {
	inst := &model.Instrument{
		ID:          model.InstrumentID{Venue: VenueName, Symbol: "ETH-USDT", Kind: enums.KindSpot},
		Base:        "ETH",
		Quote:       "USDT",
		PriceTick:   decimal.RequireFromString("0.01"),
		SizeStep:    decimal.RequireFromString("0.0001"),
		MinQty:      decimal.RequireFromString("0.001"),
		MinNotional: decimal.NewFromInt(3),
	}
	price := decimal.RequireFromString("1737")
	qty := gateAcceptanceQuantity(t, "Gate Testnet Spot", inst, decimal.NewFromInt(100), price, price)
	if !qty.Equal(decimal.RequireFromString("0.0019")) {
		t.Fatalf("qty=%s, want 0.0019", qty)
	}
	closeQty := gateAcceptanceSpotCloseQuantity(t, "Gate Testnet Spot", inst, qty)
	if !closeQty.Equal(decimal.RequireFromString("0.0018")) {
		t.Fatalf("close qty=%s, want 0.0018", closeQty)
	}
	if closeQty.Mul(price).LessThan(inst.MinNotional) {
		t.Fatalf("close notional=%s below min %s", closeQty.Mul(price), inst.MinNotional)
	}
}

func TestGatePerpAcceptanceDoesNotAutoFlattenPreExistingPositions(t *testing.T) {
	provider := newInstrumentProvider()
	inst := &model.Instrument{
		ID:                 model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp},
		Base:               "BTC",
		Quote:              "USDT",
		Settle:             "USDT",
		VenueSymbol:        "BTC_USDT",
		PriceTick:          decimal.RequireFromString("0.1"),
		SizeStep:           decimal.NewFromInt(1),
		MinQty:             decimal.NewFromInt(1),
		ContractMultiplier: decimal.RequireFromString("0.0001"),
	}
	provider.LoadSnapshot([]*model.Instrument{inst})
	adapter := &Adapter{provider: provider}
	book := &model.OrderBook{
		Bids: []model.BookLevel{{Price: decimal.NewFromInt(100000)}},
		Asks: []model.BookLevel{{Price: decimal.NewFromInt(100010)}},
	}

	spec := gateAcceptanceLifecycleSpec(t, adapter, "Gate Testnet USDT Perp", inst.ID, book, decimal.NewFromInt(100))
	if spec.CleanExistingPosition {
		t.Fatal("Gate Perp acceptance must reject, not auto-flatten, pre-existing positions")
	}
}
