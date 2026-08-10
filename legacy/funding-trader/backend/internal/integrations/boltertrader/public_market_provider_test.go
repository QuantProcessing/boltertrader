package boltertrader

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/funding-trader/backend/internal/config"
	"github.com/QuantProcessing/funding-trader/backend/internal/marketdata"
	"github.com/shopspring/decimal"
)

func TestPublicMarketProviderDiscoversOnlyReadyFundingPerpetualsWithExplicitAccountScope(t *testing.T) {
	catalogSource := publicCatalogFixture()
	publicSource := &fakePublicMarketSource{}
	provider, err := NewPublicMarketProvider(catalogSource, publicSource, config.Test)
	if err != nil {
		t.Fatal(err)
	}

	instruments, err := provider.Instruments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(instruments) != 3 {
		t.Fatalf("instruments=%+v, want mixed Demo/Testnet funding perpetuals", instruments)
	}
	if instruments[0].AccountID != "binance-demo-usdt-perp" ||
		instruments[0].InstrumentID != "binance:demo:perp:USDT:BTC-USDT" ||
		instruments[0].VenueEnvironment != "demo" ||
		instruments[0].Product != "perp" {
		t.Fatalf("first instrument lost explicit binding: %+v", instruments[0])
	}
	if instruments[1].Symbol != "ETH-USDT" {
		t.Fatalf("deterministic order=%+v", instruments)
	}
	if instruments[2].AccountID != "lighter-testnet-usdt-perp" ||
		instruments[2].VenueEnvironment != "testnet" {
		t.Fatalf("Testnet binding was not retained in Test mode: %+v", instruments[2])
	}
	if catalogSource.listCalls["binance-demo-usdt-perp"] != 1 {
		t.Fatalf("eligible account list calls=%v", catalogSource.listCalls)
	}
	for _, eligible := range []string{"binance-demo-usdt-perp", "lighter-testnet-usdt-perp"} {
		if catalogSource.listCalls[eligible] != 1 {
			t.Fatalf("eligible account %s list calls=%v", eligible, catalogSource.listCalls)
		}
	}
	for _, ineligible := range []string{
		"binance-demo-spot", "not-ready-demo-perp", "no-funding-demo-perp",
	} {
		if catalogSource.listCalls[ineligible] != 0 {
			t.Fatalf("ineligible account %s reached instrument catalog", ineligible)
		}
	}
}

func TestPublicMarketProviderResolvesAuthoritativeBindingAndNeverCallsMarketForUnknownScope(t *testing.T) {
	catalogSource := publicCatalogFixture()
	publicSource := &fakePublicMarketSource{}
	provider, err := NewPublicMarketProvider(catalogSource, publicSource, config.Test)
	if err != nil {
		t.Fatal(err)
	}
	instruments, err := provider.Instruments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	requested := marketdata.Instrument{
		AccountID: instruments[0].AccountID, InstrumentID: instruments[0].InstrumentID,
		Venue: "forged",
	}
	rate, err := provider.FundingRate(context.Background(), requested)
	if err != nil {
		t.Fatal(err)
	}
	if rate.Instrument.Venue != "binance" || publicSource.lastInstrument.Venue != "binance" {
		t.Fatalf("provider did not replace caller metadata with authoritative binding: %+v", rate.Instrument)
	}

	unknown := requested
	unknown.InstrumentID = "binance:demo:perp:USDT:SOL-USDT"
	if _, err := provider.FundingRate(context.Background(), unknown); !errors.Is(err, marketdata.ErrInstrumentNotFound) {
		t.Fatalf("unknown binding error=%v", err)
	}
	if publicSource.currentCalls != 1 {
		t.Fatalf("unknown scope reached market source, calls=%d", publicSource.currentCalls)
	}
}

func TestPublicMarketProviderRejectsMislabeledCatalogResource(t *testing.T) {
	catalogSource := publicCatalogFixture()
	resources := catalogSource.instruments["binance-demo-usdt-perp"]
	resources[0].VenueEnvironment = GatewayTestnet
	catalogSource.instruments["binance-demo-usdt-perp"] = resources
	provider, err := NewPublicMarketProvider(catalogSource, &fakePublicMarketSource{}, config.Test)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Instruments(context.Background()); !errors.Is(err, marketdata.ErrInvalidMarketData) {
		t.Fatalf("scope mismatch error=%v", err)
	}
}

