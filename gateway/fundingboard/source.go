// Package fundingboard assembles every funding-capable venue's rate surface
// into one table, without touching a credential or an account.
//
// It is deliberately not part of publicconn. That registry leases catalogs
// keyed by an account's route profile, and its Connector.Refresh answers with
// []exchange.Instrument; a board answers with rates and exists precisely so it
// can be read before any account is configured. The two share a construction
// point in cmd/exchange-gateway and nothing else.
package fundingboard

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	exchangefactory "github.com/QuantProcessing/boltertrader/exchange/factory"
	"github.com/QuantProcessing/boltertrader/routeprofile"
)

var ErrBoardUnavailable = errors.New("funding board unavailable")

// The three ways a history read can fail before a venue is ever called. They
// are separate sentinels because each is a different sentence next to a leg
// that did not draw, and the arbitrage chart puts two legs side by side:
// "this venue publishes no history" (nado, permanently), "this venue does not
// list this instrument" (a pair that only one leg trades), and "that window is
// wider than one call" (a caller asking past the product's two presets). A
// single error would collapse the first two into a blank line that reads as
// "nothing was paid here".
var (
	ErrHistoryUnsupported       = errors.New("funding history unsupported")
	ErrHistoryUnknownInstrument = errors.New("funding history instrument unknown")
	ErrHistoryWindow            = errors.New("funding history window unsupported")
)

const (
	// The polling interval is only a compatibility fallback for factories that
	// do not implement FeedFactory. Production feeds use their venue WebSocket
	// cadence instead.
	defaultRefreshInterval = 30 * time.Second
	defaultMaxAge          = 2 * time.Minute

	// Bounds one venue's call so a hung endpoint costs the board a column
	// rather than the whole table. Every venue answers its bulk funding call in
	// well under this.
	defaultVenueTimeout = 10 * time.Second

	// The widest history window a single call may ask for.
	//
	// The product offers 7d and 30d, and the loaders were sized against those:
	// each is one venue call, or one bounded walk, on every chartable venue.
	// Past a month the venues with row caps start answering short, and a series
	// that silently stops early is a chart claiming funding was never paid. So
	// the request is refused here — the gateway is where the loaders' extent is
	// known — rather than served truncated.
	maxHistorySpan = 31 * 24 * time.Hour
)

// Connector is one venue's funding surface. It matches
// exchangefactory.PublicFundingConnector so tests can substitute a fake without
// a network.
type Connector interface {
	Refresh(context.Context) (exchange.FundingBoardSnapshot, error)
	Close() error
}

// Factory builds the connector for a route profile, or reports that the profile
// has no funding surface at all.
type Factory interface {
	Build(routeprofile.Profile) (Connector, error)
}

// FeedFactory is the realtime capability implemented by the production
// exchange factory. Factory remains the small compatibility boundary used by
// tests and alternate builds; when FeedFactory is absent Source adapts the
// connector into a polling feed owned by the same venue supervisor.
type FeedFactory interface {
	BuildFeed(routeprofile.Profile) (exchange.FundingMarketFeed, error)
}

// HistoryConnector is one venue's settled-funding series for one instrument at
// a time. It matches exchangefactory.PublicFundingHistoryConnector.
type HistoryConnector interface {
	Load(context.Context, exchange.FundingHistoryRequest) (exchange.FundingHistorySnapshot, error)
	Close() error
}

// HistoryFactory is an optional capability of a Factory.
//
// It is a second interface rather than a second method on Factory because
// history is genuinely optional: a Factory that only builds board connectors is
// still a complete Factory, and every column that cannot be charted still has
// to appear on the board. Folding the two together would also make every
// existing test double a compile error for a capability none of them exercise.
type HistoryFactory interface {
	BuildHistory(routeprofile.Profile) (HistoryConnector, error)
}

// HistoryQuery names one instrument through the route and native identity that
// only the Gateway is allowed to know, plus the window.
//
// A struct rather than positional arguments for the same reason
// authorityCandlesQuery is one: three of the fields are strings in a row and
// nothing in the type system would catch a transposition.
type HistoryQuery struct {
	RouteProfileID string
	NativeSymbol   string
	NativeID       string
	Since          time.Time
	Until          time.Time
}

// Snapshot is the whole board: one column per funding-capable route profile.
type Snapshot struct {
	ObservedAt time.Time
	Venues     []VenueSnapshot
}

// VenueSnapshot is one column.
//
// Stale and Unavailable are separate because they mean different things to
// somebody about to trade a spread: stale says "these are the venue's last
// known rates and they are older than the rest of the table", unavailable says
// "we have nothing recent enough to put next to another venue's number". An
// unavailable column carries no entries by design — a three-minute-old rate
// rendered beside fresh ones is an invitation to trade a spread that closed.
type VenueSnapshot struct {
	Venue          string
	Environment    string
	Product        string
	Settlement     string
	RouteProfileID string
	ObservedAt     time.Time
	Stale          bool
	Unavailable    bool
	Status         exchange.FundingMarketFeedState
	LastSuccessAt  time.Time
	LastError      string
	Reconnects     uint64
	Entries        []exchange.FundingBoardEntry
}

type ExchangeFactory struct {
	httpClient *http.Client
}

func NewExchangeFactory(httpClient *http.Client) (*ExchangeFactory, error) {
	if httpClient == nil {
		return nil, ErrBoardUnavailable
	}
	return &ExchangeFactory{httpClient: httpClient}, nil
}

