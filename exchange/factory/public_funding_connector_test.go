package factory

import (
	"context"
	"net/http"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

func TestPublicDerivativeFeedConstruction(t *testing.T) {
	profiles := []struct {
		venue      exchange.Venue
		settlement string
	}{
		{exchange.VenueBinance, "USDT"}, {exchange.VenueAster, "USDT"},
		{exchange.VenueHyperliquid, "USDC"}, {exchange.VenueLighter, "USDC"},
		{exchange.VenueBybit, "USDT"}, {exchange.VenueBybit, "USDC"},
		{exchange.VenueBitget, "USDT"}, {exchange.VenueBitget, "USDC"},
		{exchange.VenueGate, "USDT"}, {exchange.VenueOKX, "USDT"},
		{exchange.VenueNado, "USDT0"},
	}
	for _, profile := range profiles {
		family := "perpetual"
		if profile.venue == exchange.VenueBybit {
			family = "linear"
		}
		feed, err := NewPublicDerivativeFeed(profile.venue, exchange.ProductPerp, profile.settlement, EnvironmentLive, &http.Client{}, []string{string(profile.venue) + ".perp.test.v1"}, family, staticDerivativeResolver{}, []exchange.MarketCapability{{
			Venue: profile.venue, NativeMarketFamily: family, Dataset: exchange.DatasetFunding,
			Transport: exchange.TransportREST, Granularity: exchange.GranularityAllMarket, NominalCadence: time.Second, RateLimitOperation: "funding_rates",
		}}, nil)
		if err != nil {
			t.Fatalf("NewPublicDerivativeFeed(%s/%s): %v", profile.venue, profile.settlement, err)
		}
		if err := feed.Close(); err != nil {
			t.Fatalf("%s/%s close: %v", profile.venue, profile.settlement, err)
		}
	}
	if feed, err := NewPublicDerivativeFeed(exchange.VenueBinance, exchange.ProductSpot, "", EnvironmentLive, &http.Client{}, []string{"binance.spot.v1"}, "spot", staticDerivativeResolver{}, nil, nil); err == nil || feed != nil {
		t.Fatalf("spot derivative feed=%v err=%v, want unsupported", feed, err)
	}
}

type staticDerivativeResolver struct{}

func (staticDerivativeResolver) ResolveDerivativeInstrument(context.Context, string, string, string) (exchange.DerivativeFact, bool) {
	return exchange.DerivativeFact{}, false
}
