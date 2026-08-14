package main

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/credential"
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/capability"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/httpapi"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/QuantProcessing/boltertrader/secretstore"
	"github.com/google/uuid"
	"github.com/jackc/pgx/v5"
	"github.com/shopspring/decimal"
)

func TestManagerAuthorityReadsPropagatesConfiguredCatalogFailure(t *testing.T) {
	reads := managerAuthorityReads{manager: &fakeAuthorityAccounts{
		resources:     []account.Resource{{AccountID: "broken", ReadReady: false}},
		instrumentErr: errors.New("configured catalog unavailable"),
	}}
	if _, err := reads.Instruments(); err == nil {
		t.Fatal("configured-account catalog failure was published as successful truth")
	}
}

func TestGatewayAuthorityCatalogEnumeratesProfilesWithoutAccounts(t *testing.T) {
	for _, test := range []struct {
		mode account.RuntimeMode
		want func(string) bool
	}{
		{mode: account.RuntimeModeTest, want: func(environment string) bool { return environment == "demo" || environment == "testnet" }},
		{mode: account.RuntimeModeLive, want: func(environment string) bool { return environment == "live" }},
	} {
		t.Run(string(test.mode), func(t *testing.T) {
			authority, err := newGatewayAuthority(context.Background(), newMemoryDesiredStore(), newAuthorityManager(t, test.mode, newAuthorityLifecycle()), test.mode)
			if err != nil {
				t.Fatal(err)
			}
			response, err := authority.Execute(context.Background(), httpapi.AuthorityRequest{Operation: httpapi.AuthorityCatalogVenues})
			if err != nil {
				t.Fatal(err)
			}
			venues, ok := response.Body.([]authorityVenue)
			if !ok || len(venues) == 0 {
				t.Fatalf("venues=%T %+v", response.Body, response.Body)
			}
			seen := make(map[string]struct{}, len(venues))
			for _, venue := range venues {
				if !test.want(venue.Environment) || venue.Ready {
					t.Fatalf("mode=%s venue=%+v", test.mode, venue)
				}
				key := venue.Venue + "\x00" + venue.Environment
				if _, duplicate := seen[key]; duplicate {
					t.Fatalf("duplicate venue identity %q", key)
				}
				seen[key] = struct{}{}
			}
			instrumentsResponse, err := authority.Execute(context.Background(), httpapi.AuthorityRequest{Operation: httpapi.AuthorityCatalogInstruments})
			if err != nil {
				t.Fatal(err)
			}
			instruments, ok := instrumentsResponse.Body.(authorityCatalogInstruments)
			if !ok || instruments.Revision == "" || instruments.ObservedAt.IsZero() || len(instruments.Instruments) != 0 {
				t.Fatalf("instruments=%T %+v", instrumentsResponse.Body, instrumentsResponse.Body)
			}
		})
	}
}

func TestManagerAuthorityReadsPreservesCompositeCatalogFacts(t *testing.T) {
	accounts := &fakeAuthorityAccounts{
		resources: []account.Resource{{AccountID: "perp", Venue: exchange.VenueBinance, Environment: exchange.EnvironmentTestnet, Product: exchange.ProductPerp, Settlement: "USDT", ReadReady: true}},
		snapshot: catalog.Snapshot{Revision: "catalog-1", LoadedAt: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC), Instruments: []catalog.Instrument{{
			ID: "binance:testnet:perp:BTC-USDT", Venue: exchange.VenueBinance, Environment: exchange.EnvironmentTestnet, Product: exchange.ProductPerp, Settlement: "USDT",
			Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Rules: catalog.Rules{
				PriceIncrement: decimal.RequireFromString("0.1"), QuantityIncrement: decimal.RequireFromString("0.001"), MinQuantity: decimal.RequireFromString("0.001"),
				MinNotional: exchange.OptionalDecimal{Value: decimal.RequireFromString("5"), Valid: true},
			},
		}}},
	}
	got, err := (managerAuthorityReads{manager: accounts}).Instruments()
	if err != nil || len(got.Instruments) != 1 || got.Revision == "" || got.ObservedAt.IsZero() {
		t.Fatalf("Instruments() = %+v, %v", got, err)
	}
	instrument := got.Instruments[0]
	if instrument.Environment != "testnet" || instrument.Product != "perp" || instrument.Symbol != "BTC-USDT" || instrument.BaseAsset != "BTC" || instrument.QuoteAsset != "USDT" || instrument.Rules.MinNotional != "5" {
		t.Fatalf("instrument = %+v", instrument)
	}
}

func TestManagerAuthorityReadsAggregatesCatalogAuthorityDeterministically(t *testing.T) {
	oldest := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	resources := []account.Resource{
		{AccountID: "demo", Venue: exchange.VenueBinance, Environment: exchange.EnvironmentDemo, Product: exchange.ProductPerp, ReadReady: true},
		{AccountID: "live", Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, Product: exchange.ProductSpot, ReadReady: true},
	}
	snapshots := map[domain.AccountID]catalog.Snapshot{
		"demo": {Revision: "revision-b", LoadedAt: oldest.Add(time.Minute)},
		"live": {Revision: "revision-a", LoadedAt: oldest},
	}
	first, err := (managerAuthorityReads{manager: &fakeAuthorityAccounts{resources: resources, snapshots: snapshots}}).Instruments()
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(resources)
	second, err := (managerAuthorityReads{manager: &fakeAuthorityAccounts{resources: resources, snapshots: snapshots}}).Instruments()
	if err != nil || first.Revision != second.Revision || !first.ObservedAt.Equal(oldest) || !second.ObservedAt.Equal(oldest) {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
}

func TestManagerAuthorityReadsFetchesFundingOnlyWhenCapabilityAdvertisesIt(t *testing.T) {
	observedAt := time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC)
	fundingTime := observedAt.Add(time.Hour)
	nextFundingTime := observedAt.Add(9 * time.Hour)
	perp := &fakeAuthorityPerp{
		book:    exchange.OrderBook{Time: observedAt, Bids: []exchange.BookLevel{{Price: decimal.RequireFromString("100"), Quantity: decimal.RequireFromString("2.5")}}, Asks: []exchange.BookLevel{{Price: decimal.RequireFromString("101"), Quantity: decimal.RequireFromString("3.25")}}},
		funding: exchange.FundingRate{Rate: decimal.RequireFromString("0.0007"), ObservedAt: observedAt.Add(time.Second), FundingTime: fundingTime, NextFundingTime: nextFundingTime},
		history: exchange.FundingRatePage{Rates: []exchange.FundingRate{{Rate: decimal.RequireFromString("0.0006"), ObservedAt: observedAt.Add(-8 * time.Hour), FundingTime: observedAt.Add(-7 * time.Hour)}}},
	}
	accounts := &fakeAuthorityAccounts{
		resources:  []account.Resource{{AccountID: "perp", Venue: exchange.VenueBinance, Environment: exchange.EnvironmentTestnet, Product: exchange.ProductPerp, ReadReady: true}},
		instrument: exchange.Instrument{Identity: exchange.InstrumentIdentity{Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: "BTC-USDT"}},
		perp:       perp, capability: capability.Row{Product: "perp", Operations: capability.Operations{Funding: true}},
	}
	fact, err := (managerAuthorityReads{manager: accounts}).MarketData(context.Background(), "binance", "testnet", "perp", "BTC-USDT")
	if err != nil || fact.FundingRate != "0.0007" || fact.BestBidSize != "2.5" || fact.BestAskSize != "3.25" || len(fact.Bids) != 1 || len(fact.Asks) != 1 || len(fact.FundingHistory) != 1 || !fact.FundingObservedAt.Equal(observedAt.Add(time.Second)) || !fact.FundingTime.Equal(fundingTime) || !fact.NextFundingTime.Equal(nextFundingTime) || perp.fundingCalls != 1 || perp.historyCalls != 1 || perp.bookLimit != authorityDepthLimit || perp.historyLimit != authorityFundingHistoryLimit {
		t.Fatalf("fact=%+v fundingCalls=%d historyCalls=%d err=%v", fact, perp.fundingCalls, perp.historyCalls, err)
	}

	accounts.capability.Operations.Funding = false
	perp.fundingCalls = 0
	if _, err = (managerAuthorityReads{manager: accounts}).MarketData(context.Background(), "binance", "testnet", "perp", "BTC-USDT"); err == nil || perp.fundingCalls != 0 {
		t.Fatalf("unsupported fundingCalls=%d err=%v", perp.fundingCalls, err)
	}
}

func TestManagerAuthorityReadsSelectsExactEnvironmentAndProduct(t *testing.T) {
	observedAt := time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC)
	perp := &fakeAuthorityPerp{
		book:    exchange.OrderBook{Time: observedAt, Bids: []exchange.BookLevel{{Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1)}}, Asks: []exchange.BookLevel{{Price: decimal.NewFromInt(101), Quantity: decimal.NewFromInt(2)}}},
		funding: exchange.FundingRate{Rate: decimal.RequireFromString("0.0001"), ObservedAt: observedAt, FundingTime: observedAt.Add(time.Hour), NextFundingTime: observedAt.Add(9 * time.Hour)},
	}
	accounts := &fakeAuthorityAccounts{
		resources: []account.Resource{
			{AccountID: "live-spot", Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, Product: exchange.ProductSpot, ReadReady: true},
			{AccountID: "demo-perp", Venue: exchange.VenueBinance, Environment: exchange.EnvironmentDemo, Product: exchange.ProductPerp, ReadReady: true},
		},
		instrument: exchange.Instrument{Identity: exchange.InstrumentIdentity{Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: "BTC-USDT"}},
		perp:       perp, capability: capability.Row{Product: "perp", Operations: capability.Operations{Funding: true}},
	}
	fact, err := (managerAuthorityReads{manager: accounts}).MarketData(context.Background(), "binance", "demo", "perp", "BTC-USDT")
	if err != nil || fact.Environment != "demo" || fact.Product != "perp" || accounts.resolvedAccount != "demo-perp" {
		t.Fatalf("fact=%+v resolved=%q err=%v", fact, accounts.resolvedAccount, err)
	}
}

func TestManagerAuthorityReadsSelectsCapabilityForExactRoute(t *testing.T) {
	observedAt := time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC)
	perp := &fakeAuthorityPerp{
		book:    exchange.OrderBook{Time: observedAt, Bids: []exchange.BookLevel{{Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1)}}, Asks: []exchange.BookLevel{{Price: decimal.NewFromInt(101), Quantity: decimal.NewFromInt(2)}}},
		funding: exchange.FundingRate{Rate: decimal.RequireFromString("0.0001"), ObservedAt: observedAt, FundingTime: observedAt.Add(time.Hour), NextFundingTime: observedAt.Add(9 * time.Hour)},
	}
	accounts := &fakeAuthorityAccounts{
		resources: []account.Resource{{
			AccountID: "multi-route", Venue: exchange.VenueBinance, Environment: exchange.EnvironmentDemo,
			Product: exchange.ProductPerp, Settlement: "USDT", ReadReady: true,
		}},
		instrument: exchange.Instrument{Identity: exchange.InstrumentIdentity{Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: "BTC-USDT"}},
		perp:       perp,
		capability: capability.Row{Product: "spot", Operations: capability.Operations{}},
		capabilities: []capability.Row{
			{Product: "spot", Operations: capability.Operations{}},
			{Product: "perp", Settlement: "USDT", Operations: capability.Operations{Funding: true}},
		},
	}
	fact, err := (managerAuthorityReads{manager: accounts}).MarketData(context.Background(), "binance", "demo", "perp", "BTC-USDT")
	if err != nil || fact.FundingRate != "0.0001" || perp.fundingCalls != 1 {
		t.Fatalf("fact=%+v fundingCalls=%d err=%v", fact, perp.fundingCalls, err)
	}
}