func (factory *ExchangeFactory) Build(profile routeprofile.Profile) (Connector, error) {
	if factory == nil || factory.httpClient == nil {
		return nil, ErrBoardUnavailable
	}
	connector, err := exchangefactory.NewPublicFundingConnector(
		exchange.Venue(profile.Venue),
		exchange.Product(profile.Product),
		profile.SettlementAsset,
		exchangefactory.EnvironmentLive,
		factory.httpClient,
	)
	if err != nil {
		return nil, ErrBoardUnavailable
	}
	return connector, nil
}

func (factory *ExchangeFactory) BuildFeed(profile routeprofile.Profile) (exchange.FundingMarketFeed, error) {
	if factory == nil || factory.httpClient == nil {
		return nil, ErrBoardUnavailable
	}
	feed, err := exchangefactory.NewPublicFundingMarketFeed(
		exchange.Venue(profile.Venue),
		exchange.Product(profile.Product),
		profile.SettlementAsset,
		exchangefactory.EnvironmentLive,
		factory.httpClient,
	)
	if err != nil {
		return nil, ErrBoardUnavailable
	}
	return feed, nil
}

// BuildHistory opens the same profile's history surface, or reports that the
// profile has none.
//
// The error is ErrHistoryUnsupported rather than ErrBoardUnavailable because
// the caller is newSource, which uses it to decide whether a column is
// chartable — and a venue that publishes rates but no history is not a broken
// board, it is Nado.
func (factory *ExchangeFactory) BuildHistory(profile routeprofile.Profile) (HistoryConnector, error) {
	if factory == nil || factory.httpClient == nil {
		return nil, ErrBoardUnavailable
	}
	connector, err := exchangefactory.NewPublicFundingHistoryConnector(
		exchange.Venue(profile.Venue),
		exchange.Product(profile.Product),
		profile.SettlementAsset,
		exchangefactory.EnvironmentLive,
		factory.httpClient,
	)
	if err != nil {
		return nil, ErrHistoryUnsupported
	}
	return connector, nil
}

type Source struct {
	mu           sync.RWMutex
	columns      []*column
	interval     time.Duration
	maxAge       time.Duration
	venueTimeout time.Duration
	now          func() time.Time

	runCancel context.CancelFunc
	runDone   chan struct{}
	running   bool
	closed    bool
	updates   chan ChangeKind
	sequence  uint64
	nextSub   uint64
	subs      map[uint64]chan Change
	observer  Observer
}

// Observer receives bounded per-route lifecycle metrics. Implementations must
// not retain credentials or native payloads; none are passed here.
type Observer interface {
	ObserveFundingMarket(venue, route, state string, lastSuccess time.Time, reconnects uint64)
}

// ChangeKind describes which part of the resident projection changed. Upserts
// carry market facts; venue_status reports lifecycle changes such as a feed
// degrading or reconnecting. The wire layer turns the first Change of either
// kind into a complete snapshot.
type ChangeKind string

const (
	ChangeUpsert      ChangeKind = "upsert"
	ChangeVenueStatus ChangeKind = "venue_status"
)

type Change struct {
	Sequence uint64
	Kind     ChangeKind
}

type column struct {
	profile routeprofile.Profile
	feed    exchange.FundingMarketFeed

	// nil on a venue that publishes rates but no settled series. Set once at
	// construction and never reassigned, so History may read it without the
	// mutex; entries below is the part that changes.
	history HistoryConnector

	// Guarded by Source.mu. Facts are keyed by the stable native identity.
	facts       map[string]exchange.FundingMarketFact
	status      exchange.FundingMarketFeedState
	lastSuccess time.Time
	lastError   string
	reconnects  uint64
}

type sourceOptions struct {
	interval     time.Duration
	maxAge       time.Duration
	venueTimeout time.Duration
	now          func() time.Time
}

// NewSource enumerates the perpetual route profiles and opens one
// credential-free connector per profile that has a funding surface.
//
// A profile the factory rejects is not a column. Every configured perpetual
// venue with a public funding surface, including Aster, is therefore present.
func NewSource(factory Factory, trusted *routeprofile.Registry) (*Source, error) {
	return newSource(factory, trusted, sourceOptions{
		interval:     defaultRefreshInterval,
		maxAge:       defaultMaxAge,
		venueTimeout: defaultVenueTimeout,
		now:          time.Now,
	})
}

func (source *Source) SetObserver(observer Observer) {
	if source == nil {
		return
	}
	type currentObservation struct {
		venue, route   string
		state          exchange.FundingMarketFeedState
		lastSuccess    time.Time
		reconnectCount uint64
	}
	source.mu.Lock()
	source.observer = observer
	observations := make([]currentObservation, 0, len(source.columns))
	for _, current := range source.columns {
		observations = append(observations, currentObservation{
			venue: current.profile.Venue, route: current.profile.ID, state: current.status,
			lastSuccess: current.lastSuccess, reconnectCount: current.reconnects,
		})
	}
	source.mu.Unlock()
	if observer == nil {
		return
	}
	for _, current := range observations {
		observer.ObserveFundingMarket(current.venue, current.route, string(current.state), current.lastSuccess, current.reconnectCount)
	}
}

