package factory

import (
	"context"
	"net/http"
	"sync/atomic"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/routeprofile"
)

func TestPublicMarketConnectorsCoverProfilesWithoutCredentialsOrConstructionIO(t *testing.T) {
	transport := new(countingPublicMarketTransport)
	client := &http.Client{Transport: transport}
	for _, profile := range routeprofile.DefaultRegistry().Profiles() {
		connector, err := NewPublicMarketConnector(
			exchange.Venue(profile.Venue), exchange.Product(profile.Product), profile.SettlementAsset,
			EnvironmentLive, client,
		)
		if err != nil {
			t.Fatalf("%s: %v", profile.ID, err)
		}
		if _, writes := any(connector).(exchange.OrderREST); writes {
			t.Fatalf("%s public connector exposes order methods", profile.ID)
		}
		if _, private := any(connector).(exchange.SpotAccountREST); private {
			t.Fatalf("%s public connector exposes spot account methods", profile.ID)
		}
		if _, private := any(connector).(exchange.PerpAccountREST); private {
			t.Fatalf("%s public connector exposes perp account methods", profile.ID)
		}
		if err := connector.Close(); err != nil {
			t.Fatalf("%s close: %v", profile.ID, err)
		}
	}
	if got := transport.requests.Load(); got != 0 {
		t.Fatalf("construction performed %d HTTP requests", got)
	}
}

type countingPublicMarketTransport struct {
	requests atomic.Int64
}

func (transport *countingPublicMarketTransport) RoundTrip(*http.Request) (*http.Response, error) {
	transport.requests.Add(1)
	return nil, context.Canceled
}
