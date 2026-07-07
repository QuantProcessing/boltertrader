package bitget

import (
	"testing"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
	"github.com/shopspring/decimal"
)

func TestAccountIDIsCanonicalUnifiedPool(t *testing.T) {
	if AccountIDUnified != model.AccountIDBitgetDefault {
		t.Fatalf("AccountIDUnified=%q", AccountIDUnified)
	}
	if AccountIDForKind(enums.KindSpot) != AccountIDUnified || AccountIDForKind(enums.KindPerp) != AccountIDUnified {
		t.Fatalf("Bitget unified account id must be shared across spot/perp")
	}
}

func TestInstrumentFromBitgetPreservesSpotAndSettlement(t *testing.T) {
	spot := instrumentFromBitget(bitgetsdk.Instrument{
		Category: "SPOT", Symbol: "ETHUSDT", BaseCoin: "ETH", QuoteCoin: "USDT", Status: "online",
		PricePrecision: "2", QuantityPrecision: "4", MinOrderQty: "0.001", MinOrderAmount: "5",
	})
	if spot == nil || spot.ID != (model.InstrumentID{Venue: VenueName, Symbol: "ETH-USDT", Kind: enums.KindSpot}) {
		t.Fatalf("unexpected spot instrument: %+v", spot)
	}
	if spot.Settle != "USDT" || !spot.PriceTick.Equal(mustDecimal("0.01")) || !spot.SizeStep.Equal(mustDecimal("0.0001")) {
		t.Fatalf("unexpected spot precision/settle: %+v", spot)
	}

	usdt := instrumentFromBitget(bitgetsdk.Instrument{
		Category: bitgetsdk.ProductTypeUSDTFutures, Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT", Status: "online",
		PricePrecision: "1", QuantityPrecision: "3", MinOrderQty: "0.001", MinOrderAmount: "5",
	})
	if usdt == nil || usdt.ID.Symbol != "BTC-USDT" || usdt.ID.Kind != enums.KindPerp || usdt.Settle != "USDT" {
		t.Fatalf("unexpected USDT perp instrument: %+v", usdt)
	}

	usdc := instrumentFromBitget(bitgetsdk.Instrument{
		Category: bitgetsdk.ProductTypeUSDCFutures, Symbol: "BTCPERP", BaseCoin: "BTC", QuoteCoin: "USDC", Status: "online",
		PricePrecision: "1", QuantityPrecision: "3", MinOrderQty: "0.001", MinOrderAmount: "5",
	})
	if usdc == nil || usdc.ID.Symbol != "BTC-USDC" || usdc.ID.Kind != enums.KindPerp || usdc.Settle != "USDC" {
		t.Fatalf("unexpected USDC perp instrument: %+v", usdc)
	}
}

func TestInstrumentFromBitgetRejectsUnsupportedSettlement(t *testing.T) {
	got := instrumentFromBitget(bitgetsdk.Instrument{Category: "COIN-FUTURES", Symbol: "BTCUSD", BaseCoin: "BTC", QuoteCoin: "USD"})
	if got != nil {
		t.Fatalf("coin-margined instrument must be out of first-phase scope: %+v", got)
	}
}

func TestInstrumentProviderIndexesNeutralAndVenueSymbols(t *testing.T) {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "ETHUSDT", BaseCoin: "ETH", QuoteCoin: "USDT"}),
		instrumentFromBitget(bitgetsdk.Instrument{Category: bitgetsdk.ProductTypeUSDTFutures, Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT"}),
		instrumentFromBitget(bitgetsdk.Instrument{Category: bitgetsdk.ProductTypeUSDCFutures, Symbol: "BTCPERP", BaseCoin: "BTC", QuoteCoin: "USDC"}),
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
		instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT"}),
		instrumentFromBitget(bitgetsdk.Instrument{Category: bitgetsdk.ProductTypeUSDTFutures, Symbol: "BTCUSDT", BaseCoin: "BTC", QuoteCoin: "USDT"}),
		instrumentFromBitget(bitgetsdk.Instrument{Category: "SPOT", Symbol: "BTCUSDC", BaseCoin: "BTC", QuoteCoin: "USDC"}),
		instrumentFromBitget(bitgetsdk.Instrument{Category: bitgetsdk.ProductTypeUSDCFutures, Symbol: "BTCUSDC", BaseCoin: "BTC", QuoteCoin: "USDC"}),
	})

	spot, ok := provider.ResolveVenueInstrument("BTCUSDT", enums.KindSpot, "")
	if !ok || spot != (model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindSpot}) {
		t.Fatalf("spot resolve=%+v ok=%v", spot, ok)
	}
	usdt, ok := provider.ResolveVenueInstrument("BTCUSDT", enums.KindPerp, "USDT")
	if !ok || usdt != (model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp}) {
		t.Fatalf("USDT perp resolve=%+v ok=%v", usdt, ok)
	}
	usdc, ok := provider.ResolveVenueInstrument("BTCUSDC", enums.KindPerp, "USDC")
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

func mustDecimal(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return d
}
