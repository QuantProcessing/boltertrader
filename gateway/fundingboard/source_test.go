package fundingboard

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/routeprofile"
	"github.com/shopspring/decimal"
)

// TestBoardSourceIsolatesVenueFailures pins the property the whole board is
// built around: a venue that is down costs the table its own column and
// nothing else.
//
// It also pins what "down" renders as. The alternative — dropping the column,
// or serving the venue's last rates without saying how old they are — turns an
// outage into a spread the operator can act on, because a board is read by
// comparing one venue's number against another's.
func TestBoardSourceIsolatesVenueFailures(t *testing.T) {
	clock := &testClock{now: time.Unix(1720000000, 0).UTC()}
	factory := &fakeFactory{fail: map[string]bool{"okx": true}}

	source, err := newSource(factory, routeprofile.DefaultRegistry(), sourceOptions{
		interval: 30 * time.Second, maxAge: 2 * time.Minute, venueTimeout: time.Second, now: clock.Now,
	})
	if err != nil {
		t.Fatalf("newSource: %v", err)
	}
	t.Cleanup(func() { _ = source.Close() })
	source.bootstrapAll(context.Background(), source.columns)

	snapshot, err := source.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Eleven funding-capable perp profiles. Asserting the count here keeps a venue from
	// silently falling off the board when a profile row changes.
	if len(snapshot.Venues) != 11 {
		t.Fatalf("got %d venue columns, want 11: %+v", len(snapshot.Venues), snapshot.Venues)
	}
	for _, venue := range snapshot.Venues {
		if venue.Product != string(routeprofile.ProductPerp) {
			t.Fatalf("%s: product = %q, want perp", venue.Venue, venue.Product)
		}
		if venue.RouteProfileID == "" {
			t.Fatalf("%s: column carries no route profile id", venue.Venue)
		}
	}

	for _, venue := range snapshot.Venues {
		if venue.Venue == "okx" {
			if !venue.Unavailable {
				t.Fatal("okx failed to refresh but its column is not marked unavailable")
			}
			if len(venue.Entries) != 0 {
				t.Fatalf("okx: unavailable column carries %d entries", len(venue.Entries))
			}
			if !venue.ObservedAt.IsZero() {
				t.Fatalf("okx: unavailable column carries observedAt %s", venue.ObservedAt)
			}
			continue
		}
		if venue.Unavailable || venue.Stale {
			t.Fatalf("%s: unavailable=%t stale=%t after a successful refresh", venue.Venue, venue.Unavailable, venue.Stale)
		}
		if len(venue.Entries) != 1 {
			t.Fatalf("%s: got %d entries, want 1", venue.Venue, len(venue.Entries))
		}
		if venue.Entries[0].Symbol != "BTC-"+venue.Settlement {
			t.Fatalf("%s: symbol = %q, want BTC-%s", venue.Venue, venue.Entries[0].Symbol, venue.Settlement)
		}
	}
}

// TestBoardSourceSnapshotsNeverPollVenues pins the ownership boundary: reads
// only copy memory, while the supervisor/bootstrap path owns venue I/O.
func TestBoardSourceSnapshotsNeverPollVenues(t *testing.T) {
	clock := &testClock{now: time.Unix(1720000000, 0).UTC()}
	factory := &fakeFactory{}
	source, err := newSource(factory, routeprofile.DefaultRegistry(), sourceOptions{
		interval: 30 * time.Second, maxAge: 2 * time.Minute, venueTimeout: time.Second, now: clock.Now,
	})
	if err != nil {
		t.Fatalf("newSource: %v", err)
	}
	t.Cleanup(func() { _ = source.Close() })

	source.bootstrapAll(context.Background(), source.columns)
	first := factory.refreshes()
	if first == 0 {
		t.Fatal("startup bootstrap issued no venue refreshes")
	}

	for range 8 {
		clock.advance(10 * time.Second)
		if _, err := source.Snapshot(context.Background()); err != nil {
			t.Fatalf("memory Snapshot: %v", err)
		}
	}
	if factory.refreshes() != first {
		t.Fatalf("snapshot reads re-polled venues: %d -> %d", first, factory.refreshes())
	}

	clock.advance(3 * time.Minute)
	stale, err := source.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("aged Snapshot: %v", err)
	}
	for _, venue := range stale.Venues {
		if !venue.Unavailable {
			t.Fatalf("%s: column older than maxAge is not marked unavailable", venue.Venue)
		}
		if len(venue.Entries) != 0 {
			t.Fatalf("%s: aged-out column still carries %d entries", venue.Venue, len(venue.Entries))
		}
	}
}