func newSource(factory Factory, trusted *routeprofile.Registry, options sourceOptions) (*Source, error) {
	if factory == nil {
		return nil, ErrBoardUnavailable
	}
	if trusted == nil {
		trusted = routeprofile.DefaultRegistry()
	}
	if options.interval <= 0 || options.maxAge <= options.interval || options.venueTimeout <= 0 || options.now == nil {
		return nil, ErrBoardUnavailable
	}
	profiles := trusted.Profiles()
	if len(profiles) == 0 {
		return nil, ErrBoardUnavailable
	}
	// One profile is one column. This used to need a per-market claim: bybit was
	// registered under both `demo` and `testnet` and both resolved in test mode,
	// so the unguarded enumeration stood two columns labelled "bybit · USDT"
	// side by side carrying different numbers. The registry is now keyed by a
	// profile ID derived from venue, product and settlement, so two columns
	// naming one market cannot be built in the first place. bybit and bitget
	// still stand two columns each — one per settlement asset, which are
	// different markets.
	columns := make([]*column, 0, len(profiles))
	for _, profile := range profiles {
		if profile.Product != routeprofile.ProductPerp {
			continue
		}
		var feed exchange.FundingMarketFeed
		if feeds, ok := factory.(FeedFactory); ok {
			built, err := feeds.BuildFeed(profile)
			if err != nil || built == nil {
				continue
			}
			feed = built
		} else {
			connector, err := factory.Build(profile)
			if err != nil || connector == nil {
				continue
			}
			feed = newConnectorFeed(profile, connector, options.interval, options.now)
		}
		columns = append(columns, &column{
			profile: profile,
			feed:    feed,
			history: buildHistory(factory, profile),
			facts:   make(map[string]exchange.FundingMarketFact),
			status:  exchange.FundingMarketFeedUnavailable,
		})
	}
	if len(columns) == 0 {
		// The platform has funding venues. None means the profile registry or
		// the factory is misconfigured, and a board that silently served an
		// empty table forever would look like a market-wide outage.
		return nil, ErrBoardUnavailable
	}
	return &Source{
		columns:      columns,
		interval:     options.interval,
		maxAge:       options.maxAge,
		venueTimeout: options.venueTimeout,
		now:          options.now,
		updates:      make(chan ChangeKind, 2),
		subs:         make(map[uint64]chan Change),
	}, nil
}

type connectorFeed struct {
	connector Connector
	scope     exchange.CatalogScope
	interval  time.Duration
	now       func() time.Time
	mu        sync.RWMutex
	status    exchange.FundingMarketFeedStatus
}

func newConnectorFeed(profile routeprofile.Profile, connector Connector, interval time.Duration, now func() time.Time) *connectorFeed {
	return &connectorFeed{
		connector: connector,
		scope: exchange.CatalogScope{
			Venue: exchange.Venue(profile.Venue), Environment: exchange.EnvironmentLive,
			Product: exchange.ProductPerp, Settlement: profile.SettlementAsset,
		},
		interval: interval, now: now,
		status: exchange.FundingMarketFeedStatus{State: exchange.FundingMarketFeedUnavailable},
	}
}

func (feed *connectorFeed) Bootstrap(ctx context.Context) (exchange.FundingMarketPatch, error) {
	snapshot, err := feed.connector.Refresh(ctx)
	if err != nil {
		feed.setError(err)
		return exchange.FundingMarketPatch{}, err
	}
	if snapshot.Scope == (exchange.CatalogScope{}) {
		snapshot.Scope = feed.scope
	}
	if snapshot.ObservedAt.IsZero() {
		snapshot.ObservedAt = feed.now().UTC()
	}
	patch := boardPatch(snapshot)
	feed.mu.Lock()
	feed.status.State = exchange.FundingMarketFeedLive
	feed.status.LastSuccessAt = patch.ObservedAt
	feed.status.LastError = ""
	feed.mu.Unlock()
	return patch, nil
}

func (feed *connectorFeed) Stream(ctx context.Context, emit func(exchange.FundingMarketPatch)) error {
	ticker := time.NewTicker(feed.interval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			patch, err := feed.Bootstrap(ctx)
			if err != nil {
				return err
			}
			emit(patch)
		}
	}
}

func (feed *connectorFeed) Status() exchange.FundingMarketFeedStatus {
	feed.mu.RLock()
	defer feed.mu.RUnlock()
	return feed.status
}

func (feed *connectorFeed) Close() error { return feed.connector.Close() }

func (feed *connectorFeed) setError(err error) {
	feed.mu.Lock()
	if feed.status.LastSuccessAt.IsZero() {
		feed.status.State = exchange.FundingMarketFeedUnavailable
	} else {
		feed.status.State = exchange.FundingMarketFeedDegraded
	}
	if err != nil {
		feed.status.LastError = err.Error()
	}
	feed.mu.Unlock()
}

func boardPatch(snapshot exchange.FundingBoardSnapshot) exchange.FundingMarketPatch {
	facts := make([]exchange.FundingMarketFact, 0, len(snapshot.Entries))
	for _, entry := range snapshot.Entries {
		settlement := snapshot.Scope.Settlement
		if settlement == "" {
			settlement = entry.QuoteAsset
		}
		fact := exchange.FundingMarketFact{
			Instrument: exchange.InstrumentIdentity{
				Venue: snapshot.Scope.Venue, Product: exchange.ProductPerp,
				Symbol: entry.Symbol, NativeSymbol: entry.NativeSymbol, NativeID: entry.NativeID,
				BaseAsset: entry.BaseAsset, QuoteAsset: entry.QuoteAsset, SettleAsset: settlement,
			},
			FundingRate:          exchange.OptionalDecimal{Value: entry.Rate, Valid: true},
			FundingIntervalHours: entry.IntervalHours, NextFundingTime: entry.NextFundingTime,
			MarkPrice: entry.MarkPrice, IndexPrice: entry.IndexPrice,
			OpenInterest: entry.OpenInterest, OpenInterestNotional: entry.OpenInterestNotional,
			OpenInterestNotionalAsset: entry.OpenInterestNotionalAsset,
			FundingObservedAt:         entry.FundingObservedAt, PriceObservedAt: entry.PriceObservedAt,
			OpenInterestObservedAt: entry.OpenInterestObservedAt,
		}
		if fact.FundingObservedAt.IsZero() {
			fact.FundingObservedAt = snapshot.ObservedAt
		}
		if fact.PriceObservedAt.IsZero() && (fact.MarkPrice.Valid || fact.IndexPrice.Valid) {
			fact.PriceObservedAt = snapshot.ObservedAt
		}
		if fact.OpenInterestObservedAt.IsZero() && (fact.OpenInterest.Valid || fact.OpenInterestNotional.Valid) {
			fact.OpenInterestObservedAt = snapshot.ObservedAt
		}
		if fact.OpenInterestNotional.Valid && fact.OpenInterestNotionalAsset == "" {
			fact.OpenInterestNotionalAsset = settlement
		}
		facts = append(facts, fact)
	}
	return exchange.FundingMarketPatch{
		Kind: exchange.FundingMarketPatchSnapshot, Scope: snapshot.Scope,
		ObservedAt: snapshot.ObservedAt, Fields: exchange.FundingMarketFieldAll, Facts: facts,
	}
}

