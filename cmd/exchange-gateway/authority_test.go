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

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/capability"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/httpapi"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/QuantProcessing/boltertrader/gateway/store"
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
		perp:       perp, capability: capability.Row{Operations: capability.Operations{Funding: true}},
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
		perp:       perp, capability: capability.Row{Operations: capability.Operations{Funding: true}},
	}
	fact, err := (managerAuthorityReads{manager: accounts}).MarketData(context.Background(), "binance", "demo", "perp", "BTC-USDT")
	if err != nil || fact.Environment != "demo" || fact.Product != "perp" || accounts.resolvedAccount != "demo-perp" {
		t.Fatalf("fact=%+v resolved=%q err=%v", fact, accounts.resolvedAccount, err)
	}
}

type fakeAuthorityAccounts struct {
	resources       []account.Resource
	instrumentErr   error
	snapshot        catalog.Snapshot
	instrument      exchange.Instrument
	perp            exchange.PerpClient
	capability      capability.Row
	resolvedAccount domain.AccountID
	snapshots       map[domain.AccountID]catalog.Snapshot
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
		{path: "/internal/v1/market-data/readiness", assert: func(t *testing.T, data json.RawMessage) {
			var readiness struct {
				Ready bool `json:"ready"`
			}
			if err := json.Unmarshal(data, &readiness); err != nil || !readiness.Ready {
				t.Fatalf("market-data readiness=%+v err=%v", readiness, err)
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

func TestGatewayAuthorityMarketDataReadinessRequiresReadyInstrument(t *testing.T) {
	ctx := context.Background()
	for _, test := range []struct {
		name        string
		instruments []authorityInstrument
		wantReady   bool
	}{
		{name: "no configured instruments"},
		{name: "only unavailable instruments", instruments: []authorityInstrument{{InstrumentID: "BTC-USDT", Venue: "binance"}}},
		{name: "ready instrument", instruments: []authorityInstrument{{InstrumentID: "BTC-USDT", Venue: "binance", Ready: true}}, wantReady: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			authority, err := newGatewayAuthority(ctx, newMemoryDesiredStore(), newAuthorityManager(t, account.RuntimeModeTest, newAuthorityLifecycle()), account.RuntimeModeTest)
			if err != nil {
				t.Fatal(err)
			}
			authority.reads = staticAuthorityReads{instruments: test.instruments}
			response, err := authority.Execute(ctx, httpapi.AuthorityRequest{Operation: httpapi.AuthorityMarketDataReadiness})
			if err != nil {
				t.Fatal(err)
			}
			readiness, ok := response.Body.(map[string]bool)
			if !ok || readiness["ready"] != test.wantReady {
				t.Fatalf("readiness=%T %+v, want ready=%t", response.Body, response.Body, test.wantReady)
			}
		})
	}
}

func TestGatewayAuthorityMarketDataReadinessFailsClosedOnCatalogFailure(t *testing.T) {
	ctx := context.Background()
	authority, err := newGatewayAuthority(ctx, newMemoryDesiredStore(), newAuthorityManager(t, account.RuntimeModeTest, newAuthorityLifecycle()), account.RuntimeModeTest)
	if err != nil {
		t.Fatal(err)
	}
	authority.reads = staticAuthorityReads{err: errors.New("configured catalog unavailable")}
	_, err = authority.Execute(ctx, httpapi.AuthorityRequest{Operation: httpapi.AuthorityMarketDataReadiness})
	var authorityErr httpapi.AuthorityError
	if !errors.As(err, &authorityErr) || authorityErr.Status != http.StatusInternalServerError || !authorityErr.Retryable {
		t.Fatalf("error=%T %v", err, err)
	}
}

func TestGatewayAuthoritySeparatesRuntimeStatusAndReadinessDTOs(t *testing.T) {
	ctx := context.Background()
	authority, err := newGatewayAuthority(ctx, newMemoryDesiredStore(), newAuthorityManager(t, account.RuntimeModeTest, newAuthorityLifecycle()), account.RuntimeModeTest)
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
	if !ok || readiness.Ready {
		t.Fatalf("empty readiness=%T %+v", readinessResponse.Body, readinessResponse.Body)
	}

	seedActiveAuthorityAccount(t, ctx, authority)
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

type staticAuthorityReads struct {
	venues      []authorityVenue
	instruments []authorityInstrument
	marketData  authorityMarketData
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

func TestGatewayAuthorityCreateActivateAndReadRedactsOpaqueReference(t *testing.T) {
	ctx := context.Background()
	lifecycle := newAuthorityLifecycle()
	manager := newAuthorityManager(t, account.RuntimeModeTest, lifecycle)
	desiredStore := newMemoryDesiredStore()
	authority, err := newGatewayAuthority(ctx, desiredStore, manager, account.RuntimeModeTest)
	if err != nil {
		t.Fatalf("newGatewayAuthority: %v", err)
	}

	created, err := authority.Execute(ctx, httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityCreateDesired,
		Desired: &httpapi.DesiredAccount{
			AccountID: "dynamic", Venue: "binance", VenueEnvironment: "demo",
			SecretRef: "opaque-secret-canary", DesiredStateRevision: 1,
			RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "creating",
		},
	})
	if err != nil {
		t.Fatalf("create: %v", err)
	}
	assertRedactedDesiredResponse(t, created, 1)
	if _, err := manager.Runtime("dynamic"); !errors.Is(err, account.ErrUnknownAccount) {
		t.Fatalf("creating runtime exists: %v", err)
	}

	activated, err := authority.Execute(ctx, httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityUpdateDesired, AccountID: "dynamic", ExpectedRevision: 1,
		Mutation: &httpapi.DesiredMutation{
			SecretRef: "opaque-secret-canary", DesiredStateRevision: 2,
			RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "active",
		},
	})
	if err != nil {
		t.Fatalf("activate: %v", err)
	}
	assertRedactedDesiredResponse(t, activated, 2)
	if runtime, err := manager.Runtime("dynamic"); err != nil || !runtime.Ready {
		t.Fatalf("runtime=%+v err=%v", runtime, err)
	}
	if lifecycle.activations != 1 || lifecycle.lastSecretRef != "opaque-secret-canary" {
		t.Fatalf("lifecycle=%+v", lifecycle)
	}

	read, err := authority.Execute(ctx, httpapi.AuthorityRequest{Operation: httpapi.AuthorityGetDesired, AccountID: "dynamic"})
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	assertRedactedDesiredResponse(t, read, 2)
}

func TestGatewayAuthorityRotationAndDrainPreserveFenceSemantics(t *testing.T) {
	ctx := context.Background()
	lifecycle := newAuthorityLifecycle()
	manager := newAuthorityManager(t, account.RuntimeModeTest, lifecycle)
	desiredStore := newMemoryDesiredStore()
	authority, err := newGatewayAuthority(ctx, desiredStore, manager, account.RuntimeModeTest)
	if err != nil {
		t.Fatal(err)
	}
	seedActiveAuthorityAccount(t, ctx, authority)

	_, err = authority.Execute(ctx, httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityRotateDesired, AccountID: "dynamic", ExpectedRevision: 2,
		Mutation: &httpapi.DesiredMutation{
			SecretRef: "opaque-replacement", DesiredStateRevision: 3,
			RuntimeGeneration: 1, CredentialVersion: 1,
			RotationFence: &httpapi.RotationFence{
				RotationFenceID: "rotate-1", State: "armed",
				FromRuntimeGeneration: 1, ToRuntimeGeneration: 2,
				FromCredentialVersion: 1, ToCredentialVersion: 2,
			},
		},
	})
	if err != nil {
		t.Fatalf("arm rotation: %v", err)
	}
	if lifecycle.validations < 2 || lifecycle.lastSecretRef != "opaque-replacement" {
		t.Fatalf("replacement was not validated before persistence: %+v", lifecycle)
	}

	_, err = authority.Execute(ctx, httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityUpdateDesired, AccountID: "dynamic", ExpectedRevision: 3,
		Mutation: &httpapi.DesiredMutation{
			SecretRef: "opaque-replacement", DesiredStateRevision: 4,
			RuntimeGeneration: 2, CredentialVersion: 2, RequestedState: "active",
			RotationFence: &httpapi.RotationFence{
				RotationFenceID: "rotate-1", State: "released",
				FromRuntimeGeneration: 1, ToRuntimeGeneration: 2,
				FromCredentialVersion: 1, ToCredentialVersion: 2,
			},
		},
	})
	if err != nil {
		t.Fatalf("promote rotation: %v", err)
	}
	runtime, err := manager.Runtime("dynamic")
	if err != nil || runtime.Desired.DesiredStateRevision != 4 || runtime.Desired.RuntimeGeneration != 2 {
		t.Fatalf("promoted runtime=%+v err=%v", runtime, err)
	}

	_, err = authority.Execute(ctx, httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityDrainDesired, AccountID: "dynamic", ExpectedRevision: 4,
		Mutation: &httpapi.DesiredMutation{
			DesiredStateRevision: 5, RuntimeGeneration: 2, CredentialVersion: 2, RequestedState: "draining",
		},
	})
	if err != nil {
		t.Fatalf("drain: %v", err)
	}
	drained, err := manager.Runtime("dynamic")
	if err != nil || drained.Desired.RuntimeGeneration != 2 || drained.Desired.CredentialVersion != 2 {
		t.Fatalf("drained runtime=%+v err=%v", drained, err)
	}
}