// TestBoardSourceBootstrapIsIndependentOfReaders proves the resident producer
// starts without an HTTP reader and a reader burst remains a pure memory copy.
func TestBoardSourceBootstrapIsIndependentOfReaders(t *testing.T) {
	clock := &testClock{now: time.Unix(1720000000, 0).UTC()}
	release := make(chan struct{})
	factory := &fakeFactory{gate: release}
	source, err := newSource(factory, routeprofile.DefaultRegistry(), sourceOptions{
		interval: 30 * time.Second, maxAge: 2 * time.Minute, venueTimeout: time.Second, now: clock.Now,
	})
	if err != nil {
		t.Fatalf("newSource: %v", err)
	}
	t.Cleanup(func() { _ = source.Close() })

	booted := make(chan struct{})
	go func() {
		source.bootstrapAll(context.Background(), source.columns)
		close(booted)
	}()
	time.Sleep(50 * time.Millisecond)
	if got := factory.refreshes(); got != int64(len(source.columns)) {
		t.Fatalf("startup issued %d venue calls, want %d", got, len(source.columns))
	}
	close(release)
	<-booted

	const readers = 8
	var group sync.WaitGroup
	snapshots := make([]Snapshot, readers)
	errs := make([]error, readers)
	for index := range readers {
		group.Add(1)
		go func(index int) {
			defer group.Done()
			snapshots[index], errs[index] = source.Snapshot(context.Background())
		}(index)
	}

	group.Wait()

	for index := range readers {
		if errs[index] != nil {
			t.Fatalf("reader %d: %v", index, errs[index])
		}
		if len(snapshots[index].Venues) != len(snapshots[0].Venues) {
			t.Fatalf("reader %d saw %d columns, reader 0 saw %d", index, len(snapshots[index].Venues), len(snapshots[0].Venues))
		}
	}
	if got := factory.refreshes(); got != int64(len(snapshots[0].Venues)) {
		t.Fatalf("%d readers changed venue calls to %d, want startup count %d", readers, got, len(snapshots[0].Venues))
	}
}

func TestFundingFactMergePreservesPartialFieldsAndRejectsOlderUpdates(t *testing.T) {
	identity := exchange.InstrumentIdentity{
		Venue: exchange.VenueOKX, Product: exchange.ProductPerp,
		Symbol: "BTC-USDT", NativeSymbol: "BTC-USDT-SWAP",
		BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT",
	}
	newer := time.Date(2026, time.August, 20, 2, 0, 0, 0, time.UTC)
	target := exchange.FundingMarketFact{
		Instrument:        identity,
		FundingRate:       exchange.OptionalDecimal{Value: decimal.RequireFromString("0.0001"), Valid: true},
		MarkPrice:         exchange.OptionalDecimal{Value: decimal.NewFromInt(100), Valid: true},
		FundingObservedAt: newer, PriceObservedAt: newer,
	}
	mergeFundingFact(&target, exchange.FundingMarketFact{
		Instrument:             identity,
		OpenInterest:           exchange.OptionalDecimal{Value: decimal.NewFromInt(25), Valid: true},
		OpenInterestObservedAt: newer.Add(time.Second),
	}, exchange.FundingMarketFieldOpenInterest, newer.Add(time.Second))
	if !target.FundingRate.Valid || !target.MarkPrice.Valid || !target.OpenInterest.Valid {
		t.Fatalf("partial OI update cleared another field: %+v", target)
	}
	mergeFundingFact(&target, exchange.FundingMarketFact{
		Instrument:      identity,
		MarkPrice:       exchange.OptionalDecimal{Value: decimal.NewFromInt(90), Valid: true},
		PriceObservedAt: newer.Add(-time.Second),
	}, exchange.FundingMarketFieldMarkPrice, newer.Add(-time.Second))
	if !target.MarkPrice.Value.Equal(decimal.NewFromInt(100)) || !target.PriceObservedAt.Equal(newer) {
		t.Fatalf("older mark replaced current mark: %+v", target)
	}
}