func TestManagerAuthorityReadsProjectsGatewayRuntimeFacts(t *testing.T) {
	observedAt := time.Date(2026, 8, 5, 1, 3, 4, 0, time.UTC)
	accounts := &fakeAuthorityAccounts{
		runtime: account.RuntimeAccount{
			Desired: account.DesiredRuntime{AccountID: "account-a", DesiredStateRevision: 7, RuntimeGeneration: 3, CredentialVersion: 2},
			State:   account.RuntimeStateActive, Ready: true, RuntimeVersion: 9, LastAppliedDesiredStateRevision: 7, LastAppliedRuntimeGeneration: 3, ObservedAt: observedAt,
		},
		status:     account.Status{Resource: account.Resource{AccountID: "account-a", Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Settlement: "USDT", ReadReady: true, WriteReady: true}},
		capability: capability.Row{RowID: "binance:perp:USDT", Venue: "binance", Product: "perp", Settlement: "USDT", Operations: capability.Operations{Place: true, Cancel: true, Fills: true, Positions: true}},
		capabilities: []capability.Row{
			{RowID: "binance:spot", Venue: "binance", Product: "spot", Operations: capability.Operations{Place: true, Cancel: true}},
			{RowID: "binance:perp:USDT", Venue: "binance", Product: "perp", Settlement: "USDT", Operations: capability.Operations{Place: true, Cancel: true, Fills: true, Positions: true}},
		},
	}
	projection, err := (managerAuthorityReads{manager: accounts}).Runtime("account-a")
	if err != nil || projection.AccountID != "account-a" || projection.RuntimeVersion != 9 || projection.LastAppliedDesiredStateRevision != 7 || !projection.Readiness.Ready || !projection.WriteReadiness.Ready || len(projection.Capabilities) != 2 || !projection.Capabilities[0].WriteReady || !projection.Capabilities[1].WriteReady || !projection.ObservedAt.Equal(observedAt) {
		t.Fatalf("Runtime()=%+v err=%v", projection, err)
	}
}

func TestManagerAuthorityReadsDerivesCapabilityReadinessFromExactRouteStatus(t *testing.T) {
	tests := []struct {
		name       string
		status     account.Status
		wantRead   bool
		wantWrite  bool
		wantReason string
	}{
		{name: "private stream gap", status: account.Status{Resource: account.Resource{ReadReady: true, Reason: account.ReasonPrivateStreamGap}}, wantRead: true, wantReason: account.ReasonPrivateStreamGap},
		{name: "stale catalog", status: account.Status{Resource: account.Resource{Reason: catalog.ReasonCatalogStale}}, wantReason: catalog.ReasonCatalogStale},
		{name: "live write denied", status: account.Status{Resource: account.Resource{ReadReady: true, Reason: account.ErrLiveWriteDisabled.Error()}}, wantRead: true, wantReason: account.ErrLiveWriteDisabled.Error()},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			accounts := &fakeAuthorityAccounts{
				runtime: account.RuntimeAccount{
					Desired: account.DesiredRuntime{AccountID: "account-a", DesiredStateRevision: 7, RuntimeGeneration: 3, CredentialVersion: 2},
					State:   account.RuntimeStateActive, Ready: true, RuntimeVersion: 9, LastAppliedDesiredStateRevision: 7, LastAppliedRuntimeGeneration: 3, ObservedAt: time.Now(),
				},
				status:        account.Status{Resource: account.Resource{AccountID: "account-a", ReadReady: true, WriteReady: true}},
				capabilities:  []capability.Row{{RowID: "binance:perp:USDT", Venue: "binance", Product: "perp", Settlement: "USDT", Operations: capability.Operations{Place: true}}},
				routeStatuses: map[string]account.Status{"perp\x00USDT": test.status},
			}
			projection, err := (managerAuthorityReads{manager: accounts}).Runtime("account-a")
			if err != nil {
				t.Fatal(err)
			}
			if len(accounts.routeStatusCalls) != 1 || accounts.routeStatusCalls[0] != "perp\x00USDT" {
				t.Fatalf("route status calls=%q", accounts.routeStatusCalls)
			}
			if projection.Readiness.Ready != test.wantRead || projection.WriteReadiness.Ready != test.wantWrite || projection.WriteReadiness.Reason != test.wantReason || len(projection.Capabilities) != 1 || projection.Capabilities[0].WriteReady != test.wantWrite {
				t.Fatalf("projection=%+v", projection)
			}
		})
	}
}

type fakeAuthorityAccounts struct {
	resources        []account.Resource
	instrumentErr    error
	snapshot         catalog.Snapshot
	instrument       exchange.Instrument
	perp             exchange.PerpClient
	capability       capability.Row
	capabilities     []capability.Row
	resolvedAccount  domain.AccountID
	snapshots        map[domain.AccountID]catalog.Snapshot
	runtime          account.RuntimeAccount
	status           account.Status
	routeStatuses    map[string]account.Status
	routeStatusCalls []string
}

func (accounts *fakeAuthorityAccounts) Accounts() []account.Resource { return accounts.resources }

func (accounts *fakeAuthorityAccounts) Instruments(accountID domain.AccountID) (catalog.Snapshot, error) {
	if accounts.snapshots != nil {
		return accounts.snapshots[accountID], accounts.instrumentErr
	}
	return accounts.snapshot, accounts.instrumentErr
}
func (accounts *fakeAuthorityAccounts) Resolve(_ context.Context, accountID domain.AccountID, _ domain.InstrumentID) (exchange.Instrument, error) {
	accounts.resolvedAccount = accountID
	return accounts.instrument, nil
}
func (accounts *fakeAuthorityAccounts) Spot(domain.AccountID) (exchange.SpotClient, error) {
	return nil, account.ErrProductMismatch
}
func (accounts *fakeAuthorityAccounts) Perp(domain.AccountID) (exchange.PerpClient, error) {
	return accounts.perp, nil
}
func (accounts *fakeAuthorityAccounts) Capability(domain.AccountID) (capability.Row, error) {
	return accounts.capability, nil
}
func (accounts *fakeAuthorityAccounts) Capabilities(domain.AccountID) ([]capability.Row, error) {
	if len(accounts.capabilities) == 0 {
		return []capability.Row{accounts.capability}, nil
	}
	return append([]capability.Row(nil), accounts.capabilities...), nil
}
func (accounts *fakeAuthorityAccounts) Runtime(domain.AccountID) (account.RuntimeAccount, error) {
	return accounts.runtime, nil
}
func (accounts *fakeAuthorityAccounts) Status(domain.AccountID) (account.Status, error) {
	return accounts.status, nil
}
func (accounts *fakeAuthorityAccounts) RouteStatus(_ domain.AccountID, product, settlement string) (account.Status, error) {
	key := product + "\x00" + settlement
	accounts.routeStatusCalls = append(accounts.routeStatusCalls, key)
	if status, ok := accounts.routeStatuses[key]; ok {
		return status, nil
	}
	return accounts.status, nil
}

type fakeAuthorityPerp struct {
	exchange.PerpClient
	book         exchange.OrderBook
	funding      exchange.FundingRate
	history      exchange.FundingRatePage
	fundingCalls int
	historyCalls int
	bookLimit    int
	historyLimit int
}

func (client *fakeAuthorityPerp) OrderBook(_ context.Context, request exchange.OrderBookRequest) (exchange.OrderBook, error) {
	client.bookLimit = request.Limit
	return client.book, nil
}
func (client *fakeAuthorityPerp) FundingRate(context.Context, exchange.FundingRateRequest) (exchange.FundingRate, error) {
	client.fundingCalls++
	return client.funding, nil
}
func (client *fakeAuthorityPerp) FundingRateHistory(_ context.Context, request exchange.FundingRateHistoryRequest) (exchange.FundingRatePage, error) {
	client.historyCalls++
	client.historyLimit = request.Limit
	return client.history, nil
}