func TestPublicMarketProviderReadinessForcesRefreshAndProbesFundingAndBBO(t *testing.T) {
	catalogSource := publicCatalogFixture()
	publicSource := &fakePublicMarketSource{}
	provider, err := NewPublicMarketProvider(catalogSource, publicSource, config.Test)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Instruments(context.Background()); err != nil {
		t.Fatal(err)
	}
	if _, err := provider.Instruments(context.Background()); err != nil {
		t.Fatal(err)
	}
	if catalogSource.discoveryCalls != 1 {
		t.Fatalf("fresh cache discovery calls=%d, want 1", catalogSource.discoveryCalls)
	}

	status, err := provider.Probe(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Mode != "test" || status.Accounts != 2 || status.Instruments != 3 {
		t.Fatalf("status=%+v", status)
	}
	if catalogSource.discoveryCalls != 2 || publicSource.currentCalls != 1 || publicSource.bboCalls != 1 {
		t.Fatalf("probe calls discovery=%d current=%d bbo=%d",
			catalogSource.discoveryCalls, publicSource.currentCalls, publicSource.bboCalls)
	}
}

func TestPublicMarketProviderReadinessRejectsStaleOrFutureMarketData(t *testing.T) {
	now := time.Date(2026, 7, 29, 10, 0, 0, 0, time.UTC)
	for _, test := range []struct {
		name            string
		fundingObserved time.Time
		bboObserved     time.Time
	}{
		{
			name:            "stale funding",
			fundingObserved: now.Add(-publicFundingReadinessMaxAge - time.Second),
			bboObserved:     now,
		},
		{
			name:            "stale bbo",
			fundingObserved: now,
			bboObserved:     now.Add(-publicBBOReadinessMaxAge - time.Second),
		},
		{
			name:            "future bbo",
			fundingObserved: now,
			bboObserved:     now.Add(publicObservationFutureSkew + time.Second),
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			publicSource := &fakePublicMarketSource{
				fundingObserved: test.fundingObserved,
				bboObserved:     test.bboObserved,
			}
			provider, err := NewPublicMarketProvider(publicCatalogFixture(), publicSource, config.Test)
			if err != nil {
				t.Fatal(err)
			}
			provider.now = func() time.Time { return now }
			if _, err := provider.Probe(context.Background()); !errors.Is(err, marketdata.ErrInvalidMarketData) {
				t.Fatalf("stale readiness error=%v", err)
			}
		})
	}
}

func TestPublicMarketProviderUsesSameReadPathForLiveMode(t *testing.T) {
	catalogSource := publicCatalogFixture()
	catalogSource.discovery.Mode = config.Live
	catalogSource.discovery.Accounts = []DiscoveredAccount{{
		AccountID: "binance-live-usdt-perp", Venue: "binance", Product: "perp",
		VenueEnvironment: GatewayLive, Settlement: "USDT", ReadReady: true,
		Capability: CapabilityRow{Operations: CapabilityOperations{Funding: true}},
	}}
	catalogSource.instruments = map[string][]InstrumentResource{
		"binance-live-usdt-perp": {
			instrumentResource("binance", GatewayLive, "BTC-USDT"),
		},
	}
	provider, err := NewPublicMarketProvider(catalogSource, &fakePublicMarketSource{}, config.Live)
	if err != nil {
		t.Fatal(err)
	}
	instruments, err := provider.Instruments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(instruments) != 1 || instruments[0].VenueEnvironment != "live" {
		t.Fatalf("live read binding=%+v", instruments)
	}
}

type fakePublicCatalog struct {
	discovery      AccountDiscovery
	instruments    map[string][]InstrumentResource
	discoveryCalls int
	listCalls      map[string]int
}

func publicCatalogFixture() *fakePublicCatalog {
	return &fakePublicCatalog{
		discovery: AccountDiscovery{
			Mode: config.Test,
			Accounts: []DiscoveredAccount{
				{
					AccountID: "binance-demo-usdt-perp", Venue: "binance", Product: "perp",
					VenueEnvironment: GatewayDemo, Settlement: "USDT", ReadReady: true,
					Capability: CapabilityRow{Operations: CapabilityOperations{Funding: true}},
				},
				{
					AccountID: "binance-demo-spot", Venue: "binance", Product: "spot",
					VenueEnvironment: GatewayDemo, Settlement: "USDT", ReadReady: true,
					Capability: CapabilityRow{Operations: CapabilityOperations{Funding: true}},
				},
				{
					AccountID: "lighter-testnet-usdt-perp", Venue: "lighter", Product: "perp",
					VenueEnvironment: GatewayTestnet, Settlement: "USDT", ReadReady: true,
					Capability: CapabilityRow{Operations: CapabilityOperations{Funding: true}},
				},
				{
					AccountID: "not-ready-demo-perp", Venue: "lighter", Product: "perp",
					VenueEnvironment: GatewayDemo, Settlement: "USDC", ReadReady: false,
					Capability: CapabilityRow{Operations: CapabilityOperations{Funding: true}},
				},
				{
					AccountID: "no-funding-demo-perp", Venue: "lighter", Product: "perp",
					VenueEnvironment: GatewayDemo, Settlement: "USDC", ReadReady: true,
				},
			},
		},
		instruments: map[string][]InstrumentResource{
			"binance-demo-usdt-perp": {
				instrumentResource("binance", GatewayDemo, "ETH-USDT"),
				instrumentResource("binance", GatewayDemo, "BTC-USDT"),
			},
			"lighter-testnet-usdt-perp": {
				instrumentResource("lighter", GatewayTestnet, "BTC-USDT"),
			},
		},
		listCalls: make(map[string]int),
	}
}

