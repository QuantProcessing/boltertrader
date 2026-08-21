package factory

import (
	"context"
	"net/http"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
)

func TestPublicFundingMarketFeedBootstrap(t *testing.T) {
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
		feed, err := NewPublicFundingMarketFeed(profile.venue, exchange.ProductPerp, profile.settlement, EnvironmentLive, &http.Client{})
		if err != nil {
			t.Fatalf("NewPublicFundingMarketFeed(%s/%s): %v", profile.venue, profile.settlement, err)
		}
		// The public factory intentionally owns production endpoints;
		// construction is the contract exercised here. Adapter fixture tests
		// cover bootstrap mapping without issuing a live request here.
		if feed.Status().State != exchange.FundingMarketFeedUnavailable {
			t.Fatalf("%s/%s initial feed state = %q", profile.venue, profile.settlement, feed.Status().State)
		}
		if err := feed.Close(); err != nil {
			t.Fatalf("%s/%s close: %v", profile.venue, profile.settlement, err)
		}
	}
	if feed, err := NewPublicFundingMarketFeed(exchange.VenueBinance, exchange.ProductSpot, "", EnvironmentLive, &http.Client{}); err == nil || feed != nil {
		t.Fatalf("spot funding feed=%v err=%v, want unsupported", feed, err)
	}
}

// TestFundingBoardSourceDegradesPerVenue pins the two properties a multi-venue
// board depends on: construction must not touch the network or a credential,
// and one venue's failure must be that venue's failure alone.
//
// The second is the whole point of building the board out of independent
// connectors. A shared client would let a single unreachable venue take the
// table down; here the caller gets an error for that column and snapshots for
// the rest, which is what lets the page render the other eight venues when one
// is down (or the other ten route columns when one settlement shard is down).
func TestFundingBoardSourceDegradesPerVenue(t *testing.T) {
	transport := new(countingTransport)
	httpClient := &http.Client{Transport: transport}

	fundingProfiles := []struct {
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
		{exchange.VenueNado, "USDT0"},
	}

	connectors := make([]*PublicFundingConnector, 0, len(fundingProfiles))
	for _, profile := range fundingProfiles {
		connector, err := NewPublicFundingConnector(
			profile.venue,
			exchange.ProductPerp,
			profile.settlement,
			EnvironmentLive,
			httpClient,
		)
		if err != nil {
			t.Fatalf("NewPublicFundingConnector(%s/%s): %v", profile.venue, profile.settlement, err)
		}
		if connector.Scope().Venue != profile.venue {
			t.Fatalf("connector scope venue = %s, want %s", connector.Scope().Venue, profile.venue)
		}
		if connector.Scope().Settlement != profile.settlement {
			t.Fatalf("connector scope settlement = %s, want %s", connector.Scope().Settlement, profile.settlement)
		}
		connectors = append(connectors, connector)
	}
	if requests := transport.requests.Load(); requests != 0 {
		t.Fatalf("construction issued %d requests, want none", requests)
	}

	// Every venue fails here, because the transport refuses everything. What is
	// being asserted is that each failure is returned to its own caller rather
	// than shared: ten errors, ten empty snapshots, no panic, no cross-talk.
	for index, connector := range connectors {
		snapshot, err := connector.Refresh(context.Background())
		if err == nil {
			t.Fatalf("%s: Refresh over a failing transport returned no error", fundingProfiles[index].venue)
		}
		if len(snapshot.Entries) != 0 {
			t.Fatalf("%s: failed Refresh returned %d entries, want none", fundingProfiles[index].venue, len(snapshot.Entries))
		}
		if err := connector.Close(); err != nil {
			t.Fatalf("%s: Close: %v", fundingProfiles[index].venue, err)
		}
	}
	if requests := transport.requests.Load(); requests == 0 {
		t.Fatal("Refresh did not use the injected HTTP client")
	}

	var unbuilt *PublicFundingConnector
	if _, err := unbuilt.Refresh(context.Background()); err == nil {
		t.Fatal("Refresh on an unbuilt connector returned no error")
	}
	if scope := unbuilt.Scope(); scope != (exchange.CatalogScope{}) {
		t.Fatalf("Scope on an unbuilt connector = %+v, want zero", scope)
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
			// Spot has no funding. The catalog connector accepts this profile,
			// so the two must not be assumed interchangeable.
			name:        "spot has no funding surface",
			venue:       exchange.VenueBinance,
			product:     exchange.ProductSpot,
			environment: EnvironmentLive,
			httpClient:  httpClient,
		},
		{
			name:        "missing http client",
			venue:       exchange.VenueBinance,
			product:     exchange.ProductPerp,
			settlement:  "USDT",
			environment: EnvironmentLive,
		},
		{
			name:        "unknown venue",
			venue:       exchange.Venue("unknown"),
			product:     exchange.ProductPerp,
			settlement:  "USDT",
			environment: EnvironmentLive,
			httpClient:  httpClient,
		},
	}
	for _, rejection := range rejections {
		t.Run(rejection.name, func(t *testing.T) {
			connector, err := NewPublicFundingConnector(
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
