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
	"github.com/QuantProcessing/boltertrader/gateway/publicmarket"
	"github.com/QuantProcessing/boltertrader/gateway/secret"
	"github.com/QuantProcessing/boltertrader/gateway/store"
	"github.com/QuantProcessing/boltertrader/routeprofile"
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
	if _, err := reads.Instruments(context.Background()); err == nil {
		t.Fatal("configured-account catalog failure was published as successful truth")
	}
}

func TestPublicMarketAuthorityOperationsDoNotTakeAccountControlLock(t *testing.T) {
	independent := []httpapi.AuthorityOperation{
		httpapi.AuthorityRuntimeReadiness,
		httpapi.AuthorityCatalogVenues,
		httpapi.AuthorityCatalogInstruments,
		httpapi.AuthorityMarketData,
		httpapi.AuthorityCandles,
		httpapi.AuthorityFundingBoard,
		httpapi.AuthorityFundingHistory,
	}
	for _, operation := range independent {
		if authorityOperationSerialized(operation) {
			t.Errorf("%s unexpectedly takes the account-control lock", operation)
		}
	}
	if !authorityOperationSerialized(httpapi.AuthorityApplyDesired) {
		t.Fatal("account mutation no longer takes the account-control lock")
	}
}

// Public venue and instrument discovery is Gateway state, not account state.
// A fresh install must open the trader before it has credentials to configure.
func TestGatewayAuthorityCatalogAndMarketDataExistWithoutAccounts(t *testing.T) {
	observedAt := time.Date(2026, 8, 19, 1, 2, 3, 0, time.UTC)
	markets := staticPublicMarkets{snapshot: publicmarket.Snapshot{
		Revision: "public-catalog", ObservedAt: observedAt,
		Instruments: []publicmarket.Instrument{{Catalog: catalog.Instrument{
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
	manager := newAuthorityManager(t, newAuthorityLifecycle())
	if len(manager.Accounts()) != 0 {
		t.Fatal("test manager unexpectedly has an account")
	}
	authority, err := newGatewayAuthority(context.Background(), newMemoryDesiredStore(), manager, withPublicMarkets(markets))
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

func (markets staticPublicMarkets) MarketData(_ context.Context, query publicmarket.Query, _, _ int) (publicmarket.Fact, error) {
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
		resources:  []account.Resource{{AccountID: "perp", Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp, ReadReady: true}},
		instrument: exchange.Instrument{Identity: exchange.InstrumentIdentity{Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: "BTC-USDT"}},
		perp:       perp, capability: capability.Row{Product: "perp", Operations: capability.Operations{Funding: true}},
	}
	fact, err := (managerAuthorityReads{manager: accounts}).MarketData(context.Background(), "binance", "live", "perp", "BTC-USDT")
	if err != nil || fact.FundingRate != "0.0007" || fact.BestBidSize != "2.5" || fact.BestAskSize != "3.25" || len(fact.Bids) != 1 || len(fact.Asks) != 1 || len(fact.FundingHistory) != 1 || !fact.FundingObservedAt.Equal(observedAt.Add(time.Second)) || !fact.FundingTime.Equal(fundingTime) || !fact.NextFundingTime.Equal(nextFundingTime) || perp.fundingCalls != 1 || perp.historyCalls != 1 || perp.bookLimit != authorityDepthLimit || perp.historyLimit != authorityFundingHistoryLimit {
		t.Fatalf("fact=%+v fundingCalls=%d historyCalls=%d err=%v", fact, perp.fundingCalls, perp.historyCalls, err)
	}

	accounts.capability.Operations.Funding = false
	perp.fundingCalls = 0
	if _, err = (managerAuthorityReads{manager: accounts}).MarketData(context.Background(), "binance", "live", "perp", "BTC-USDT"); err == nil || perp.fundingCalls != 0 {
		t.Fatalf("unsupported fundingCalls=%d err=%v", perp.fundingCalls, err)
	}
}

// A venue may settle one product in more than one asset — bybit runs USDT and
// USDC perpetuals side by side — and the account manager installs a separate
// route for each. Accounts() emits one Resource per route and the catalog
// resolves an instrument for the whole account, so this read must pair the
// instrument with the one route that can actually trade it. It did neither:
// it resolved the client by bare account id, an ambiguous question the manager
// refuses to answer, and it paired whichever route sorted first with every
// instrument. Both defects took the whole venue down rather than one route.
func TestManagerAuthorityReadsPairsInstrumentWithTheRouteThatSettlesIt(t *testing.T) {
	observedAt := time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC)
	newPerp := func(settlement, bid string) *fakeAuthorityPerp {
		return &fakeAuthorityPerp{
			settlement: settlement,
			book:       exchange.OrderBook{Time: observedAt, Bids: []exchange.BookLevel{{Price: decimal.RequireFromString(bid), Quantity: decimal.NewFromInt(1)}}, Asks: []exchange.BookLevel{{Price: decimal.RequireFromString(bid).Add(decimal.NewFromInt(1)), Quantity: decimal.NewFromInt(2)}}},
			funding:    exchange.FundingRate{Rate: decimal.RequireFromString("0.0001"), ObservedAt: observedAt},
		}
	}
	// USDC sorts first, so a reader that ignores settlement reaches for it.
	usdc, usdt := newPerp("USDC", "100"), newPerp("USDT", "200")
	accounts := &fakeAuthorityAccounts{
		resources: []account.Resource{
			{AccountID: "bybit", Venue: exchange.VenueBybit, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp, Settlement: "USDC", ReadReady: true},
			{AccountID: "bybit", Venue: exchange.VenueBybit, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp, Settlement: "USDT", ReadReady: true},
		},
		instrument:       exchange.Instrument{Identity: exchange.InstrumentIdentity{Venue: exchange.VenueBybit, Product: exchange.ProductPerp, Symbol: "SIREN-USDT", SettleAsset: "USDT"}},
		perpBySettlement: map[string]exchange.PerpClient{"USDC": usdc, "USDT": usdt},
		capabilities: []capability.Row{
			{Product: "perp", Settlement: "USDC", Operations: capability.Operations{Funding: true}},
			{Product: "perp", Settlement: "USDT", Operations: capability.Operations{Funding: true}},
		},
	}
	fact, err := (managerAuthorityReads{manager: accounts}).MarketData(context.Background(), "bybit", "live", "perp", "SIREN-USDT")
	if err != nil || fact.BestBid != "200" || accounts.perpRouteSettlement != "USDT" || usdc.fundingCalls != 0 {
		t.Fatalf("fact=%+v settlement=%q usdcCalls=%d err=%v", fact, accounts.perpRouteSettlement, usdc.fundingCalls, err)
	}
}

// The environment half of this selection is still compared, at
// authority.go:1454, but it can no longer discriminate: both fixtures are live
// because live is the only environment left. Product is what separates them
// here, and an exact product lookup is the rule that has to keep holding.
func TestManagerAuthorityReadsSelectsExactEnvironmentAndProduct(t *testing.T) {
	observedAt := time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC)
	perp := &fakeAuthorityPerp{
		book:    exchange.OrderBook{Time: observedAt, Bids: []exchange.BookLevel{{Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(1)}}, Asks: []exchange.BookLevel{{Price: decimal.NewFromInt(101), Quantity: decimal.NewFromInt(2)}}},
		funding: exchange.FundingRate{Rate: decimal.RequireFromString("0.0001"), ObservedAt: observedAt, FundingTime: observedAt.Add(time.Hour), NextFundingTime: observedAt.Add(9 * time.Hour)},
	}
	accounts := &fakeAuthorityAccounts{
		resources: []account.Resource{
			{AccountID: "binance-spot", Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, Product: exchange.ProductSpot, ReadReady: true},
			{AccountID: "binance-perp", Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive, Product: exchange.ProductPerp, ReadReady: true},
		},
		instrument: exchange.Instrument{Identity: exchange.InstrumentIdentity{Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: "BTC-USDT"}},
		perp:       perp, capability: capability.Row{Product: "perp", Operations: capability.Operations{Funding: true}},
	}
	fact, err := (managerAuthorityReads{manager: accounts}).MarketData(context.Background(), "binance", "live", "perp", "BTC-USDT")
	if err != nil || fact.Environment != "live" || fact.Product != "perp" || accounts.resolvedAccount != "binance-perp" {
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
			AccountID: "multi-route", Venue: exchange.VenueBinance, Environment: exchange.EnvironmentLive,
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
	fact, err := (managerAuthorityReads{manager: accounts}).MarketData(context.Background(), "binance", "live", "perp", "BTC-USDT")
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
	authority, err := newGatewayAuthority(ctx, newMemoryDesiredStore(), newAuthorityManager(t, newAuthorityLifecycle()))
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
	authority, err := newGatewayAuthority(ctx, newMemoryDesiredStore(), newAuthorityManager(t, newAuthorityLifecycle()))
	if err != nil {
		t.Fatal(err)
	}
	authority.reads = staticAuthorityReads{
		venues:      []authorityVenue{{Venue: "binance", Environment: "live", Ready: true}},
		instruments: []authorityInstrument{{InstrumentID: "BTC-USDT", Venue: "binance", Ready: true}},
		marketData:  authorityMarketData{Venue: "binance", Environment: "live", Product: "perp", InstrumentID: "BTC-USDT", MarkPrice: "100.5", ObservedAt: time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC)},
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
		{path: "/internal/v1/market-data?venue=binance&environment=live&product=perp&instrumentId=BTC-USDT", assert: func(t *testing.T, data json.RawMessage) {
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
	authority, err := newGatewayAuthority(ctx, newMemoryDesiredStore(), newAuthorityManager(t, newAuthorityLifecycle()))
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
	authority, err := newGatewayAuthority(ctx, newMemoryDesiredStore(), newAuthorityManager(t, newAuthorityLifecycle()))
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

func TestAuthorityErrorPreservesCandidateCredentialFailure(t *testing.T) {
	err := authorityError(account.CandidateFailure{RouteID: "perp-usdc", ReasonCode: "CREDENTIAL_EXPIRED"})
	var authorityErr httpapi.AuthorityError
	if !errors.As(err, &authorityErr) || authorityErr.Status != http.StatusUnprocessableEntity ||
		authorityErr.Code != "CREDENTIAL_EXPIRED" || authorityErr.Retryable {
		t.Fatalf("authorityError = %+v", authorityErr)
	}
}

// newTestFundingBoardSource builds a board whose every venue answers with the
// same single entry, so a projection test can assert field mapping without
// depending on which venues the profile registry happens to carry.
func newTestFundingBoardSource(t *testing.T, entry exchange.FundingBoardEntry) *fundingboard.Source {
	t.Helper()
	source, err := fundingboard.NewSource(constantFundingFactory{entry: entry}, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	runCtx, cancel := context.WithCancel(context.Background())
	go func() { _ = source.Run(runCtx) }()
	deadline := time.Now().Add(time.Second)
	for {
		snapshot, snapshotErr := source.Snapshot(context.Background())
		ready := snapshotErr == nil
		if ready {
			ready = false
			for _, venue := range snapshot.Venues {
				if len(venue.Entries) > 0 {
					ready = true
					break
				}
			}
		}
		if ready {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("funding board did not bootstrap")
		}
		time.Sleep(time.Millisecond)
	}
	t.Cleanup(func() {
		cancel()
		_ = source.Close()
	})
	return source
}

type constantFundingFactory struct{ entry exchange.FundingBoardEntry }

func (factory constantFundingFactory) Build(profile routeprofile.Profile) (fundingboard.Connector, error) {
	return constantFundingConnector{
		scope: exchange.CatalogScope{
			Venue:   exchange.Venue(profile.Venue),
			Product: exchange.Product(profile.Product),
			// The profile no longer carries an environment: every route is live.
			// The scope field stays because a snapshot is labelled with the venue
			// it came from and callers compare that label against their request.
			Environment: exchange.EnvironmentLive,
			Settlement:  profile.SettlementAsset,
		},
		entry: factory.entry,
	}, nil
}

type constantFundingConnector struct {
	scope exchange.CatalogScope
	entry exchange.FundingBoardEntry
}

func (connector constantFundingConnector) Refresh(context.Context) (exchange.FundingBoardSnapshot, error) {
	return exchange.FundingBoardSnapshot{Scope: connector.scope, Entries: []exchange.FundingBoardEntry{connector.entry}}, nil
}

func (constantFundingConnector) Close() error { return nil }

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
	authority, err := newGatewayAuthority(ctx, newMemoryDesiredStore(), newAuthorityManager(t, newAuthorityLifecycle()))
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
	desiredStore := newMemoryDesiredStore()
	authority, err := newGatewayAuthority(ctx, desiredStore, newAuthorityManager(t, newAuthorityLifecycle()))
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
	if !ok || len(status.Accounts) != 1 {
		t.Fatalf("active status=%T %+v", statusResponse.Body, statusResponse.Body)
	}
}

func TestGatewayAuthorityRuntimeListAndStatusJSONOmitSecretReference(t *testing.T) {
	ctx := context.Background()
	desiredStore := newMemoryDesiredStore()
	authority, err := newGatewayAuthority(ctx, desiredStore, newAuthorityManager(t, newAuthorityLifecycle()))
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
	venues        []authorityVenue
	instruments   []authorityInstrument
	marketData    authorityMarketData
	candles       authorityCandles
	fundingBoard  authorityFundingBoard
	fundingSeries authorityFundingSeries
	runtime       authorityRuntimeProjection
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

func (reads staticAuthorityReads) Runtime(string) (authorityRuntimeProjection, error) {
	return reads.runtime, reads.err
}

func TestGatewayAuthorityRestoresActiveAndRotatingAccounts(t *testing.T) {
	ctx := context.Background()
	desiredStore := newMemoryDesiredStore()
	desiredStore.items["active"] = store.DesiredAccount{
		AccountID: "active", Venue: "binance", VenueEnvironment: "live", SecretRef: "opaque-active",
		DesiredStateRevision: 2, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: store.DesiredActive,
	}
	desiredStore.items["rotating"] = store.DesiredAccount{
		AccountID: "rotating", Venue: "binance", VenueEnvironment: "live", SecretRef: "opaque-new", PreviousSecretRef: "opaque-old",
		DesiredStateRevision: 3, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: store.DesiredRotating,
		RotationFence: &store.RotationFence{ID: "rotate-1", State: store.RotationArmed, FromCredentialVersion: 1, ToCredentialVersion: 2, FromRuntimeGeneration: 1, ToRuntimeGeneration: 2, ArmedAt: time.Unix(1, 0)},
	}
	lifecycle := newAuthorityLifecycle()
	manager := newAuthorityManager(t, lifecycle)
	if _, err := newGatewayAuthority(ctx, desiredStore, manager); err != nil {
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
		AccountID: "dynamic", Venue: "binance", VenueEnvironment: "live",
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
		AccountID: "dynamic", Venue: "binance", VenueEnvironment: "live", SecretRef: "opaque-initial",
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

func newAuthorityManager(t *testing.T, lifecycle account.RuntimeLifecycle) *account.Manager {
	t.Helper()
	gatewayCatalog, err := catalog.NewService(time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	manager, err := account.NewManager(context.Background(), nil, account.Options{
		Secrets: secret.StaticProvider{}, ExchangeCatalog: exchange.NewDefaultMemoryCatalog(), GatewayCatalog: gatewayCatalog,
		HTTPClient: &http.Client{}, Lifecycle: lifecycle,
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
		HTTPClient: &http.Client{}, RuntimeGroupBuilder: authorityCandidateBuilder{},
		CredentialResolver: authorityCredentialResolver{}, Lifecycle: newAuthorityLifecycle(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	snapshotStore := newSnapshotAuthorityStore()
	authority, err := newGatewayAuthority(context.Background(), snapshotStore, manager)
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
		Operation: httpapi.AuthorityApplyDesired, AccountID: "account-a", OperationID: applyOperationID,
		DesiredApply: &httpapi.DesiredApplyRequest{
			OperationID: applyOperationID, AccountID: "account-a", Venue: "binance", NativeEnvironment: "live", SecretRef: opaqueRef,
			DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "active",
			Routes: []httpapi.DesiredRoute{{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.v1", RequestedState: "active"}},
		},
	}
	if _, err := authority.Execute(context.Background(), apply); err != nil {
		t.Fatal(err)
	}
	retire := httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityRetireDesired, AccountID: "account-a", OperationID: retireOperationID,
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
	restartedManager := newAuthorityManager(t, newAuthorityLifecycle())
	restarted, err := newGatewayAuthority(context.Background(), snapshotStore, restartedManager)
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
		OperationID: reused.OperationID, AccountID: "account-a", Venue: "binance", NativeEnvironment: "live", SecretRef: opaqueRef,
		DesiredStateRevision: 3, RuntimeGeneration: 2, CredentialVersion: 1, RequestedState: "active",
		Routes: []httpapi.DesiredRoute{{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.v1", RequestedState: "active"}},
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
		GatewayCatalog: gatewayCatalog, HTTPClient: &http.Client{},
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
	authority, err := newGatewayAuthority(context.Background(), store, manager)
	if err != nil {
		t.Fatal(err)
	}
	const applyOperationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445d01"
	const retireOperationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445d02"
	if _, err := authority.Execute(context.Background(), httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityApplyDesired, AccountID: "account-a", OperationID: applyOperationID,
		DesiredApply: &httpapi.DesiredApplyRequest{
			OperationID: applyOperationID, AccountID: "account-a", Venue: "binance", NativeEnvironment: "live",
			SecretRef: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", DesiredStateRevision: 1,
			RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "active",
			Routes: []httpapi.DesiredRoute{{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.v1", RequestedState: "active"}},
		},
	}); err != nil {
		t.Fatalf("apply: %v", err)
	}
	response, err := authority.Execute(context.Background(), httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityRetireDesired, AccountID: "account-a", OperationID: retireOperationID,
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
	if err := lifecycle.attach(blockerQuerierStub{}, &positionBlockerSourceStub{err: account.ErrUnknownAccount}); err != nil {
		t.Fatal(err)
	}
	if _, err := lifecycle.Blockers(context.Background(), "account-a"); !errors.Is(err, account.ErrUnknownAccount) {
		t.Fatalf("absent runtime error=%v, want ErrUnknownAccount", err)
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
		HTTPClient: &http.Client{}, RuntimeGroupBuilder: authorityCandidateBuilder{},
		CredentialResolver: authorityCredentialResolver{}, Lifecycle: newAuthorityLifecycle(),
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	snapshotStore := newSnapshotAuthorityStore()
	authority, err := newGatewayAuthority(context.Background(), snapshotStore, manager)
	if err != nil {
		t.Fatal(err)
	}
	apply := func(operationID string, revision, generation, credentialVersion int64, secretRef string) {
		t.Helper()
		_, err := authority.Execute(context.Background(), httpapi.AuthorityRequest{
			Operation: httpapi.AuthorityApplyDesired, AccountID: "account-a", OperationID: operationID,
			DesiredApply: &httpapi.DesiredApplyRequest{
				OperationID: operationID, AccountID: "account-a", Venue: "binance", NativeEnvironment: "live", SecretRef: secretRef,
				DesiredStateRevision: revision, RuntimeGeneration: generation, CredentialVersion: credentialVersion, RequestedState: "active",
				Routes: []httpapi.DesiredRoute{{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.v1", RequestedState: "active"}},
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
		Operation: httpapi.AuthorityFinalizeRotation, AccountID: "account-a", OperationID: activeOperationID,
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
	if snapshotStore.rotationCommits != 1 || snapshotStore.rotationRequest == nil || snapshotStore.rotationRequest.Scope != authoritySecretScope() {
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
		HTTPClient: &http.Client{}, RuntimeGroupBuilder: authorityCandidateBuilder{},
		CredentialResolver: authorityCredentialResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	snapshotStore := newSnapshotAuthorityStore()
	authority, err := newGatewayAuthority(context.Background(), snapshotStore, manager)
	if err != nil {
		t.Fatal(err)
	}
	operationID, _ := secretstore.ParseOperationID("018f47a8-2578-7d3c-9d7d-5e4f4a445c33")
	secretRef, _ := secretstore.ParseOpaqueRef("AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA")
	desired := store.DesiredSnapshot{
		SnapshotID: uuid.MustParse("018f47a8-2578-7d3c-9d7d-5e4f4a445c35"), OperationID: operationID,
		AccountID: "account-a", Venue: "binance", NativeEnvironment: "live", SecretRef: secretRef,
		Fence:          domain.AccountFence{AccountID: "account-a", DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1},
		RequestedState: store.DesiredActive,
		Routes:         []store.RouteDefinition{{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.v1", RequestedState: store.RouteActive}},
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
		HTTPClient: &http.Client{}, RuntimeGroupBuilder: authorityCandidateBuilder{},
		CredentialResolver: authorityCredentialResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	snapshotStore := newSnapshotAuthorityStore()
	authority, err := newGatewayAuthority(context.Background(), snapshotStore, manager)
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
		AccountID:   "account-a",
		OperationID: operationID,
		DesiredApply: &httpapi.DesiredApplyRequest{
			OperationID: operationID, AccountID: "account-a", Venue: "binance", NativeEnvironment: "live", SecretRef: opaqueRef,
			DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "active",
			Routes: []httpapi.DesiredRoute{{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.v1", RequestedState: "active"}},
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
		HTTPClient: &http.Client{}, RuntimeGroupBuilder: authorityCandidateBuilder{},
		CredentialResolver: authorityCredentialResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	snapshotStore := newSnapshotAuthorityStore()
	authority, err := newGatewayAuthority(context.Background(), snapshotStore, manager)
	if err != nil {
		t.Fatal(err)
	}
	const operationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445c33"
	const opaqueRef = "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA"
	_, err = authority.Execute(context.Background(), httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityApplyDesired, AccountID: "account-a", OperationID: operationID,
		DesiredApply: &httpapi.DesiredApplyRequest{
			OperationID: operationID, AccountID: "account-a", Venue: "binance", NativeEnvironment: "live", SecretRef: opaqueRef,
			DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "active",
			Routes: []httpapi.DesiredRoute{{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.v1", RequestedState: "active"}},
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
	// The authority used to be switched to live mode here, because the rule
	// under test — a route whose only complaint is the live-write switch stays
	// active — was written as "live mode AND that reason". Only the reason is
	// left to arrange now, and the stub above already carries it.
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
		HTTPClient: &http.Client{}, RuntimeGroupBuilder: authorityCandidateBuilder{},
		CredentialResolver: authorityStaticCredentialResolver{bundle: []byte(`{"schemaVersion":1,"venue":"lighter","authKind":"lighter_signer","metadata":{"accountIndex":0,"keyIndex":0},"secrets":{"privateKey":"key"}}`)},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	authority, err := newGatewayAuthority(context.Background(), newSnapshotAuthorityStore(), manager)
	if err != nil {
		t.Fatal(err)
	}
	authority.routeStatus = func(domain.AccountID, string, string) (account.Status, error) {
		return account.Status{Resource: account.Resource{ReadReady: true, WriteReady: true}}, nil
	}
	const operationID = "018f47a8-2578-7d3c-9d7d-5e4f4a445c33"
	_, err = authority.Execute(context.Background(), httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityApplyDesired, AccountID: "account-a", OperationID: operationID,
		DesiredApply: &httpapi.DesiredApplyRequest{
			OperationID: operationID, AccountID: "account-a", Venue: "lighter", NativeEnvironment: "live",
			SecretRef: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "active",
			Routes: []httpapi.DesiredRoute{{RouteID: "spot", Product: "spot", RouteProfileID: "lighter.spot.v1", RequestedState: "active"}},
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
		HTTPClient:          &http.Client{},
		RuntimeGroupBuilder: authorityFailingCandidateBuilder{failGeneration: 2}, CredentialResolver: authorityCredentialResolver{},
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = manager.Close() })
	snapshotStore := newSnapshotAuthorityStore()
	authority, err := newGatewayAuthority(context.Background(), snapshotStore, manager)
	if err != nil {
		t.Fatal(err)
	}
	request := func(operationID string, revision, generation int64) httpapi.AuthorityRequest {
		return httpapi.AuthorityRequest{
			Operation: httpapi.AuthorityApplyDesired, AccountID: "account-a", OperationID: operationID,
			DesiredApply: &httpapi.DesiredApplyRequest{
				OperationID: operationID, AccountID: "account-a", Venue: "binance", NativeEnvironment: "live",
				SecretRef: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", DesiredStateRevision: revision,
				RuntimeGeneration: generation, CredentialVersion: 1, RequestedState: "active",
				Routes: []httpapi.DesiredRoute{{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.v1", RequestedState: "active"}},
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
	if !ok || view.Active == nil || view.Active.RuntimeGeneration != 1 || view.Pending == nil || view.Pending.RuntimeGeneration != 2 || view.Pending.State != string(store.OperationCandidateFailed) || view.Pending.Reason != "CANDIDATE_BUILD_FAILED" || len(view.Pending.Routes) != 1 || view.Pending.Routes[0].Ready || view.Pending.Routes[0].Reason != "CANDIDATE_BUILD_FAILED" {
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
			HTTPClient: &http.Client{}, RuntimeGroupBuilder: authorityCandidateBuilder{},
			CredentialResolver: resolver,
		})
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = manager.Close() })
		return manager
	}
	request := httpapi.AuthorityRequest{
		Operation: httpapi.AuthorityApplyDesired, AccountID: "account-a",
		OperationID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c33",
		DesiredApply: &httpapi.DesiredApplyRequest{
			OperationID: "018f47a8-2578-7d3c-9d7d-5e4f4a445c33", AccountID: "account-a", Venue: "binance", NativeEnvironment: "live",
			SecretRef: "AAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA", DesiredStateRevision: 1,
			RuntimeGeneration: 1, CredentialVersion: 1, RequestedState: "active",
			Routes: []httpapi.DesiredRoute{{RouteID: "spot", Product: "spot", RouteProfileID: "binance.spot.v1", RequestedState: "active"}},
		},
	}
	snapshotStore := newSnapshotAuthorityStore()
	firstAuthority, err := newGatewayAuthority(context.Background(), snapshotStore, newManager(t, authorityCredentialResolver{}))
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
	restartedAuthority, err := newGatewayAuthority(context.Background(), snapshotStore, restartedManager)
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
