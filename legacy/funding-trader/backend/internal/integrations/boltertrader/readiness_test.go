package boltertrader

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"github.com/QuantProcessing/funding-trader/backend/internal/config"
	"github.com/QuantProcessing/funding-trader/backend/internal/marketdata"
)

func TestGatewayComponentReturnsReadyWhenGatewayReady(t *testing.T) {
	client := mustGatewayClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		if r.URL.Path != "/v1/health/ready" {
			t.Fatalf("path = %s, want /v1/health/ready", r.URL.Path)
		}
		return jsonFixture(GatewayReadiness{Ready: true}), nil
	}))

	component := GatewayComponent(client)(context.Background())
	if !component.Ready || component.Reason != "" {
		t.Fatalf("component = %+v, want ready", component)
	}
}

func TestGatewayComponentUsesStableGatewayReasonWhenGatewayIsNotReady(t *testing.T) {
	client := mustGatewayClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		return jsonFixture(GatewayReadiness{Ready: false, Reasons: []string{"Catalog_Unavailable"}}), nil
	}))

	component := GatewayComponent(client)(context.Background())
	if component.Ready || component.Reason != "catalog-unavailable" {
		t.Fatalf("component = %+v, want catalog-unavailable", component)
	}
}

func TestGatewayComponentFailsClosedWhenClientIsMissing(t *testing.T) {
	component := GatewayComponent(nil)(context.Background())
	if component.Ready || component.Reason != "gateway-unreachable" {
		t.Fatalf("component = %+v, want gateway-unreachable", component)
	}
}

func TestAccountsComponentReportsModeAndMixedVenueEnvironments(t *testing.T) {
	client := mustGatewayClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/accounts":
			return jsonFixture(GatewayCollection[AccountResource]{Items: []AccountResource{
				{
					AccountID: "binance-demo-usdt-perp", Venue: "binance", Product: "perp",
					VenueEnvironment: GatewayDemo, Settlement: "USDT", ReadReady: true, WriteReady: true,
				},
				{
					AccountID: "lighter-testnet-usdc-perp", Venue: "lighter", Product: "perp",
					VenueEnvironment: GatewayTestnet, Settlement: "USDC", ReadReady: true, WriteReady: true,
				},
			}}), nil
		case "/v1/capabilities":
			return jsonFixture(GatewayCollection[AccountCapability]{Items: []AccountCapability{
				{AccountID: "binance-demo-usdt-perp", Capability: CapabilityRow{Code: "binance-demo"}},
				{AccountID: "lighter-testnet-usdc-perp", Capability: CapabilityRow{Code: "lighter-testnet"}},
			}}), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return nil, nil
	}))

	component := AccountsComponent(client)(context.Background())
	if !component.Ready || component.Reason != "" {
		t.Fatalf("component = %+v, want ready", component)
	}
	if component.Details["count"] != "2" ||
		component.Details["mode"] != "test" ||
		component.Details["mixedVenueEnvironments"] != "true" {
		t.Fatalf("details = %+v, want Test count and mixed marker", component.Details)
	}
}

func TestAccountsComponentFailsClosedWhenNoAccountMatchesMode(t *testing.T) {
	client := mustGatewayClient(t, roundTripFunc(func(r *http.Request) (*http.Response, error) {
		switch r.URL.Path {
		case "/v1/accounts":
			return jsonFixture(GatewayCollection[AccountResource]{Items: []AccountResource{{
				AccountID: "binance-live-usdt-perp", Venue: "binance", Product: "perp",
				VenueEnvironment: GatewayLive, Settlement: "USDT", ReadReady: true, WriteReady: true,
			}}}), nil
		case "/v1/capabilities":
			return jsonFixture(GatewayCollection[AccountCapability]{Items: []AccountCapability{
				{AccountID: "binance-live-usdt-perp"},
			}}), nil
		default:
			t.Fatalf("unexpected path %s", r.URL.Path)
		}
		return nil, nil
	}))

	component := AccountsComponent(client)(context.Background())
	if component.Ready || component.Reason != "no-accounts-for-mode" {
		t.Fatalf("component = %+v, want no-accounts-for-mode", component)
	}
	if component.Details["count"] != "0" {
		t.Fatalf("details = %+v, want count 0", component.Details)
	}
}

func TestAccountsComponentFailsClosedWhenClientIsMissing(t *testing.T) {
	component := AccountsComponent(nil)(context.Background())
	if component.Ready || component.Reason != "accounts-unreachable" {
		t.Fatalf("component = %+v, want accounts-unreachable", component)
	}
}

func TestPublicDataNotConfiguredComponentFailsClosed(t *testing.T) {
	component := PublicDataNotConfiguredComponent()(context.Background())
	if component.Ready || component.Reason != "public-data-client-not-configured" {
		t.Fatalf("component = %+v, want public-data-client-not-configured", component)
	}
}

func TestPublicDataComponentReportsBoundedProductionProbe(t *testing.T) {
	probe := &fakePublicDataProbe{status: marketdata.Readiness{
		Mode: "test", Accounts: 2, Instruments: 150,
	}}
	component := PublicDataComponent(probe)(context.Background())
	if !component.Ready || component.Reason != "" {
		t.Fatalf("component=%+v, want ready", component)
	}
	if component.Details["mode"] != "test" ||
		component.Details["accounts"] != "2" ||
		component.Details["instruments"] != "150" {
		t.Fatalf("details=%+v", component.Details)
	}
	if probe.calls != 1 {
		t.Fatalf("probe calls=%d", probe.calls)
	}
}

func TestPublicDataComponentFailsClosedWithStableReasons(t *testing.T) {
	tests := []struct {
		name   string
		probe  PublicDataProber
		reason string
	}{
		{name: "missing", reason: "public-data-unreachable"},
		{
			name:   "none",
			probe:  &fakePublicDataProbe{err: marketdata.ErrNoConfiguredInstruments},
			reason: "public-data-no-instruments",
		},
		{
			name:   "invalid",
			probe:  &fakePublicDataProbe{err: marketdata.ErrInvalidMarketData},
			reason: "public-data-invalid",
		},
		{
			name:   "transport",
			probe:  &fakePublicDataProbe{err: errors.New("secret upstream detail")},
			reason: "public-data-unreachable",
		},
		{
			name:   "invalid status",
			probe:  &fakePublicDataProbe{status: marketdata.Readiness{Mode: "test"}},
			reason: "public-data-invalid",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			component := PublicDataComponent(test.probe)(context.Background())
			if component.Ready || component.Reason != test.reason {
				t.Fatalf("component=%+v, want %s", component, test.reason)
			}
			if component.Details != nil {
				t.Fatalf("failure leaked details=%+v", component.Details)
			}
		})
	}
}

type fakePublicDataProbe struct {
	status marketdata.Readiness
	err    error
	calls  int
}

func (probe *fakePublicDataProbe) Probe(context.Context) (marketdata.Readiness, error) {
	probe.calls++
	return probe.status, probe.err
}

func mustGatewayClient(t *testing.T, transport http.RoundTripper) *GatewayClient {
	t.Helper()
	client, err := NewGatewayClient(GatewayClientOptions{
		BaseURL: "http://gateway.local", Token: "token", Mode: config.Test,
		HTTPClient: &http.Client{Transport: transport},
	})
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	return client
}