func TestGatewayAuthorityProjectsConfiguredCatalogAndMarketData(t *testing.T) {
	ctx := context.Background()
	authority, err := newGatewayAuthority(ctx, newMemoryDesiredStore(), newAuthorityManager(t, account.RuntimeModeTest, newAuthorityLifecycle()), account.RuntimeModeTest)
	if err != nil {
		t.Fatal(err)
	}
	observedAt := time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC)
	authority.reads = staticAuthorityReads{
		venues:      []authorityVenue{{Venue: "binance", Environment: "demo", Ready: true}},
		instruments: []authorityInstrument{{InstrumentID: "binance:demo:perp:BTC-USDT", Venue: "binance", Ready: true}},
		marketData: authorityMarketData{
			Venue: "binance", Environment: "demo", Product: "perp", InstrumentID: "binance:demo:perp:BTC-USDT", BestBid: "100", BestAsk: "101",
			MarkPrice: "100.5", FundingRate: "0.0001", ObservedAt: observedAt,
		},
	}

	venues, err := authority.Execute(ctx, httpapi.AuthorityRequest{Operation: httpapi.AuthorityCatalogVenues})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := venues.Body.([]authorityVenue); !ok || len(got) != 1 || !got[0].Ready {
		t.Fatalf("venues=%T %+v", venues.Body, venues.Body)
	}
	instruments, err := authority.Execute(ctx, httpapi.AuthorityRequest{Operation: httpapi.AuthorityCatalogInstruments})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := instruments.Body.(authorityCatalogInstruments); !ok || len(got.Instruments) != 1 || got.Instruments[0].InstrumentID == "" || got.Revision == "" || got.ObservedAt.IsZero() {
		t.Fatalf("instruments=%T %+v", instruments.Body, instruments.Body)
	}
	market, err := authority.Execute(ctx, httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityMarketData,
		Query:     map[string][]string{"venue": {"binance"}, "environment": {"demo"}, "product": {"perp"}, "instrumentId": {"binance:demo:perp:BTC-USDT"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := market.Body.(authorityMarketData); !ok || got.MarkPrice != "100.5" || got.ObservedAt != observedAt {
		t.Fatalf("market=%T %+v", market.Body, market.Body)
	}
}

func TestMigrationValidationPublishesStaticTestCatalogWithoutPrivateRuntime(t *testing.T) {
	authority := validationSnapshotAuthority{mode: account.RuntimeModeTest}
	venuesResponse, err := authority.Execute(context.Background(), httpapi.AuthorityRequest{Operation: httpapi.AuthorityCatalogVenues})
	if err != nil {
		t.Fatal(err)
	}
	venues, ok := venuesResponse.Body.([]authorityVenue)
	if !ok || len(venues) == 0 {
		t.Fatalf("venues=%T %+v", venuesResponse.Body, venuesResponse.Body)
	}
	seen := make(map[string]struct{}, len(venues))
	for _, venue := range venues {
		key := venue.Venue + "\x00" + venue.Environment
		if venue.Venue == "" || venue.Environment == "" || venue.Environment == "live" || venue.Ready {
			t.Fatalf("unsafe validation venue=%+v", venue)
		}
		if _, exists := seen[key]; exists {
			t.Fatalf("duplicate validation venue=%+v", venue)
		}
		seen[key] = struct{}{}
	}

	instrumentsResponse, err := authority.Execute(context.Background(), httpapi.AuthorityRequest{Operation: httpapi.AuthorityCatalogInstruments})
	if err != nil {
		t.Fatal(err)
	}
	instruments, ok := instrumentsResponse.Body.(authorityCatalogInstruments)
	if !ok || instruments.Revision == "" || instruments.ObservedAt.IsZero() || instruments.Instruments == nil || len(instruments.Instruments) != 0 {
		t.Fatalf("instruments=%T %+v", instrumentsResponse.Body, instrumentsResponse.Body)
	}
}

func TestGatewayAuthorityInternalReadRoutesReturnStrictAuthenticatedPlatformDTOs(t *testing.T) {
	ctx := context.Background()
	authority, err := newGatewayAuthority(ctx, newMemoryDesiredStore(), newAuthorityManager(t, account.RuntimeModeTest, newAuthorityLifecycle()), account.RuntimeModeTest)
	if err != nil {
		t.Fatal(err)
	}
	authority.reads = staticAuthorityReads{
		venues:      []authorityVenue{{Venue: "binance", Environment: "demo", Ready: true}},
		instruments: []authorityInstrument{{InstrumentID: "BTC-USDT", Venue: "binance", Ready: true}},
		marketData:  authorityMarketData{Venue: "binance", Environment: "demo", Product: "perp", InstrumentID: "BTC-USDT", MarkPrice: "100.5", ObservedAt: time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC)},
		runtime: authorityRuntimeProjection{
			AccountID: "account-a", DesiredStateRevision: 7, RuntimeGeneration: 3, CredentialVersion: 2, RuntimeVersion: 9,
			Readiness: authorityProjectionReadiness{Ready: true}, WriteReadiness: authorityProjectionReadiness{Ready: true},
			LastAppliedDesiredStateRevision: 7, LastAppliedRuntimeGeneration: 3,
			Capabilities: []authorityRuntimeCapability{{RowID: "binance:perp:USDT", Venue: "binance", Product: "perp", Settlement: "USDT", WriteReady: true, Operations: capability.Operations{Place: true, Cancel: true, Fills: true, Positions: true}}},
			ObservedAt:   time.Date(2026, 8, 5, 1, 3, 4, 0, time.UTC),
		},
	}
	handler, err := httpapi.NewInternalHandler(httpapi.InternalOptions{
		Authority: authority,
		Authenticate: func(request *http.Request) error {
			if request.Header.Get("Authorization") != "Bearer gateway-adapter-token" {
				return errors.New("authentication required")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}

	unauthorized := httptest.NewRecorder()
	handler.ServeHTTP(unauthorized, httptest.NewRequest(http.MethodGet, "/internal/v1/catalog/venues", nil))
	if unauthorized.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status=%d body=%s", unauthorized.Code, unauthorized.Body.String())
	}

	for _, test := range []struct {
		path   string
		assert func(*testing.T, json.RawMessage)
	}{
		{path: "/internal/v1/catalog/venues", assert: func(t *testing.T, data json.RawMessage) {
			var items []authorityVenue
			if err := json.Unmarshal(data, &items); err != nil || len(items) != 1 || items[0].Environment != "demo" {
				t.Fatalf("venues=%+v err=%v", items, err)
			}
		}},
		{path: "/internal/v1/catalog/instruments", assert: func(t *testing.T, data json.RawMessage) {
			var items authorityCatalogInstruments
			if err := json.Unmarshal(data, &items); err != nil || len(items.Instruments) != 1 || items.Instruments[0].InstrumentID != "BTC-USDT" || items.Revision == "" || items.ObservedAt.IsZero() {
				t.Fatalf("instruments=%+v err=%v", items, err)
			}
		}},
		{path: "/internal/v1/market-data?venue=binance&environment=demo&product=perp&instrumentId=BTC-USDT", assert: func(t *testing.T, data json.RawMessage) {
			var fact authorityMarketData
			if err := json.Unmarshal(data, &fact); err != nil || fact.MarkPrice != "100.5" || fact.ObservedAt.IsZero() {
				t.Fatalf("market data=%+v err=%v", fact, err)
			}
		}},
		{path: "/internal/v1/accounts/runtime/account-a", assert: func(t *testing.T, data json.RawMessage) {
			var runtime authorityRuntimeProjection
			if err := json.Unmarshal(data, &runtime); err != nil || runtime.AccountID != "account-a" || runtime.RuntimeVersion != 9 || runtime.LastAppliedDesiredStateRevision != 7 || len(runtime.Capabilities) != 1 || !runtime.Capabilities[0].WriteReady || runtime.ObservedAt.IsZero() || strings.Contains(string(data), "secretRef") {
				t.Fatalf("runtime projection=%+v body=%s err=%v", runtime, data, err)
			}
		}},
	} {
		request := httptest.NewRequest(http.MethodGet, test.path, nil)
		request.Header.Set("Authorization", "Bearer gateway-adapter-token")
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", test.path, response.Code, response.Body.String())
		}
		envelope := struct {
			Data json.RawMessage `json:"data"`
		}{}
		if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
			t.Fatalf("%s envelope: %v", test.path, err)
		}
		test.assert(t, envelope.Data)
	}
}

func TestGatewayAuthorityRejectsUnavailableMarketDataProjection(t *testing.T) {
	ctx := context.Background()
	authority, err := newGatewayAuthority(ctx, newMemoryDesiredStore(), newAuthorityManager(t, account.RuntimeModeTest, newAuthorityLifecycle()), account.RuntimeModeTest)
	if err != nil {
		t.Fatal(err)
	}
	authority.reads = staticAuthorityReads{err: errors.New("read capability unavailable")}
	_, err = authority.Execute(ctx, httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityMarketData,
		Query:     map[string][]string{"venue": {"binance"}, "environment": {"demo"}, "product": {"perp"}, "instrumentId": {"BTC-USDT"}},
	})
	var authorityErr httpapi.AuthorityError
	if !errors.As(err, &authorityErr) || authorityErr.Status != http.StatusInternalServerError || !authorityErr.Retryable {
		t.Fatalf("error=%T %v", err, err)
	}
}

func TestGatewayAuthoritySeparatesRuntimeStatusAndReadinessDTOs(t *testing.T) {
	ctx := context.Background()
	desiredStore := newMemoryDesiredStore()
	authority, err := newGatewayAuthority(ctx, desiredStore, newAuthorityManager(t, account.RuntimeModeTest, newAuthorityLifecycle()), account.RuntimeModeTest)
	if err != nil {
		t.Fatal(err)
	}

	statusResponse, err := authority.Execute(ctx, httpapi.AuthorityRequest{Operation: httpapi.AuthorityRuntimeStatus})
	if err != nil {
		t.Fatal(err)
	}
	status, ok := statusResponse.Body.(authorityRuntimeStatus)
	if !ok || status.Mode != account.RuntimeModeTest || len(status.Accounts) != 0 {
		t.Fatalf("status=%T %+v", statusResponse.Body, statusResponse.Body)
	}
	readinessResponse, err := authority.Execute(ctx, httpapi.AuthorityRequest{Operation: httpapi.AuthorityRuntimeReadiness})
	if err != nil {
		t.Fatal(err)
	}
	readiness, ok := readinessResponse.Body.(httpapi.RuntimeReadiness)
	if !ok || !readiness.Ready {
		t.Fatalf("empty readiness=%T %+v", readinessResponse.Body, readinessResponse.Body)
	}

	seedActiveAuthorityAccount(t, ctx, authority, desiredStore)
	readinessResponse, err = authority.Execute(ctx, httpapi.AuthorityRequest{Operation: httpapi.AuthorityRuntimeReadiness})
	if err != nil {
		t.Fatal(err)
	}
	readiness, ok = readinessResponse.Body.(httpapi.RuntimeReadiness)
	if !ok || !readiness.Ready {
		t.Fatalf("active readiness=%T %+v", readinessResponse.Body, readinessResponse.Body)
	}
	statusResponse, err = authority.Execute(ctx, httpapi.AuthorityRequest{Operation: httpapi.AuthorityRuntimeStatus})
	if err != nil {
		t.Fatal(err)
	}
	status, ok = statusResponse.Body.(authorityRuntimeStatus)
	if !ok || status.Mode != account.RuntimeModeTest || len(status.Accounts) != 1 {
		t.Fatalf("active status=%T %+v", statusResponse.Body, statusResponse.Body)
	}
}

func TestGatewayAuthorityRuntimeListAndStatusJSONOmitSecretReference(t *testing.T) {
	ctx := context.Background()
	desiredStore := newMemoryDesiredStore()
	authority, err := newGatewayAuthority(ctx, desiredStore, newAuthorityManager(t, account.RuntimeModeTest, newAuthorityLifecycle()), account.RuntimeModeTest)
	if err != nil {
		t.Fatal(err)
	}
	seedActiveAuthorityAccount(t, ctx, authority, desiredStore)
	handler, err := httpapi.NewInternalHandler(httpapi.InternalOptions{Authority: authority, Authenticate: func(*http.Request) error { return nil }})
	if err != nil {
		t.Fatal(err)
	}

	for _, path := range []string{"/internal/v1/accounts/runtime", "/internal/v1/runtime/status"} {
		t.Run(path, func(t *testing.T) {
			response := httptest.NewRecorder()
			handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, path, nil))
			if response.Code != http.StatusOK {
				t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
			}
			assertRuntimeJSONHasNoSecretRef(t, response.Body.Bytes(), "opaque-initial")
		})
	}
}

func assertRuntimeJSONHasNoSecretRef(t *testing.T, encoded []byte, canary string) {
	t.Helper()
	if strings.Contains(strings.ToLower(string(encoded)), strings.ToLower(canary)) {
		t.Fatalf("runtime response leaked secret canary: %s", encoded)
	}
	var value any
	if err := json.Unmarshal(encoded, &value); err != nil {
		t.Fatal(err)
	}
	var visit func(any)
	visit = func(current any) {
		switch typed := current.(type) {
		case map[string]any:
			for key, child := range typed {
				if strings.EqualFold(key, "secretRef") {
					t.Errorf("runtime response serialized forbidden field %q: %s", key, encoded)
				}
				visit(child)
			}
		case []any:
			for _, child := range typed {
				visit(child)
			}
		}
	}
	visit(value)
}

type staticAuthorityReads struct {
	venues      []authorityVenue
	instruments []authorityInstrument
	marketData  authorityMarketData
	candles     authorityCandles
	runtime     authorityRuntimeProjection
	err         error
}

func (reads staticAuthorityReads) Venues() []authorityVenue {
	return append([]authorityVenue(nil), reads.venues...)
}

