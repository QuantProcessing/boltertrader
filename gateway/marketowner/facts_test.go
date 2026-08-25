package marketowner

import (
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/fundingboard"
	"github.com/QuantProcessing/boltertrader/gateway/publicmarket"
	"github.com/shopspring/decimal"
)

func TestProjectDerivativeFactsUsesCanonicalIdentityAndIndependentClocks(t *testing.T) {
	now := time.Date(2026, time.August, 25, 0, 0, 0, 0, time.UTC)
	identity := exchange.InstrumentIdentity{
		Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: "BTC-USDT",
		NativeSymbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT",
	}
	market := publicmarket.Instrument{
		RouteProfileID: "binance.perp.usdt.v1",
		Catalog:        catalog.Instrument{ID: "binance:live:perp:USDT:BTC-USDT", Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp, Symbol: "BTC-USDT"},
		Canonical:      exchange.Instrument{Identity: identity},
	}
	board := fundingboard.Snapshot{Venues: []fundingboard.VenueSnapshot{{
		RouteProfileID: market.RouteProfileID,
		Entries: []exchange.FundingBoardEntry{{
			Symbol: "BTC-USDT", NativeSymbol: "BTCUSDT", Rate: decimal.RequireFromString("0.0001"), RateValid: true,
			MarkPrice:         exchange.OptionalDecimal{Value: decimal.NewFromInt(78000), Valid: true},
			FundingObservedAt: now.Add(-time.Minute), MarkObservedAt: now,
		}},
	}}}

	facts := projectDerivativeFacts(board, publicmarket.Snapshot{Instruments: []publicmarket.Instrument{market}})
	if len(facts) != 1 {
		t.Fatalf("facts=%+v", facts)
	}
	fact := facts[0]
	if fact.InstrumentID != string(market.Catalog.ID) || fact.Instrument.NativeSymbol != "BTCUSDT" || !fact.FundingObservedAt.Equal(now.Add(-time.Minute)) || !fact.MarkObservedAt.Equal(now) {
		t.Fatalf("fact=%+v", fact)
	}
	if err := fact.Validate(); err != nil {
		t.Fatalf("validate: %v", err)
	}
}

func TestProjectDerivativeFactsRejectsUnresolvedValues(t *testing.T) {
	board := fundingboard.Snapshot{Venues: []fundingboard.VenueSnapshot{{
		RouteProfileID: "binance.perp.usdt.v1",
		Entries:        []exchange.FundingBoardEntry{{Symbol: "UNKNOWN", NativeSymbol: "UNKNOWN", Rate: decimal.RequireFromString("0.1"), RateValid: true}},
	}}}
	if facts := projectDerivativeFacts(board, publicmarket.Snapshot{}); len(facts) != 0 {
		t.Fatalf("unresolved facts=%+v", facts)
	}
}
