package httpapi

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/platform/accounts"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/catalog"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/marketdata"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/reservations"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/funding/planning"
	fundingpolicy "github.com/QuantProcessing/boltertrader/backend/internal/products/funding/policy"
)

type catalogFixture struct{ snapshot catalog.Snapshot }

func (source *catalogFixture) Snapshot(context.Context) (catalog.Snapshot, error) {
	return source.snapshot, nil
}
func (source *catalogFixture) Status() catalog.Status {
	return catalog.Status{Ready: true, Revision: source.snapshot.Revision}
}

type marketFixture map[marketdata.Query]marketdata.Fact

func (source marketFixture) Get(_ context.Context, query marketdata.Query) (marketdata.Fact, error) {
	item, ok := source[query]
	if !ok {
		return marketdata.Fact{}, errors.New("market fact not found")
	}
	return item, nil
}

type accountFixture map[string]accounts.Account

func (source accountFixture) Get(_ context.Context, id string) (accounts.Account, error) {
	item, ok := source[id]
	if !ok {
		return accounts.Account{}, accounts.ErrNotFound
	}
	return item, nil
}
func (source accountFixture) List(context.Context) ([]accounts.Account, error) {
	items := make([]accounts.Account, 0, len(source))
	for _, item := range source {
		items = append(items, item)
	}
	return items, nil
}

type reservationFixture map[string]reservations.Reservation

func (source reservationFixture) Get(_ context.Context, id string) (reservations.Reservation, error) {
	item, ok := source[id]
	if !ok {
		return reservations.Reservation{}, reservations.ErrNotFound
	}
	return item, nil
}
func (source reservationFixture) List(_ context.Context, principal string, mode reservations.Mode) ([]reservations.Reservation, error) {
	items := []reservations.Reservation{}
	for _, item := range source {
		if item.PrincipalID == principal && item.Mode == mode {
			items = append(items, item)
		}
	}
	return items, nil
}