func (reads staticAuthorityReads) Instruments() (authorityCatalogInstruments, error) {
	return authorityCatalogInstruments{Revision: "static-catalog", ObservedAt: time.Now().UTC(), Instruments: append([]authorityInstrument(nil), reads.instruments...)}, reads.err
}

func (reads staticAuthorityReads) MarketData(context.Context, string, string, string, string) (authorityMarketData, error) {
	return reads.marketData, reads.err
}

func (reads staticAuthorityReads) Candles(_ context.Context, query authorityCandlesQuery) (authorityCandles, error) {
	candles := reads.candles
	// The dispatcher rejects a page whose identity does not echo the request, so
	// a stub that returned a zero-valued identity would fail every caller for a
	// reason that has nothing to do with what the test is asserting.
	candles.Venue, candles.Environment = query.Venue, query.Environment
	candles.Product, candles.InstrumentID, candles.Interval = query.Product, query.InstrumentID, query.Interval
	return candles, reads.err
}

func (reads staticAuthorityReads) Runtime(string) (authorityRuntimeProjection, error) {
	return reads.runtime, reads.err
}

func TestGatewayAuthorityRestoresActiveAndRotatingAccounts(t *testing.T) {
	ctx := context.Background()
	desiredStore := newMemoryDesiredStore()
	desiredStore.items["active"] = store.DesiredAccount{
		AccountID: "active", Venue: "binance", VenueEnvironment: "demo", SecretRef: "opaque-active",
		DesiredStateRevision: 2, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: store.DesiredActive,
	}
	desiredStore.items["rotating"] = store.DesiredAccount{
		AccountID: "rotating", Venue: "binance", VenueEnvironment: "demo", SecretRef: "opaque-new", PreviousSecretRef: "opaque-old",
		DesiredStateRevision: 3, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: store.DesiredRotating,
		RotationFence: &store.RotationFence{ID: "rotate-1", State: store.RotationArmed, FromCredentialVersion: 1, ToCredentialVersion: 2, FromRuntimeGeneration: 1, ToRuntimeGeneration: 2, ArmedAt: time.Unix(1, 0)},
	}
	lifecycle := newAuthorityLifecycle()
	manager := newAuthorityManager(t, account.RuntimeModeTest, lifecycle)
	if _, err := newGatewayAuthority(ctx, desiredStore, manager, account.RuntimeModeTest); err != nil {
		t.Fatalf("restore: %v", err)
	}
	if got, err := manager.Runtime("active"); err != nil || !got.Ready {
		t.Fatalf("active=%+v err=%v", got, err)
	}
	rotating, err := manager.Runtime("rotating")
	if err != nil || !rotating.Ready || rotating.Desired.SecretRef.Fingerprint() == "" || rotating.Desired.DesiredStateRevision != 3 {
		t.Fatalf("rotating=%+v err=%v", rotating, err)
	}
	if lifecycle.activations != 2 || lifecycle.lastSecretRef != "opaque-old" {
		t.Fatalf("restore lifecycle=%+v", lifecycle)
	}
}

