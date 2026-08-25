package publicmarket

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
	"github.com/QuantProcessing/boltertrader/routeprofile"
	"github.com/shopspring/decimal"
)

type residentMarketOwner struct{ observedAt time.Time }

func (owner residentMarketOwner) Open(_ context.Context, key markethub.SubscriptionKey) (markethub.NativeStream, error) {
	var data any
	switch key.Dataset {
	case exchange.DatasetDepth:
		data = map[string]any{
			"bids": []exchange.BookLevel{{Price: decimal.NewFromInt(99), Quantity: decimal.NewFromInt(2)}},
			"asks": []exchange.BookLevel{{Price: decimal.NewFromInt(101), Quantity: decimal.NewFromInt(3)}},
		}
	case exchange.DatasetMark:
		data = map[string]any{"price": "100.5"}
	default:
		return markethub.NativeStream{}, markethub.ErrUnavailable
	}
	return markethub.NativeStream{
		Snapshot: markethub.Message{Kind: markethub.MessageSnapshot, ObservedAt: owner.observedAt, SourceSequence: "resident-1", Data: markethub.Encode(data)},
		Events:   make(chan markethub.Message), Errors: make(chan error), Close: func() error { return nil },
	}, nil
}

type catalogRevisionStoreStub struct{ revisions []CatalogRevision }

func (store *catalogRevisionStoreStub) SaveCatalogRevision(_ context.Context, revision CatalogRevision) error {
	store.revisions = append(store.revisions, revision)
	return nil
}

type catalogRevisionLoaderStub struct{ revision CatalogRevision }

func (loader catalogRevisionLoaderStub) LoadCatalogRevision(_ context.Context, scopeKey string) (CatalogRevision, bool, error) {
	if loader.revision.ScopeKey != scopeKey {
		return CatalogRevision{}, false, nil
	}
	return loader.revision, true, nil
}

func TestCatalogRevisionFingerprintIsStableAcrossVenueOrdering(t *testing.T) {
	profile := routeprofile.Profile{ID: "binance.perp.usdt.v1"}
	first := exchange.Instrument{Identity: exchange.InstrumentIdentity{NativeID: "2", NativeSymbol: "ETHUSDT"}}
	second := exchange.Instrument{Identity: exchange.InstrumentIdentity{NativeID: "1", NativeSymbol: "BTCUSDT"}}
	store := &catalogRevisionStoreStub{}
	if err := persistCatalogRevision(t.Context(), store, profile, []exchange.Instrument{first, second}); err != nil {
		t.Fatal(err)
	}
	if err := persistCatalogRevision(t.Context(), store, profile, []exchange.Instrument{second, first}); err != nil {
		t.Fatal(err)
	}
	if len(store.revisions) != 2 || store.revisions[0].Fingerprint != store.revisions[1].Fingerprint || !json.Valid(store.revisions[0].Instruments) {
		t.Fatalf("catalog revisions = %+v", store.revisions)
	}
}

