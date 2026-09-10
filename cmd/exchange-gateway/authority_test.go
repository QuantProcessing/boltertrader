package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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
	"github.com/QuantProcessing/boltertrader/gateway/fundingboard"
	"github.com/QuantProcessing/boltertrader/gateway/httpapi"
	"github.com/QuantProcessing/boltertrader/gateway/marketcapability"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
	"github.com/QuantProcessing/boltertrader/gateway/publicmarket"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/QuantProcessing/boltertrader/routeprofile"
	"github.com/shopspring/decimal"
)

const (
	authorityRuntimeIDA = "018f47a8-2578-7d3c-9d7d-5e4f4a445e33"
	authorityRuntimeIDB = "018f47a8-2578-7d3c-9d7d-5e4f4a445e34"
)

func TestManagerAuthorityReadsPropagatesConfiguredCatalogFailure(t *testing.T) {
	reads := managerAuthorityReads{manager: &fakeAuthorityAccounts{
		resources:     []account.Resource{{AccountID: "broken", ReadReady: false}},
		instrumentErr: errors.New("configured catalog unavailable"),
	}}
	if _, err := reads.Instruments(context.Background()); err == nil {
		t.Fatal("configured-account catalog failure was published as successful truth")
	}
}

// Public venue and instrument discovery is Gateway state, not account state.
// A fresh install must open the trader before it has credentials to configure.
func TestGatewayAuthorityCatalogAndMarketDataExistWithoutAccounts(t *testing.T) {
	observedAt := time.Date(2026, 8, 19, 1, 2, 3, 0, time.UTC)
	markets := staticPublicMarkets{snapshot: publicmarket.Snapshot{
		Revision: "public-catalog", ObservedAt: observedAt,
		Instruments: []publicmarket.Instrument{{Ready: true, Catalog: catalog.Instrument{
			ID: "binance:live:perp:USDT:BTC-USDT", Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive,
			Product: exchange.ProductPerp, Settlement: "USDT", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT",
			Rules: catalog.Rules{PriceIncrement: decimal.RequireFromString("0.1"), QuantityIncrement: decimal.RequireFromString("0.001"), MinQuantity: decimal.RequireFromString("0.001")},
		}}},
	}, fact: publicmarket.Fact{
		Book: exchange.OrderBook{Time: observedAt,
			Bids: []exchange.BookLevel{{Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(2)}},
			Asks: []exchange.BookLevel{{Price: decimal.NewFromInt(101), Quantity: decimal.NewFromInt(3)}},
		},
	}}
	manager := newAuthorityManager(t)
	if len(manager.Accounts()) != 0 {
		t.Fatal("test manager unexpectedly has an account")
	}
	authority, err := newGatewayAuthority(context.Background(), manager, withPublicMarkets(markets))
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
		if venue.Environment != string(exchange.EnvironmentLive) || !venue.Ready {
			t.Fatalf("venue=%+v", venue)
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
	if !ok || instruments.Revision == "" || instruments.ObservedAt.IsZero() || len(instruments.Instruments) != 1 || !instruments.Instruments[0].Ready {
		t.Fatalf("instruments=%T %+v", instrumentsResponse.Body, instrumentsResponse.Body)
	}
	marketResponse, err := authority.Execute(context.Background(), httpapi.AuthorityRequest{Operation: httpapi.AuthorityMarketData, Query: map[string][]string{
		"venue": {"binance"}, "environment": {"live"}, "product": {"perp"}, "instrumentId": {"binance:live:perp:USDT:BTC-USDT"},
	}})
	if err != nil {
		t.Fatal(err)
	}
	if fact, ok := marketResponse.Body.(authorityMarketData); !ok || fact.BestBid != "100" || fact.BestAsk != "101" {
		t.Fatalf("market=%T %+v", marketResponse.Body, marketResponse.Body)
	}
}

type staticPublicMarkets struct {
	snapshot publicmarket.Snapshot
	fact     publicmarket.Fact
	candles  publicmarket.CandleResult
	err      error
}

func (markets staticPublicMarkets) Venues() []string {
	return []string{"aster", "binance", "bitget", "bybit", "gate", "hyperliquid", "lighter", "nado", "okx"}
}

func (markets staticPublicMarkets) Snapshot(context.Context) (publicmarket.Snapshot, error) {
	return markets.snapshot, markets.err
}

func (markets staticPublicMarkets) MarketData(_ context.Context, query publicmarket.Query, _ int) (publicmarket.Fact, error) {
	result := markets.fact
	result.Query = query
	return result, markets.err
}

func (markets staticPublicMarkets) Candles(_ context.Context, query publicmarket.Query, _ string, _ int) (publicmarket.CandleResult, error) {
	result := markets.candles
	result.Query = query
	return result, markets.err
}

func TestManagerAuthorityReadsPreservesCompositeCatalogFacts(t *testing.T) {
	accounts := &fakeAuthorityAccounts{
		resources: []account.Resource{{AccountID: "perp", Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp, Settlement: "USDT", ReadReady: true}},
		snapshot: catalog.Snapshot{Revision: "catalog-1", LoadedAt: time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC), Instruments: []catalog.Instrument{{
			ID: "binance:live:perp:BTC-USDT", Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp, Settlement: "USDT",
			Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Rules: catalog.Rules{
				PriceIncrement: decimal.RequireFromString("0.1"), QuantityIncrement: decimal.RequireFromString("0.001"), MinQuantity: decimal.RequireFromString("0.001"),
				MinNotional: exchange.OptionalDecimal{Value: decimal.RequireFromString("5"), Valid: true},
			},
		}}},
	}
	got, err := (managerAuthorityReads{manager: accounts}).Instruments(context.Background())
	if err != nil || len(got.Instruments) != 1 || got.Revision == "" || got.ObservedAt.IsZero() {
		t.Fatalf("Instruments() = %+v, %v", got, err)
	}
	instrument := got.Instruments[0]
	if instrument.Environment != "live" || instrument.Product != "perp" || instrument.Symbol != "BTC-USDT" || instrument.BaseAsset != "BTC" || instrument.QuoteAsset != "USDT" || instrument.Rules.MinNotional != "5" {
		t.Fatalf("instrument = %+v", instrument)
	}
}