func instrumentResource(venue string, environment VenueEnvironment, symbol string) InstrumentResource {
	return InstrumentResource{
		InstrumentID: venue + ":" + string(environment) + ":perp:USDT:" + symbol,
		Venue:        venue, VenueEnvironment: environment, Product: "perp", Settlement: "USDT",
		Symbol: symbol, BaseAsset: symbol[:3], QuoteAsset: "USDT",
		Rules: catalog.Rules{
			PriceIncrement:    decimal.RequireFromString("0.1"),
			QuantityIncrement: decimal.RequireFromString("0.001"),
			MinQuantity:       decimal.RequireFromString("0.001"),
			MinNotional: exchange.OptionalDecimal{
				Value: decimal.RequireFromString("5"), Valid: true,
			},
		},
	}
}

func (source *fakePublicCatalog) DiscoverAccounts(context.Context) (AccountDiscovery, error) {
	source.discoveryCalls++
	return source.discovery, nil
}

func (source *fakePublicCatalog) ListInstruments(_ context.Context, accountID string) ([]InstrumentResource, error) {
	source.listCalls[accountID]++
	return source.instruments[accountID], nil
}

type fakePublicMarketSource struct {
	currentCalls    int
	bboCalls        int
	lastInstrument  marketdata.Instrument
	fundingObserved time.Time
	bboObserved     time.Time
}

func (source *fakePublicMarketSource) CurrentFunding(
	_ context.Context,
	instrument marketdata.Instrument,
) (marketdata.FundingRate, error) {
	source.currentCalls++
	source.lastInstrument = instrument
	observedAt := source.fundingObserved
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	return marketdata.FundingRate{Instrument: instrument, ObservedAt: observedAt}, nil
}

func (source *fakePublicMarketSource) FundingHistory(
	_ context.Context,
	request marketdata.FundingRateHistoryRequest,
) (marketdata.FundingRatePage, error) {
	source.lastInstrument = request.Instrument
	return marketdata.FundingRatePage{}, nil
}

func (source *fakePublicMarketSource) OrderBook(
	_ context.Context,
	request marketdata.OrderBookRequest,
) (marketdata.OrderBook, error) {
	source.lastInstrument = request.Instrument
	return marketdata.OrderBook{Instrument: request.Instrument}, nil
}

func (source *fakePublicMarketSource) BBO(
	_ context.Context,
	instrument marketdata.Instrument,
) (marketdata.BBO, error) {
	source.bboCalls++
	source.lastInstrument = instrument
	observedAt := source.bboObserved
	if observedAt.IsZero() {
		observedAt = time.Now().UTC()
	}
	return marketdata.BBO{Instrument: instrument, ObservedAt: observedAt}, nil
}

func (source *fakePublicMarketSource) MarkPrice(
	_ context.Context,
	instrument marketdata.Instrument,
) (marketdata.MarkPrice, error) {
	source.lastInstrument = instrument
	return marketdata.MarkPrice{Instrument: instrument}, nil
}

func (source *fakePublicMarketSource) WatchOrderBook(
	context.Context,
	marketdata.Instrument,
) (marketdata.Subscription[marketdata.OrderBook], error) {
	return fakeMarketSubscription[marketdata.OrderBook]{}, nil
}

func (source *fakePublicMarketSource) WatchBBO(
	context.Context,
	marketdata.Instrument,
) (marketdata.Subscription[marketdata.BBO], error) {
	return fakeMarketSubscription[marketdata.BBO]{}, nil
}

func (source *fakePublicMarketSource) WatchMarkPrice(
	context.Context,
	marketdata.Instrument,
) (marketdata.Subscription[marketdata.MarkPrice], error) {
	return fakeMarketSubscription[marketdata.MarkPrice]{}, nil
}

func (source *fakePublicMarketSource) WatchFundingRate(
	context.Context,
	marketdata.Instrument,
) (marketdata.Subscription[marketdata.FundingRate], error) {
	return fakeMarketSubscription[marketdata.FundingRate]{}, nil
}

type fakeMarketSubscription[T any] struct{}

func (fakeMarketSubscription[T]) Next(context.Context) (marketdata.StreamEvent[T], error) {
	return marketdata.StreamEvent[T]{}, nil
}

func (fakeMarketSubscription[T]) Close() error { return nil }
