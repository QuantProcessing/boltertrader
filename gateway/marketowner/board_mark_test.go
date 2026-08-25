package marketowner

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
	"github.com/QuantProcessing/boltertrader/gateway/publicmarket"
	"github.com/shopspring/decimal"
)

func TestBoardMarksMergesRichHotReferenceWithIndependentFieldClocks(t *testing.T) {
	store := markethub.NewFactStore(nil, nil)
	baseAt := time.Unix(1_780_000_000, 0).UTC()
	fact := exchange.DerivativeFact{
		InstrumentID: "binance:perp:BTC-USDT", RouteProfileID: "binance.perp.usdt.v1",
		Environment: exchange.EnvironmentLive,
		Instrument: exchange.InstrumentIdentity{
			Venue: exchange.VenueBinance, Product: exchange.ProductPerp,
			Symbol: "BTC-USDT", NativeSymbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT",
		},
		FundingRate: exchange.OptionalDecimal{Value: decimal.RequireFromString("0.0001"), Valid: true}, FundingObservedAt: baseAt,
		MarkPrice: exchange.OptionalDecimal{Value: decimal.NewFromInt(70_000), Valid: true}, MarkObservedAt: baseAt,
		IndexPrice: exchange.OptionalDecimal{Value: decimal.NewFromInt(69_990), Valid: true}, IndexObservedAt: baseAt,
		OpenInterest: exchange.OptionalDecimal{Value: decimal.NewFromInt(10), Valid: true}, OpenInterestObservedAt: baseAt,
	}
	if err := store.Apply(exchange.DerivativePatch{
		Kind:         exchange.EventSnapshot,
		Scope:        exchange.CatalogScope{Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp, Settlement: "USDT"},
		NativeFamily: "perpetual", Generation: 1, ObservedAt: baseAt,
		Fields: exchange.DerivativeFieldAll, Facts: []exchange.DerivativeFact{fact},
	}); err != nil {
		t.Fatal(err)
	}

	marks := BoardMarks{Facts: store, Markets: &publicmarket.Source{}}
	hotAt := baseAt.Add(time.Second)
	marks.PublishReference(context.Background(), fact.InstrumentID, exchange.DerivativeReferenceEvent{
		MarkPrice: exchange.OptionalDecimal{Value: decimal.NewFromInt(70_100), Valid: true}, MarkObservedAt: hotAt,
		IndexPrice: exchange.OptionalDecimal{Value: decimal.NewFromInt(70_090), Valid: true}, IndexObservedAt: hotAt,
		FundingRate: exchange.OptionalDecimal{Value: decimal.RequireFromString("0.0002"), Valid: true}, FundingObservedAt: hotAt,
		OpenInterest: exchange.OptionalDecimal{Value: decimal.NewFromInt(11), Valid: true}, OpenInterestObservedAt: hotAt,
	})
	got, ok := store.Get(fact.InstrumentID)
	if !ok || !got.MarkPrice.Value.Equal(decimal.NewFromInt(70_100)) || !got.IndexPrice.Value.Equal(decimal.NewFromInt(70_090)) ||
		!got.FundingRate.Value.Equal(decimal.RequireFromString("0.0002")) || !got.OpenInterest.Value.Equal(decimal.NewFromInt(11)) {
		t.Fatalf("rich hot reference was not merged: %+v", got)
	}

	marks.PublishReference(context.Background(), fact.InstrumentID, exchange.DerivativeReferenceEvent{
		MarkPrice: exchange.OptionalDecimal{Value: decimal.NewFromInt(1), Valid: true}, MarkObservedAt: baseAt,
	})
	got, _ = store.Get(fact.InstrumentID)
	if !got.MarkPrice.Value.Equal(decimal.NewFromInt(70_100)) {
		t.Fatalf("late mark replaced newer value: %+v", got.MarkPrice)
	}
}
