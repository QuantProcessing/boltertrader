package boltertrader

import (
	"context"
	"net/http"
	"testing"

	"github.com/QuantProcessing/funding-trader/backend/internal/config"
)

func TestVenueEnvironmentEligibilityIsModeScoped(t *testing.T) {
	tests := []struct {
		name        string
		mode        config.Mode
		environment VenueEnvironment
		want        bool
	}{
		{name: "test includes demo", mode: config.Test, environment: GatewayDemo, want: true},
		{name: "test includes testnet", mode: config.Test, environment: GatewayTestnet, want: true},
		{name: "test excludes live", mode: config.Test, environment: GatewayLive, want: false},
		{name: "live includes live", mode: config.Live, environment: GatewayLive, want: true},
		{name: "live excludes demo", mode: config.Live, environment: GatewayDemo, want: false},
		{name: "live excludes testnet", mode: config.Live, environment: GatewayTestnet, want: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			if got := eligibleVenueEnvironment(test.mode, test.environment); got != test.want {
				t.Fatalf("eligibleVenueEnvironment(%q, %q)=%t, want %t",
					test.mode, test.environment, got, test.want)
			}
		})
	}
}

func TestTestModeDiscoveryRetainsMixedDemoAndTestnetIdentity(t *testing.T) {
	transport := roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/accounts":
			return jsonFixture(GatewayCollection[AccountResource]{Items: []AccountResource{
				{
					AccountID: "binance-demo", Venue: "binance", Product: "perp",
					VenueEnvironment: GatewayDemo, ReadReady: true, WriteReady: true,
				},
				{
					AccountID: "lighter-testnet", Venue: "lighter", Product: "perp",
					VenueEnvironment: GatewayTestnet, ReadReady: true, WriteReady: true,
				},
				{
					AccountID: "binance-live", Venue: "binance", Product: "perp",
					VenueEnvironment: GatewayLive, ReadReady: true, WriteReady: true,
				},
			}}), nil
		case "/v1/capabilities":
			return jsonFixture(GatewayCollection[AccountCapability]{Items: []AccountCapability{
				{AccountID: "binance-demo"},
				{AccountID: "lighter-testnet"},
				{AccountID: "binance-live"},
			}}), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return nil, nil
	})
	client, err := NewGatewayClient(GatewayClientOptions{
		BaseURL: "http://gateway.local", Token: "token", Mode: config.Test,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatal(err)
	}

	discovery, err := client.DiscoverAccounts(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if discovery.Mode != config.Test || !discovery.Mixed || len(discovery.Accounts) != 2 {
		t.Fatalf("mixed Test discovery=%+v", discovery)
	}
	if discovery.Accounts[0].VenueEnvironment != GatewayDemo ||
		discovery.Accounts[1].VenueEnvironment != GatewayTestnet {
		t.Fatalf("native venue environments were not retained: %+v", discovery.Accounts)
	}
}