func TestSourceRestoresCatalogAsStaleUntilVenueRefresh(t *testing.T) {
	now := time.Date(2026, 8, 25, 4, 0, 0, 0, time.UTC)
	instrument := exchange.Instrument{Identity: exchange.InstrumentIdentity{
		Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: "BTC-USDT", NativeSymbol: "BTCUSDT",
		BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT",
	}, Rules: exchange.InstrumentRules{
		PriceIncrement: decimal.RequireFromString("0.1"), QuantityIncrement: decimal.RequireFromString("0.001"), MinQuantity: decimal.RequireFromString("0.001"),
	}}
	connector := &fakeConnector{instrument: instrument}
	source, err := newSource(fakeFactory{connector: connector}, routeprofile.DefaultRegistry(), sourceOptions{
		interval: time.Minute, maxAge: 2 * time.Minute, venueTimeout: time.Second, now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	payload, _ := json.Marshal([]exchange.Instrument{instrument})
	if err := source.RestoreCatalogRevisions(t.Context(), catalogRevisionLoaderStub{revision: CatalogRevision{
		ScopeKey: "binance.perp.usdt.v1", Fingerprint: "saved", ObservedAt: now, Instruments: payload,
	}}); err != nil {
		t.Fatal(err)
	}
	restored, err := source.Snapshot(t.Context())
	if err != nil || len(restored.Instruments) != 1 || restored.Instruments[0].Ready {
		t.Fatalf("restored=%+v err=%v", restored, err)
	}
	if err := source.columns[0].refresh(t.Context()); err != nil {
		t.Fatal(err)
	}
	live, err := source.Snapshot(t.Context())
	if err != nil || len(live.Instruments) != 1 || !live.Instruments[0].Ready {
		t.Fatalf("live=%+v err=%v", live, err)
	}
}

func TestSnapshotOwnsPublicToNativeFundingIdentity(t *testing.T) {
	instrumentID := domain.InstrumentID("okx:live:perp:USDT:BTC-USDT-SWAP")
	snapshot := Snapshot{Instruments: []Instrument{{
		RouteProfileID: "okx.perp.usdt.v1",
		Catalog: catalog.Instrument{
			ID: instrumentID, Venue: exchange.VenueOKX, Environment: exchange.EnvironmentLive,
			Product: exchange.ProductPerp, Settlement: "USDT", Symbol: "BTC-USDT-SWAP",
			BaseAsset: "BTC", QuoteAsset: "USDT",
		},
		Canonical: exchange.Instrument{Identity: exchange.InstrumentIdentity{
			Venue: exchange.VenueOKX, Product: exchange.ProductPerp,
			Symbol: "BTC-USDT-SWAP", NativeSymbol: "BTC-USDT-SWAP", NativeID: "swap-1",
			BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT",
		}},
	}}}

	matched, ok := snapshot.MatchFundingEntry("okx.perp.usdt.v1", exchange.FundingBoardEntry{
		// A public spelling is deliberately not used as an identity key.
		Symbol: "BTC-USDT", NativeSymbol: "BTC-USDT-SWAP", NativeID: "swap-1",
	})
	if !ok || matched.Catalog.ID != instrumentID {
		t.Fatalf("matched=%+v ok=%v", matched, ok)
	}
	if _, ok := snapshot.MatchFundingEntry("okx.perp.usdc.v1", exchange.FundingBoardEntry{NativeID: "swap-1"}); ok {
		t.Fatal("native id from another route profile was accepted")
	}
	if found, ok := snapshot.Find(string(instrumentID)); !ok || found.Canonical.Identity.NativeID != "swap-1" {
		t.Fatalf("found=%+v ok=%v", found, ok)
	}
}

func TestSourceServesPublicMarketWithoutAnAccount(t *testing.T) {
	observedAt := time.Date(2026, 8, 19, 1, 2, 3, 0, time.UTC)
	connector := &fakeConnector{
		instrument: exchange.Instrument{
			Identity: exchange.InstrumentIdentity{
				Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: "BTC-USDT",
				BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT", NativeSymbol: "BTCUSDT",
			},
			Rules: exchange.InstrumentRules{
				PriceIncrement: decimal.RequireFromString("0.1"), QuantityIncrement: decimal.RequireFromString("0.001"),
				MinQuantity: decimal.RequireFromString("0.001"),
			},
		},
		book: exchange.OrderBook{
			Time: observedAt,
			Bids: []exchange.BookLevel{{Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(2)}},
			Asks: []exchange.BookLevel{{Price: decimal.NewFromInt(101), Quantity: decimal.NewFromInt(3)}},
		},
		mark: exchange.MarkPriceEvent{Price: decimal.RequireFromString("100.5"), Time: observedAt},
		funding: exchange.FundingRate{
			Rate: decimal.RequireFromString("0.0001"), ObservedAt: observedAt,
		},
		history: exchange.FundingRatePage{Rates: []exchange.FundingRate{{
			Rate: decimal.RequireFromString("0.0002"), ObservedAt: observedAt.Add(-time.Hour), FundingTime: observedAt.Add(-time.Hour),
		}}},
		candles: exchange.CandlePage{Candles: []exchange.Candle{{
			OpenTime: observedAt.Add(-time.Minute), CloseTime: observedAt,
			Open: decimal.NewFromInt(99), High: decimal.NewFromInt(102), Low: decimal.NewFromInt(98), Close: decimal.NewFromInt(100), Volume: decimal.NewFromInt(7),
		}}},
	}
	source, err := NewSource(fakeFactory{connector: connector}, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })

	snapshot, err := source.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Instruments) != 1 || snapshot.Revision == "" || snapshot.ObservedAt.IsZero() {
		t.Fatalf("snapshot=%+v", snapshot)
	}
	instrumentID := snapshot.Instruments[0].Catalog.ID.String()
	if instrumentID != "binance:live:perp:USDT:BTC-USDT" {
		t.Fatalf("instrument id=%q", instrumentID)
	}

	query := Query{Venue: "binance", Environment: "live", Product: "perp", InstrumentID: instrumentID}
	fact, err := source.MarketData(context.Background(), query, 20, 20)
	if err != nil {
		t.Fatal(err)
	}
	if fact.Book.Bids[0].Price.String() != "100" || !fact.HasMark || !fact.HasFunding || len(fact.FundingHistory.Rates) != 1 {
		t.Fatalf("fact=%+v", fact)
	}
	page, err := source.Candles(context.Background(), query, "1m", 300)
	if err != nil || len(page.Page.Candles) != 1 {
		t.Fatalf("candles=%+v err=%v", page, err)
	}
	if connector.refreshes != 1 {
		t.Fatalf("catalog refreshes=%d, want one cached public refresh", connector.refreshes)
	}
}