// buildHistory opens a column's history surface when the factory has one to
// give, and returns nil otherwise.
//
// Every outcome here is nil rather than an error on purpose: the column is
// already built, and a venue whose series cannot be opened still belongs on the
// board. The absence surfaces later as ErrHistoryUnsupported, next to the leg
// that did draw.
func buildHistory(factory Factory, profile routeprofile.Profile) HistoryConnector {
	histories, ok := factory.(HistoryFactory)
	if !ok {
		return nil
	}
	history, err := histories.BuildHistory(profile)
	if err != nil || history == nil {
		return nil
	}
	return history
}

// Snapshot is a memory-only read. Venue I/O belongs exclusively to Run, so the
// number of HTTP callers cannot change the number or cadence of exchange
// requests.
func (source *Source) Snapshot(ctx context.Context) (Snapshot, error) {
	if source == nil || ctx == nil || ctx.Err() != nil {
		return Snapshot{}, ErrBoardUnavailable
	}
	source.mu.RLock()
	defer source.mu.RUnlock()
	if source.closed {
		return Snapshot{}, ErrBoardUnavailable
	}
	return source.projectLocked(), nil
}

// Ready reports whether at least one route profile currently contributes a
// fresh strategy fact. Funding, price and OI age independently; the strategy
// layer applies its own field-specific requirements later.
func (source *Source) Ready() bool {
	if source == nil {
		return false
	}
	source.mu.RLock()
	defer source.mu.RUnlock()
	if source.closed {
		return false
	}
	now := source.clock()
	for _, current := range source.columns {
		if current.lastSuccess.IsZero() || now.Sub(current.lastSuccess) > source.maxAge {
			continue
		}
		for _, fact := range current.facts {
			if fundingFactFresh(fact, now, source.maxAge) || priceFactFresh(fact, now, source.maxAge) || openInterestFactFresh(fact, now, source.maxAge) {
				return true
			}
		}
	}
	return false
}

// Run owns every public feed until ctx ends. A failed venue is retried by its
// one supervisor and never prevents another venue from becoming live.
func (source *Source) Run(ctx context.Context) error {
	if source == nil || ctx == nil || ctx.Err() != nil {
		return ErrBoardUnavailable
	}
	source.mu.Lock()
	if source.closed || source.running {
		source.mu.Unlock()
		return ErrBoardUnavailable
	}
	runCtx, cancel := context.WithCancel(ctx)
	source.runCancel = cancel
	source.runDone = make(chan struct{})
	source.running = true
	targets := append([]*column(nil), source.columns...)
	done := source.runDone
	source.mu.Unlock()
	defer func() {
		source.mu.Lock()
		if source.runDone == done {
			close(done)
			source.runDone = nil
			source.running = false
			source.runCancel = nil
		}
		source.mu.Unlock()
	}()

	source.bootstrapAll(runCtx, targets)
	var group sync.WaitGroup
	group.Add(1)
	go func() {
		defer group.Done()
		source.publishLoop(runCtx)
	}()
	for _, venueColumns := range groupColumnsByVenue(targets) {
		group.Add(1)
		go func(venueColumns []*column) {
			defer group.Done()
			source.superviseVenue(runCtx, venueColumns)
		}(venueColumns)
	}
	group.Wait()
	if ctx.Err() != nil {
		return ctx.Err()
	}
	return nil
}

// Subscribe reports resident projection changes. The first sequence always
// means "send a complete snapshot"; subsequent sequences mean "upsert from
// the current in-memory projection". A closed channel requires a resnapshot.
func (source *Source) Subscribe(ctx context.Context) (<-chan Change, func(), error) {
	if source == nil || ctx == nil || ctx.Err() != nil {
		return nil, nil, ErrBoardUnavailable
	}
	source.mu.Lock()
	if source.closed {
		source.mu.Unlock()
		return nil, nil, ErrBoardUnavailable
	}
	source.nextSub++
	id := source.nextSub
	channel := make(chan Change, 2)
	source.subs[id] = channel
	channel <- Change{Sequence: source.sequence, Kind: ChangeUpsert}
	source.mu.Unlock()

	subscriptionCtx, stop := context.WithCancel(ctx)
	var once sync.Once
	cancel := func() {
		once.Do(func() {
			stop()
			source.mu.Lock()
			if current, ok := source.subs[id]; ok {
				delete(source.subs, id)
				close(current)
			}
			source.mu.Unlock()
		})
	}
	go func() {
		<-subscriptionCtx.Done()
		cancel()
	}()
	return channel, cancel, nil
}

