package bitget

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

func TestBitgetAcceptanceLifecycleQuantityCoversRestingMinNotional(t *testing.T) {
	provider := newInstrumentProvider()
	inst := &model.Instrument{
		ID:          model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindSpot},
		Base:        "BTC",
		Quote:       "USDT",
		Settle:      "USDT",
		VenueSymbol: "BTCUSDT",
		PriceTick:   decimal.RequireFromString("0.01"),
		SizeStep:    decimal.RequireFromString("0.000001"),
		MinQty:      decimal.RequireFromString("0.000001"),
		MinNotional: decimal.RequireFromString("1"),
	}
	provider.LoadSnapshot([]*model.Instrument{inst})
	adapter := &Adapter{provider: provider}
	book := &model.OrderBook{
		Bids: []model.BookLevel{{Price: decimal.RequireFromString("100")}},
		Asks: []model.BookLevel{{Price: decimal.RequireFromString("101")}},
	}

	spec := bitgetAcceptanceLifecycleSpec(t, adapter, "Bitget Testnet Spot", inst.ID, book, decimal.RequireFromString("20"))
	if spec.Quantity.Mul(spec.RestingPrice).LessThan(inst.MinNotional) {
		t.Fatalf("resting notional %s below min %s with spec %+v", spec.Quantity.Mul(spec.RestingPrice), inst.MinNotional, spec)
	}
}
