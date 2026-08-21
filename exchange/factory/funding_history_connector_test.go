package factory

import (
	"context"
	"errors"
	"net/http"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

// TestFundingHistoryRejectsUnsupportedVenue pins where "this venue keeps no
// history" is discovered.
//
// Ten of the eleven board route columns can be charted and Nado cannot, and the
// difference has to surface at construction rather than at read time. A
// connector that built for Nado and then answered with an empty series would
// put a blank chart next to a populated one, which reads as "nothing was paid
// on this leg" — the opposite of the truth, and indistinguishable from a real
// stretch of zero funding. Failing to build instead lets the caller say
// "unsupported" once, in words, beside the leg that can be drawn.
//
// The message is asserted, not just the error, because there are two different
// refusals here and only one of them is about history. Nado and spot are
// profiles the factory recognises — each resolves a scope and a source — and
// they fail at the last gate, on the missing loader. An unknown venue or a
// missing client never gets a profile at all. Asserting the error alone would
// let the first group start failing for the second group's reason, which would
// hide a real regression: a charted venue quietly losing its loader looks
// exactly like a typo in a venue name.
func TestFundingHistoryRejectsUnsupportedVenue(t *testing.T) {
	transport := new(countingTransport)
	httpClient := &http.Client{Transport: transport}

	supported := []struct {
		venue      exchange.Venue
		settlement string
	}{
		{exchange.VenueBinance, "USDT"},
		{exchange.VenueAster, "USDT"},
		{exchange.VenueOKX, "USDT"},
		{exchange.VenueLighter, "USDC"},
		{exchange.VenueHyperliquid, "USDC"},
		{exchange.VenueBybit, "USDT"},
		{exchange.VenueBybit, "USDC"},
		{exchange.VenueBitget, "USDT"},
		{exchange.VenueBitget, "USDC"},
		{exchange.VenueGate, "USDT"},
	}

	for _, profile := range supported {
		connector, err := NewPublicFundingHistoryConnector(
			profile.venue,
			exchange.ProductPerp,
			profile.settlement,
			EnvironmentLive,
			httpClient,
		)
		if err != nil {
			t.Fatalf("NewPublicFundingHistoryConnector(%s/%s): %v", profile.venue, profile.settlement, err)
		}
		// The scope is what labels a line on a two-leg chart, so a connector
		// that reported someone else's venue would mislabel the series rather
		// than fail to draw it.
		if scope := connector.Scope(); scope.Venue != profile.venue || scope.Settlement != profile.settlement {
			t.Fatalf("scope = %s/%s, want %s/%s", scope.Venue, scope.Settlement, profile.venue, profile.settlement)
		}
		if err := connector.Close(); err != nil {
			t.Fatalf("%s: Close: %v", profile.venue, err)
		}
	}
	// Construction is credential-free and offline; the board source builds all
	// of these on startup, before any account exists.
	if requests := transport.requests.Load(); requests != 0 {
		t.Fatalf("construction issued %d requests, want none", requests)
	}

	rejections := []struct {
		name        string
		venue       exchange.Venue
		product     exchange.Product
		settlement  string
		environment Environment
		httpClient  *http.Client
		message     string
	}{
		{
			name:        "nado publishes no funding history",
			venue:       exchange.VenueNado,
			product:     exchange.ProductPerp,
			settlement:  "USDT0",
			environment: EnvironmentLive,
			httpClient:  httpClient,
			message:     "public funding history connector loader is unavailable",
		},
		{
			// Spot is a real profile with a real catalog source and no funding
			// of any kind, so it lands on the same gate Nado does. The catalog
			// connector accepts this profile, which is why the two must not be
			// assumed interchangeable.
			name:        "spot has no funding surface",
			venue:       exchange.VenueBinance,
			product:     exchange.ProductSpot,
			environment: EnvironmentLive,
			httpClient:  httpClient,
			message:     "public funding history connector loader is unavailable",
		},
		{
			name:        "missing http client",
			venue:       exchange.VenueBinance,
			product:     exchange.ProductPerp,
			settlement:  "USDT",
			environment: EnvironmentLive,
			message:     "public funding history connector profile is unsupported",
		},
		{
			name:        "unknown venue",
			venue:       exchange.Venue("unknown"),
			product:     exchange.ProductPerp,
			settlement:  "USDT",
			environment: EnvironmentLive,
			httpClient:  httpClient,
			message:     "public funding history connector profile is unsupported",
		},
	}
	for _, rejection := range rejections {
		t.Run(rejection.name, func(t *testing.T) {
			connector, err := NewPublicFundingHistoryConnector(
				rejection.venue,
				rejection.product,
				rejection.settlement,
				rejection.environment,
				rejection.httpClient,
			)
			if connector != nil {
				t.Fatalf("unsupported profile built a connector: %+v", connector)
			}
			var exchangeErr *exchange.Error
			if !errors.As(err, &exchangeErr) {
				t.Fatalf("error %v is not an exchange error", err)
			}
			if exchangeErr.Kind() != exchange.KindInvalidConfig {
				t.Errorf("kind = %s, want %s", exchangeErr.Kind(), exchange.KindInvalidConfig)
			}
			if safe := exchangeErr.Details().SafeMessage; safe != rejection.message {
				t.Errorf("message = %q, want %q", safe, rejection.message)
			}
		})
	}

	// A nil connector is reachable through the same field a built one lives in,
	// so it answers rather than panics.
	var unbuilt *PublicFundingHistoryConnector
	if _, err := unbuilt.Load(context.Background(), exchange.FundingHistoryRequest{
		Symbol: "BTC-USDT",
		Since:  time.Unix(1_719_964_800, 0).UTC(),
		Until:  time.Unix(1_720_051_200, 0).UTC(),
	}); err == nil {
		t.Fatal("Load on an unbuilt connector returned no error")
	}
	if scope := unbuilt.Scope(); scope != (exchange.CatalogScope{}) {
		t.Fatalf("Scope on an unbuilt connector = %+v, want zero", scope)
	}
	if err := unbuilt.Close(); err != nil {
		t.Fatalf("Close on an unbuilt connector: %v", err)
	}
}