func TestBoardSourceExpiresFieldsIndependentlyAndReadinessNeedsOneFreshRoute(t *testing.T) {
	now := time.Date(2026, time.August, 20, 2, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	identity := exchange.InstrumentIdentity{
		Venue: exchange.VenueBinance, Product: exchange.ProductPerp,
		Symbol: "BTC-USDT", NativeSymbol: "BTCUSDT",
		BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT",
	}
	source := &Source{
		columns: []*column{{
			profile: routeprofile.Profile{ID: "binance.perp.usdt.v1", Venue: "binance", Product: routeprofile.ProductPerp, SettlementAsset: "USDT"},
			status:  exchange.FundingMarketFeedLive, lastSuccess: now,
			facts: map[string]exchange.FundingMarketFact{"btc": {
				Instrument:                identity,
				FundingRate:               exchange.OptionalDecimal{Value: decimal.RequireFromString("0.0001"), Valid: true},
				MarkPrice:                 exchange.OptionalDecimal{Value: decimal.NewFromInt(100), Valid: true},
				OpenInterest:              exchange.OptionalDecimal{Value: decimal.NewFromInt(5), Valid: true},
				OpenInterestNotional:      exchange.OptionalDecimal{Value: decimal.NewFromInt(500), Valid: true},
				OpenInterestNotionalAsset: "USDT",
				FundingObservedAt:         now, PriceObservedAt: now.Add(-121 * time.Second),
				OpenInterestObservedAt: now.Add(-121 * time.Second),
			}},
		}},
		maxAge: 2 * time.Minute, now: clock.Now, subs: map[uint64]chan Change{}, updates: make(chan ChangeKind, 2),
	}
	if !source.Ready() {
		t.Fatal("fresh funding fact did not make the public projection ready")
	}
	snapshot, err := source.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	row := snapshot.Venues[0].Entries[0]
	if row.MarkPrice.Valid || row.OpenInterest.Valid || row.OpenInterestNotional.Valid || !row.Rate.Equal(decimal.RequireFromString("0.0001")) {
		t.Fatalf("field expiry did not preserve only fresh funding: %+v", row)
	}
	clock.advance(121 * time.Second)
	if source.Ready() {
		t.Fatal("an expired route still reported ready")
	}
}

// TestBoardSourceKeepsOneColumnPerMarket pins that a column is an exchange
// market, not a route profile.
//
// The two used to come apart: bybit was registered under both `demo` and
// `testnet`, test mode resolved both, and an enumeration that trusted the
// registry's shape stood two columns labelled "bybit · USDT" beside each other
// carrying different rates. That is the failure this board exists to prevent
// wearing its most convincing disguise — a table is read by comparing one
// venue's number against another's, so two columns with one name read as a
// spread, and no trade can capture it. The registry is now keyed so that a
// market cannot resolve twice; this test is what keeps it that way.
func TestBoardSourceKeepsOneColumnPerMarket(t *testing.T) {
	clock := &testClock{now: time.Unix(1720000000, 0).UTC()}
	source, err := newSource(&fakeFactory{}, routeprofile.DefaultRegistry(), sourceOptions{
		interval: 30 * time.Second, maxAge: 2 * time.Minute, venueTimeout: time.Second, now: clock.Now,
	})
	if err != nil {
		t.Fatalf("newSource: %v", err)
	}
	t.Cleanup(func() { _ = source.Close() })
	source.bootstrapAll(context.Background(), source.columns)

	snapshot, err := source.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	seen := map[string]string{}
	for _, venue := range snapshot.Venues {
		market := venue.Venue + "/" + venue.Settlement
		if previous, duplicate := seen[market]; duplicate {
			t.Fatalf("%s occupies two columns: %s and %s", market, previous, venue.RouteProfileID)
		}
		seen[market] = venue.RouteProfileID
	}
	if len(seen) != 11 {
		t.Fatalf("got %d distinct markets, want 11: %+v", len(seen), snapshot.Venues)
	}
}

func TestBoardSourceGroupsSettlementShardsUnderOneVenueSupervisor(t *testing.T) {
	clock := &testClock{now: time.Unix(1720000000, 0).UTC()}
	source, err := newSource(&fakeFactory{}, routeprofile.DefaultRegistry(), sourceOptions{
		interval: 30 * time.Second, maxAge: 2 * time.Minute, venueTimeout: time.Second, now: clock.Now,
	})
	if err != nil {
		t.Fatalf("newSource: %v", err)
	}
	t.Cleanup(func() { _ = source.Close() })

	groups := groupColumnsByVenue(source.columns)
	if len(groups) != 9 {
		t.Fatalf("got %d venue supervisors, want 9", len(groups))
	}
	counts := make(map[string]int, len(groups))
	for _, group := range groups {
		if len(group) == 0 {
			t.Fatal("empty venue supervisor group")
		}
		venue := group[0].profile.Venue
		for _, current := range group {
			if current.profile.Venue != venue {
				t.Fatalf("supervisor %q contains route from %q", venue, current.profile.Venue)
			}
		}
		counts[venue] = len(group)
	}
	for _, venue := range []string{"bybit", "bitget"} {
		if counts[venue] != 2 {
			t.Fatalf("%s supervisor owns %d settlement shards, want 2", venue, counts[venue])
		}
	}
	for _, venue := range []string{"aster", "binance", "gate", "hyperliquid", "lighter", "nado", "okx"} {
		if counts[venue] != 1 {
			t.Fatalf("%s supervisor owns %d shards, want 1", venue, counts[venue])
		}
	}
}

func TestBoardSourceKeepsFreshPriceWhenFundingExpires(t *testing.T) {
	now := time.Date(2026, time.August, 20, 2, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	source := &Source{
		columns: []*column{{
			profile: routeprofile.Profile{ID: "hyperliquid.perp.usdc.v1", Venue: "hyperliquid", Product: routeprofile.ProductPerp, SettlementAsset: "USDC"},
			status:  exchange.FundingMarketFeedLive, lastSuccess: now,
			facts: map[string]exchange.FundingMarketFact{"btc": {
				Instrument: exchange.InstrumentIdentity{
					Venue: exchange.VenueHyperliquid, Product: exchange.ProductPerp,
					Symbol: "BTC-USDC", NativeSymbol: "BTC", BaseAsset: "BTC", QuoteAsset: "USDC", SettleAsset: "USDC",
				},
				FundingRate:       exchange.OptionalDecimal{Value: decimal.RequireFromString("0.0001"), Valid: true},
				MarkPrice:         exchange.OptionalDecimal{Value: decimal.NewFromInt(100), Valid: true},
				FundingObservedAt: now.Add(-121 * time.Second), PriceObservedAt: now,
			}},
		}},
		maxAge: 2 * time.Minute, now: clock.Now, subs: map[uint64]chan Change{}, updates: make(chan ChangeKind, 2),
	}
	if !source.Ready() {
		t.Fatal("fresh price did not make the public fact projection ready")
	}
	snapshot, err := source.Snapshot(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Venues[0].Entries) != 1 {
		t.Fatalf("fresh price row was dropped: %+v", snapshot.Venues[0])
	}
	row := snapshot.Venues[0].Entries[0]
	if row.RateValid || !row.MarkPrice.Valid || !row.MarkPrice.Value.Equal(decimal.NewFromInt(100)) {
		t.Fatalf("expired funding was presented beside fresh price: %+v", row)
	}
}

func TestBoardSourceFreshnessTransitionPublishesWithoutVenueIO(t *testing.T) {
	now := time.Date(2026, time.August, 20, 2, 0, 0, 0, time.UTC)
	clock := &testClock{now: now}
	source := &Source{
		columns: []*column{{
			profile: routeprofile.Profile{ID: "binance.perp.usdt.v1", Venue: "binance", Product: routeprofile.ProductPerp, SettlementAsset: "USDT"},
			status:  exchange.FundingMarketFeedLive, lastSuccess: now,
			facts: map[string]exchange.FundingMarketFact{"btc": {
				Instrument: exchange.InstrumentIdentity{
					Venue: exchange.VenueBinance, Product: exchange.ProductPerp,
					Symbol: "BTC-USDT", NativeSymbol: "BTCUSDT", BaseAsset: "BTC", QuoteAsset: "USDT", SettleAsset: "USDT",
				},
				FundingRate:       exchange.OptionalDecimal{Value: decimal.RequireFromString("0.0001"), Valid: true},
				MarkPrice:         exchange.OptionalDecimal{Value: decimal.NewFromInt(100), Valid: true},
				FundingObservedAt: now, PriceObservedAt: now,
			}},
		}},
		maxAge: 2 * time.Minute, now: clock.Now, subs: map[uint64]chan Change{}, updates: make(chan ChangeKind, 2),
	}
	live := source.freshnessSignature()
	clock.advance(16 * time.Second)
	priceDelayed := source.freshnessSignature()
	if live == priceDelayed {
		t.Fatal("crossing the 15-second price boundary did not change projection freshness")
	}
	clock.advance(45 * time.Second)
	fundingDelayed := source.freshnessSignature()
	if priceDelayed == fundingDelayed {
		t.Fatal("crossing the 60-second funding boundary did not change projection freshness")
	}
	clock.advance(60 * time.Second)
	expired := source.freshnessSignature()
	if fundingDelayed == expired {
		t.Fatal("crossing the two-minute hard expiry did not change projection freshness")
	}
}

func TestBoardSourceRejectsUnusableConfiguration(t *testing.T) {
	clock := &testClock{now: time.Unix(1720000000, 0).UTC()}
	valid := sourceOptions{interval: 30 * time.Second, maxAge: 2 * time.Minute, venueTimeout: time.Second, now: clock.Now}

	cases := []struct {
		name    string
		factory Factory
		options sourceOptions
	}{
		{name: "nil factory", factory: nil, options: valid},
		{
			// A max age at or below the refresh interval would mark every
			// column unavailable the instant it was written.
			name: "max age does not exceed the refresh interval", factory: &fakeFactory{},
			options: sourceOptions{interval: time.Minute, maxAge: time.Minute, venueTimeout: time.Second, now: clock.Now},
		},
		{
			// Rejecting every profile leaves no columns, which would render as a
			// market-wide outage rather than as the misconfiguration it is.
			name: "factory builds nothing", factory: &fakeFactory{rejectAll: true}, options: valid,
		},
		{name: "no clock", factory: &fakeFactory{}, options: sourceOptions{interval: 30 * time.Second, maxAge: 2 * time.Minute, venueTimeout: time.Second}},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			source, err := newSource(testCase.factory, routeprofile.DefaultRegistry(), testCase.options)
			if source != nil || !errors.Is(err, ErrBoardUnavailable) {
				t.Fatalf("source=%v err=%v", source, err)
			}
		})
	}

	var unbuilt *Source
	if _, err := unbuilt.Snapshot(context.Background()); !errors.Is(err, ErrBoardUnavailable) {
		t.Fatalf("Snapshot on an unbuilt source: %v", err)
	}
	if err := unbuilt.Close(); err != nil {
		t.Fatalf("Close on an unbuilt source: %v", err)
	}

	source, err := newSource(&fakeFactory{}, routeprofile.DefaultRegistry(), valid)
	if err != nil {
		t.Fatalf("newSource: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := source.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
	if _, err := source.Snapshot(context.Background()); !errors.Is(err, ErrBoardUnavailable) {
		t.Fatalf("Snapshot after Close: %v", err)
	}
}

type testClock struct {
	mu  sync.Mutex
	now time.Time
}

func (clock *testClock) Now() time.Time {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	return clock.now
}

func (clock *testClock) advance(delta time.Duration) {
	clock.mu.Lock()
	defer clock.mu.Unlock()
	clock.now = clock.now.Add(delta)
}

type fakeFactory struct {
	rejectAll bool
	gate      <-chan struct{}

	mu       sync.Mutex
	fail     map[string]bool
	everyone bool
	calls    atomic.Int64
}

func (factory *fakeFactory) Build(profile routeprofile.Profile) (Connector, error) {
	if factory.rejectAll {
		return nil, ErrBoardUnavailable
	}
	return &fakeConnector{factory: factory, profile: profile}, nil
}

func (factory *fakeFactory) failing(venue string) bool {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	return factory.everyone || factory.fail[venue]
}

func (factory *fakeFactory) failAll(fail bool) {
	factory.mu.Lock()
	defer factory.mu.Unlock()
	factory.everyone = fail
}

func (factory *fakeFactory) refreshes() int64 { return factory.calls.Load() }

type fakeConnector struct {
	factory *fakeFactory
	profile routeprofile.Profile
}

func (connector *fakeConnector) Refresh(ctx context.Context) (exchange.FundingBoardSnapshot, error) {
	connector.factory.calls.Add(1)
	if connector.factory.gate != nil {
		select {
		case <-ctx.Done():
			return exchange.FundingBoardSnapshot{}, ctx.Err()
		case <-connector.factory.gate:
		}
	}
	if connector.factory.failing(connector.profile.Venue) {
		return exchange.FundingBoardSnapshot{}, errors.New("venue unreachable")
	}
	return exchange.FundingBoardSnapshot{
		SchemaVersion: 1,
		ObservedAt:    time.Unix(1720000000, 0).UTC(),
		Entries: []exchange.FundingBoardEntry{{
			Symbol: "BTC-" + connector.profile.SettlementAsset,
			// Deliberately not derivable from the canonical symbol: a history
			// read that reconstructed these instead of borrowing them from the
			// row would still produce a chart, just of nothing.
			NativeSymbol: "BTC" + connector.profile.SettlementAsset + "-native",
			NativeID:     connector.profile.Venue + "-1",
			BaseAsset:    "BTC", QuoteAsset: connector.profile.SettlementAsset,
			Rate: decimal.RequireFromString("0.0001"),
		}},
	}, nil
}

func (connector *fakeConnector) Close() error { return nil }