func (source *Source) signalUpdate(kind ChangeKind) {
	select {
	case source.updates <- kind:
	default:
	}
}

func (source *Source) publishLoop(ctx context.Context) {
	ticker := time.NewTicker(time.Second)
	defer ticker.Stop()
	pending := map[ChangeKind]bool{}
	freshness := source.freshnessSignature()
	for {
		select {
		case <-ctx.Done():
			return
		case kind := <-source.updates:
			pending[kind] = true
		case <-ticker.C:
			currentFreshness := source.freshnessSignature()
			if currentFreshness != freshness {
				pending[ChangeVenueStatus] = true
				freshness = currentFreshness
			}
			if len(pending) == 0 {
				continue
			}
			source.mu.Lock()
			for _, kind := range []ChangeKind{ChangeVenueStatus, ChangeUpsert} {
				if !pending[kind] {
					continue
				}
				source.sequence++
				change := Change{Sequence: source.sequence, Kind: kind}
				for id, subscriber := range source.subs {
					select {
					case subscriber <- change:
					default:
						close(subscriber)
						delete(source.subs, id)
					}
				}
			}
			source.mu.Unlock()
			clear(pending)
		}
	}
}

// freshnessSignature changes only when a projected field crosses one of the
// public freshness boundaries. It lets a silent-but-open socket age from live
// to degraded/unavailable in Trading without a periodic exchange request or a
// fixed-rate stream of unchanged board snapshots.
func (source *Source) freshnessSignature() uint64 {
	const offset64 = uint64(14695981039346656037)
	const prime64 = uint64(1099511628211)
	mix := func(signature, value uint64) uint64 { return (signature ^ value) * prime64 }

	source.mu.RLock()
	defer source.mu.RUnlock()
	now := source.clock()
	signature := offset64
	for _, current := range source.columns {
		var available, fundingFresh, fundingDelayed, priceFresh, priceDelayed, openInterestFresh uint64
		if !current.lastSuccess.IsZero() && now.Sub(current.lastSuccess) <= source.maxAge {
			available = 1
		}
		for _, fact := range current.facts {
			if fundingFactFresh(fact, now, source.maxAge) {
				if now.Sub(fact.FundingObservedAt) > time.Minute {
					fundingDelayed++
				} else {
					fundingFresh++
				}
			}
			if priceFactFresh(fact, now, source.maxAge) {
				if now.Sub(fact.PriceObservedAt) > 15*time.Second {
					priceDelayed++
				} else {
					priceFresh++
				}
			}
			if openInterestFactFresh(fact, now, source.maxAge) {
				openInterestFresh++
			}
		}
		for _, value := range []uint64{available, fundingFresh, fundingDelayed, priceFresh, priceDelayed, openInterestFresh} {
			signature = mix(signature, value)
		}
	}
	return signature
}

// bootstrapAll is kept separate so offline tests can prove startup behavior
// deterministically without starting long-lived socket loops.
func (source *Source) bootstrapAll(ctx context.Context, targets []*column) {
	var group sync.WaitGroup
	for _, current := range targets {
		group.Add(1)
		go func(current *column) {
			defer group.Done()
			venueCtx, cancel := context.WithTimeout(ctx, source.venueTimeout)
			patch, err := current.feed.Bootstrap(venueCtx)
			cancel()
			if err != nil {
				source.recordFailure(current, err, false)
				return
			}
			source.apply(current, patch)
		}(current)
	}
	group.Wait()
}

// groupColumnsByVenue creates the lifecycle ownership boundary. A venue may
// still need multiple sockets because settlement markets or exchange limits
// require sharding, but those sockets are started and re-snapshotted by one
// supervisor rather than by independent route refresh tasks.
func groupColumnsByVenue(columns []*column) [][]*column {
	byVenue := make(map[string][]*column)
	for _, current := range columns {
		if current == nil {
			continue
		}
		byVenue[current.profile.Venue] = append(byVenue[current.profile.Venue], current)
	}
	venues := make([]string, 0, len(byVenue))
	for venue := range byVenue {
		venues = append(venues, venue)
	}
	sort.Strings(venues)
	groups := make([][]*column, 0, len(venues))
	for _, venue := range venues {
		group := byVenue[venue]
		sort.Slice(group, func(left, right int) bool {
			return group[left].profile.ID < group[right].profile.ID
		})
		groups = append(groups, group)
	}
	return groups
}

func (source *Source) superviseVenue(ctx context.Context, columns []*column) {
	if len(columns) == 0 {
		return
	}
	backoff := time.Second
	resnapshot := source.venueNeedsResnapshot(columns)
	for ctx.Err() == nil {
		if resnapshot {
			if !source.bootstrapVenue(ctx, columns) {
				if !waitFundingReconnect(ctx, backoff) {
					return
				}
				backoff = nextFundingBackoff(backoff)
				continue
			}
			resnapshot = false
		}
		err := source.streamVenue(ctx, columns)
		if ctx.Err() != nil {
			return
		}
		if err == nil {
			err = errors.New("funding market stream ended")
		}
		for _, current := range columns {
			source.recordFailure(current, err, true)
		}
		resnapshot = true
		if !waitFundingReconnect(ctx, backoff) {
			return
		}
		backoff = nextFundingBackoff(backoff)
	}
}

func (source *Source) venueNeedsResnapshot(columns []*column) bool {
	source.mu.RLock()
	defer source.mu.RUnlock()
	for _, current := range columns {
		if current.lastSuccess.IsZero() {
			return true
		}
	}
	return false
}

