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
	if !spec.CloseQuantity.IsPositive() || !spec.CloseQuantity.LessThan(spec.Quantity) {
		t.Fatalf("close quantity=%s quantity=%s, want fee-buffered Spot close", spec.CloseQuantity, spec.Quantity)
	}
	if spec.CloseQuantity.LessThan(inst.MinQty) || spec.CloseQuantity.Mul(spec.ClosePrice).LessThan(inst.MinNotional) {
		t.Fatalf("buffered close is not tradable: close_qty=%s close_price=%s min_qty=%s min_notional=%s", spec.CloseQuantity, spec.ClosePrice, inst.MinQty, inst.MinNotional)
	}
}

func TestBybitSpotAcceptanceLifecyclePricesUseTightIOCSlippage(t *testing.T) {
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

func TestBybitPerpAcceptanceLifecyclePricesUseWiderIOCSlippage(t *testing.T) {
	provider := newInstrumentProvider()
	inst := &model.Instrument{
		ID:          model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp},
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

	spec := bybitAcceptanceLifecycleSpec(t, adapter, "Bybit Demo USDT Perp", inst.ID, book, decimal.RequireFromString("20"))
	if spec.CleanExistingPosition {
		t.Fatal("Bybit Perp acceptance must reject, not auto-flatten, pre-existing positions")
	}
	if spec.FillPrice.GreaterThan(decimal.RequireFromString("102.02")) {
		t.Fatalf("fill price %s uses too much IOC buy slippage", spec.FillPrice)
	}
	if spec.ClosePrice.LessThan(decimal.RequireFromString("99.00")) {
		t.Fatalf("close price %s uses too much IOC sell slippage", spec.ClosePrice)
	}
}

func TestBybitAvailableBalanceUsesLargestFreeForCurrency(t *testing.T) {
	state := model.AccountState{
		Balances: []model.AccountBalance{
			{Currency: "USDT", Free: decimal.RequireFromString("1.25")},
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

func TestAcceptanceEnvironmentRecognizesDemo(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		label string
		want  string
	}{
		{label: "Bybit Demo Spot", want: "Demo"},
		{label: "Bybit Demo USDT Perp Runtime", want: "Demo"},
		{label: "Bybit Testnet Spot", want: "Testnet"},
		{label: "Bybit Mainnet Spot", want: ""},
	} {
		t.Run(tc.label, func(t *testing.T) {
			t.Parallel()
			if got := acceptanceEnvironment(tc.label); got != tc.want {
				t.Fatalf("acceptanceEnvironment(%q)=%q, want %q", tc.label, got, tc.want)
			}
		})
	}
}

func runtimeLifecycleFundsSpec(id model.InstrumentID) runtimeaccept.OrderLifecycleSpec {
	return runtimeaccept.OrderLifecycleSpec{
		InstrumentID: id,
		Quantity:     decimal.RequireFromString("0.001"),
		FillPrice:    decimal.RequireFromString("50000"),
	}
}
