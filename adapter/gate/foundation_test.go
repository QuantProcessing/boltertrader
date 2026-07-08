package gate

import (
	"errors"
	"testing"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	gatesdk "github.com/QuantProcessing/boltertrader/sdk/gate"
	"github.com/shopspring/decimal"
)

func TestAccountIDIsCanonicalUnifiedPool(t *testing.T) {
	if AccountIDUnified != model.AccountIDGateDefault {
		t.Fatalf("AccountIDUnified=%q", AccountIDUnified)
	}
	if AccountIDForKind(enums.KindSpot) != AccountIDUnified || AccountIDForKind(enums.KindPerp) != AccountIDUnified {
		t.Fatalf("Gate account id must be shared across spot/perp")
	}
}

func TestInstrumentFromGatePreservesSpotAndUSDTPerp(t *testing.T) {
	spot := instrumentFromGateSpot(gatesdk.CurrencyPair{
		ID: "ETH_USDT", Base: "ETH", Quote: "USDT", TradeStatus: "tradable",
		AmountPrecision: 4, Precision: 2, MinBaseAmount: "0.001", MinQuoteAmount: "5",
	})
	if spot == nil || spot.ID != (model.InstrumentID{Venue: VenueName, Symbol: "ETH-USDT", Kind: enums.KindSpot}) {
		t.Fatalf("unexpected spot instrument: %+v", spot)
	}
	if spot.Settle != "USDT" || !spot.PriceTick.Equal(mustDecimal("0.01")) || !spot.SizeStep.Equal(mustDecimal("0.0001")) {
		t.Fatalf("unexpected spot precision/settle: %+v", spot)
	}

	perp := instrumentFromGateContract(gatesdk.SettleUSDT, gatesdk.Contract{
		Name: "BTC_USDT", Status: "trading", QuantoMultiplier: "0.0001", OrderPriceRound: "0.1", OrderSizeMin: 1,
	})
	if perp == nil || perp.ID != (model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp}) {
		t.Fatalf("unexpected perp instrument: %+v", perp)
	}
	if perp.Settle != "USDT" || perp.VenueSymbol != "BTC_USDT" || !perp.ContractMultiplier.Equal(mustDecimal("0.0001")) {
		t.Fatalf("unexpected perp settlement/multiplier: %+v", perp)
	}
}

func TestInstrumentFromGateRejectsUnsupportedSurfaces(t *testing.T) {
	if got := instrumentFromGateSpot(gatesdk.CurrencyPair{ID: "ETH_USDT", Base: "ETH", Quote: "USDT", TradeStatus: "untradable"}); got != nil {
		t.Fatalf("untradable spot instrument must be rejected: %+v", got)
	}
	if got := instrumentFromGateContract(gatesdk.SettleUSDC, gatesdk.Contract{Name: "BTC_USDC", Status: "trading"}); got != nil {
		t.Fatalf("USDC futures must be out of first-phase scope: %+v", got)
	}
	if got := instrumentFromGateContract(gatesdk.SettleUSDT, gatesdk.Contract{Name: "BTC_USDT", Status: "trading", InDelisting: true}); got != nil {
		t.Fatalf("delisting contract must be rejected: %+v", got)
	}
}

func TestInstrumentProviderResolvesVenueSymbolByKindAndSettlement(t *testing.T) {
	provider := newInstrumentProvider()
	provider.LoadSnapshot([]*model.Instrument{
		instrumentFromGateSpot(gatesdk.CurrencyPair{ID: "BTC_USDT", Base: "BTC", Quote: "USDT"}),
		instrumentFromGateContract(gatesdk.SettleUSDT, gatesdk.Contract{Name: "BTC_USDT", Status: "trading", OrderPriceRound: "0.1", OrderSizeMin: 1}),
	})

	spot, ok := provider.ResolveVenueInstrument("BTC_USDT", enums.KindSpot, "")
	if !ok || spot != (model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindSpot}) {
		t.Fatalf("spot resolve=%+v ok=%v", spot, ok)
	}
	perp, ok := provider.ResolveVenueInstrument("BTC_USDT", enums.KindPerp, "USDT")
	if !ok || perp != (model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDT", Kind: enums.KindPerp}) {
		t.Fatalf("perp resolve=%+v ok=%v", perp, ok)
	}
	if got := provider.All(); len(got) != 2 {
		t.Fatalf("provider all len=%d", len(got))
	}
}

func TestProductForInstrumentRejectsUnsupportedSettlement(t *testing.T) {
	_, _, err := productForInstrument(&model.Instrument{
		ID:     model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDC", Kind: enums.KindPerp},
		Settle: "USDC",
	})
	if !errors.Is(err, errs.ErrNotSupported) {
		t.Fatalf("err=%v, want ErrNotSupported", err)
	}
}

func TestCapabilityRowsOnlyClaimSpotAndUSDTPerp(t *testing.T) {
	rows := CapabilityRows()
	want := map[string]bool{"Spot cash": false, "USDT-linear Perp/SWAP": false}
	for _, row := range rows {
		if row.Venue != VenueName || !row.AccountStateSnapshot {
			t.Fatalf("unexpected row: %+v", row)
		}
		if row.Modify {
			t.Fatalf("Gate phase-one must not claim modify support before adapter implementation proves it: %+v", row)
		}
		if _, ok := want[row.Product]; ok {
			want[row.Product] = true
		}
		if row.Product == "USDC-linear Perp/SWAP" {
			t.Fatalf("USDC row must not be claimed in phase one")
		}
	}
	for product, seen := range want {
		if !seen {
			t.Fatalf("missing capability row for %s", product)
		}
	}
}

func TestGateTestnetUSDCPerpDeferredCapability(t *testing.T) {
	if inst := instrumentFromGateContract(gatesdk.SettleUSDC, gatesdk.Contract{Name: "BTC_USDC", Status: "trading", QuantoMultiplier: "0.0001", OrderPriceRound: "0.1", OrderSizeMin: 1}); inst != nil {
		t.Fatalf("USDC-linear futures must remain deferred in phase one, got %+v", inst)
	}
	_, _, err := productForInstrument(&model.Instrument{
		ID:     model.InstrumentID{Venue: VenueName, Symbol: "BTC-USDC", Kind: enums.KindPerp},
		Settle: "USDC",
	})
	if !errors.Is(err, errs.ErrNotSupported) {
		t.Fatalf("USDC-linear product err=%v, want ErrNotSupported", err)
	}
	for _, row := range CapabilityRows() {
		if row.Venue == VenueName && row.Product == "USDC-linear Perp/Futures" {
			t.Fatalf("USDC-linear must not appear as a supported Gate capability row: %+v", row)
		}
	}
}

func TestGateEnumConversions(t *testing.T) {
	if got, _ := sideToGate(enums.SideBuy); got != "buy" {
		t.Fatalf("side=%q", got)
	}
	if got, _ := orderTypeToGate(enums.TypeLimit); got != "limit" {
		t.Fatalf("order type=%q", got)
	}
	if got, _ := tifToGate(enums.TifGTX); got != "poc" {
		t.Fatalf("tif=%q", got)
	}
	if statusFromGate("cancelled") != enums.StatusCanceled || positionSideFromGate(-1) != enums.PosShort {
		t.Fatal("unexpected reverse conversion")
	}
}

func mustDecimal(s string) decimal.Decimal {
	d, err := decimal.NewFromString(s)
	if err != nil {
		panic(err)
	}
	return d
}
