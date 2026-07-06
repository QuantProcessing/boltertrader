package bybit

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/adapter/internal/runtimeaccept"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/shopspring/decimal"
)

func TestBybitAcceptanceLifecycleQuantityCoversRestingMinNotional(t *testing.T) {
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

	spec := bybitAcceptanceLifecycleSpec(t, adapter, "Bybit Demo Spot", inst.ID, book, decimal.RequireFromString("20"))
	if spec.Quantity.Mul(spec.RestingPrice).LessThan(inst.MinNotional) {
		t.Fatalf("resting notional %s below min %s with spec %+v", spec.Quantity.Mul(spec.RestingPrice), inst.MinNotional, spec)
	}
}

func TestBybitAcceptanceLifecyclePricesUseTightIOCSlippage(t *testing.T) {
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

	spec := bybitAcceptanceLifecycleSpec(t, adapter, "Bybit Demo Spot", inst.ID, book, decimal.RequireFromString("20"))
	if spec.FillPrice.GreaterThan(decimal.RequireFromString("101.11")) {
		t.Fatalf("fill price %s uses too much IOC buy slippage", spec.FillPrice)
	}
	if spec.ClosePrice.LessThan(decimal.RequireFromString("99.90")) {
		t.Fatalf("close price %s uses too much IOC sell slippage", spec.ClosePrice)
	}
}

func TestBybitAvailableBalanceUsesLargestFreeOrAvailableForCurrency(t *testing.T) {
	state := model.AccountState{
		Balances: []model.AccountBalance{
			{Currency: "USDT", Available: decimal.RequireFromString("1.25")},
			{Currency: "USDT", Free: decimal.RequireFromString("2.5")},
			{Currency: "USDC", Free: decimal.RequireFromString("10")},
		},
	}

	got := bybitAvailableBalance(state, "USDT")
	if !got.Equal(decimal.RequireFromString("2.5")) {
		t.Fatalf("available USDT=%s, want 2.5", got)
	}
}

func TestBybitLifecycleFundsUseUnifiedBaseCurrencyForPerps(t *testing.T) {
	provider := newInstrumentProvider()
	inst := &model.Instrument{
		ID:          model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDC", Kind: enums.KindPerp},
		Base:        "BTC",
		Quote:       "USDC",
		Settle:      "USDC",
		VenueSymbol: "BTCPERP",
	}
	provider.LoadSnapshot([]*model.Instrument{inst})
	adapter := &Adapter{provider: provider}
	state := model.AccountState{
		BaseCurrency: "USD",
		Balances: []model.AccountBalance{
			{Currency: "USDC", Free: decimal.Zero},
			{Currency: "USD", Free: decimal.RequireFromString("100")},
		},
	}
	spec := runtimeLifecycleFundsSpec(inst.ID)

	ensureBybitLifecycleFunds(t, "Bybit Demo USDC Perp", adapter, state, spec)
}

func runtimeLifecycleFundsSpec(id model.InstrumentID) runtimeaccept.OrderLifecycleSpec {
	return runtimeaccept.OrderLifecycleSpec{
		InstrumentID: id,
		Quantity:     decimal.RequireFromString("0.001"),
		FillPrice:    decimal.RequireFromString("50000"),
	}
}