// bootstrapVenue rebuilds every shard before any delta stream resumes. This
// is intentionally all-or-nothing at the venue lifecycle boundary: a partial
// baseline followed by incremental updates is indistinguishable from clearing
// fields the venue simply did not repeat.
func (source *Source) bootstrapVenue(ctx context.Context, columns []*column) bool {
	results := make(chan bool, len(columns))
	for _, current := range columns {
		go func(current *column) {
			venueCtx, cancel := context.WithTimeout(ctx, source.venueTimeout)
			patch, err := current.feed.Bootstrap(venueCtx)
			cancel()
			if err != nil {
				source.recordFailure(current, err, true)
				results <- false
				return
			}
			source.apply(current, patch)
			results <- true
		}(current)
	}
	complete := true
	for range columns {
		complete = <-results && complete
	}
	return complete
}

type venueStreamResult struct {
	column *column
	err    error
}

// streamVenue starts the exchange-required socket shards as one supervised
// attempt. If any shard ends, the attempt is cancelled and the supervisor
// re-establishes a complete REST baseline before opening fresh streams.
func (source *Source) streamVenue(ctx context.Context, columns []*column) error {
	attemptCtx, cancel := context.WithCancel(ctx)
	results := make(chan venueStreamResult, len(columns))
	for _, current := range columns {
		go func(current *column) {
			err := current.feed.Stream(attemptCtx, func(patch exchange.FundingMarketPatch) {
				source.apply(current, patch)
			})
			results <- venueStreamResult{column: current, err: err}
		}(current)
	}
	first := <-results
	cancel()
	for remaining := 1; remaining < len(columns); remaining++ {
		<-results
	}
	if first.err == nil {
		return errors.New("funding market stream ended for " + first.column.profile.ID)
	}
	return first.err
}