func TestMarketDataReadsResidentHubWithoutJoiningFundingHistory(t *testing.T) {
	observedAt := time.Date(2026, 8, 25, 1, 2, 3, 0, time.UTC)
	connector := &fakeConnector{instrument: exchange.Instrument{Identity: exchange.InstrumentIdentity{
		Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: "BTC-USDT", NativeSymbol: "BTCUSDT",
		BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT",
	}, Rules: exchange.InstrumentRules{
		PriceIncrement: decimal.RequireFromString("0.1"), QuantityIncrement: decimal.RequireFromString("0.001"), MinQuantity: decimal.RequireFromString("0.001"),
	}}}
	source, err := NewSource(fakeFactory{connector: connector}, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	defer source.Close()
	snapshot, err := source.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	hub, err := markethub.New(markethub.Options{Owner: residentMarketOwner{observedAt: observedAt}, IdleGrace: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer hub.Close()
	source.SetMarketHub(hub)
	instrumentID := string(snapshot.Instruments[0].Catalog.ID)
	fact, err := source.MarketData(t.Context(), Query{Venue: "binance", Environment: "live", Product: "perp", InstrumentID: instrumentID}, 20, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(fact.Book.Bids) != 1 || fact.Book.Bids[0].Price.String() != "99" || !fact.HasMark || fact.Mark.Price.String() != "100.5" || fact.HasFunding || len(fact.FundingHistory.Rates) != 0 {
		t.Fatalf("resident fact=%+v", fact)
	}
}

func TestRunBootstrapsCatalogAndSnapshotDoesNotRefreshVenue(t *testing.T) {
	connector := &signalingConnector{
		fakeConnector: fakeConnector{instrument: exchange.Instrument{
			Identity: exchange.InstrumentIdentity{
				Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: "BTC-USDT",
				BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT", NativeSymbol: "BTCUSDT",
			},
			Rules: exchange.InstrumentRules{
				PriceIncrement: decimal.RequireFromString("0.1"), QuantityIncrement: decimal.RequireFromString("0.001"),
				MinQuantity: decimal.RequireFromString("0.001"),
			},
		}},
		refreshed: make(chan struct{}, 2),
	}
	source, err := newSource(fakeFactory{connector: connector}, routeprofile.DefaultRegistry(), sourceOptions{
		interval: time.Hour, maxAge: 2 * time.Hour, venueTimeout: time.Second, now: time.Now,
	})
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- source.Run(ctx) }()
	select {
	case <-connector.refreshed:
	case <-time.After(time.Second):
		t.Fatal("Run did not bootstrap the catalog")
	}

	if snapshot, err := source.Snapshot(context.Background()); err != nil || len(snapshot.Instruments) != 1 {
		t.Fatalf("Snapshot() = %+v, %v", snapshot, err)
	}
	select {
	case <-connector.refreshed:
		t.Fatal("Snapshot triggered a venue refresh")
	case <-time.After(20 * time.Millisecond):
	}
	cancel()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("Run did not stop")
	}
	if err := source.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestSourceKeepsDepthWhenOptionalMarketFacetsFail(t *testing.T) {
	observedAt := time.Date(2026, 8, 19, 1, 2, 3, 0, time.UTC)
	connector := &fakeConnector{
		instrument: exchange.Instrument{
			Identity: exchange.InstrumentIdentity{
				Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: "BTC-USDT",
				BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT", NativeSymbol: "BTCUSDT",
			},
			Rules: exchange.InstrumentRules{
				PriceIncrement: decimal.RequireFromString("0.1"), QuantityIncrement: decimal.RequireFromString("0.001"),
				MinQuantity: decimal.RequireFromString("0.001"),
			},
		},
		book: exchange.OrderBook{
			Time: observedAt,
			Bids: []exchange.BookLevel{{Price: decimal.NewFromInt(100), Quantity: decimal.NewFromInt(2)}},
			Asks: []exchange.BookLevel{{Price: decimal.NewFromInt(101), Quantity: decimal.NewFromInt(3)}},
		},
		markErr:    errors.New("mark unavailable"),
		fundingErr: errors.New("funding unavailable"),
	}
	source, err := NewSource(fakeFactory{connector: connector}, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })

	snapshot, err := source.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	query := Query{
		Venue: "binance", Environment: "live", Product: "perp",
		InstrumentID: string(snapshot.Instruments[0].Catalog.ID),
	}
	fact, err := source.MarketData(context.Background(), query, 20, 20)
	if err != nil {
		t.Fatal(err)
	}
	if len(fact.Book.Bids) != 1 || len(fact.Book.Asks) != 1 || fact.HasMark || fact.HasFunding {
		t.Fatalf("fact=%+v", fact)
	}
}

func TestSourceJoinsMarkPriceSnapshotToPublicPerpStream(t *testing.T) {
	instrument := exchange.Instrument{Identity: exchange.InstrumentIdentity{
		Venue: exchange.VenueBinance, Product: exchange.ProductPerp, Symbol: "BTC-USDT",
		BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT", NativeSymbol: "BTCUSDT",
	}, Rules: exchange.InstrumentRules{
		PriceIncrement: decimal.RequireFromString("0.1"), QuantityIncrement: decimal.RequireFromString("0.001"),
		MinQuantity: decimal.RequireFromString("0.001"),
	}}
	subscription := newInertSubscription[exchange.MarkPriceEvent]()
	socket := &fakeMarkSocket{subscription: subscription}
	connector := &fakeConnector{
		instrument: instrument,
		mark: exchange.MarkPriceEvent{
			Instrument: instrument, Price: decimal.RequireFromString("77103.2"), Time: time.Now().UTC(),
		},
		socket: socket,
	}
	source, err := NewSource(fakeFactory{connector: connector}, routeprofile.DefaultRegistry())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = source.Close() })
	snapshot, err := source.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	stream, err := source.WatchMarkPrice(context.Background(), snapshot.Instruments[0].Catalog.ID.String())
	if err != nil {
		t.Fatal(err)
	}
	if stream.Snapshot.Price.String() != "77103.2" || stream.Subscription != subscription {
		t.Fatalf("stream=%+v", stream)
	}
	if socket.request.Instrument.Identity.NativeSymbol != "BTCUSDT" {
		t.Fatalf("watch request=%+v", socket.request)
	}
}

