package factory

import (
	"context"
	"github.com/QuantProcessing/boltertrader/routeprofile"
	"net/http"
	"testing"

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
		family := exchange.NativeMarketFamily(profile.venue, exchange.ProductPerp, profile.settlement)
		route, _ := routeprofile.DefaultRegistry().ResolveLegacy(string(profile.venue), "perp", profile.settlement)
		feed, err := NewPublicDerivativeFeed(profile.venue, exchange.ProductPerp, profile.settlement, EnvironmentLive, &http.Client{}, []string{route.ID}, family, staticDerivativeResolver{}, exchange.MarketCapabilitiesFor(profile.venue, family), nil)
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

func TestPublicDerivativeFeedRejectsForeignOrInventedFamilies(t *testing.T) {
	for _, ids := range [][]string{{"bybit.perp.test.v1"}, {"bybit.spot.v1"}, {"binance.perp.usdt.v1"}, {"bybit.perp.usdt.v1", "bybit.perp.usdt.v1"}} {
		if _, err := NewPublicDerivativeFeed(exchange.VenueBybit, exchange.ProductPerp, "USDT", EnvironmentLive, &http.Client{}, ids, "linear", staticDerivativeResolver{}, nil, nil); err == nil {
			t.Fatalf("accepted routes %v", ids)
		}
	}
}
