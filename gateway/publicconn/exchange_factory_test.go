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
	for _, mode := range []routeprofile.Mode{routeprofile.ModeTest, routeprofile.ModeLive} {
		for _, profile := range trusted.Profiles(mode) {
			profile := profile
			t.Run(string(mode)+"/"+profile.ID, func(t *testing.T) {
				key := routeprofile.Key{Venue: profile.Venue, Mode: mode, RouteProfileID: profile.ID}
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
}

func TestExchangeFactoryFailsClosedForUnsupportedAndModeConflict(t *testing.T) {
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
			key:  routeprofile.Key{Venue: "binance", Mode: routeprofile.ModeTest, RouteProfileID: "binance.perp.usdc.demo.v1"},
		},
		{
			name: "mode conflict",
			key:  routeprofile.Key{Venue: "binance", Mode: routeprofile.ModeLive, RouteProfileID: "binance.perp.usdt.demo.v1"},
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

	connector, err := exchangefactory.NewPublicInstrumentConnector(
		exchange.VenueBinance,
		exchange.ProductPerp,
		"USDC",
		exchangefactory.EnvironmentDemo,
		httpClient,
	)
	if connector != nil || err == nil {
		t.Fatalf("unsupported public connector=%T err=%v", connector, err)
	}
}