func TestSharedFundingProjectionBindsCompositeFactsAndRevalidatesAuthority(t *testing.T) {
	now := time.Date(2026, 8, 6, 2, 30, 0, 0, time.UTC)
	catalogSource := &catalogFixture{snapshot: fundingCatalog(now)}
	marketSource := fundingMarkets(now)
	accountSource := accountFixture{
		"account-long":  {ID: "account-long", Venue: "venue-a", VenueEnvironment: accounts.EnvironmentTestnet, ProductIDs: []string{"funding"}, DesiredStateRevision: 11, RuntimeGeneration: 4, CredentialVersion: 3, State: accounts.StateActive},
		"account-short": {ID: "account-short", Venue: "venue-b", VenueEnvironment: accounts.EnvironmentTestnet, ProductIDs: []string{"funding"}, DesiredStateRevision: 13, RuntimeGeneration: 7, CredentialVersion: 5, State: accounts.StateActive},
	}
	reservationSource := reservationFixture{
		"reservation-long":        fundingReservation("reservation-long", "browser-a", reservations.KindOpen, "account-long", "venue-a", "btc-a", "50", now),
		"reservation-short":       fundingReservation("reservation-short", "browser-a", reservations.KindOpen, "account-short", "venue-b", "btc-b", "50", now),
		"reservation-close-long":  fundingReservation("reservation-close-long", "browser-a", reservations.KindClose, "account-long", "venue-a", "btc-a", "50", now),
		"reservation-close-short": fundingReservation("reservation-close-short", "browser-a", reservations.KindClose, "account-short", "venue-b", "btc-b", "50", now),
		"reservation-b-long":      fundingReservation("reservation-b-long", "browser-b", reservations.KindOpen, "account-long", "venue-a", "btc-a", "50", now),
		"reservation-b-short":     fundingReservation("reservation-b-short", "browser-b", reservations.KindOpen, "account-short", "venue-b", "btc-b", "50", now),
	}
	projection, err := NewSharedFundingProjection(SharedFundingProjectionOptions{Catalog: catalogSource, MarketData: marketSource, Accounts: accountSource, Reservations: reservationSource, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	opportunities, err := projection.ListOpportunities(context.Background(), "browser-a", "test")
	if err != nil || len(opportunities) != 1 {
		t.Fatalf("ListOpportunities()=%+v err=%v", opportunities, err)
	}
	opportunity := opportunities[0]
	preview, err := projection.CreatePreview(context.Background(), "browser-a", FundingPreviewRequest{OpportunityID: opportunity.OpportunityID, LongAccountID: "account-long", ShortAccountID: "account-short", CanonicalMarketID: opportunity.CanonicalMarketID, NotionalPerLeg: "50", MaxSlippageBps: 10, HoldThroughSettlement: true, Mode: "test"})
	if err != nil || !preview.CanSubmit || preview.PrincipalID != "browser-a" || preview.Mode != "test" || preview.CatalogRevision != "catalog-rev-1" || preview.FactsRevision == "" || len(preview.Facts) != 1 || preview.Facts[0].Cursor == "" || !preview.ExpiresAt.Equal(now.Add(30*time.Second)) {
		t.Fatalf("CreatePreview()=%+v err=%v", preview, err)
	}
	var frozen planning.PlanningFact
	if err := json.Unmarshal(preview.Facts[0].Payload, &frozen); err != nil || len(frozen.ReservationAuthorities) != 2 || len(frozen.PolicyAssumptions) != 2 || frozen.ReservationAuthorities[0].Revision.IsZero() {
		t.Fatalf("frozen planning authority=%+v err=%v", frozen, err)
	}
	if _, err := projection.GetPreview(context.Background(), "browser-b", preview.PreviewID); !errors.Is(err, ErrFundingPreviewStale) {
		t.Fatalf("cross-principal GetPreview() err=%v", err)
	}
	if _, err := projection.GetPreview(context.Background(), "browser-a", preview.PreviewID); err != nil {
		t.Fatalf("GetPreview() err=%v", err)
	}
	browserBPreview, err := projection.CreatePreview(context.Background(), "browser-b", FundingPreviewRequest{OpportunityID: opportunity.OpportunityID, LongAccountID: "account-long", ShortAccountID: "account-short", CanonicalMarketID: opportunity.CanonicalMarketID, NotionalPerLeg: "50", MaxSlippageBps: 10, Mode: "test"})
	if err != nil || browserBPreview.PreviewID == preview.PreviewID {
		t.Fatalf("principal authority identity collided: a=%s b=%s err=%v", preview.PreviewID, browserBPreview.PreviewID, err)
	}
	closeAuthority, err := projection.CreateCloseAuthority(context.Background(), "browser-a", preview.PreviewID, "lineage-a")
	if err != nil || len(closeAuthority.Facts) != 2 || closeAuthority.Facts[0].Kind != "funding.unwind-reservation" || closeAuthority.Facts[0].EffectiveAt != now || !bytes.Contains(closeAuthority.WorkPayload, []byte(`"requestedAt":"2026-08-06T02:30:00Z"`)) {
		t.Fatalf("CreateCloseAuthority()=%+v err=%v", closeAuthority, err)
	}

	shortQuery := marketdata.Query{Venue: "venue-b", Environment: "testnet", Product: "perp", InstrumentID: "btc-b"}
	shortFact := marketSource[shortQuery]
	shortFact.FundingRate = "0.0012"
	marketSource[shortQuery] = shortFact
	if _, err := projection.GetPreview(context.Background(), "browser-a", preview.PreviewID); !errors.Is(err, ErrFundingPreviewStale) {
		t.Fatalf("changed facts GetPreview() err=%v", err)
	}
	shortFact.FundingRate = "0.0011"
	marketSource[shortQuery] = shortFact

	longAccount := accountSource["account-long"]
	longAccount.DesiredStateRevision++
	accountSource["account-long"] = longAccount
	if _, err := projection.GetPreview(context.Background(), "browser-a", preview.PreviewID); !errors.Is(err, ErrFundingPreviewStale) {
		t.Fatalf("changed account GetPreview() err=%v", err)
	}
	longAccount.DesiredStateRevision--
	accountSource["account-long"] = longAccount

	longReservation := reservationSource["reservation-long"]
	longReservation.Status = reservations.StatusReleased
	reservationSource["reservation-long"] = longReservation
	if _, err := projection.GetPreview(context.Background(), "browser-a", preview.PreviewID); !errors.Is(err, ErrFundingPreviewStale) {
		t.Fatalf("released reservation GetPreview() err=%v", err)
	}
	longReservation.Status = reservations.StatusHeld
	reservationSource["reservation-long"] = longReservation
	longReservation.UpdatedAt = longReservation.UpdatedAt.Add(time.Second)
	reservationSource["reservation-long"] = longReservation
	if _, err := projection.GetPreview(context.Background(), "browser-a", preview.PreviewID); !errors.Is(err, ErrFundingPreviewStale) {
		t.Fatalf("changed reservation revision GetPreview() err=%v", err)
	}
	longReservation.UpdatedAt = longReservation.UpdatedAt.Add(-time.Second)
	reservationSource["reservation-long"] = longReservation

	catalogSource.snapshot.Revision = "catalog-rev-2"
	if _, err := projection.GetPreview(context.Background(), "browser-a", preview.PreviewID); !errors.Is(err, ErrFundingPreviewStale) {
		t.Fatalf("changed catalog GetPreview() err=%v", err)
	}
	catalogSource.snapshot.Revision = "catalog-rev-1"
	now = now.Add(30 * time.Second)
	if _, err := projection.GetPreview(context.Background(), "browser-a", preview.PreviewID); !errors.Is(err, ErrFundingPreviewStale) {
		t.Fatalf("expired GetPreview() err=%v", err)
	}
	if _, err := projection.CreatePreview(context.Background(), "browser-a", FundingPreviewRequest{OpportunityID: opportunity.OpportunityID, LongAccountID: "account-long", ShortAccountID: "account-short", CanonicalMarketID: opportunity.CanonicalMarketID, NotionalPerLeg: "50", Mode: "live"}); err == nil {
		t.Fatal("CreatePreview() accepted cross-mode accounts and facts")
	}
}

func TestSharedFundingProjectionEvictsExpiredAndStalePreviewAuthority(t *testing.T) {
	now := time.Date(2026, 8, 8, 8, 0, 0, 0, time.UTC)
	catalogSource := &catalogFixture{snapshot: fundingCatalog(now)}
	marketSource := fundingMarkets(now)
	accountSource := accountFixture{
		"account-long":  {ID: "account-long", Venue: "venue-a", VenueEnvironment: accounts.EnvironmentTestnet, ProductIDs: []string{"funding"}, State: accounts.StateActive, DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1},
		"account-short": {ID: "account-short", Venue: "venue-b", VenueEnvironment: accounts.EnvironmentTestnet, ProductIDs: []string{"funding"}, State: accounts.StateActive, DesiredStateRevision: 1, RuntimeGeneration: 1, CredentialVersion: 1},
	}
	reservationSource := reservationFixture{
		"reservation-long":  fundingReservation("reservation-long", "browser-a", reservations.KindOpen, "account-long", "venue-a", "btc-a", "50", now),
		"reservation-short": fundingReservation("reservation-short", "browser-a", reservations.KindOpen, "account-short", "venue-b", "btc-b", "50", now),
	}
	projectionAPI, err := NewSharedFundingProjection(SharedFundingProjectionOptions{Catalog: catalogSource, MarketData: marketSource, Accounts: accountSource, Reservations: reservationSource, Admission: fundingpolicy.DefaultAdmission(), Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	projection := projectionAPI.(*sharedFundingProjection)
	opportunities, err := projection.ListOpportunities(context.Background(), "browser-a", "test")
	if err != nil || len(opportunities) == 0 {
		t.Fatalf("ListOpportunities()=%+v error=%v", opportunities, err)
	}
	request := FundingPreviewRequest{OpportunityID: opportunities[0].OpportunityID, LongAccountID: "account-long", ShortAccountID: "account-short", CanonicalMarketID: opportunities[0].CanonicalMarketID, NotionalPerLeg: "50", MaxSlippageBps: 10, HoldThroughSettlement: true, Mode: "test"}
	preview, err := projection.CreatePreview(context.Background(), "browser-a", request)
	if err != nil {
		t.Fatal(err)
	}
	now = preview.ExpiresAt
	if _, err := projection.GetPreview(context.Background(), "browser-a", preview.PreviewID); !errors.Is(err, ErrFundingPreviewStale) {
		t.Fatalf("expired GetPreview() error=%v", err)
	}
	if len(projection.previews) != 0 {
		t.Fatalf("expired preview retained: %d", len(projection.previews))
	}

	now = now.Add(time.Second)
	for query, fact := range fundingMarkets(now) {
		marketSource[query] = fact
	}
	preview, err = projection.CreatePreview(context.Background(), "browser-a", request)
	if err != nil {
		t.Fatal(err)
	}
	changed := accountSource["account-long"]
	changed.DesiredStateRevision++
	accountSource["account-long"] = changed
	if _, err := projection.GetPreview(context.Background(), "browser-a", preview.PreviewID); !errors.Is(err, ErrFundingPreviewStale) {
		t.Fatalf("stale GetPreview() error=%v", err)
	}
	if len(projection.previews) != 0 {
		t.Fatalf("stale preview retained: %d", len(projection.previews))
	}
}

func fundingReservation(id, principal string, kind reservations.Kind, account, venue, instrument, notional string, now time.Time) reservations.Reservation {
	currency := "USDT"
	return reservations.Reservation{ReservationID: id, PrincipalID: principal, Mode: reservations.ModeTest, ProductID: "funding", Kind: kind, Scope: reservations.NettingScope{AccountID: account, Venue: venue, InstrumentID: instrument, VenueEnvironment: "testnet"}, Notional: &notional, Currency: &currency, Status: reservations.StatusHeld, CreatedAt: now.Add(-time.Minute), UpdatedAt: now.Add(-time.Minute)}
}

func fundingCatalog(now time.Time) catalog.Snapshot {
	rules := catalog.TradeRules{PriceIncrement: "0.01", QuantityIncrement: "0.001", MinQuantity: "0.001", MinNotional: "5"}
	return catalog.Snapshot{Revision: "catalog-rev-1", ObservedAt: now, Venues: []catalog.Venue{{Venue: "venue-a", Environment: "testnet", Ready: true}, {Venue: "venue-b", Environment: "testnet", Ready: true}}, Instruments: []catalog.Instrument{
		{InstrumentID: "btc-a", Venue: "venue-a", Environment: "testnet", Product: "perp", Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Rules: rules, Ready: true},
		{InstrumentID: "btc-b", Venue: "venue-b", Environment: "testnet", Product: "perp", Symbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", Rules: rules, Ready: true},
	}}
}

func fundingMarkets(now time.Time) marketFixture {
	makeFact := func(venue, instrument, rate string) marketdata.Fact {
		return marketdata.Fact{Venue: venue, Environment: "testnet", Product: "perp", InstrumentID: instrument, BestBid: "99.9", BestBidSize: "10", BestAsk: "100.1", BestAskSize: "10", Bids: []marketdata.Level{{Price: "99.9", Quantity: "10"}}, Asks: []marketdata.Level{{Price: "100.1", Quantity: "10"}}, MarkPrice: "100", FundingRate: rate, FundingObservedAt: now.Add(-time.Second), FundingTime: now, NextFundingTime: now.Add(8 * time.Hour), FundingHistory: []marketdata.FundingPoint{{Rate: rate, ObservedAt: now.Add(-8 * time.Hour), FundingTime: now.Add(-8 * time.Hour)}, {Rate: rate, ObservedAt: now, FundingTime: now}}, ObservedAt: now.Add(-time.Second)}
	}
	return marketFixture{
		{Venue: "venue-a", Environment: "testnet", Product: "perp", InstrumentID: "btc-a"}: makeFact("venue-a", "btc-a", "0.0001"),
		{Venue: "venue-b", Environment: "testnet", Product: "perp", InstrumentID: "btc-b"}: makeFact("venue-b", "btc-b", "0.0011"),
	}
}
