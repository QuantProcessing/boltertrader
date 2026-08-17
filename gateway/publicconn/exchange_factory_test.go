package publicconn

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	exchangefactory "github.com/QuantProcessing/boltertrader/exchange/factory"
	"github.com/QuantProcessing/boltertrader/routeprofile"
)

type noNetworkRoundTripper struct{ t *testing.T }

func (transport noNetworkRoundTripper) RoundTrip(*http.Request) (*http.Response, error) {
	transport.t.Fatal("connector construction performed network I/O")
	return nil, errors.New("network I/O is forbidden")
}

func TestExchangeFactoryBuildsEveryDefaultProfileWithoutNetworkIO(t *testing.T) {
	trusted := routeprofile.DefaultRegistry()
	httpClient := &http.Client{Transport: noNetworkRoundTripper{t: t}}
	factory, err := NewExchangeFactory(httpClient, trusted)
	if err != nil {
		t.Fatal(err)
	}
	for _, profile := range trusted.Profiles() {
		t.Run(profile.ID, func(t *testing.T) {
			key := routeprofile.Key{Venue: profile.Venue, RouteProfileID: profile.ID}
			connector, err := factory.Build(context.Background(), key)
			if err != nil || connector == nil {
				t.Fatalf("profile=%+v connector=%T err=%v", profile, connector, err)
			}
			if err := connector.Close(); err != nil {
				t.Fatal(err)
			}
		})
	}
}

// TestExchangeFactoryFailsClosedForUnknownProfiles keeps the fail-closed half of
// what used to be a mode-conflict test.
//
// The second case below was once a live key carrying a demo profile id. Modes
// are gone, but the property that made it worth testing is not: a key is two
// fields and the registry is keyed on both, so a profile id that exists under a
// different venue must not resolve. Building it anyway would point one venue's
// runtime at another venue's endpoints.
func TestExchangeFactoryFailsClosedForUnknownProfiles(t *testing.T) {
	trusted := routeprofile.DefaultRegistry()
	httpClient := &http.Client{Transport: noNetworkRoundTripper{t: t}}
	factory, err := NewExchangeFactory(httpClient, trusted)
	if err != nil {
		t.Fatal(err)
	}
	tests := []struct {
		name string
		key  routeprofile.Key
	}{
		{
			name: "unsupported profile",
			key:  routeprofile.Key{Venue: "binance", RouteProfileID: "binance.perp.usdc.v1"},
		},
		{
			name: "cross-venue profile",
			key:  routeprofile.Key{Venue: "okx", RouteProfileID: "binance.spot.v1"},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			connector, err := factory.Build(context.Background(), test.key)
			if connector != nil || !errors.Is(err, ErrConnectorUnavailable) {
				t.Fatalf("connector=%T err=%v", connector, err)
			}
		})
	}

	// The same fail-closed rule, one layer down: the factory's own profile switch
	// has no binance perp-USDC entry, so the triple is refused before any
	// endpoint is chosen.
	connector, err := exchangefactory.NewPublicInstrumentConnector(
		exchange.VenueBinance,
		exchange.ProductPerp,
		"USDC",
		exchangefactory.EnvironmentLive,
		httpClient,
	)
	if connector != nil || err == nil {
		t.Fatalf("unsupported public connector=%T err=%v", connector, err)
	}
}