func waitFundingReconnect(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextFundingBackoff(current time.Duration) time.Duration {
	if current >= 30*time.Second {
		return 30 * time.Second
	}
	current *= 2
	if current > 30*time.Second {
		return 30 * time.Second
	}
	return current
}

func (source *Source) recordFailure(current *column, err error, reconnect bool) {
	source.mu.Lock()
	if source.closed {
		source.mu.Unlock()
		return
	}
	if current.lastSuccess.IsZero() {
		current.status = exchange.FundingMarketFeedUnavailable
	} else {
		current.status = exchange.FundingMarketFeedDegraded
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		current.lastError = err.Error()
	}
	if reconnect {
		current.reconnects++
	}
	observer := source.observer
	state, lastSuccess, reconnects := current.status, current.lastSuccess, current.reconnects
	source.mu.Unlock()
	if observer != nil {
		observer.ObserveFundingMarket(current.profile.Venue, current.profile.ID, string(state), lastSuccess, reconnects)
	}
	if err != nil && !errors.Is(err, context.Canceled) {
		slog.Warn("funding market feed degraded",
			"venue", current.profile.Venue, "route_profile", current.profile.ID,
			"reconnect", reconnect, "error", err)
	}
	source.signalUpdate(ChangeVenueStatus)
}

func (source *Source) apply(current *column, patch exchange.FundingMarketPatch) {
	if err := patch.Validate(); err != nil {
		source.recordFailure(current, err, false)
		return
	}
	if patch.Scope.Venue != exchange.Venue(current.profile.Venue) ||
		patch.Scope.Product != exchange.Product(current.profile.Product) ||
		patch.Scope.Settlement != current.profile.SettlementAsset {
		source.recordFailure(current, errors.New("funding market patch scope mismatch"), false)
		return
	}
	source.mu.Lock()
	if source.closed {
		source.mu.Unlock()
		return
	}
	if patch.Kind == exchange.FundingMarketPatchSnapshot {
		current.facts = make(map[string]exchange.FundingMarketFact, len(patch.Facts))
	}
	for _, incoming := range patch.Facts {
		key := incoming.Key()
		fact := current.facts[key]
		mergeFundingFact(&fact, incoming, patch.Fields, patch.ObservedAt)
		current.facts[key] = fact
	}
	current.status = exchange.FundingMarketFeedLive
	if current.lastSuccess.IsZero() || patch.ObservedAt.UTC().After(current.lastSuccess) {
		current.lastSuccess = patch.ObservedAt.UTC()
	}
	current.lastError = ""
	observer := source.observer
	state, lastSuccess, reconnects := current.status, current.lastSuccess, current.reconnects
	source.mu.Unlock()
	if observer != nil {
		observer.ObserveFundingMarket(current.profile.Venue, current.profile.ID, string(state), lastSuccess, reconnects)
	}
	source.signalUpdate(ChangeUpsert)
}

func mergeFundingFact(target *exchange.FundingMarketFact, incoming exchange.FundingMarketFact, fields exchange.FundingMarketField, observedAt time.Time) {
	if fields.Has(exchange.FundingMarketFieldIdentity) || target.Instrument.NativeSymbol == "" {
		target.Instrument = incoming.Instrument
	}
	fundingObservedAt := observationOr(incoming.FundingObservedAt, observedAt)
	fundingCurrent := target.FundingObservedAt.IsZero() || !fundingObservedAt.Before(target.FundingObservedAt)
	if fields.Has(exchange.FundingMarketFieldFunding) && fundingCurrent {
		target.FundingRate = incoming.FundingRate
		target.FundingObservedAt = fundingObservedAt
	}
	if fields.Has(exchange.FundingMarketFieldFundingInterval) && fundingCurrent {
		target.FundingIntervalHours = incoming.FundingIntervalHours
		if target.FundingObservedAt.IsZero() {
			target.FundingObservedAt = fundingObservedAt
		}
	}
	if fields.Has(exchange.FundingMarketFieldNextFundingTime) && fundingCurrent {
		target.NextFundingTime = incoming.NextFundingTime
	}
	priceObservedAt := observationOr(incoming.PriceObservedAt, observedAt)
	priceCurrent := target.PriceObservedAt.IsZero() || !priceObservedAt.Before(target.PriceObservedAt)
	if fields.Has(exchange.FundingMarketFieldMarkPrice) && priceCurrent {
		target.MarkPrice = incoming.MarkPrice
		target.PriceObservedAt = priceObservedAt
	}
	if fields.Has(exchange.FundingMarketFieldIndexPrice) && priceCurrent {
		target.IndexPrice = incoming.IndexPrice
		target.PriceObservedAt = priceObservedAt
	}
	openInterestObservedAt := observationOr(incoming.OpenInterestObservedAt, observedAt)
	openInterestCurrent := target.OpenInterestObservedAt.IsZero() || !openInterestObservedAt.Before(target.OpenInterestObservedAt)
	if fields.Has(exchange.FundingMarketFieldOpenInterest) && openInterestCurrent {
		target.OpenInterest = incoming.OpenInterest
		target.OpenInterestObservedAt = openInterestObservedAt
	}
	if fields.Has(exchange.FundingMarketFieldOpenInterestNotional) && openInterestCurrent {
		target.OpenInterestNotional = incoming.OpenInterestNotional
		target.OpenInterestNotionalAsset = incoming.OpenInterestNotionalAsset
		target.OpenInterestObservedAt = openInterestObservedAt
	}
}

func observationOr(candidate, fallback time.Time) time.Time {
	if !candidate.IsZero() {
		return candidate.UTC()
	}
	return fallback.UTC()
}

// History returns one instrument's settled funding series from one charted
// venue.
//
// The canonical-to-native translation happens here and nowhere above. Every
// venue's history endpoint is addressed by its own identifier — BTCUSDT, an
// integer market id, a bare coin — while a caller comparing two legs of a
// spread only knows BTC-USDT. The board already holds that mapping on the row
// it refreshed, so the read borrows it instead of standing up a second resolver
// that could disagree with the table the user is looking at.
//
// This deliberately does not touch the refresh clock or the single-flight slot.
// A chart being opened is not a reason to age the board, and the board's cadence
// is not a reason to make a chart wait.
func (source *Source) History(ctx context.Context, query HistoryQuery) (exchange.FundingHistorySnapshot, error) {
	if source == nil || ctx == nil || ctx.Err() != nil {
		return exchange.FundingHistorySnapshot{}, ErrBoardUnavailable
	}
	if query.RouteProfileID == "" || (query.NativeSymbol == "" && query.NativeID == "") {
		return exchange.FundingHistorySnapshot{}, ErrHistoryUnsupported
	}
	if query.Since.IsZero() || !query.Until.After(query.Since) || query.Until.Sub(query.Since) > maxHistorySpan {
		return exchange.FundingHistorySnapshot{}, ErrHistoryWindow
	}

	target, entry, _, err := source.resolveHistory(query)
	if err != nil {
		return exchange.FundingHistorySnapshot{}, err
	}

	venueCtx, cancel := context.WithTimeout(ctx, source.venueTimeout)
	defer cancel()
	return target.history.Load(venueCtx, exchange.FundingHistoryRequest{
		Symbol:       entry.Symbol,
		NativeSymbol: entry.NativeSymbol,
		NativeID:     entry.NativeID,
		Since:        query.Since,
		Until:        query.Until,
	})
}

// resolveHistory finds the exact charted route and its native board row.
//
// warm reports whether every candidate column has been read at least once. It
// is returned separately from the error because "the board has not loaded this
// venue yet" and "this venue does not list this instrument" arrive at the same
// place and only the first is worth a refresh.
func (source *Source) resolveHistory(query HistoryQuery) (*column, exchange.FundingBoardEntry, bool, error) {
	var missing exchange.FundingBoardEntry

	source.mu.Lock()
	defer source.mu.Unlock()
	if source.closed {
		return nil, missing, false, ErrBoardUnavailable
	}
	var matched bool
	for _, current := range source.columns {
		if current.profile.ID != query.RouteProfileID {
			continue
		}
		matched = true
		if current.history == nil {
			return nil, missing, true, ErrHistoryUnsupported
		}
		if len(current.facts) == 0 {
			return nil, missing, false, ErrHistoryUnknownInstrument
		}
		for _, fact := range current.facts {
			candidate := fundingBoardEntry(fact)
			if query.NativeID != "" && candidate.NativeID != "" {
				if candidate.NativeID == query.NativeID {
					return current, candidate, true, nil
				}
				continue
			}
			if query.NativeSymbol != "" && candidate.NativeSymbol == query.NativeSymbol {
				return current, candidate, true, nil
			}
		}
		return nil, missing, true, ErrHistoryUnknownInstrument
	}
	if !matched {
		return nil, missing, true, ErrHistoryUnsupported
	}
	return nil, missing, true, ErrHistoryUnknownInstrument
}

func (source *Source) projectLocked() Snapshot {
	observedAt := source.clock()
	venues := make([]VenueSnapshot, 0, len(source.columns))
	for _, current := range source.columns {
		venue := VenueSnapshot{
			Venue:          current.profile.Venue,
			Environment:    string(exchange.EnvironmentLive),
			Product:        string(current.profile.Product),
			Settlement:     current.profile.SettlementAsset,
			RouteProfileID: current.profile.ID,
			Status:         current.status,
			LastSuccessAt:  current.lastSuccess,
			LastError:      current.lastError,
			Reconnects:     current.reconnects,
		}
		if current.lastSuccess.IsZero() || observedAt.Sub(current.lastSuccess) > source.maxAge {
			venue.Unavailable = true
			venue.Status = exchange.FundingMarketFeedUnavailable
			venues = append(venues, venue)
			continue
		}
		venue.ObservedAt = current.lastSuccess
		for _, fact := range current.facts {
			fundingFresh := fundingFactFresh(fact, observedAt, source.maxAge)
			priceFresh := priceFactFresh(fact, observedAt, source.maxAge)
			openInterestFresh := openInterestFactFresh(fact, observedAt, source.maxAge)
			if !fundingFresh && !priceFresh && !openInterestFresh {
				continue
			}
			projected := fact
			if !fundingFresh {
				projected.FundingRate = exchange.OptionalDecimal{}
				projected.FundingIntervalHours = exchange.OptionalInt{}
				projected.NextFundingTime = time.Time{}
				projected.FundingObservedAt = time.Time{}
			}
			if !priceFresh {
				projected.MarkPrice = exchange.OptionalDecimal{}
				projected.IndexPrice = exchange.OptionalDecimal{}
				projected.PriceObservedAt = time.Time{}
			}
			if !openInterestFresh {
				projected.OpenInterest = exchange.OptionalDecimal{}
				projected.OpenInterestNotional = exchange.OptionalDecimal{}
				projected.OpenInterestNotionalAsset = ""
				projected.OpenInterestObservedAt = time.Time{}
			}
			if (fundingFresh && observedAt.Sub(projected.FundingObservedAt) > time.Minute) ||
				(priceFresh && observedAt.Sub(projected.PriceObservedAt) > 15*time.Second) {
				venue.Stale = true
			}
			venue.Entries = append(venue.Entries, fundingBoardEntry(projected))
		}
		if venue.Stale && venue.Status == exchange.FundingMarketFeedLive {
			venue.Status = exchange.FundingMarketFeedDegraded
		}
		sort.Slice(venue.Entries, func(left, right int) bool {
			return venue.Entries[left].Symbol < venue.Entries[right].Symbol
		})
		if len(venue.Entries) == 0 {
			venue.ObservedAt = time.Time{}
			venue.Unavailable = true
			venue.Status = exchange.FundingMarketFeedUnavailable
		}
		venues = append(venues, venue)
	}
	return Snapshot{ObservedAt: observedAt, Venues: venues}
}

func fundingFactFresh(fact exchange.FundingMarketFact, now time.Time, maxAge time.Duration) bool {
	return fact.FundingRate.Valid && !fact.FundingObservedAt.IsZero() && now.Sub(fact.FundingObservedAt) <= maxAge
}

func priceFactFresh(fact exchange.FundingMarketFact, now time.Time, maxAge time.Duration) bool {
	return (fact.MarkPrice.Valid || fact.IndexPrice.Valid) && !fact.PriceObservedAt.IsZero() && now.Sub(fact.PriceObservedAt) <= maxAge
}

func openInterestFactFresh(fact exchange.FundingMarketFact, now time.Time, maxAge time.Duration) bool {
	return (fact.OpenInterest.Valid || fact.OpenInterestNotional.Valid) && !fact.OpenInterestObservedAt.IsZero() && now.Sub(fact.OpenInterestObservedAt) <= maxAge
}

func fundingBoardEntry(fact exchange.FundingMarketFact) exchange.FundingBoardEntry {
	return exchange.FundingBoardEntry{
		Symbol: fact.Instrument.Symbol, NativeSymbol: fact.Instrument.NativeSymbol,
		NativeID: fact.Instrument.NativeID, BaseAsset: fact.Instrument.BaseAsset,
		QuoteAsset: fact.Instrument.QuoteAsset, Rate: fact.FundingRate.Value, RateValid: fact.FundingRate.Valid,
		IntervalHours: fact.FundingIntervalHours, NextFundingTime: fact.NextFundingTime,
		MarkPrice: fact.MarkPrice, IndexPrice: fact.IndexPrice,
		OpenInterest: fact.OpenInterest, OpenInterestNotional: fact.OpenInterestNotional,
		OpenInterestNotionalAsset: fact.OpenInterestNotionalAsset,
		FundingObservedAt:         fact.FundingObservedAt, PriceObservedAt: fact.PriceObservedAt,
		OpenInterestObservedAt: fact.OpenInterestObservedAt,
	}
}

func (source *Source) clock() time.Time {
	return source.now().UTC()
}

// Close releases every connector. It is idempotent so a shutdown path may call
// it alongside a deferred close without double-closing a venue.
func (source *Source) Close() error {
	if source == nil {
		return nil
	}
	source.mu.Lock()
	if source.closed {
		source.mu.Unlock()
		return nil
	}
	source.closed = true
	cancel := source.runCancel
	done := source.runDone
	columns := source.columns
	for id, subscriber := range source.subs {
		close(subscriber)
		delete(source.subs, id)
	}
	source.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}

	var closeErr error
	for _, current := range columns {
		if err := current.feed.Close(); err != nil {
			closeErr = errors.Join(closeErr, ErrBoardUnavailable)
		}
		if current.history == nil {
			continue
		}
		if err := current.history.Close(); err != nil {
			closeErr = errors.Join(closeErr, ErrBoardUnavailable)
		}
	}
	return closeErr
}
