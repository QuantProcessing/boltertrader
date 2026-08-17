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

	snapshot, err := source.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("Snapshot: %v", err)
	}

	// Ten funding-capable perp profiles: eleven perp rows in the profile
	// registry minus aster, which the capability matrix records as publishing no
	// funding at all. Asserting the count here is what keeps a venue from
	// silently falling off the board when a profile row changes.
	if len(snapshot.Venues) != 10 {
		t.Fatalf("got %d venue columns, want 10: %+v", len(snapshot.Venues), snapshot.Venues)
	}
	for _, venue := range snapshot.Venues {
		if venue.Venue == "aster" {
			t.Fatal("aster has no funding surface and must not appear as a column")
		}
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

// TestBoardSourceRefreshCadence covers the three time-dependent transitions in
// one place, because each is only meaningful relative to the others: a read
// inside the interval must not re-poll a dozen venues, a read past it must,
// and a column nobody could refresh must stop being rendered as a rate.
func TestBoardSourceRefreshCadence(t *testing.T) {
	clock := &testClock{now: time.Unix(1720000000, 0).UTC()}
	factory := &fakeFactory{}
	source, err := newSource(factory, routeprofile.DefaultRegistry(), sourceOptions{
		interval: 30 * time.Second, maxAge: 2 * time.Minute, venueTimeout: time.Second, now: clock.Now,
	})
	if err != nil {
		t.Fatalf("newSource: %v", err)
	}
	t.Cleanup(func() { _ = source.Close() })

	if _, err := source.Snapshot(context.Background()); err != nil {
		t.Fatalf("first Snapshot: %v", err)
	}
	first := factory.refreshes()
	if first == 0 {
		t.Fatal("first Snapshot issued no venue refreshes")
	}

	clock.advance(29 * time.Second)
	if _, err := source.Snapshot(context.Background()); err != nil {
		t.Fatalf("cached Snapshot: %v", err)
	}
	if factory.refreshes() != first {
		t.Fatalf("a read inside the refresh interval re-polled the venues: %d -> %d", first, factory.refreshes())
	}

	clock.advance(2 * time.Second)
	snapshot, err := source.Snapshot(context.Background())
	if err != nil {
		t.Fatalf("refreshed Snapshot: %v", err)
	}
	if factory.refreshes() != first*2 {
		t.Fatalf("a read past the refresh interval polled %d venues, want %d", factory.refreshes()-first, first)
	}
	for _, venue := range snapshot.Venues {
		if venue.Stale || venue.Unavailable {
			t.Fatalf("%s: stale=%t unavailable=%t straight after a refresh", venue.Venue, venue.Stale, venue.Unavailable)
		}
	}

	// Every venue now fails, so the last good rates age out. They are withheld
	// rather than served with a stale flag once they pass maxAge: a two-minute
	// old rate beside a fresh one is a spread that has already closed.
	factory.failAll(true)
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

// TestBoardSourceSingleFlight asserts that a burst of readers costs the venues
// one round of calls, not one per reader. Ten venues behind an unthrottled
// board is the shape that gets an IP rate-limited.
func TestBoardSourceSingleFlight(t *testing.T) {
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

	// Let the readers pile up on the single-flight slot before the venues answer.
	time.Sleep(50 * time.Millisecond)
	close(release)
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
		t.Fatalf("%d readers caused %d venue calls, want %d", readers, got, len(snapshots[0].Venues))
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
	if len(seen) != 10 {
		t.Fatalf("got %d distinct markets, want 10: %+v", len(seen), snapshot.Venues)
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
	// Mirrors the real factory's rejection of the one perp venue that publishes
	// no funding, so the column count under test is the production one.
	if profile.Venue == "aster" {
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
