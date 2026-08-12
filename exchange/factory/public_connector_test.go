package factory

import (
	"context"
	"net/http"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
)

func TestPublicInstrumentConnectorConstructsOnlySupportedProfilesAndStaysCredentialFree(t *testing.T) {
	transport := &countingTransport{}
	httpClient := &http.Client{Transport: transport}

	connector, err := NewPublicInstrumentConnector(
		exchange.VenueBinance,
		exchange.ProductSpot,
		"",
		EnvironmentLive,
		httpClient,
	)
	if err != nil {
		t.Fatalf("NewPublicInstrumentConnector: %v", err)
	}
	if connector == nil {
		t.Fatal("supported profile produced a nil connector")
	}
	if requests := transport.requests.Load(); requests != 0 {
		t.Fatalf("construction issued %d requests, want none", requests)
	}

	instruments, err := connector.Refresh(context.Background())
	if err == nil {
		t.Fatal("Refresh over a failing transport returned no error")
	}
	if instruments != nil {
		t.Fatalf("failed Refresh returned %d instruments, want none", len(instruments))
	}
	if requests := transport.requests.Load(); requests == 0 {
		t.Fatal("Refresh did not use the injected HTTP client")
	}
	if err := connector.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	var unbuilt *PublicInstrumentConnector
	if _, err := unbuilt.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh on an unbuilt connector returned no error")
	}

	rejections := []struct {
		name        string
		venue       exchange.Venue
		product     exchange.Product
		settlement  string
		environment Environment
		httpClient  *http.Client
	}{
		{
			name:        "unsupported settlement",
			venue:       exchange.VenueBinance,
			product:     exchange.ProductPerp,
			settlement:  "USDC",
			environment: EnvironmentLive,
			httpClient:  httpClient,
		},
		{
			name:        "missing http client",
			venue:       exchange.VenueBinance,
			product:     exchange.ProductSpot,
			environment: EnvironmentLive,
		},
		{
			name:        "unknown venue",
			venue:       exchange.Venue("unknown"),
			product:     exchange.ProductSpot,
			environment: EnvironmentLive,
			httpClient:  httpClient,
		},
	}
	for _, rejection := range rejections {
		t.Run(rejection.name, func(t *testing.T) {
			connector, err := NewPublicInstrumentConnector(
				rejection.venue,
				rejection.product,
				rejection.settlement,
				rejection.environment,
				rejection.httpClient,
			)
			if connector != nil || err == nil {
				t.Fatalf("connector=%v err=%v", connector, err)
			}
		})
	}
}