type fakeFactory struct {
	connector Connector
}

func (factory fakeFactory) Build(profile routeprofile.Profile) (Connector, error) {
	if profile.Venue == "binance" && profile.Product == routeprofile.ProductPerp && profile.SettlementAsset == "USDT" {
		return factory.connector, nil
	}
	return nil, errors.New("profile not exercised")
}

type fakeConnector struct {
	instrument exchange.Instrument
	book       exchange.OrderBook
	mark       exchange.MarkPriceEvent
	funding    exchange.FundingRate
	history    exchange.FundingRatePage
	candles    exchange.CandlePage
	markErr    error
	fundingErr error
	historyErr error
	refreshes  int
	socket     exchange.SpotWebSocket
}

type signalingConnector struct {
	fakeConnector
	refreshed chan struct{}
}

func (connector *signalingConnector) Refresh(ctx context.Context) ([]exchange.Instrument, error) {
	instruments, err := connector.fakeConnector.Refresh(ctx)
	if err == nil {
		connector.refreshed <- struct{}{}
	}
	return instruments, err
}

func (connector *fakeConnector) Refresh(context.Context) ([]exchange.Instrument, error) {
	connector.refreshes++
	return []exchange.Instrument{connector.instrument}, nil
}

func (connector *fakeConnector) OrderBook(context.Context, exchange.OrderBookRequest) (exchange.OrderBook, error) {
	return connector.book, nil
}