func TestManagerAuthorityReadsAggregatesCatalogAuthorityDeterministically(t *testing.T) {
	oldest := time.Date(2026, 8, 5, 0, 0, 0, 0, time.UTC)
	resources := []account.Resource{
		{AccountID: "binance-perp", Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp, ReadReady: true},
		{AccountID: "binance-spot", Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, Product: exchange.ProductSpot, ReadReady: true},
	}
	snapshots := map[domain.AccountID]catalog.Snapshot{
		"binance-perp": {Revision: "revision-b", LoadedAt: oldest.Add(time.Minute)},
		"binance-spot": {Revision: "revision-a", LoadedAt: oldest},
	}
	first, err := (managerAuthorityReads{manager: &fakeAuthorityAccounts{resources: resources, snapshots: snapshots}}).Instruments(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	slices.Reverse(resources)
	second, err := (managerAuthorityReads{manager: &fakeAuthorityAccounts{resources: resources, snapshots: snapshots}}).Instruments(context.Background())
	if err != nil || first.Revision != second.Revision || !first.ObservedAt.Equal(oldest) || !second.ObservedAt.Equal(oldest) {
		t.Fatalf("first=%+v second=%+v err=%v", first, second, err)
	}
}

func TestManagerAuthorityReadsRequiresPublicMarketSource(t *testing.T) {
	accounts := &fakeAuthorityAccounts{}
	if _, err := (managerAuthorityReads{manager: accounts}).MarketData(context.Background(), "binance", "live", "perp", "BTC-USDT"); err == nil {
		t.Fatal("market data silently fell back to an account route")
	}
	if _, err := (managerAuthorityReads{manager: accounts}).Candles(context.Background(), authorityCandlesQuery{Venue: "binance", Environment: "live", Product: "perp", InstrumentID: "BTC-USDT", Interval: "1m"}); err == nil {
		t.Fatal("candles silently fell back to an account route")
	}
}

type fakeAuthorityAccounts struct {
	resources     []account.Resource
	instrumentErr error
	snapshot      catalog.Snapshot
	instrument    exchange.Instrument
	perp          exchange.PerpClient
	// perpBySettlement models a venue that settles one product in several
	// assets. Set it instead of perp to exercise the multi-route account.
	perpBySettlement map[string]exchange.PerpClient
	capability       capability.Row
	capabilities     []capability.Row
	resolvedAccount  domain.AccountID
	// perpRouteSettlement records the settlement the reader disambiguated with,
	// so a test can prove the route the loop selected is the route it asked a
	// client for.
	perpRouteSettlement string
	snapshots           map[domain.AccountID]catalog.Snapshot
	runtime             account.RuntimeAccount
	status              account.Status
	routeStatuses       map[string]account.Status
	routeStatusCalls    []string
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
	// Faithful to the account manager: an account running more than one
	// perpetual route cannot answer "give me the perpetual client".
	if len(accounts.perpBySettlement) > 1 {
		return nil, account.ErrProductMismatch
	}
	for _, client := range accounts.perpBySettlement {
		return client, nil
	}
	return accounts.perp, nil
}
func (accounts *fakeAuthorityAccounts) SpotRoute(accountID domain.AccountID, _ string) (exchange.SpotClient, error) {
	return accounts.Spot(accountID)
}
func (accounts *fakeAuthorityAccounts) PerpRoute(accountID domain.AccountID, settlement string) (exchange.PerpClient, error) {
	accounts.perpRouteSettlement = settlement
	if accounts.perpBySettlement != nil {
		client, ok := accounts.perpBySettlement[settlement]
		if !ok {
			return nil, account.ErrProductMismatch
		}
		return client, nil
	}
	return accounts.Perp(accountID)
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
	// settlement, when set, makes this client behave like a real settlement-scoped
	// one: it refuses an instrument that settles in another asset instead of
	// quietly answering for a contract it does not list.
	settlement   string
	book         exchange.OrderBook
	funding      exchange.FundingRate
	history      exchange.FundingRatePage
	fundingCalls int
	historyCalls int
	bookLimit    int
	historyLimit int
}

func (client *fakeAuthorityPerp) settles(instrument exchange.Instrument) error {
	if client.settlement == "" || client.settlement == instrument.Identity.SettleAsset {
		return nil
	}
	return fmt.Errorf("instrument quote must match %s settlement", client.settlement)
}

func (client *fakeAuthorityPerp) OrderBook(_ context.Context, request exchange.OrderBookRequest) (exchange.OrderBook, error) {
	if err := client.settles(request.Instrument); err != nil {
		return exchange.OrderBook{}, err
	}
	client.bookLimit = request.Limit
	return client.book, nil
}
func (client *fakeAuthorityPerp) FundingRate(_ context.Context, request exchange.FundingRateRequest) (exchange.FundingRate, error) {
	if err := client.settles(request.Instrument); err != nil {
		return exchange.FundingRate{}, err
	}
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
	authority, err := newGatewayAuthority(ctx, newAuthorityManager(t))
	if err != nil {
		t.Fatal(err)
	}
	observedAt := time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC)
	authority.reads = staticAuthorityReads{
		venues:      []authorityVenue{{Venue: "binance", Environment: "live", Ready: true}},
		instruments: []authorityInstrument{{InstrumentID: "binance:live:perp:BTC-USDT", Venue: "binance", Ready: true}},
		marketData: authorityMarketData{
			Venue: "binance", Environment: "live", Product: "perp", InstrumentID: "binance:live:perp:BTC-USDT", BestBid: "100", BestAsk: "101",
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
		Query:     map[string][]string{"venue": {"binance"}, "environment": {"live"}, "product": {"perp"}, "instrumentId": {"binance:live:perp:BTC-USDT"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if got, ok := market.Body.(authorityMarketData); !ok || got.MarkPrice != "100.5" || got.ObservedAt != observedAt {
		t.Fatalf("market=%T %+v", market.Body, market.Body)
	}
}

func TestMigrationValidationPublishesStaticTestCatalogWithoutPrivateRuntime(t *testing.T) {
	authority := validationSnapshotAuthority{}
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
		// Ready is the assertion that carries the test now. It used to also check
		// that no venue was labelled live, because a live label during migration
		// validation meant a real-money endpoint; with demo and testnet gone every
		// venue is live and the label proves nothing. What still has to hold is
		// that validation publishes this catalog without a private runtime, so
		// nothing in it is ready to be traded.
		if venue.Venue == "" || venue.Environment == "" || venue.Ready {
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
	authority, err := newGatewayAuthority(ctx, newAuthorityManager(t))
	if err != nil {
		t.Fatal(err)
	}
	authority.reads = staticAuthorityReads{
		venues:      []authorityVenue{{Venue: "binance", Environment: "live", Ready: true}},
		instruments: []authorityInstrument{{InstrumentID: "BTC-USDT", Venue: "binance", Environment: "live", Product: "perp", Ready: true}},
		marketData:  authorityMarketData{Venue: "binance", Environment: "live", Product: "perp", InstrumentID: "BTC-USDT", MarkPrice: "100.5", ObservedAt: time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC)},
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
			if err := json.Unmarshal(data, &items); err != nil || len(items) != 1 || items[0].Environment != "live" {
				t.Fatalf("venues=%+v err=%v", items, err)
			}
		}},
		{path: "/internal/v1/catalog/instruments", assert: func(t *testing.T, data json.RawMessage) {
			var items authorityCatalogInstruments
			if err := json.Unmarshal(data, &items); err != nil || len(items.Instruments) != 1 || items.Instruments[0].InstrumentID != "BTC-USDT" || items.Revision == "" || items.ObservedAt.IsZero() {
				t.Fatalf("instruments=%+v err=%v", items, err)
			}
		}},
		{path: "/internal/v1/market-data?instrumentId=BTC-USDT", assert: func(t *testing.T, data json.RawMessage) {
			var fact authorityMarketData
			if err := json.Unmarshal(data, &fact); err != nil || fact.MarkPrice != "100.5" || fact.ObservedAt.IsZero() {
				t.Fatalf("market data=%+v err=%v", fact, err)
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
	authority, err := newGatewayAuthority(ctx, newAuthorityManager(t))
	if err != nil {
		t.Fatal(err)
	}
	authority.reads = staticAuthorityReads{err: errors.New("read capability unavailable")}
	_, err = authority.Execute(ctx, httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityMarketData,
		Query:     map[string][]string{"venue": {"binance"}, "environment": {"live"}, "product": {"perp"}, "instrumentId": {"BTC-USDT"}},
	})
	var authorityErr httpapi.AuthorityError
	if !errors.As(err, &authorityErr) || authorityErr.Status != http.StatusInternalServerError || !authorityErr.Retryable {
		t.Fatalf("error=%T %v", err, err)
	}
}

// TestGatewayAuthorityFundingBoardProjectsWithoutAccountsAndOmitsAbsentCells
// pins the two properties the board projection exists to hold.
//
// The first is that it answers with no accounts configured. The board is what
// an operator looks at before configuring anything, and every other read on
// managerAuthorityReads walks the manager for a ReadReady route; a refactor
// that "helpfully" routed this one through the manager too would leave a fresh
// install staring at an empty market with no error to explain it.
//
// The second is that an optional cell the venue never quoted stays off the
// wire. exchange.OptionalDecimal marshals as {"value":…,"valid":…}, so emitting
// it raw would render an absent open interest as the number zero — and seven of
// the ten venues leave open interest empty in their bulk call. A board is read
// by comparing venues against each other, which makes an invented zero worse
// than a blank.
func TestGatewayAuthorityFundingBoardProjectsWithoutAccountsAndOmitsAbsentCells(t *testing.T) {
	ctx := context.Background()
	observedAt := time.Unix(1720000000, 0).UTC()
	accounts := &fakeAuthorityAccounts{}
	reads := managerAuthorityReads{
		manager: accounts,
		markets: staticPublicMarkets{snapshot: publicmarket.Snapshot{Instruments: []publicmarket.Instrument{{
			RouteProfileID: "binance.perp.usdt.v1",
			Catalog: catalog.Instrument{
				ID: "binance:live:perp:USDT:BTC-USDT", Venue: exchange.VenueBinance,
				Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp,
				Settlement: "USDT", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT",
				MarketFamily: exchange.MarketFamilyVenuePerpetual, AssetClass: exchange.AssetClassCrypto,
				ClassificationSource: "fixture.official", RawCategories: []string{"crypto"},
			},
			Canonical: exchange.Instrument{Identity: exchange.InstrumentIdentity{
				Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: "BTC-USDT",
				NativeSymbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT",
			}},
		}}}},
		board: newTestFundingBoardSource(t, exchange.FundingBoardEntry{
			Symbol: "BTC-USDT", NativeSymbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT",
			Rate:          decimal.RequireFromString("-0.00042"),
			IntervalHours: exchange.OptionalInt{Value: 8, Valid: true},
			MarkPrice:     exchange.OptionalDecimal{Value: decimal.RequireFromString("64000.5"), Valid: true},
			OpenInterestNotional: exchange.OptionalDecimal{
				Value: decimal.RequireFromString("790129052.835"), Valid: true,
			},
		}),
		now: func() time.Time { return observedAt },
	}

	board, err := reads.FundingBoard(ctx)
	if err != nil {
		t.Fatalf("FundingBoard: %v", err)
	}
	if len(accounts.resources) != 0 || accounts.routeStatusCalls != nil {
		t.Fatalf("board consulted the account manager: %+v", accounts.routeStatusCalls)
	}
	if len(board.Venues) == 0 {
		t.Fatal("board carries no venue columns")
	}

	var rows int
	for _, venue := range board.Venues {
		rows += len(venue.Rows)
	}
	if rows == 0 {
		t.Fatalf("board carries no rows: %+v", board.Venues)
	}

	encoded, err := json.Marshal(board)
	if err != nil {
		t.Fatal(err)
	}
	for _, absent := range []string{"\"openInterest\":", "indexPrice", "nativeSymbol", "nativeId", "\"valid\"", "\"value\"", "0001-01-01"} {
		if strings.Contains(string(encoded), absent) {
			t.Fatalf("board JSON carries %q for a cell no venue quoted: %s", absent, encoded)
		}
	}
	for _, present := range []string{"\"instrumentId\":\"binance:live:perp:USDT:BTC-USDT\"", "\"marketFamily\":\"venue_perpetual\"", "\"assetClass\":\"crypto\"", "\"classificationSource\":\"fixture.official\"", "\"rawCategories\":[\"crypto\"]", "\"rate\":\"-0.00042\"", "\"markPrice\":\"64000.5\"", "\"openInterestNotional\":\"790129052.835\"", "\"intervalHours\":8"} {
		if !strings.Contains(string(encoded), present) {
			t.Fatalf("board JSON is missing %s: %s", present, encoded)
		}
	}
}

// TestGatewayAuthorityFundingBoardWithoutSourceIsUnavailable pins that a
// gateway with no board reports one, rather than serving a table with no
// venues. The two are indistinguishable once they reach the browser, and "every
// venue is down" is a conclusion an operator would trade on.
func TestGatewayAuthorityFundingBoardWithoutSourceIsUnavailable(t *testing.T) {
	ctx := context.Background()
	authority, err := newGatewayAuthority(ctx, newAuthorityManager(t))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = authority.Execute(ctx, httpapi.AuthorityRequest{Operation: httpapi.AuthorityFundingBoard}); err == nil {
		t.Fatal("a gateway with no funding board answered as though it had one")
	}
	var authorityErr httpapi.AuthorityError
	if !errors.As(err, &authorityErr) || authorityErr.Status != http.StatusServiceUnavailable ||
		authorityErr.Code != "FUNDING_BOARD_UNAVAILABLE" || !authorityErr.Retryable {
		t.Fatalf("error=%T %v", err, err)
	}
}

// newTestFundingBoardSource builds a board whose every venue answers with the
// same single entry, so a projection test can assert field mapping without
// depending on which venues the profile registry happens to carry.
func newTestFundingBoardSource(t *testing.T, entry exchange.FundingBoardEntry) fundingboard.Board {
	t.Helper()
	now := time.Now().UTC()
	fact := exchange.DerivativeFact{
		InstrumentID: "binance:live:perp:USDT:BTC-USDT", RouteProfileID: "binance.perp.usdt.v1",
		Environment: exchange.EnvironmentLive,
		Instrument:  exchange.InstrumentIdentity{Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: entry.Symbol, NativeSymbol: entry.NativeSymbol, NativeID: entry.NativeID, BaseAsset: entry.BaseAsset, QuoteAsset: entry.QuoteAsset, SettleAsset: "USDT"},
		FundingRate: exchange.OptionalDecimal{Value: entry.Rate, Valid: true}, FundingIntervalHours: entry.IntervalHours,
		NextFundingTime: entry.NextFundingTime, FundingObservedAt: now,
		MarkPrice: entry.MarkPrice, IndexPrice: entry.IndexPrice,
		OpenInterest: entry.OpenInterest, OpenInterestNotional: entry.OpenInterestNotional,
		OpenInterestNotionalAsset: entry.OpenInterestNotionalAsset,
	}
	if fact.OpenInterestNotional.Valid && fact.OpenInterestNotionalAsset == "" {
		fact.OpenInterestNotionalAsset = fact.Instrument.SettleAsset
	}
	if fact.MarkPrice.Valid {
		fact.MarkObservedAt = now
	}
	if fact.IndexPrice.Valid {
		fact.IndexObservedAt = now
	}
	if fact.OpenInterest.Valid || fact.OpenInterestNotional.Valid {
		fact.OpenInterestObservedAt = now
	}
	store := markethub.NewFactStore(nil, time.Now)
	err := store.Apply(exchange.DerivativePatch{
		Kind: exchange.EventSnapshot, Scope: exchange.CatalogScope{Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Environment: exchange.EnvironmentLive, Settlement: "USDT"},
		NativeFamily: "perpetual", Generation: 1, ObservedAt: now, Fields: exchange.DerivativeFieldAll, Facts: []exchange.DerivativeFact{fact},
	})
	if err != nil {
		t.Fatal(err)
	}
	capabilities, capabilityErr := marketcapability.DefaultRegistry()
	if capabilityErr != nil {
		t.Fatal(capabilityErr)
	}
	projection, err := fundingboard.NewProjection(store, nil, routeprofile.DefaultRegistry(), capabilities)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = projection.Close() })
	return projection
}

// TestGatewayAuthorityFundingHistoryDispatchParsesBoundsAndLabelsItsAnswer
// pins the three things this dispatch does that the board's does not, because
// unlike the board it takes a query.
//
// It parses two instants, it refuses a partial identity, and it checks that
// the answer came back wearing the label it asked for. The last is the one
// worth a test: two of these series are drawn on one chart as the legs of a
// spread, so a response that arrived under the wrong venue renders as a
// plausible number in the wrong place rather than as a visible gap.
func TestGatewayAuthorityFundingHistoryDispatchParsesBoundsAndLabelsItsAnswer(t *testing.T) {
	ctx := context.Background()
	authority, err := newGatewayAuthority(ctx, newAuthorityManager(t))
	if err != nil {
		t.Fatal(err)
	}
	fundingTime := time.Date(2026, 8, 14, 8, 0, 0, 0, time.UTC)
	authority.reads = staticAuthorityReads{fundingSeries: authorityFundingSeries{
		Venue:      "binance",
		ObservedAt: time.Date(2026, 8, 14, 9, 0, 0, 0, time.UTC),
		Points:     []authorityFundingSeriesPoint{{FundingTime: fundingTime, Rate: "-0.00042", IntervalHours: 8}},
	}}
	query := func(mutate func(map[string][]string)) map[string][]string {
		values := map[string][]string{
			"instrumentId": {"binance:live:perp:USDT:BTC-USDT"},
			"since":        {"2026-08-07T16:00:00+08:00"}, "until": {"2026-08-14T16:00:00+08:00"},
		}
		mutate(values)
		return values
	}
	unchanged := func(map[string][]string) {}

	response, err := authority.Execute(ctx, httpapi.AuthorityRequest{Operation: httpapi.AuthorityFundingHistory, Query: query(unchanged)})
	if err != nil {
		t.Fatalf("FundingHistory: %v", err)
	}
	series, ok := response.Body.(authorityFundingSeries)
	if !ok {
		t.Fatalf("series=%T %+v", response.Body, response.Body)
	}
	// The offset is normalised away rather than carried, so two legs quoted by
	// venues on different clocks land on one axis.
	wantSince := time.Date(2026, 8, 7, 8, 0, 0, 0, time.UTC)
	if series.InstrumentID != "binance:live:perp:USDT:BTC-USDT" || series.Venue != "binance" ||
		!series.Since.Equal(wantSince) || series.Since.Location() != time.UTC {
		t.Fatalf("series identity=%+v", series)
	}
	if len(series.Points) != 1 || series.Points[0].Rate != "-0.00042" ||
		series.Points[0].IntervalHours != 8 || !series.Points[0].FundingTime.Equal(fundingTime) {
		t.Fatalf("series points=%+v", series.Points)
	}

	for name, mutate := range map[string]func(map[string][]string){
		"no instrument":    func(values map[string][]string) { delete(values, "instrumentId") },
		"blank instrument": func(values map[string][]string) { values["instrumentId"] = []string{"  "} },
		"no since":         func(values map[string][]string) { delete(values, "since") },
		// A bound that lost its unit somewhere upstream still parses as an
		// integer, into a different decade, and the only symptom is an empty
		// chart. Both of these must be refused at the boundary, not carried.
		"epoch millis":      func(values map[string][]string) { values["since"] = []string{"1754553600000"} },
		"date without zone": func(values map[string][]string) { values["until"] = []string{"2026-08-14"} },
	} {
		t.Run(name, func(t *testing.T) {
			rejected, err := authority.Execute(ctx, httpapi.AuthorityRequest{Operation: httpapi.AuthorityFundingHistory, Query: query(mutate)})
			if err == nil || rejected.Body != nil {
				t.Fatalf("body=%+v err=%v", rejected.Body, err)
			}
		})
	}

	authority.reads = relabelingFundingReads{}
	if _, err = authority.Execute(ctx, httpapi.AuthorityRequest{Operation: httpapi.AuthorityFundingHistory, Query: query(unchanged)}); err == nil {
		t.Fatal("a series labelled with another venue was served as the one that was asked for")
	}

	authority.reads = staticAuthorityReads{err: fundingboard.ErrHistoryUnsupported}
	_, err = authority.Execute(ctx, httpapi.AuthorityRequest{Operation: httpapi.AuthorityFundingHistory, Query: query(unchanged)})
	var authorityErr httpapi.AuthorityError
	// Not retryable, unlike the board's own unavailability: nado publishes no
	// settled series and never will, so a caller that polls this is polling for
	// a chart that cannot exist.
	if !errors.As(err, &authorityErr) || authorityErr.Status != http.StatusNotFound ||
		authorityErr.Code != "FUNDING_HISTORY_UNSUPPORTED" || authorityErr.Retryable {
		t.Fatalf("error=%T %v", err, err)
	}
}

// relabelingFundingReads answers with a series that is not the one asked for.
// staticAuthorityReads cannot express this: it echoes the query back precisely
// so that every other caller clears the dispatcher's identity check.
type relabelingFundingReads struct{ staticAuthorityReads }

func (relabelingFundingReads) FundingHistory(context.Context, authorityFundingHistoryQuery) (authorityFundingSeries, error) {
	return authorityFundingSeries{InstrumentID: "bybit:live:perp:USDT:BTC-USDT", Venue: "bybit"}, nil
}

func TestGatewayAuthoritySeparatesRuntimeStatusAndReadinessDTOs(t *testing.T) {
	ctx := context.Background()
	authority, err := newGatewayAuthority(ctx, newAuthorityManager(t))
	if err != nil {
		t.Fatal(err)
	}

	statusResponse, err := authority.Execute(ctx, httpapi.AuthorityRequest{Operation: httpapi.AuthorityRuntimeStatus})
	if err != nil {
		t.Fatal(err)
	}
	status, ok := statusResponse.Body.(authorityRuntimeStatus)
	if !ok || len(status.Accounts) != 0 {
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
	if !ok || len(status.Accounts) != 1 {
		t.Fatalf("active status=%T %+v", statusResponse.Body, statusResponse.Body)
	}
}

func TestGatewayAuthorityRuntimeListAndStatusJSONOmitSecretReference(t *testing.T) {
	ctx := context.Background()
	authority, err := newGatewayAuthority(ctx, newAuthorityManager(t))
	if err != nil {
		t.Fatal(err)
	}
	seedActiveAuthorityAccount(t, ctx, authority)
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
	venues        []authorityVenue
	instruments   []authorityInstrument
	marketData    authorityMarketData
	candles       authorityCandles
	fundingBoard  authorityFundingBoard
	fundingSeries authorityFundingSeries
	err           error
}

func (reads staticAuthorityReads) Venues() []authorityVenue {
	return append([]authorityVenue(nil), reads.venues...)
}

func (reads staticAuthorityReads) Instruments(context.Context) (authorityCatalogInstruments, error) {
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

func (reads staticAuthorityReads) FundingBoard(context.Context) (authorityFundingBoard, error) {
	return reads.fundingBoard, reads.err
}

func (reads staticAuthorityReads) FundingHistory(_ context.Context, query authorityFundingHistoryQuery) (authorityFundingSeries, error) {
	series := reads.fundingSeries
	// Same reason Candles echoes its query back: the dispatcher rejects a
	// response whose identity does not match the request, so a stub returning a
	// zero-valued one would fail every caller for a reason unrelated to what
	// the test is asserting.
	series.InstrumentID = query.InstrumentID
	series.Since, series.Until = query.Since, query.Until
	return series, reads.err
}

func seedActiveAuthorityAccount(t *testing.T, ctx context.Context, authority *gatewayAuthority) {
	t.Helper()
	connection, err := authority.manager.PrepareRegistration(ctx, account.RegistrationRecord{Registration: account.Registration{ID: "dynamic", Venue: "binance"}, CredentialBundle: []byte(`{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"fake-key","apiSecret":"fake-secret"}}`)})
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Activate(); err != nil {
		t.Fatal(err)
	}
}

func newAuthorityManager(t *testing.T) *account.Manager {
	t.Helper()
	gatewayCatalog, err := catalog.NewService(time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := account.NewManager(context.Background(), nil, account.Options{
		Secrets: secret.StaticProvider{}, ExchangeCatalog: exchange.NewDefaultMemoryCatalog(), GatewayCatalog: gatewayCatalog,
		HTTPClient: &http.Client{}, RuntimeGroupBuilder: authorityCandidateBuilder{}, AllowLiveWrites: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	return manager
}

type authorityConnectionRoute struct{}

func (authorityConnectionRoute) Probe(context.Context) error { return nil }
func (authorityConnectionRoute) Close() error                { return nil }

type authorityCandidateBuilder struct{}

func (authorityCandidateBuilder) BuildRoute(context.Context, account.ConnectionSpec, account.RouteDefinition, *credential.Decoded) (account.ConnectionRoute, error) {
	return authorityConnectionRoute{}, nil
}
