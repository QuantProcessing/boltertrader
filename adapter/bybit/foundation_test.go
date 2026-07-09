package bybit

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	bybitsdk "github.com/QuantProcessing/boltertrader/sdk/bybit"
)

func TestAccountIDIsCanonicalUnifiedPool(t *testing.T) {
	if AccountIDUnified != model.AccountIDBybitDefault {
		t.Fatalf("AccountIDUnified=%q", AccountIDUnified)
	}
	if AccountIDForKind(enums.KindSpot) != AccountIDUnified || AccountIDForKind(enums.KindPerp) != AccountIDUnified {
		t.Fatalf("Bybit unified account id must be shared across spot/perp")
	}
}

func TestInstrumentFromBybitPreservesSpotAndSettlement(t *testing.T) {
	spot := instrumentFromBybit("spot", bybitsdk.Instrument{
		Symbol: "ETHUSDT", BaseCoin: "ETH", QuoteCoin: "USDT", Status: "Trading",
		PriceFilter:   bybitsdk.PriceFilter{TickSize: "0.01"},
		LotSizeFilter: bybitsdk.LotSizeFilter{BasePrecision: "0.0001", MinOrderQty: "0.001", MinNotionalValue: "5"},
	})
	if spot == nil || spot.ID != (model.InstrumentID{Venue: VenueName, Symbol: "ETH-USDT", Kind: enums.KindSpot}) {
		t.Fatalf("unexpected spot instrument: %+v", spot)
	}
	if spot.Settle != "USDT" {
		t.Fatalf("spot settle=%q", spot.Settle)
	}

	usdt := instrumentFromBybit("linear", bybitsdk.Instrument{
		Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", SettleCoin: bybitsdk.SettleCoinUSDT, Status: "Trading",
		PriceFilter:   bybitsdk.PriceFilter{TickSize: "0.1"},
		LotSizeFilter: bybitsdk.LotSizeFilter{QtyStep: "0.001", MinOrderQty: "0.001", MinNotionalValue: "5"},
	})
	if usdt == nil || usdt.ID.Symbol != "BTC-USDT" || usdt.ID.Kind != enums.KindPerp || usdt.Settle != bybitsdk.SettleCoinUSDT {
		t.Fatalf("unexpected USDT linear instrument: %+v", usdt)
	}

	usdc := instrumentFromBybit("linear", bybitsdk.Instrument{
		Symbol: "BTCPERP", BaseCoin: "BTC", QuoteCoin: "USDC", SettleCoin: bybitsdk.SettleCoinUSDC, Status: "Trading",
		PriceFilter:   bybitsdk.PriceFilter{TickSize: "0.1"},
		LotSizeFilter: bybitsdk.LotSizeFilter{QtyStep: "0.001", MinOrderQty: "0.001", MinNotionalValue: "5"},
	})
	if usdc == nil || usdc.ID.Symbol != "BTC-USDC" || usdc.ID.Kind != enums.KindPerp || usdc.Settle != bybitsdk.SettleCoinUSDC {
		t.Fatalf("unexpected USDC linear instrument: %+v", usdc)
	}
}

func TestInstrumentFromBybitRejectsUnsupportedSettlement(t *testing.T) {
	got := instrumentFromBybit("inverse", bybitsdk.Instrument{Symbol: "BTCUSD", BaseCoin: "BTC", QuoteCoin: "USD", SettleCoin: "BTC"})
	if got != nil {
		t.Fatalf("inverse instrument must be out of first-phase scope: %+v", got)
	}
}

func TestInstrumentFromBybitRejectsDatedLinearFutures(t *testing.T) {
	got := instrumentFromBybit("linear", bybitsdk.Instrument{
		Symbol:       "BTCUSDT-31JUL26",
		BaseCoin:     "BTC",
		QuoteCoin:    "USDT",
		SettleCoin:   bybitsdk.SettleCoinUSDT,
		Status:       "Trading",
		DeliveryTime: "1785456000000",
	})
	if got != nil {
		t.Fatalf("dated linear futures must not be modeled as perp: %+v", got)
	}
}

func TestInstrumentProviderIndexesNeutralAndVenueSymbols(t *testing.T) {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		instrumentFromBybit("spot", bybitsdk.Instrument{Symbol: "ETHUSDT", BaseCoin: "ETH", QuoteCoin: "USDT"}),
		instrumentFromBybit("linear", bybitsdk.Instrument{Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", SettleCoin: bybitsdk.SettleCoinUSDT}),
		instrumentFromBybit("linear", bybitsdk.Instrument{Symbol: "BTCPERP", BaseCoin: "BTC", QuoteCoin: "USDC", SettleCoin: bybitsdk.SettleCoinUSDC}),
	})

	id, ok := provider.ResolveVenueSymbol("BTCPERP")
	if !ok || id != (model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDC", Kind: enums.KindPerp}) {
		t.Fatalf("resolve venue symbol=%+v ok=%v", id, ok)
	}
	if _, ok := provider.Instrument(id); !ok {
		t.Fatalf("expected provider to return %s", id)
	}
	if got := provider.All(); len(got) != 3 {
		t.Fatalf("provider all len=%d", len(got))
	}
}

func TestInstrumentProviderResolvesVenueSymbolByKindAndSettlement(t *testing.T) {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		instrumentFromBybit("spot", bybitsdk.Instrument{Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT"}),
		instrumentFromBybit("linear", bybitsdk.Instrument{Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", SettleCoin: bybitsdk.SettleCoinUSDT}),
		instrumentFromBybit("spot", bybitsdk.Instrument{Symbol: "BTCUSDC", BaseCoin: "BTC", QuoteCoin: "USDC"}),
		instrumentFromBybit("linear", bybitsdk.Instrument{Symbol: "BTCUSDC", BaseCoin: "BTC", QuoteCoin: "USDC", SettleCoin: bybitsdk.SettleCoinUSDC}),
	})

	spot, ok := provider.ResolveVenueInstrument("BTCUSDT", enums.KindSpot, "")
	if !ok || spot != (model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindSpot}) {
		t.Fatalf("spot resolve=%+v ok=%v", spot, ok)
	}
	usdt, ok := provider.ResolveVenueInstrument("BTCUSDT", enums.KindPerp, bybitsdk.SettleCoinUSDT)
	if !ok || usdt != (model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp}) {
		t.Fatalf("USDT perp resolve=%+v ok=%v", usdt, ok)
	}
	usdc, ok := provider.ResolveVenueInstrument("BTCUSDC", enums.KindPerp, bybitsdk.SettleCoinUSDC)
	if !ok || usdc != (model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDC", Kind: enums.KindPerp}) {
		t.Fatalf("USDC perp resolve=%+v ok=%v", usdc, ok)
	}
}

func TestCapabilityRowsSplitSettlementCategories(t *testing.T) {
	rows := CapabilityRows()
	want := map[string]bool{"Spot cash": false, "USDT-linear Perp/SWAP": false, "USDC-linear Perp/SWAP": false}
	for _, row := range rows {
		if row.Venue != VenueName || !row.AccountStateSnapshot {
			t.Fatalf("unexpected row: %+v", row)
		}
		if _, ok := want[row.Product]; ok {
			want[row.Product] = true
		}
	}
	for product, seen := range want {
		if !seen {
			t.Fatalf("missing capability row for %s", product)
		}
	}
}