func (connector *fakeConnector) Candles(context.Context, exchange.CandlesRequest) (exchange.CandlePage, error) {
	return connector.candles, nil
}

func (connector *fakeConnector) FundingRate(context.Context, exchange.FundingRateRequest) (exchange.FundingRate, bool, error) {
	return connector.funding, true, connector.fundingErr
}

func (connector *fakeConnector) FundingRateHistory(context.Context, exchange.FundingRateHistoryRequest) (exchange.FundingRatePage, bool, error) {
	return connector.history, true, connector.historyErr
}

func (connector *fakeConnector) MarkPrice(context.Context, exchange.Instrument) (exchange.MarkPriceEvent, bool, error) {
	return connector.mark, true, connector.markErr
}

func (connector *fakeConnector) WebSocket() exchange.SpotWebSocket { return connector.socket }

func (*fakeConnector) Close() error { return nil }

type fakeMarkSocket struct {
	exchange.PerpWebSocket
	subscription exchange.Subscription[exchange.MarkPriceEvent]
	request      exchange.WatchRequest
}

func (socket *fakeMarkSocket) WatchMarkPrice(_ context.Context, request exchange.WatchRequest) (exchange.Subscription[exchange.MarkPriceEvent], error) {
	socket.request = request
	return socket.subscription, nil
}

type inertSubscription[T any] struct {
	events   chan T
	statuses chan exchange.StreamStatusEvent
	errors   chan error
}

func newInertSubscription[T any]() *inertSubscription[T] {
	return &inertSubscription[T]{events: make(chan T), statuses: make(chan exchange.StreamStatusEvent), errors: make(chan error)}
}

func (*inertSubscription[T]) ID() string                    { return "public-mark" }
func (subscription *inertSubscription[T]) Events() <-chan T { return subscription.events }
func (subscription *inertSubscription[T]) Status() <-chan exchange.StreamStatusEvent {
	return subscription.statuses
}
func (subscription *inertSubscription[T]) Errors() <-chan error { return subscription.errors }
func (*inertSubscription[T]) Close() error                      { return nil }

func TestNativeMarketFamilyUsesVenueConnectionBoundary(t *testing.T) {
	tests := []struct {
		venue, settlement, want string
	}{
		{string(exchange.VenueBinance), "USDT", "perpetual"},
		{string(exchange.VenueBinance), "USDC", "perpetual"},
		{string(exchange.VenueBybit), "USDT", "linear"},
		{string(exchange.VenueBybit), "USDC", "linear"},
		{string(exchange.VenueBitget), "USDT", "usdt-futures"},
		{string(exchange.VenueBitget), "USDC", "usdc-futures"},
		{string(exchange.VenueOKX), "USDT", "swap"},
		{string(exchange.VenueNado), "USDT0", "usdt0-perpetual"},
	}
	for _, test := range tests {
		profile := routeprofile.Profile{Venue: test.venue, Product: routeprofile.ProductPerp, SettlementAsset: test.settlement}
		if got := nativeMarketFamily(profile); got != test.want {
			t.Errorf("%s/%s family=%q want=%q", test.venue, test.settlement, got, test.want)
		}
	}
}