func TestGatewayAuthorityPersistsBeforeActivationSoFailureIsRestartRecoverable(t *testing.T) {
	ctx := context.Background()
	lifecycle := newAuthorityLifecycle()
	manager := newAuthorityManager(t, account.RuntimeModeTest, lifecycle)
	desiredStore := newMemoryDesiredStore()
	authority, err := newGatewayAuthority(ctx, desiredStore, manager, account.RuntimeModeTest)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Execute(ctx, httpapi.AuthorityRequest{Operation: httpapi.AuthorityCreateDesired, Desired: &httpapi.DesiredAccount{
		AccountID: "dynamic", Venue: "binance", VenueEnvironment: "demo", SecretRef: "opaque-initial",
		DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "creating",
	}}); err != nil {
		t.Fatal(err)
	}
	lifecycle.activationErr = errors.New("secret-material-canary")
	_, err = authority.Execute(ctx, httpapi.AuthorityRequest{Operation: httpapi.AuthorityUpdateDesired, AccountID: "dynamic", ExpectedRevision: 1, Mutation: &httpapi.DesiredMutation{
		SecretRef: "opaque-initial", DesiredStateRevision: 2, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "active",
	}})
	if err == nil || strings.Contains(err.Error(), "secret-material-canary") {
		t.Fatalf("activation error=%v", err)
	}
	persisted, err := desiredStore.LoadDesiredAccount(ctx, "dynamic")
	if err != nil || persisted.RequestedState != store.DesiredActive || persisted.DesiredStateRevision != 2 {
		t.Fatalf("persisted=%+v err=%v", persisted, err)
	}
	if _, err := manager.Runtime("dynamic"); !errors.Is(err, account.ErrUnknownAccount) {
		t.Fatalf("failed activation admitted runtime: %v", err)
	}
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

func assertRedactedDesiredResponse(t *testing.T, response httpapi.AuthorityResponse, revision int64) {
	t.Helper()
	desired, ok := response.Body.(httpapi.DesiredAccount)
	if !ok {
		t.Fatalf("body type = %T", response.Body)
	}
	if desired.SecretRef != "redacted" || response.DesiredStateRevision != revision {
		t.Fatalf("response=%+v", response)
	}
}

func seedActiveAuthorityAccount(t *testing.T, ctx context.Context, authority *gatewayAuthority) {
	t.Helper()
	if _, err := authority.Execute(ctx, httpapi.AuthorityRequest{Operation: httpapi.AuthorityCreateDesired, Desired: &httpapi.DesiredAccount{
		AccountID: "dynamic", Venue: "binance", VenueEnvironment: "demo", SecretRef: "opaque-initial",
		DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "creating",
	}}); err != nil {
		t.Fatal(err)
	}
	if _, err := authority.Execute(ctx, httpapi.AuthorityRequest{Operation: httpapi.AuthorityUpdateDesired, AccountID: "dynamic", ExpectedRevision: 1, Mutation: &httpapi.DesiredMutation{
		SecretRef: "opaque-initial", DesiredStateRevision: 2, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "active",
	}}); err != nil {
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
