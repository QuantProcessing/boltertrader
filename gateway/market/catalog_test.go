package market

import (
	"context"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/venue"
	"github.com/shopspring/decimal"
)

func TestUnloadedMarketsListsOnlyEmptyMarkets(t *testing.T) {
	catalog := NewStaticCatalog([]exchange.Instrument{{
		Identity: exchange.InstrumentIdentity{Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: "BTC-USDT",
			BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT", NativeSymbol: "BTCUSDT"},
		Rules: exchange.InstrumentRules{PriceIncrement: decimal.RequireFromString("0.1"), QuantityIncrement: decimal.RequireFromString("0.001"), MinQuantity: decimal.RequireFromString("0.001")},
	}})
	missing := catalog.unloadedMarkets()
	if len(missing) != len(venue.Markets)-1 {
		t.Fatalf("missing = %d, want %d", len(missing), len(venue.Markets)-1)
	}
	for _, market := range missing {
		if market.String() == "binance:perp:USDT" {
			t.Fatal("loaded market reported as missing")
		}
	}
}

func TestRunStopsWithContext(t *testing.T) {
	catalog := NewStaticCatalog(nil)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { catalog.Run(ctx, time.Hour, 5*time.Millisecond); close(done) }()
	time.Sleep(30 * time.Millisecond) // several retry passes over connector-less markets
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop")
	}
}