func TestOpaqueRuntimeLifecycleContainsSecretMaterialAndRedactsFailures(t *testing.T) {
	ctx := context.Background()
	desired := authorityDesiredRuntime(t, "opaque-provider-ref")
	var activatedSecret string
	deactivations := 0
	lifecycle, err := newOpaqueRuntimeLifecycle(
		func(context.Context, secret.OpaqueRef) (string, error) { return "secret-material-canary", nil },
		func(_ context.Context, _ account.DesiredRuntime, material string) error {
			activatedSecret = material
			return nil
		},
		func(context.Context, domain.AccountID) error { deactivations++; return nil },
		func(context.Context, domain.AccountID) (account.RuntimeBlockers, error) {
			return account.RuntimeBlockers{OpenOrders: 1}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := lifecycle.Validate(ctx, desired); err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if err := lifecycle.Activate(ctx, desired); err != nil {
		t.Fatalf("Activate: %v", err)
	}
	if activatedSecret != "secret-material-canary" {
		t.Fatalf("activation did not receive resolved material")
	}
	if blockers, err := lifecycle.Blockers(ctx, desired.AccountID); err != nil || blockers.OpenOrders != 1 {
		t.Fatalf("Blockers=%+v err=%v", blockers, err)
	}
	if err := lifecycle.Deactivate(ctx, desired.AccountID); err != nil || deactivations != 1 {
		t.Fatalf("Deactivate err=%v calls=%d", err, deactivations)
	}

	failing, err := newOpaqueRuntimeLifecycle(
		func(context.Context, secret.OpaqueRef) (string, error) {
			return "", errors.New("secret-material-canary")
		},
		func(context.Context, account.DesiredRuntime, string) error { return nil },
		func(context.Context, domain.AccountID) error { return nil },
		func(context.Context, domain.AccountID) (account.RuntimeBlockers, error) {
			return account.RuntimeBlockers{}, nil
		},
	)
	if err != nil {
		t.Fatal(err)
	}
	if err := failing.Validate(ctx, desired); err == nil || strings.Contains(err.Error(), "secret-material-canary") {
		t.Fatalf("validation error leaked secret material: %v", err)
	}
}

func authorityDesiredRuntime(t *testing.T, refValue string) account.DesiredRuntime {
	t.Helper()
	ref, err := secret.ParseOpaqueRef(refValue)
	if err != nil {
		t.Fatal(err)
	}
	return account.DesiredRuntime{
		AccountID: "dynamic", Venue: "binance", VenueEnvironment: "demo", Mode: account.RuntimeModeTest,
		SecretRef: ref, DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1,
		RequestedState: account.RuntimeStateActive,
	}
}

// seedActiveAuthorityAccount brings one account to active the way a restart
// does: the desired record lands in the store and the authority restores it.
// The account lifecycle service owns the write path now, so the authority has
// no create/activate operation left for a test to call.
func seedActiveAuthorityAccount(t *testing.T, ctx context.Context, authority *gatewayAuthority, desiredStore *memoryDesiredStore) {
	t.Helper()
	desiredStore.items["dynamic"] = store.DesiredAccount{
		AccountID: "dynamic", Venue: "binance", VenueEnvironment: "demo", SecretRef: "opaque-initial",
		DesiredStateRevision: 2, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: store.DesiredActive,
	}
	if err := authority.restore(ctx); err != nil {
		t.Fatal(err)
	}
}

type authorityLifecycle struct {
	validations   int
	activations   int
	deactivations int
	lastSecretRef string
	blockers      account.RuntimeBlockers
	activationErr error
}

func newAuthorityLifecycle() *authorityLifecycle { return &authorityLifecycle{} }

func (lifecycle *authorityLifecycle) Validate(_ context.Context, desired account.DesiredRuntime) error {
	lifecycle.validations++
	lifecycle.lastSecretRef = opaqueRefValue(desired.SecretRef)
	return nil
}

func (lifecycle *authorityLifecycle) Activate(_ context.Context, desired account.DesiredRuntime) error {
	lifecycle.activations++
	lifecycle.lastSecretRef = opaqueRefValue(desired.SecretRef)
	return lifecycle.activationErr
}

func (lifecycle *authorityLifecycle) Deactivate(context.Context, domain.AccountID) error {
	lifecycle.deactivations++
	return nil
}

func (lifecycle *authorityLifecycle) Blockers(context.Context, domain.AccountID) (account.RuntimeBlockers, error) {
	return lifecycle.blockers, nil
}

func opaqueRefValue(ref secret.OpaqueRef) string { return ref.ProviderRef().RawForProvider() }

func newAuthorityManager(t *testing.T, mode account.RuntimeMode, lifecycle account.RuntimeLifecycle) *account.Manager {
	t.Helper()
	gatewayCatalog, err := catalog.NewService(time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := account.NewManager(context.Background(), nil, account.Options{
		Secrets: secret.StaticProvider{}, ExchangeCatalog: exchange.NewDefaultMemoryCatalog(), GatewayCatalog: gatewayCatalog,
		HTTPClient: &http.Client{}, RuntimeMode: mode, Lifecycle: lifecycle,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

type authorityCandidateRoute struct{}

func (authorityCandidateRoute) Probe(context.Context) error { return nil }
func (authorityCandidateRoute) Close() error                { return nil }

type authorityCandidateBuilder struct{}

func (authorityCandidateBuilder) BuildRoute(context.Context, store.DesiredSnapshot, store.RouteDefinition, *credential.Decoded) (account.CandidateRoute, error) {
	return authorityCandidateRoute{}, nil
}

type authorityFailingCandidateBuilder struct {
	failGeneration uint64
}

func (builder authorityFailingCandidateBuilder) BuildRoute(_ context.Context, snapshot store.DesiredSnapshot, _ store.RouteDefinition, _ *credential.Decoded) (account.CandidateRoute, error) {
	if snapshot.Fence.RuntimeGeneration == builder.failGeneration {
		return nil, errors.New("secret-material-canary")
	}
	return authorityCandidateRoute{}, nil
}

type authorityCredentialResolver struct{}

func (authorityCredentialResolver) Check(context.Context) error { return nil }
func (authorityCredentialResolver) Resolve(context.Context, secretstore.OpaqueRef) ([]byte, error) {
	return []byte(`{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"key","apiSecret":"secret"}}`), nil
}

type authorityStaticCredentialResolver struct{ bundle []byte }

func (authorityStaticCredentialResolver) Check(context.Context) error { return nil }
func (resolver authorityStaticCredentialResolver) Resolve(context.Context, secretstore.OpaqueRef) ([]byte, error) {
	return append([]byte(nil), resolver.bundle...), nil
}

type authorityFailOnceCredentialResolver struct{ failures int }

func (*authorityFailOnceCredentialResolver) Check(context.Context) error { return nil }
func (resolver *authorityFailOnceCredentialResolver) Resolve(context.Context, secretstore.OpaqueRef) ([]byte, error) {
	if resolver.failures > 0 {
		resolver.failures--
		return nil, errors.New("temporary credential resolver failure")
	}
	return authorityCredentialResolver{}.Resolve(context.Background(), "")
}

type snapshotAuthorityStore struct {
	*memoryDesiredStore
	pending           *store.PendingSnapshot
	active            *store.ActiveSnapshot
	observations      []store.CandidateObservation
	promotions        int
	retirement        *store.RetirementResult
	retirementDigest  string
	retirementCommits int
	retireErr         error
	rotationRequest   *store.RotationFinalizationRequest
	rotationResult    *store.RotationFinalizationResult
	rotationCommits   int
	rotationErr       error
}

func newSnapshotAuthorityStore() *snapshotAuthorityStore {
	return &snapshotAuthorityStore{memoryDesiredStore: newMemoryDesiredStore()}
}

func (memory *snapshotAuthorityStore) PreparePending(_ context.Context, desired store.DesiredSnapshot) (store.PendingSnapshot, error) {
	pending := store.PendingSnapshot{DesiredSnapshot: desired, OperationState: store.OperationPrepared}
	memory.pending = &pending
	return pending, nil
}

func (memory *snapshotAuthorityStore) RecordCandidateObservation(_ context.Context, observation store.CandidateObservation) error {
	memory.observations = append(memory.observations, observation)
	return nil
}

func (memory *snapshotAuthorityStore) MarkCandidateReady(context.Context, secretstore.OperationID) error {
	if memory.pending == nil || len(memory.observations) == 0 {
		return store.ErrCandidateNotReady
	}
	memory.pending.OperationState = store.OperationCandidateReady
	return nil
}

func (memory *snapshotAuthorityStore) MarkCandidateFailed(context.Context, secretstore.OperationID) error {
	if memory.pending == nil || len(memory.observations) == 0 {
		return store.ErrCandidateNotReady
	}
	memory.pending.OperationState = store.OperationCandidateFailed
	memory.pending.Observations = append([]store.CandidateObservation(nil), memory.observations...)
	return nil
}

func (memory *snapshotAuthorityStore) PromotePending(_ context.Context, request store.PromotionRequest) (store.PromotionResult, error) {
	if memory.pending == nil || memory.pending.OperationID != request.OperationID {
		return store.PromotionResult{}, store.ErrOperationConflict
	}
	active := store.ActiveSnapshot{DesiredSnapshot: memory.pending.DesiredSnapshot, PromotedAt: time.Now()}
	memory.active, memory.pending = &active, nil
	memory.promotions++
	return store.PromotionResult{Active: active}, nil
}

func (memory *snapshotAuthorityStore) LookupRotationFinalization(_ context.Context, request store.RotationFinalizationRequest) (store.RotationFinalizationResult, error) {
	if memory.rotationResult == nil || memory.rotationRequest == nil {
		return store.RotationFinalizationResult{}, store.ErrNotFound
	}
	storedDigest, storedErr := memory.rotationRequest.CanonicalDigest()
	requestDigest, requestErr := request.CanonicalDigest()
	if storedErr != nil || requestErr != nil || memory.rotationRequest.OperationID != request.OperationID || storedDigest != requestDigest {
		return store.RotationFinalizationResult{}, store.ErrOperationConflict
	}
	return *memory.rotationResult, nil
}

func (memory *snapshotAuthorityStore) FinalizeRotation(_ context.Context, request store.RotationFinalizationRequest) (store.RotationFinalizationResult, error) {
	if memory.rotationErr != nil {
		return store.RotationFinalizationResult{}, memory.rotationErr
	}
	if replay, err := memory.LookupRotationFinalization(context.Background(), request); err == nil || !errors.Is(err, store.ErrNotFound) {
		return replay, err
	}
	result := store.RotationFinalizationResult{
		OperationID: request.OperationID, AccountID: request.AccountID,
		ActiveFence: request.ExpectedActiveFence, RetiringFence: request.ExpectedRetiringFence,
		TombstoneRevision: request.ExpectedActiveFence.DesiredStateRevision,
		RetiringState:     "retired", SecretReferenceState: "tombstoned", CompletedAt: time.Now().UTC(),
	}
	requestCopy, resultCopy := request, result
	memory.rotationRequest, memory.rotationResult = &requestCopy, &resultCopy
	memory.rotationCommits++
	return result, nil
}

func (memory *snapshotAuthorityStore) RetireAccount(_ context.Context, request store.RetirementRequest) (store.RetirementResult, error) {
	if memory.retireErr != nil {
		return store.RetirementResult{}, memory.retireErr
	}
	digest, err := request.CanonicalDigest()
	if err != nil {
		return store.RetirementResult{}, err
	}
	if memory.retirement != nil {
		if memory.retirement.OperationID != request.OperationID || memory.retirementDigest != digest {
			return store.RetirementResult{}, store.ErrOperationConflict
		}
		return *memory.retirement, nil
	}
	if memory.active == nil || memory.active.Fence != request.ExpectedActiveFence {
		return store.RetirementResult{}, store.ErrPromotionFenceConflict
	}
	result := store.RetirementResult{
		OperationID: request.OperationID, AccountID: request.AccountID, TombstoneRevision: request.TombstoneRevision,
		Tombstoned: true, CompletedAt: time.Now().UTC(),
	}
	memory.active, memory.pending = nil, nil
	memory.retirement, memory.retirementDigest = &result, digest
	memory.retirementCommits++
	return result, nil
}

func (memory *snapshotAuthorityStore) LoadRuntimeState(context.Context, domain.AccountID) (store.RuntimeState, error) {
	state := store.RuntimeState{AccountID: "account-a", Active: memory.active, Pending: memory.pending}
	if memory.retirement != nil {
		state.Tombstoned = true
		state.TombstoneRevision = memory.retirement.TombstoneRevision
	}
	return state, nil
}

func (memory *snapshotAuthorityStore) ListRuntimeStateForRecovery(context.Context) ([]store.RuntimeState, error) {
	if memory.retirement != nil {
		return []store.RuntimeState{{
			AccountID: "account-a", Tombstoned: true, TombstoneRevision: memory.retirement.TombstoneRevision,
		}}, nil
	}
	if memory.active == nil && memory.pending == nil {
		return nil, nil
	}
	return []store.RuntimeState{{AccountID: "account-a", Active: memory.active, Pending: memory.pending}}, nil
}

func TestGatewayAuthorityRetirementIsExplicitReplayableAndRedacted(t *testing.T) {
	gatewayCatalog, err := catalog.NewService(time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := account.NewManager(context.Background(), nil, account.Options{
		Secrets: secret.StaticProvider{}, ExchangeCatalog: exchange.NewDefaultMemoryCatalog(), GatewayCatalog: gatewayCatalog,
		HTTPClient: &http.Client{}, RuntimeMode: account.RuntimeModeTest, RuntimeGroupBuilder: authorityCandidateBuilder{},
		CredentialResolver: authorityCredentialResolver{}, Lifecycle: newAuthorityLifecycle(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	snapshotStore := newSnapshotAuthorityStore()
	authority, err := newGatewayAuthority(context.Background(), snapshotStore, manager, account.RuntimeModeTest)
	if err != nil {
		t.Fatal(err)
	}
	authority.routeStatus = func(domain.AccountID, string, string) (account.Status, error) {
		return account.Status{Resource: account.Resource{ReadReady: true, WriteReady: true}}, nil
	}
	const applyOperationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445c33"
	const retireOperationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445c34"
	const opaqueRef = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	apply := httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityApplyDesired, Mode: "test", AccountID: "account-a", OperationID: applyOperationID,
		DesiredApply: &httpapi.DesiredApplyRequest{
			OperationID: applyOperationID, AccountID: "account-a", Venue: "binance", NativeEnvironment: "demo", SecretRef: opaqueRef,
			DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "active",
			Routes: []httpapi.DesiredRoute{{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.demo.v1", RequestedState: "active"}},
		},
	}
	if _, err := authority.Execute(context.Background(), apply); err != nil {
		t.Fatal(err)
	}
	retire := httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityRetireDesired, Mode: "test", AccountID: "account-a", OperationID: retireOperationID,
		Retirement: &httpapi.RetireRequest{
			OperationID: retireOperationID, AccountID: "account-a", DesiredStateRevision: 2, RuntimeGeneration: 1, CredentialVersion: 1,
		},
	}
	for attempt := 0; attempt < 2; attempt++ {
		response, err := authority.Execute(context.Background(), retire)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt+1, err)
		}
		view, ok := response.Body.(httpapi.RuntimeView)
		if !ok || !view.Tombstoned || view.Active != nil || view.Pending != nil || len(view.Retiring) != 0 {
			t.Fatalf("attempt %d view=%T %+v", attempt+1, response.Body, response.Body)
		}
		encoded, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{opaqueRef, "secretRef", "provider", "master-key"} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("retirement response leaked %q: %s", forbidden, encoded)
			}
		}
	}
	if snapshotStore.retirementCommits != 1 {
		t.Fatalf("retirement commits=%d want=1", snapshotStore.retirementCommits)
	}
	if !errors.Is(manager.CheckWriteFence(account.WriteFence{AccountID: "account-a", DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1}), account.ErrStaleWriteFence) {
		t.Fatal("retired account still passed write fence")
	}
	restartedManager := newAuthorityManager(t, account.RuntimeModeTest, newAuthorityLifecycle())
	restarted, err := newGatewayAuthority(context.Background(), snapshotStore, restartedManager, account.RuntimeModeTest)
	if err != nil {
		t.Fatal(err)
	}
	restartedResponse, err := restarted.Execute(context.Background(), httpapi.AuthorityRequest{Operation: httpapi.AuthorityGetDesired, AccountID: "account-a"})
	restartedView, ok := restartedResponse.Body.(httpapi.RuntimeView)
	if err != nil || !ok || !restartedView.Tombstoned || restartedView.Active != nil || restartedManager.ActiveSnapshot("account-a") != nil {
		t.Fatalf("restart view=%T %+v active=%+v err=%v", restartedResponse.Body, restartedResponse.Body, restartedManager.ActiveSnapshot("account-a"), err)
	}
	reused := apply
	reused.OperationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445c35"
	reused.DesiredApply = &httpapi.DesiredApplyRequest{
		OperationID: reused.OperationID, AccountID: "account-a", Venue: "binance", NativeEnvironment: "demo", SecretRef: opaqueRef,
		DesiredStateRevision: 3, RuntimeGeneration: 2, CredentialVersion: 1, RequestedState: "active",
		Routes: []httpapi.DesiredRoute{{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.demo.v1", RequestedState: "active"}},
	}
	_, err = authority.Execute(context.Background(), reused)
	var retiredErr httpapi.AuthorityError
	if !errors.As(err, &retiredErr) || retiredErr.Code != "RETIRED_SECRET_REFERENCE" {
		t.Fatalf("retired reference reuse err=%T %v", err, err)
	}

	conflict := retire
	conflict.Retirement = &httpapi.RetireRequest{
		OperationID: retireOperationID, AccountID: "account-a", DesiredStateRevision: 3, RuntimeGeneration: 1, CredentialVersion: 1,
	}
	_, err = authority.Execute(context.Background(), conflict)
	var authorityErr httpapi.AuthorityError
	if !errors.As(err, &authorityErr) || authorityErr.Code != "OPERATION_ID_CONFLICT" {
		t.Fatalf("conflicting replay err=%T %v", err, err)
	}
}

func TestProductionRuntimeLifecycleAllowsSnapshotRetirementWithoutInjectedCallbacks(t *testing.T) {
	gatewayCatalog, err := catalog.NewService(time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	lifecycle := &productionRuntimeLifecycle{}
	manager, err := account.NewManager(context.Background(), nil, account.Options{
		GatewayCatalog: gatewayCatalog, HTTPClient: &http.Client{}, RuntimeMode: account.RuntimeModeTest,
		RuntimeGroupBuilder: authorityCandidateBuilder{}, CredentialResolver: authorityCredentialResolver{},
		Lifecycle: lifecycle,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	if err := lifecycle.attach(blockerQuerierStub{}, manager); err != nil {
		t.Fatal(err)
	}
	store := newSnapshotAuthorityStore()
	authority, err := newGatewayAuthority(context.Background(), store, manager, account.RuntimeModeTest)
	if err != nil {
		t.Fatal(err)
	}
	const applyOperationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445d01"
	const retireOperationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445d02"
	if _, err := authority.Execute(context.Background(), httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityApplyDesired, Mode: "test", AccountID: "account-a", OperationID: applyOperationID,
		DesiredApply: &httpapi.DesiredApplyRequest{
			OperationID: applyOperationID, AccountID: "account-a", Venue: "binance", NativeEnvironment: "demo",
			SecretRef: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", DesiredStateRevision: 1,
			RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "active",
			Routes: []httpapi.DesiredRoute{{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.demo.v1", RequestedState: "active"}},
		},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	response, err := authority.Execute(context.Background(), httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityRetireDesired, Mode: "test", AccountID: "account-a", OperationID: retireOperationID,
		Retirement: &httpapi.RetireRequest{
			OperationID: retireOperationID, AccountID: "account-a", DesiredStateRevision: 2,
			RuntimeGeneration: 1, CredentialVersion: 1,
		},
	})
	view, ok := response.Body.(httpapi.RuntimeView)
	if err != nil || !ok || !view.Tombstoned || manager.ActiveSnapshot("account-a") != nil {
		t.Fatalf("retire response=%T %+v active=%+v err=%v", response.Body, response.Body, manager.ActiveSnapshot("account-a"), err)
	}
}

func TestProductionRuntimeLifecycleUsesDatabaseBlockersAndFailsClosed(t *testing.T) {
	lifecycle := &productionRuntimeLifecycle{}
	positions := &positionBlockerSourceStub{positions: 3}
	if err := lifecycle.attach(blockerQuerierStub{openOrders: 2, uncertainCommands: 1}, positions); err != nil {
		t.Fatal(err)
	}
	blockers, err := lifecycle.Blockers(context.Background(), "account-a")
	if err != nil || blockers.OpenOrders != 2 || blockers.Positions != 3 || blockers.UncertainCommands != 1 || !blockers.Any() {
		t.Fatalf("blockers=%+v err=%v", blockers, err)
	}
	if err := lifecycle.Deactivate(context.Background(), "account-a"); err != nil {
		t.Fatalf("snapshot deactivation: %v", err)
	}
	if err := lifecycle.attach(blockerQuerierStub{err: errors.New("postgres-secret-canary")}, positions); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Blockers(context.Background(), "account-a"); err == nil || strings.Contains(err.Error(), "postgres-secret-canary") {
		t.Fatalf("blocker query error=%v", err)
	}
	if err := lifecycle.attach(blockerQuerierStub{}, &positionBlockerSourceStub{err: errors.New("position-secret-canary")}); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Blockers(context.Background(), "account-a"); err == nil || strings.Contains(err.Error(), "position-secret-canary") {
		t.Fatalf("position blocker query error=%v", err)
	}
	if err := lifecycle.attach(blockerQuerierStub{}, nil); err == nil {
		t.Fatal("missing position blocker source was accepted")
	}
	if err := lifecycle.Activate(context.Background(), authorityDesiredRuntime(t, "opaque-provider-ref")); err == nil {
		t.Fatal("legacy activation unexpectedly enabled")
	}
}

type positionBlockerSourceStub struct {
	positions int
	err       error
}

func (source *positionBlockerSourceStub) RuntimePositionBlockers(context.Context, domain.AccountID) (int, error) {
	return source.positions, source.err
}

type blockerQuerierStub struct {
	openOrders        int
	uncertainCommands int
	err               error
}

func (querier blockerQuerierStub) QueryRow(context.Context, string, ...any) pgx.Row {
	return blockerRowStub{openOrders: querier.openOrders, uncertainCommands: querier.uncertainCommands, err: querier.err}
}

type blockerRowStub struct {
	openOrders        int
	uncertainCommands int
	err               error
}

func (row blockerRowStub) Scan(destinations ...any) error {
	if row.err != nil {
		return row.err
	}
	if len(destinations) != 2 {
		return errors.New("unexpected blocker scan")
	}
	*(destinations[0].(*int)) = row.openOrders
	*(destinations[1].(*int)) = row.uncertainCommands
	return nil
}

func TestGatewayAuthorityRotationFinalizationIsScopedReplayableAndRedacted(t *testing.T) {
	gatewayCatalog, err := catalog.NewService(time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := account.NewManager(context.Background(), nil, account.Options{
		Secrets: secret.StaticProvider{}, ExchangeCatalog: exchange.NewDefaultMemoryCatalog(), GatewayCatalog: gatewayCatalog,
		HTTPClient: &http.Client{}, RuntimeMode: account.RuntimeModeTest, RuntimeGroupBuilder: authorityCandidateBuilder{},
		CredentialResolver: authorityCredentialResolver{}, Lifecycle: newAuthorityLifecycle(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	snapshotStore := newSnapshotAuthorityStore()
	authority, err := newGatewayAuthority(context.Background(), snapshotStore, manager, account.RuntimeModeTest)
	if err != nil {
		t.Fatal(err)
	}
	apply := func(operationID string, revision, generation, credentialVersion int64, secretRef string) {
		t.Helper()
		_, err := authority.Execute(context.Background(), httpapi.AuthorityRequest{
			Operation: httpapi.AuthorityApplyDesired, Mode: "test", AccountID: "account-a", OperationID: operationID,
			DesiredApply: &httpapi.DesiredApplyRequest{
				OperationID: operationID, AccountID: "account-a", Venue: "binance", NativeEnvironment: "demo", SecretRef: secretRef,
				DesiredStateRevision: revision, RuntimeGeneration: generation, CredentialVersion: credentialVersion, RequestedState: "active",
				Routes: []httpapi.DesiredRoute{{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.demo.v1", RequestedState: "active"}},
			},
		})
		if err != nil {
			t.Fatalf("apply generation %d: %v", generation, err)
		}
	}
	const retiringOperationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445c70"
	const activeOperationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445c71"
	apply(retiringOperationID, 1, 1, 1, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	apply(activeOperationID, 2, 2, 2, "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE")

	request := httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityFinalizeRotation, Mode: "test", AccountID: "account-a", OperationID: activeOperationID,
		RotationFinalization: &httpapi.RotationFinalizationRequest{
			OperationID: activeOperationID, AccountID: "account-a",
			ExpectedActiveFence:   httpapi.AccountFence{AccountID: "account-a", DesiredStateRevision: 2, RuntimeGeneration: 2, CredentialVersion: 2},
			ExpectedRetiringFence: httpapi.AccountFence{AccountID: "account-a", DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1},
		},
	}
	var first httpapi.RotationFinalizationProof
	for attempt := 0; attempt < 2; attempt++ {
		response, err := authority.Execute(context.Background(), request)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt+1, err)
		}
		proof, ok := response.Body.(httpapi.RotationFinalizationProof)
		if !ok || response.Status != http.StatusOK || proof.OperationID != activeOperationID || proof.AccountID != "account-a" ||
			proof.ActiveFence != request.RotationFinalization.ExpectedActiveFence || proof.RetiringFence != request.RotationFinalization.ExpectedRetiringFence ||
			proof.TombstoneRevision != 2 || proof.RetiringState != "retired" || proof.SecretReferenceState != "tombstoned" {
			t.Fatalf("attempt %d proof=%T %+v status=%d", attempt+1, response.Body, response.Body, response.Status)
		}
		if proof.CompletedAt.IsZero() || proof.CompletedAt.Location() != time.UTC {
			t.Fatalf("completedAt=%v", proof.CompletedAt)
		}
		encoded, err := json.Marshal(proof)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{`"secretRef":`, "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "AQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQEBAQE", `"namespace":`, `"provider":`, `"keyVersion":`} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("proof leaked %q: %s", forbidden, encoded)
			}
		}
		if attempt == 0 {
			first = proof
		} else if proof != first {
			t.Fatalf("replay proof=%+v first=%+v", proof, first)
		}
	}
	if snapshotStore.rotationCommits != 1 || snapshotStore.rotationRequest == nil || snapshotStore.rotationRequest.Scope != authoritySecretScope(account.RuntimeModeTest) {
		t.Fatalf("commits=%d request=%+v", snapshotStore.rotationCommits, snapshotStore.rotationRequest)
	}

	changed := request
	changed.RotationFinalization = &httpapi.RotationFinalizationRequest{
		OperationID: activeOperationID, AccountID: "account-a",
		ExpectedActiveFence:   httpapi.AccountFence{AccountID: "account-a", DesiredStateRevision: 3, RuntimeGeneration: 3, CredentialVersion: 3},
		ExpectedRetiringFence: httpapi.AccountFence{AccountID: "account-a", DesiredStateRevision: 2, RuntimeGeneration: 2, CredentialVersion: 2},
	}
	_, err = authority.Execute(context.Background(), changed)
	var conflict httpapi.AuthorityError
	if !errors.As(err, &conflict) || conflict.Code != "OPERATION_ID_CONFLICT" {
		t.Fatalf("changed request err=%T %v", err, err)
	}
}

func (memory *snapshotAuthorityStore) TombstoneSecretReference(_ context.Context, request store.TombstoneRequest) (store.Tombstone, error) {
	return store.Tombstone{TombstoneRequest: request, CreatedAt: time.Now()}, nil
}

func (memory *snapshotAuthorityStore) IsSecretReferenceRetired(context.Context, secretstore.Scope, secretstore.OpaqueRef) (bool, error) {
	return memory.retirement != nil, nil
}

func TestAuthoritySnapshotApplyPromotesOnlyAfterDurableCAS(t *testing.T) {
	gatewayCatalog, err := catalog.NewService(time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := account.NewManager(context.Background(), nil, account.Options{
		Secrets: secret.StaticProvider{}, ExchangeCatalog: exchange.NewDefaultMemoryCatalog(), GatewayCatalog: gatewayCatalog,
		HTTPClient: &http.Client{}, RuntimeMode: account.RuntimeModeTest, RuntimeGroupBuilder: authorityCandidateBuilder{},
		CredentialResolver: authorityCredentialResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	snapshotStore := newSnapshotAuthorityStore()
	authority, err := newGatewayAuthority(context.Background(), snapshotStore, manager, account.RuntimeModeTest)
	if err != nil {
		t.Fatal(err)
	}
	operationID, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c33")
	secretRef, _ := secretstore.ParseOpaqueRef("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	desired := store.DesiredSnapshot{
		SnapshotID: uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c35"), OperationID: operationID,
		AccountID: "account-a", Venue: "binance", NativeEnvironment: "demo", SecretRef: secretRef,
		Fence:          domain.AccountFence{AccountID: "account-a", DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1},
		RequestedState: store.DesiredActive,
		Routes:         []store.RouteDefinition{{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.demo.v1", RequestedState: store.RouteActive}},
		CreatedAt:      time.Now(),
	}
	desired.RequestDigest, err = desired.CanonicalDigest()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.ApplySnapshot(context.Background(), desired); err != nil {
		t.Fatal(err)
	}
	active := manager.ActiveSnapshot("account-a")
	if snapshotStore.promotions != 1 || snapshotStore.pending != nil || active == nil || active.Fence != desired.Fence {
		t.Fatalf("promotions=%d pending=%+v active=%+v", snapshotStore.promotions, snapshotStore.pending, active)
	}
}

func TestGatewayAuthorityApplyDesiredReplaysExactActiveAndRedactsReference(t *testing.T) {
	gatewayCatalog, err := catalog.NewService(time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := account.NewManager(context.Background(), nil, account.Options{
		Secrets: secret.StaticProvider{}, ExchangeCatalog: exchange.NewDefaultMemoryCatalog(), GatewayCatalog: gatewayCatalog,
		HTTPClient: &http.Client{}, RuntimeMode: account.RuntimeModeTest, RuntimeGroupBuilder: authorityCandidateBuilder{},
		CredentialResolver: authorityCredentialResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	snapshotStore := newSnapshotAuthorityStore()
	authority, err := newGatewayAuthority(context.Background(), snapshotStore, manager, account.RuntimeModeTest)
	if err != nil {
		t.Fatal(err)
	}
	authority.routeStatus = func(domain.AccountID, string, string) (account.Status, error) {
		return account.Status{Resource: account.Resource{ReadReady: true, WriteReady: true}}, nil
	}
	const operationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445c33"
	const opaqueRef = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	request := httpapi.AuthorityRequest{
		Operation:   httpapi.AuthorityApplyDesired,
		Mode:        "test",
		AccountID:   "account-a",
		OperationID: operationID,
		DesiredApply: &httpapi.DesiredApplyRequest{
			OperationID: operationID, AccountID: "account-a", Venue: "binance", NativeEnvironment: "demo", SecretRef: opaqueRef,
			DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "active",
			Routes: []httpapi.DesiredRoute{{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.demo.v1", RequestedState: "active"}},
		},
	}
	for attempt := 0; attempt < 2; attempt++ {
		response, err := authority.Execute(context.Background(), request)
		if err != nil {
			t.Fatalf("attempt %d: %v", attempt+1, err)
		}
		if response.Status != http.StatusOK {
			t.Fatalf("attempt %d status=%d", attempt+1, response.Status)
		}
		view, ok := response.Body.(httpapi.RuntimeView)
		if !ok || view.AccountID != "account-a" || view.Active == nil || view.Active.OperationID != operationID || view.Active.RuntimeGeneration != 1 || len(view.Active.Routes) != 1 || !view.Active.Routes[0].Ready {
			t.Fatalf("attempt %d view=%T %+v", attempt+1, response.Body, response.Body)
		}
		encoded, err := json.Marshal(view)
		if err != nil {
			t.Fatal(err)
		}
		for _, forbidden := range []string{"secretRef", opaqueRef, "provider", "master-key"} {
			if strings.Contains(string(encoded), forbidden) {
				t.Fatalf("response leaked %q: %s", forbidden, encoded)
			}
		}
	}
	if snapshotStore.promotions != 1 {
		t.Fatalf("promotions=%d want=1", snapshotStore.promotions)
	}
}

func TestGatewayAuthorityRuntimeReadUsesDynamicSnapshotState(t *testing.T) {
	gatewayCatalog, err := catalog.NewService(time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := account.NewManager(context.Background(), nil, account.Options{
		Secrets: secret.StaticProvider{}, ExchangeCatalog: exchange.NewDefaultMemoryCatalog(), GatewayCatalog: gatewayCatalog,
		HTTPClient: &http.Client{}, RuntimeMode: account.RuntimeModeTest, RuntimeGroupBuilder: authorityCandidateBuilder{},
		CredentialResolver: authorityCredentialResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	snapshotStore := newSnapshotAuthorityStore()
	authority, err := newGatewayAuthority(context.Background(), snapshotStore, manager, account.RuntimeModeTest)
	if err != nil {
		t.Fatal(err)
	}
	const operationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445c33"
	const opaqueRef = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	_, err = authority.Execute(context.Background(), httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityApplyDesired, Mode: "test", AccountID: "account-a", OperationID: operationID,
		DesiredApply: &httpapi.DesiredApplyRequest{
			OperationID: operationID, AccountID: "account-a", Venue: "binance", NativeEnvironment: "demo", SecretRef: opaqueRef,
			DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "active",
			Routes: []httpapi.DesiredRoute{{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.demo.v1", RequestedState: "active"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	authority.routeStatus = func(domain.AccountID, string, string) (account.Status, error) {
		return account.Status{Resource: account.Resource{ReadReady: true, WriteReady: true}}, nil
	}
	handler, err := httpapi.NewInternalHandler(httpapi.InternalOptions{
		Authority: authority, Authenticate: func(*http.Request) error { return nil },
	})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/internal/v1/accounts/runtime/account-a", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	var envelope struct {
		Data struct {
			httpapi.RuntimeView
			DesiredStateRevision            int64                        `json:"desiredStateRevision"`
			RuntimeGeneration               int64                        `json:"runtimeGeneration"`
			CredentialVersion               int64                        `json:"credentialVersion"`
			RuntimeVersion                  int64                        `json:"runtimeVersion"`
			Readiness                       authorityProjectionReadiness `json:"readiness"`
			WriteReadiness                  authorityProjectionReadiness `json:"writeReadiness"`
			LastAppliedDesiredStateRevision int64                        `json:"lastAppliedDesiredStateRevision"`
			LastAppliedRuntimeGeneration    int64                        `json:"lastAppliedRuntimeGeneration"`
			Capabilities                    []authorityRuntimeCapability `json:"capabilities"`
			ObservedAt                      time.Time                    `json:"observedAt"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &envelope); err != nil {
		t.Fatal(err)
	}
	got := envelope.Data
	if got.AccountID != "account-a" || got.Active == nil || got.Active.OperationID != operationID || len(got.Active.Routes) != 1 || !got.Active.Routes[0].Ready ||
		got.DesiredStateRevision != 1 || got.RuntimeGeneration != 1 || got.CredentialVersion != 1 || got.RuntimeVersion != 1 ||
		got.LastAppliedDesiredStateRevision != 1 || got.LastAppliedRuntimeGeneration != 1 || !got.Readiness.Ready || !got.WriteReadiness.Ready ||
		len(got.Capabilities) != 1 || got.Capabilities[0].RowID != "binance:spot" || !got.Capabilities[0].WriteReady || got.ObservedAt.IsZero() {
		t.Fatalf("runtime=%+v", got)
	}
	for _, forbidden := range []string{"secretRef", opaqueRef, "provider", "master-key"} {
		if strings.Contains(response.Body.String(), forbidden) {
			t.Fatalf("response leaked %q: %s", forbidden, response.Body.String())
		}
	}

	authority.routeStatus = func(domain.AccountID, string, string) (account.Status, error) {
		return account.Status{Resource: account.Resource{ReadReady: true, WriteReady: false, Reason: account.ReasonPrivateStreamGap}}, nil
	}
	gap, err := authority.Execute(context.Background(), httpapi.AuthorityRequest{Operation: httpapi.AuthorityGetRuntimeAccount, AccountID: "account-a"})
	if err != nil {
		t.Fatal(err)
	}
	gapView := gap.Body.(httpapi.RuntimeView)
	if !gapView.Readiness.Ready || gapView.WriteReadiness.Ready || gapView.WriteReadiness.Reason != account.ReasonPrivateStreamGap || gapView.BlockedReason == nil || *gapView.BlockedReason != account.ReasonPrivateStreamGap ||
		gapView.Active == nil || gapView.Active.Routes[0].Ready || gapView.Active.Routes[0].Reason != account.ReasonPrivateStreamGap {
		t.Fatalf("gap view=%+v", gapView)
	}
	authority.routeStatus = func(domain.AccountID, string, string) (account.Status, error) {
		return account.Status{Resource: account.Resource{ReadReady: true, WriteReady: true}}, nil
	}
	recovered, err := authority.Execute(context.Background(), httpapi.AuthorityRequest{Operation: httpapi.AuthorityGetRuntimeAccount, AccountID: "account-a"})
	if err != nil {
		t.Fatal(err)
	}
	recoveredView := recovered.Body.(httpapi.RuntimeView)
	if !recoveredView.Readiness.Ready || !recoveredView.WriteReadiness.Ready || recoveredView.BlockedReason != nil ||
		recoveredView.Active == nil || !recoveredView.Active.Routes[0].Ready || recoveredView.Active.Routes[0].Reason != "" {
		t.Fatalf("recovered view=%+v", recoveredView)
	}
	authority.routeStatus = func(domain.AccountID, string, string) (account.Status, error) {
		return account.Status{Resource: account.Resource{Reason: catalog.ReasonCatalogStale}}, nil
	}
	stale, err := authority.Execute(context.Background(), httpapi.AuthorityRequest{Operation: httpapi.AuthorityGetRuntimeAccount, AccountID: "account-a"})
	if err != nil {
		t.Fatal(err)
	}
	staleView := stale.Body.(httpapi.RuntimeView)
	if staleView.Readiness.Ready || staleView.WriteReadiness.Ready || staleView.Readiness.Reason != catalog.ReasonCatalogStale || staleView.BlockedReason == nil || *staleView.BlockedReason != catalog.ReasonCatalogStale ||
		staleView.Active == nil || staleView.Active.Routes[0].Ready || staleView.Active.Routes[0].Reason != catalog.ReasonCatalogStale {
		t.Fatalf("stale view=%+v", staleView)
	}
	authority.routeStatus = func(domain.AccountID, string, string) (account.Status, error) {
		return account.Status{Resource: account.Resource{ReadReady: true, Reason: account.ReasonExactLookupUnavailable}}, nil
	}
	exactLookupUnavailable, err := authority.Execute(context.Background(), httpapi.AuthorityRequest{Operation: httpapi.AuthorityGetRuntimeAccount, AccountID: "account-a"})
	if err != nil {
		t.Fatal(err)
	}
	exactLookupView := exactLookupUnavailable.Body.(httpapi.RuntimeView)
	if !exactLookupView.Readiness.Ready || exactLookupView.WriteReadiness.Ready || exactLookupView.WriteReadiness.Reason != account.ReasonExactLookupUnavailable || exactLookupView.BlockedReason == nil || *exactLookupView.BlockedReason != account.ReasonExactLookupUnavailable ||
		exactLookupView.Active == nil || exactLookupView.Active.Routes[0].Ready || exactLookupView.Active.Routes[0].Reason != account.ReasonExactLookupUnavailable {
		t.Fatalf("exact lookup view=%+v", exactLookupView)
	}
	authority.routeStatus = func(_ domain.AccountID, product, settlement string) (account.Status, error) {
		if product != "spot" || settlement != "" {
			t.Fatalf("RouteStatus product=%q settlement=%q", product, settlement)
		}
		return account.Status{Resource: account.Resource{ReadReady: true, Reason: account.ErrLiveWriteDisabled.Error()}}, nil
	}
	authority.mode = account.RuntimeModeLive
	liveDenied, err := authority.Execute(context.Background(), httpapi.AuthorityRequest{Operation: httpapi.AuthorityGetRuntimeAccount, AccountID: "account-a"})
	if err != nil {
		t.Fatal(err)
	}
	liveDeniedView := liveDenied.Body.(httpapi.RuntimeView)
	if !liveDeniedView.Readiness.Ready || liveDeniedView.WriteReadiness.Ready || liveDeniedView.WriteReadiness.Reason != account.ErrLiveWriteDisabled.Error() || liveDeniedView.Capabilities[0].WriteReady ||
		liveDeniedView.Active == nil || !liveDeniedView.Active.Routes[0].Ready || liveDeniedView.Active.Routes[0].Reason != "" {
		t.Fatalf("live denied view=%+v", liveDeniedView)
	}
}

func TestGatewayAuthorityRuntimeWriteReadinessHonorsCapabilityPolicy(t *testing.T) {
	gatewayCatalog, err := catalog.NewService(time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := account.NewManager(context.Background(), nil, account.Options{
		Secrets: secret.StaticProvider{}, ExchangeCatalog: exchange.NewDefaultMemoryCatalog(), GatewayCatalog: gatewayCatalog,
		HTTPClient: &http.Client{}, RuntimeMode: account.RuntimeModeTest, RuntimeGroupBuilder: authorityCandidateBuilder{},
		CredentialResolver: authorityStaticCredentialResolver{bundle: []byte(`{"schemaVersion":1,"venue":"lighter","authKind":"lighter_signer","metadata":{"accountIndex":0,"keyIndex":0},"secrets":{"privateKey":"key"}}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	authority, err := newGatewayAuthority(context.Background(), newSnapshotAuthorityStore(), manager, account.RuntimeModeTest)
	if err != nil {
		t.Fatal(err)
	}
	authority.routeStatus = func(domain.AccountID, string, string) (account.Status, error) {
		return account.Status{Resource: account.Resource{ReadReady: true, WriteReady: true}}, nil
	}
	const operationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445c33"
	_, err = authority.Execute(context.Background(), httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityApplyDesired, Mode: "test", AccountID: "account-a", OperationID: operationID,
		DesiredApply: &httpapi.DesiredApplyRequest{
			OperationID: operationID, AccountID: "account-a", Venue: "lighter", NativeEnvironment: "testnet",
			SecretRef: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "active",
			Routes: []httpapi.DesiredRoute{{RouteID: "spot", Product: "spot", RouteProfileID: "lighter.spot.testnet.v1", RequestedState: "active"}},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	response, err := authority.Execute(context.Background(), httpapi.AuthorityRequest{Operation: httpapi.AuthorityGetRuntimeAccount, AccountID: "account-a"})
	if err != nil {
		t.Fatal(err)
	}
	view, ok := response.Body.(httpapi.RuntimeView)
	if !ok || !view.Readiness.Ready || view.WriteReadiness.Ready || len(view.Capabilities) != 1 || view.Capabilities[0].WriteReady || view.BlockedReason == nil {
		t.Fatalf("runtime=%T %+v", response.Body, response.Body)
	}
}

func TestGatewayAuthorityCandidateFailurePreservesActiveAndPublishesRedactedPending(t *testing.T) {
	gatewayCatalog, err := catalog.NewService(time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := account.NewManager(context.Background(), nil, account.Options{
		Secrets: secret.StaticProvider{}, ExchangeCatalog: exchange.NewDefaultMemoryCatalog(), GatewayCatalog: gatewayCatalog,
		HTTPClient: &http.Client{}, RuntimeMode: account.RuntimeModeTest,
		RuntimeGroupBuilder: authorityFailingCandidateBuilder{failGeneration: 2}, CredentialResolver: authorityCredentialResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	snapshotStore := newSnapshotAuthorityStore()
	authority, err := newGatewayAuthority(context.Background(), snapshotStore, manager, account.RuntimeModeTest)
	if err != nil {
		t.Fatal(err)
	}
	request := func(operationID string, revision, generation int64) httpapi.AuthorityRequest {
		return httpapi.AuthorityRequest{
			Operation: httpapi.AuthorityApplyDesired, Mode: "test", AccountID: "account-a", OperationID: operationID,
			DesiredApply: &httpapi.DesiredApplyRequest{
				OperationID: operationID, AccountID: "account-a", Venue: "binance", NativeEnvironment: "demo",
				SecretRef: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", DesiredStateRevision: revision,
				RuntimeGeneration: generation, CredentialVersion: 1, RequestedState: "active",
				Routes: []httpapi.DesiredRoute{{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.demo.v1", RequestedState: "active"}},
			},
		}
	}
	if _, err := authority.Execute(context.Background(), request("018f47a8-2578-7d3c-9d7d-5e4f4a445c33", 1, 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Execute(context.Background(), request("018f47a8-2578-7d3c-9d7d-5e4f4a445c34", 2, 2)); err == nil || strings.Contains(err.Error(), "secret-material-canary") {
		t.Fatalf("candidate failure=%v", err)
	}
	response, err := authority.Execute(context.Background(), httpapi.AuthorityRequest{Operation: httpapi.AuthorityGetDesired, AccountID: "account-a"})
	if err != nil {
		t.Fatal(err)
	}
	view, ok := response.Body.(httpapi.RuntimeView)
	if !ok || view.Active == nil || view.Active.RuntimeGeneration != 1 || view.Pending == nil || view.Pending.RuntimeGeneration != 2 || view.Pending.State != string(store.OperationCandidateFailed) || len(view.Pending.Routes) != 1 || view.Pending.Routes[0].Ready || view.Pending.Routes[0].Reason != "CANDIDATE_BUILD_FAILED" {
		t.Fatalf("view=%T %+v", response.Body, response.Body)
	}
	encoded, err := json.Marshal(view)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"secretRef", "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", "secret-material-canary"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("pending view leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestGatewayAuthorityExactReplayRehydratesTransientActiveRestoreFailure(t *testing.T) {
	newManager := func(t *testing.T, resolver account.ScopedCredentialResolver) *account.Manager {
		t.Helper()
		gatewayCatalog, err := catalog.NewService(time.Minute, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		manager, err := account.NewManager(context.Background(), nil, account.Options{
			Secrets: secret.StaticProvider{}, ExchangeCatalog: exchange.NewDefaultMemoryCatalog(), GatewayCatalog: gatewayCatalog,
			HTTPClient: &http.Client{}, RuntimeMode: account.RuntimeModeTest, RuntimeGroupBuilder: authorityCandidateBuilder{},
			CredentialResolver: resolver,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = manager.Close() })
		return manager
	}
	request := httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityApplyDesired, Mode: "test", AccountID: "account-a",
		OperationID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c33",
		DesiredApply: &httpapi.DesiredApplyRequest{
			OperationID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c33", AccountID: "account-a", Venue: "binance", NativeEnvironment: "demo",
			SecretRef: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", DesiredStateRevision: 1,
			RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "active",
			Routes: []httpapi.DesiredRoute{{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.demo.v1", RequestedState: "active"}},
		},
	}
	snapshotStore := newSnapshotAuthorityStore()
	firstAuthority, err := newGatewayAuthority(context.Background(), snapshotStore, newManager(t, authorityCredentialResolver{}), account.RuntimeModeTest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := firstAuthority.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	if snapshotStore.promotions != 1 {
		t.Fatalf("initial promotions=%d", snapshotStore.promotions)
	}

	restartedManager := newManager(t, &authorityFailOnceCredentialResolver{failures: 1})
	restartedAuthority, err := newGatewayAuthority(context.Background(), snapshotStore, restartedManager, account.RuntimeModeTest)
	if err != nil {
		t.Fatal(err)
	}
	if active := restartedManager.ActiveSnapshot("account-a"); active != nil {
		t.Fatalf("startup failure unexpectedly restored active=%+v", active)
	}
	if _, err := restartedAuthority.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	active := restartedManager.ActiveSnapshot("account-a")
	if active == nil || active.Fence.RuntimeGeneration != 1 || len(active.Routes) != 1 {
		t.Fatalf("exact replay did not restore active=%+v", active)
	}
	if snapshotStore.promotions != 1 {
		t.Fatalf("exact replay promotions=%d, want 1", snapshotStore.promotions)
	}
}

type memoryDesiredStore struct {
	items map[domain.AccountID]store.DesiredAccount
}

func newMemoryDesiredStore() *memoryDesiredStore {
	return &memoryDesiredStore{items: make(map[domain.AccountID]store.DesiredAccount)}
}

func (memory *memoryDesiredStore) CreateDesiredAccount(_ context.Context, desired store.DesiredAccount) (store.DesiredAccount, error) {
	if current, ok := memory.items[desired.AccountID]; ok {
		if current.SecretRef == desired.SecretRef && current.DesiredStateRevision == desired.DesiredStateRevision {
			return current, nil
		}
		return store.DesiredAccount{}, store.ErrDesiredStateRevisionConflict
	}
	memory.items[desired.AccountID] = desired
	return desired, nil
}

func (memory *memoryDesiredStore) ApplyDesiredAccount(_ context.Context, expected int64, desired store.DesiredAccount) (store.DesiredAccount, error) {
	current, ok := memory.items[desired.AccountID]
	if !ok {
		return store.DesiredAccount{}, store.ErrNotFound
	}
	if current.DesiredStateRevision != expected || desired.DesiredStateRevision != expected+1 {
		return store.DesiredAccount{}, store.ErrDesiredStateRevisionConflict
	}
	if err := store.ValidateDesiredAccountTransition(current.RequestedState, desired.RequestedState); err != nil {
		return store.DesiredAccount{}, err
	}
	if current.RequestedState == store.DesiredRotating && desired.RequestedState == store.DesiredActive {
		desired.PreviousSecretRef = ""
	} else if desired.RequestedState == store.DesiredRotating {
		desired.PreviousSecretRef = current.SecretRef
	}
	memory.items[desired.AccountID] = desired
	return desired, nil
}

func (memory *memoryDesiredStore) LoadDesiredAccount(_ context.Context, accountID domain.AccountID) (store.DesiredAccount, error) {
	desired, ok := memory.items[accountID]
	if !ok {
		return store.DesiredAccount{}, store.ErrNotFound
	}
	return desired, nil
}

func (memory *memoryDesiredStore) ListDesiredAccounts(context.Context) ([]store.DesiredAccount, error) {
	result := make([]store.DesiredAccount, 0, len(memory.items))
	for _, desired := range memory.items {
		result = append(result, desired)
	}
	return result, nil
}

func (memory *memoryDesiredStore) ListRecoverableDesiredAccounts(ctx context.Context) ([]store.DesiredAccount, error) {
	return memory.ListDesiredAccounts(ctx)
}

func (memory *memoryDesiredStore) DeleteDesiredAccount(_ context.Context, accountID domain.AccountID, expected int64) error {
	current, ok := memory.items[accountID]
	if !ok {
		return store.ErrNotFound
	}
	if current.DesiredStateRevision != expected {
		return store.ErrDesiredStateRevisionConflict
	}
	delete(memory.items, accountID)
	return nil
}

func (memory *memoryDesiredStore) ValidateRuntimeFence(_ context.Context, fence store.RuntimeFence) error {
	current, ok := memory.items[fence.AccountID]
	if !ok || current.DesiredStateRevision != fence.DesiredStateRevision || current.RuntimeGeneration != fence.RuntimeGeneration || current.CredentialVersion != fence.CredentialVersion {
		return store.ErrStaleRuntimeGeneration
	}
	return nil
}
