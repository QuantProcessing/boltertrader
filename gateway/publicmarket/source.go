// Package publicmarket owns Gateway's account-independent market-data plane.
// Every connector in this package is credential-free and read-only.
package publicmarket

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	exchangefactory "github.com/QuantProcessing/boltertrader/exchange/factory"
	"github.com/QuantProcessing/boltertrader/gateway/catalog"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/boltertrader/gateway/marketcapability"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
	"github.com/QuantProcessing/boltertrader/gateway/ratelimit"
	"github.com/QuantProcessing/boltertrader/routeprofile"
	"github.com/shopspring/decimal"
)

var ErrUnavailable = errors.New("public market unavailable")

const (
	defaultRefreshInterval = time.Minute
	defaultMaxAge          = 2 * time.Minute
	defaultVenueTimeout    = 30 * time.Second
)

// Connector contains only the capabilities used by Gateway public routes.
// Its socket exposes market streams without account or order methods.
type Connector interface {
	Refresh(context.Context) ([]exchange.Instrument, error)
	Candles(context.Context, exchange.CandlesRequest) (exchange.CandlePage, error)
	WebSocket() exchange.MarketWebSocket
	Close() error
}

type Factory interface {
	Build(routeprofile.Profile) (Connector, error)
}

type ExchangeFactory struct {
	httpClient *http.Client
}

func NewExchangeFactory(httpClient *http.Client) (*ExchangeFactory, error) {
	if httpClient == nil {
		return nil, ErrUnavailable
	}
	return &ExchangeFactory{httpClient: httpClient}, nil
}

func (factory *ExchangeFactory) Build(profile routeprofile.Profile) (Connector, error) {
	if factory == nil || factory.httpClient == nil {
		return nil, ErrUnavailable
	}
	connector, err := exchangefactory.NewPublicMarketConnector(
		exchange.Venue(profile.Venue), exchange.Product(profile.Product), profile.SettlementAsset,
		exchangefactory.EnvironmentLive, factory.httpClient,
	)
	if err != nil {
		return nil, ErrUnavailable
	}
	return connector, nil
}

type Instrument struct {
	RouteProfileID string
	Catalog        catalog.Instrument
	Canonical      exchange.Instrument
	Ready          bool
}

type Snapshot struct {
	Revision    string
	ObservedAt  time.Time
	Instruments []Instrument
	// UnavailableProfiles identifies omitted catalog scopes. A partial snapshot
	// must never imply that instruments from a failed venue have been delisted.
	UnavailableProfiles []string
}

type Query struct {
	Venue        string
	Environment  string
	Product      string
	InstrumentID string
}

type Fact struct {
	Query      Query
	Instrument exchange.Instrument
	Book       exchange.OrderBook
	Mark       exchange.MarkPriceEvent
	HasMark    bool
}

type CandleResult struct {
	Query      Query
	Instrument exchange.Instrument
	Page       exchange.CandlePage
}

// OrderBookStream is bootstrapped exclusively from the venue WebSocket. The
// Exchange layer replays its current bounded snapshot when a logical consumer
// joins an already shared native topic, so Gateway never joins REST depth to a
// different WS sequence.
type OrderBookStream struct {
	Instrument   exchange.Instrument
	Snapshot     exchange.OrderBook
	Subscription exchange.Subscription[exchange.BookEvent]
	Replay       func() (exchange.OrderBook, bool)
}

// CandleStream carries the venue's native forming-bar updates. Historical bars
// remain a bounded REST read; only the newest bar belongs on this stream.
type CandleStream struct {
	Instrument   exchange.Instrument
	Snapshot     exchange.CandlePage
	Subscription exchange.Subscription[exchange.CandleEvent]
}

type BBOStream struct {
	Instrument   exchange.Instrument
	Snapshot     exchange.BBOEvent
	Subscription exchange.Subscription[exchange.BBOEvent]
}

// SubscriptionKey resolves a canonical instrument to the venue's native
// connection boundary. Route profiles are projections; Bybit USDT and USDC,
// for example, both resolve to the single native linear owner.
func (source *Source) SubscriptionKey(ctx context.Context, instrumentID string, dataset exchange.MarketDataset, interval string, depth int) (markethub.SubscriptionKey, error) {
	current, _, err := source.resolvePublicID(ctx, instrumentID)
	if err != nil {
		return markethub.SubscriptionKey{}, err
	}
	key := markethub.SubscriptionKey{
		Owner:   markethub.OwnerKey{Venue: exchange.Venue(current.profile.Venue), Environment: exchange.EnvironmentLive, NativeFamily: nativeMarketFamily(current.profile)},
		Dataset: dataset, InstrumentID: instrumentID, Interval: strings.TrimSpace(interval), Depth: depth,
	}
	if err := key.Validate(); err != nil {
		return markethub.SubscriptionKey{}, ErrUnavailable
	}
	return key, nil
}

func nativeMarketFamily(profile routeprofile.Profile) string {
	return exchange.NativeMarketFamily(exchange.Venue(profile.Venue), exchange.Product(profile.Product), profile.SettlementAsset)
}

func observedTime(value time.Time) time.Time {
	if value.IsZero() {
		return time.Now().UTC()
	}
	return value.UTC()
}

// MarkPriceStream is bootstrapped by the public perpetual WebSocket. It is
// credential-free: the account-specific position remains on the private plane
// while every viewer may share the same mark.
type MarkPriceStream struct {
	Instrument   exchange.Instrument
	Snapshot     exchange.MarkPriceEvent
	Subscription exchange.Subscription[exchange.MarkPriceEvent]
}

// DerivativeReferenceStream preserves rich native ticker fields for the
// Gateway Feed Owner while retaining an immediate mark snapshot for the public
// mark topic.
type DerivativeReferenceStream struct {
	Instrument   exchange.Instrument
	Snapshot     exchange.DerivativeReferenceEvent
	Subscription exchange.Subscription[exchange.DerivativeReferenceEvent]
}

type Source struct {
	columns      []*column
	sockets      map[markethub.OwnerKey]exchange.MarketWebSocket
	interval     time.Duration
	venueTimeout time.Duration
	now          func() time.Time

	mu              sync.Mutex
	marketHub       *markethub.Hub
	derivativeMu    sync.RWMutex
	derivativeIndex map[string]map[string]exchange.DerivativeFact
	closed          bool
	running         bool
	runCancel       context.CancelFunc
	runDone         chan struct{}
}

// ResolveDerivativeInstrument implements exchange.DerivativeInstrumentResolver
// from the resident catalog index. Catalog publication rebuilds one route's
// index atomically, so projecting an all-market venue event is O(1) per row and
// performs no venue I/O or whole-directory scans.
func (source *Source) ResolveDerivativeInstrument(ctx context.Context, routeProfileID, nativeSymbol, nativeID string) (exchange.DerivativeFact, bool) {
	if source == nil || ctx == nil || ctx.Err() != nil {
		return exchange.DerivativeFact{}, false
	}
	source.derivativeMu.RLock()
	index := source.derivativeIndex[strings.TrimSpace(routeProfileID)]
	if nativeID = strings.TrimSpace(nativeID); nativeID != "" {
		if fact, ok := index[derivativeNativeKey("id", nativeID)]; ok {
			source.derivativeMu.RUnlock()
			return fact, true
		}
	}
	if nativeSymbol = strings.TrimSpace(nativeSymbol); nativeSymbol != "" {
		if fact, ok := index[derivativeNativeKey("symbol", nativeSymbol)]; ok {
			source.derivativeMu.RUnlock()
			return fact, true
		}
	}
	source.derivativeMu.RUnlock()
	return exchange.DerivativeFact{}, false
}

func derivativeNativeKey(kind, value string) string { return kind + "\x00" + value }

func (source *Source) indexDerivativeCatalog(profile routeprofile.Profile, instruments []exchange.Instrument) error {
	if source == nil || profile.Product != routeprofile.ProductPerp {
		return nil
	}
	index := make(map[string]exchange.DerivativeFact, len(instruments)*2)
	for _, instrument := range instruments {
		identity := instrument.Identity
		instrumentID, err := domain.NewInstrumentID(
			profile.Venue, string(exchange.EnvironmentLive), string(profile.Product), profile.SettlementAsset, identity.Symbol,
		)
		if err != nil {
			return fmt.Errorf("index derivative catalog %s: %w", profile.ID, err)
		}
		fact := exchange.DerivativeFact{
			InstrumentID: string(instrumentID), RouteProfileID: profile.ID,
			Instrument: identity, Environment: exchange.EnvironmentLive,
		}
		keys := make([]string, 0, 2)
		if identity.NativeID != "" {
			keys = append(keys, derivativeNativeKey("id", identity.NativeID))
		}
		if identity.NativeSymbol != "" {
			keys = append(keys, derivativeNativeKey("symbol", identity.NativeSymbol))
		}
		for _, key := range keys {
			if existing, duplicate := index[key]; duplicate && existing.InstrumentID != fact.InstrumentID {
				return fmt.Errorf("index derivative catalog %s: duplicate native identity", profile.ID)
			}
			index[key] = fact
		}
	}
	source.derivativeMu.Lock()
	source.derivativeIndex[profile.ID] = index
	source.derivativeMu.Unlock()
	return nil
}

// SetMarketHub attaches the only realtime market-data plane. Catalog, candle
// history and funding history remain bounded REST reads, but depth and mark
// snapshots never fall back to a second venue request path.
func (source *Source) SetMarketHub(hub *markethub.Hub) {
	if source == nil {
		return
	}
	source.mu.Lock()
	source.marketHub = hub
	source.mu.Unlock()
}

// SetRequestCoordinator attaches the single process-wide public REST gate to
// catalog refreshes and point-in-time market bootstraps.
func (source *Source) SetRequestCoordinator(coordinator *ratelimit.MarketCoordinator) {
	if source == nil {
		return
	}
	for _, current := range source.columns {
		current.mu.Lock()
		current.requests = coordinator
		current.mu.Unlock()
	}
}

// SetCapabilities binds catalog refreshes to the same validated runtime matrix
// used by broad and hot market owners. It must run before collection starts so
// cadence and operation identity cannot change under a live feed.
func (source *Source) SetCapabilities(registry *marketcapability.Registry) error {
	if source == nil || registry == nil {
		return ErrUnavailable
	}
	source.mu.Lock()
	defer source.mu.Unlock()
	if source.running || source.closed {
		return ErrUnavailable
	}
	interval := time.Duration(0)
	for _, current := range source.columns {
		capability, err := registry.Resolve(exchange.Venue(current.profile.Venue), nativeMarketFamily(current.profile), exchange.DatasetCatalog, false)
		if err != nil || capability.Transport != exchange.TransportREST || capability.RateLimitOperation == "" {
			return fmt.Errorf("catalog capability %s: %w", current.profile.ID, errors.Join(ErrUnavailable, err))
		}
		current.capability = capability
		if capability.NominalCadence > 0 && (interval == 0 || capability.NominalCadence < interval) {
			interval = capability.NominalCadence
		}
	}
	if interval > 0 {
		source.interval = interval
	}
	return nil
}

type CatalogRevision struct {
	ScopeKey    string
	Fingerprint string
	ObservedAt  time.Time
	Instruments json.RawMessage
}

type CatalogRevisionStore interface {
	SaveCatalogRevision(context.Context, CatalogRevision) error
}

type CatalogRevisionLoader interface {
	LoadCatalogRevision(context.Context, string) (CatalogRevision, bool, error)
}

// SetRevisionStore enables durable catalog revisions after PostgreSQL has
// passed authority verification. Public collection starts before that point,
// so the store is attached rather than required by NewSource.
func (source *Source) SetRevisionStore(store CatalogRevisionStore) {
	if source == nil {
		return
	}
	for _, current := range source.columns {
		current.mu.Lock()
		current.revisions = store
		current.mu.Unlock()
	}
}

// RestoreCatalogRevisions hydrates last-known canonical identities before a
// successful venue refresh. Restored rows remain stale/read-only until the
// resident catalog producer confirms them against the exchange.
func (source *Source) RestoreCatalogRevisions(ctx context.Context, loader CatalogRevisionLoader) error {
	if source == nil || loader == nil || ctx == nil || ctx.Err() != nil {
		return ErrUnavailable
	}
	for _, current := range source.columns {
		revision, found, err := loader.LoadCatalogRevision(ctx, current.profile.ID)
		if err != nil {
			return err
		}
		if !found {
			continue
		}
		var instruments []exchange.Instrument
		if json.Unmarshal(revision.Instruments, &instruments) != nil || len(instruments) == 0 {
			return fmt.Errorf("restore catalog %s: %w", current.profile.ID, ErrUnavailable)
		}
		restored, err := current.catalog.Restore(current.accountID, instruments, revision.ObservedAt)
		if err != nil {
			return fmt.Errorf("restore catalog %s: %w", current.profile.ID, err)
		}
		// Restore is deliberately a no-op after a live publication. Do not let
		// an older durable payload replace the native index in that race.
		if current.onCatalog != nil && restored.LoadedAt.Equal(revision.ObservedAt.UTC()) {
			if err := current.onCatalog(current.profile, instruments); err != nil {
				return err
			}
		}
	}
	return nil
}

// Run owns the low-frequency catalog refresh loop. Public HTTP reads never
// determine its cadence: after the initial bootstrap they read only the last
// successfully published directory from memory.
func (source *Source) Run(ctx context.Context) error {
	if source == nil || ctx == nil || ctx.Err() != nil {
		return ErrUnavailable
	}
	source.mu.Lock()
	if source.closed || source.running {
		source.mu.Unlock()
		return ErrUnavailable
	}
	runCtx, cancel := context.WithCancel(ctx)
	source.running = true
	source.runCancel = cancel
	source.runDone = make(chan struct{})
	done := source.runDone
	targets := append([]*column(nil), source.columns...)
	source.mu.Unlock()
	defer func() {
		source.mu.Lock()
		if source.runDone == done {
			close(done)
			source.runDone = nil
			source.runCancel = nil
			source.running = false
		}
		source.mu.Unlock()
	}()

	source.refreshAll(runCtx, targets)
	ticker := time.NewTicker(source.interval)
	defer ticker.Stop()
	for {
		select {
		case <-runCtx.Done():
			return runCtx.Err()
		case <-ticker.C:
			source.refreshAll(runCtx, targets)
		}
	}
}

func (source *Source) refreshAll(ctx context.Context, targets []*column) {
	var group sync.WaitGroup
	for _, current := range targets {
		group.Add(1)
		go func(current *column) {
			defer group.Done()
			venueCtx, cancel := context.WithTimeout(ctx, source.venueTimeout)
			defer cancel()
			if err := current.refresh(venueCtx); err != nil && ctx.Err() == nil {
				slog.Warn("public market catalog refresh failed",
					"venue", current.profile.Venue,
					"route_profile", current.profile.ID,
					"error", err,
				)
			}
		}(current)
	}
	group.Wait()
}

type column struct {
	profile   routeprofile.Profile
	connector Connector
	catalog   *catalog.Service
	accountID domain.AccountID
	onCatalog func(routeprofile.Profile, []exchange.Instrument) error

	mu         sync.Mutex
	inFlight   chan struct{}
	revisions  CatalogRevisionStore
	requests   *ratelimit.MarketCoordinator
	capability exchange.MarketCapability
}

type sourceOptions struct {
	interval     time.Duration
	maxAge       time.Duration
	venueTimeout time.Duration
	now          func() time.Time
}

func NewSource(factory Factory, trusted *routeprofile.Registry) (*Source, error) {
	return newSource(factory, trusted, sourceOptions{
		interval: defaultRefreshInterval, maxAge: defaultMaxAge,
		venueTimeout: defaultVenueTimeout, now: time.Now,
	})
}

func newSource(factory Factory, trusted *routeprofile.Registry, options sourceOptions) (*Source, error) {
	if factory == nil || options.interval <= 0 || options.maxAge <= options.interval || options.venueTimeout <= 0 || options.now == nil {
		return nil, ErrUnavailable
	}
	if trusted == nil {
		trusted = routeprofile.DefaultRegistry()
	}
	columns := make([]*column, 0, len(trusted.Profiles()))
	for _, profile := range trusted.Profiles() {
		connector, err := factory.Build(profile)
		if err != nil || connector == nil {
			continue
		}
		projection, err := catalog.NewService(options.maxAge, options.now)
		if err != nil {
			_ = connector.Close()
			continue
		}
		accountID := publicAccountID(profile)
		scope := catalog.Scope{
			Venue: exchange.Venue(profile.Venue), Environment: exchange.EnvironmentLive,
			Product: exchange.Product(profile.Product), Settlement: profile.SettlementAsset,
		}
		if err := projection.RegisterAccount(accountID, scope); err != nil {
			_ = connector.Close()
			continue
		}
		columns = append(columns, &column{profile: profile, connector: connector, catalog: projection, accountID: accountID})
	}
	if len(columns) == 0 {
		return nil, ErrUnavailable
	}
	sockets := make(map[markethub.OwnerKey]exchange.MarketWebSocket)
	for _, current := range columns {
		key := markethub.OwnerKey{Venue: exchange.Venue(current.profile.Venue), Environment: exchange.EnvironmentLive, NativeFamily: nativeMarketFamily(current.profile)}
		if sockets[key] == nil {
			sockets[key] = current.connector.WebSocket()
		}
	}
	source := &Source{
		columns: columns, sockets: sockets, interval: options.interval, venueTimeout: options.venueTimeout, now: options.now,
		derivativeIndex: make(map[string]map[string]exchange.DerivativeFact),
	}
	for _, current := range columns {
		current.onCatalog = source.indexDerivativeCatalog
	}
	return source, nil
}

// Venues reports the profiles this source can serve without performing I/O.
func (source *Source) Venues() []string {
	if source == nil {
		return nil
	}
	seen := make(map[string]struct{}, len(source.columns))
	venues := make([]string, 0, len(source.columns))
	for _, current := range source.columns {
		if _, ok := seen[current.profile.Venue]; ok {
			continue
		}
		seen[current.profile.Venue] = struct{}{}
		venues = append(venues, current.profile.Venue)
	}
	sort.Strings(venues)
	return venues
}

// Snapshot assembles every currently reachable public catalog. A failed venue
// removes only its own column; it cannot blank healthy venues beside it.
func (source *Source) Snapshot(ctx context.Context) (Snapshot, error) {
	if source == nil || ctx == nil || ctx.Err() != nil || source.isClosed() {
		return Snapshot{}, ErrUnavailable
	}
	type result struct {
		column   *column
		snapshot catalog.Snapshot
		err      error
	}
	results := make(chan result, len(source.columns))
	var group sync.WaitGroup
	for _, current := range source.columns {
		group.Add(1)
		go func(current *column) {
			defer group.Done()
			venueCtx, cancel := context.WithTimeout(ctx, source.venueTimeout)
			defer cancel()
			snapshot, err := current.snapshot(venueCtx)
			results <- result{column: current, snapshot: snapshot, err: err}
		}(current)
	}
	group.Wait()
	close(results)

	combined := Snapshot{Instruments: make([]Instrument, 0), UnavailableProfiles: make([]string, 0)}
	contributors := make([]string, 0, len(source.columns))
	for result := range results {
		if result.err != nil {
			slog.Warn("public market catalog column unavailable",
				"venue", result.column.profile.Venue,
				"route_profile", result.column.profile.ID,
				"error", result.err,
			)
			contributors = append(contributors, result.column.profile.ID+"\x00unavailable")
			combined.UnavailableProfiles = append(combined.UnavailableProfiles, result.column.profile.ID)
			continue
		}
		contributors = append(contributors, result.column.profile.ID+"\x00"+result.snapshot.Revision)
		status := result.column.catalog.Status(result.column.accountID)
		if combined.ObservedAt.IsZero() || result.snapshot.LoadedAt.Before(combined.ObservedAt) {
			combined.ObservedAt = result.snapshot.LoadedAt.UTC()
		}
		for _, instrument := range result.snapshot.Instruments {
			canonical, err := result.column.catalog.ResolveLastKnown(ctx, result.column.accountID, instrument.ID)
			if err != nil {
				continue
			}
			combined.Instruments = append(combined.Instruments, Instrument{
				RouteProfileID: result.column.profile.ID,
				Catalog:        instrument,
				Canonical:      canonical,
				Ready:          !status.Stale,
			})
		}
	}
	if len(combined.Instruments) == 0 || combined.ObservedAt.IsZero() {
		return Snapshot{}, ErrUnavailable
	}
	sort.Slice(combined.Instruments, func(i, j int) bool {
		left, right := combined.Instruments[i].Catalog, combined.Instruments[j].Catalog
		if left.Venue != right.Venue {
			return left.Venue < right.Venue
		}
		if left.Product != right.Product {
			return left.Product < right.Product
		}
		return left.ID < right.ID
	})
	sort.Strings(contributors)
	sort.Strings(combined.UnavailableProfiles)
	payload, _ := json.Marshal(contributors)
	revision := sha256.Sum256(payload)
	combined.Revision = "public-" + hex.EncodeToString(revision[:16])
	return combined, nil
}

// Find resolves the public identifier used above the Gateway boundary to the
// canonical venue instrument kept inside it. Native symbols and ids never need
// to cross that boundary: callers carry InstrumentID and this directory owns
// the one conversion back to the venue's address.
func (snapshot Snapshot) Find(instrumentID string) (Instrument, bool) {
	instrumentID = strings.TrimSpace(instrumentID)
	if instrumentID == "" {
		return Instrument{}, false
	}
	for _, instrument := range snapshot.Instruments {
		if string(instrument.Catalog.ID) == instrumentID {
			return instrument, true
		}
	}
	return Instrument{}, false
}

// MatchFundingEntry joins a funding-board row to the public instrument
// directory for the same route profile. Prefer the venue's opaque native id
// when both sides publish it, then its native symbol. The public symbol is not
// a fallback: it is precisely the field whose venue-specific spellings caused
// cross-venue rows to be reported as unlisted.
func (snapshot Snapshot) MatchFundingEntry(routeProfileID string, entry exchange.FundingBoardEntry) (Instrument, bool) {
	routeProfileID = strings.TrimSpace(routeProfileID)
	for _, instrument := range snapshot.Instruments {
		if instrument.RouteProfileID != routeProfileID {
			continue
		}
		identity := instrument.Canonical.Identity
		if entry.NativeID != "" && identity.NativeID != "" && entry.NativeID == identity.NativeID {
			return instrument, true
		}
		if entry.NativeSymbol != "" && entry.NativeSymbol == identity.NativeSymbol {
			return instrument, true
		}
	}
	return Instrument{}, false
}

func (source *Source) MarketData(ctx context.Context, query Query, depthLimit int) (Fact, error) {
	current, instrument, err := source.resolve(ctx, query)
	if err != nil {
		return Fact{}, err
	}
	source.mu.Lock()
	hub := source.marketHub
	source.mu.Unlock()
	if hub == nil {
		return Fact{}, fmt.Errorf("read public market-data Hub: %w", ErrUnavailable)
	}
	return source.marketDataFromHub(ctx, hub, current, instrument, query, depthLimit)
}

func (source *Source) marketDataFromHub(ctx context.Context, hub *markethub.Hub, current *column, instrument exchange.Instrument, query Query, depthLimit int) (Fact, error) {
	// The REST-shaped browser read consumes the same WS-only Hub topic as the
	// live browser. It does not call the venue's REST order-book endpoint; the
	// Exchange adapter supplies the initial or replayed bounded snapshot.
	depthKey := markethub.SubscriptionKey{
		Owner:   markethub.OwnerKey{Venue: exchange.Venue(current.profile.Venue), Environment: exchange.EnvironmentLive, NativeFamily: nativeMarketFamily(current.profile)},
		Dataset: exchange.DatasetDepth, InstrumentID: query.InstrumentID, Depth: depthLimit,
	}
	depth, err := marketHubSnapshot(ctx, hub, depthKey)
	if err != nil {
		return Fact{}, fmt.Errorf("read public order-book websocket snapshot: %w", err)
	}
	var value struct {
		Bids []exchange.BookLevel `json:"bids"`
		Asks []exchange.BookLevel `json:"asks"`
	}
	if json.Unmarshal(depth.Data, &value) != nil {
		return Fact{}, fmt.Errorf("read public order-book websocket snapshot: %w", ErrUnavailable)
	}
	book := exchange.OrderBook{
		Instrument: instrument, Bids: value.Bids, Asks: value.Asks,
		Time: depth.ObservedAt, Sequence: depth.SourceSequence, Page: exchange.PageInfo{Limit: depthLimit},
	}
	if len(book.Bids) == 0 || len(book.Asks) == 0 {
		return Fact{}, fmt.Errorf("read public order-book snapshot: %w", ErrUnavailable)
	}
	fact := Fact{Query: query, Instrument: instrument, Book: book}
	markKey := markethub.SubscriptionKey{
		Owner:   markethub.OwnerKey{Venue: exchange.Venue(current.profile.Venue), Environment: exchange.EnvironmentLive, NativeFamily: nativeMarketFamily(current.profile)},
		Dataset: exchange.DatasetMark, InstrumentID: query.InstrumentID,
	}
	if instrument.Product() == exchange.ProductPerp {
		if mark, markErr := marketHubSnapshot(ctx, hub, markKey); markErr == nil {
			var value struct {
				Price string `json:"price"`
			}
			if json.Unmarshal(mark.Data, &value) == nil {
				if price, parseErr := decimal.NewFromString(value.Price); parseErr == nil && price.IsPositive() {
					fact.Mark = exchange.MarkPriceEvent{Instrument: instrument, Price: price, Time: mark.ObservedAt.UTC()}
					fact.HasMark = true
				}
			}
		}
	}
	return fact, nil
}

func marketHubSnapshot(ctx context.Context, hub *markethub.Hub, key markethub.SubscriptionKey) (markethub.Message, error) {
	subscription, err := hub.Subscribe(ctx, key, 1)
	if err != nil {
		return markethub.Message{}, err
	}
	defer subscription.Close()
	select {
	case <-ctx.Done():
		return markethub.Message{}, ctx.Err()
	case message, open := <-subscription.Events():
		if !open || message.Kind != markethub.MessageSnapshot {
			return markethub.Message{}, markethub.ErrUnavailable
		}
		return message, nil
	}
}

func (source *Source) Candles(ctx context.Context, query Query, interval string, limit int) (CandleResult, error) {
	current, instrument, err := source.resolve(ctx, query)
	if err != nil {
		return CandleResult{}, err
	}
	page, err := current.connector.Candles(ctx, exchange.CandlesRequest{Instrument: instrument, Interval: interval, Limit: limit})
	if err != nil {
		slog.Warn("public market candles unavailable",
			"venue", current.profile.Venue,
			"route_profile", current.profile.ID,
			"instrument_id", query.InstrumentID,
			"error", err,
		)
		return CandleResult{}, fmt.Errorf("read public candles: %w", err)
	}
	return CandleResult{Query: query, Instrument: instrument, Page: page}, nil
}

// WatchOrderBook resolves the one public instrument id and starts a
// credential-free venue subscription. Callers own Subscription.Close.
func (source *Source) WatchOrderBook(ctx context.Context, instrumentID string, limit int) (OrderBookStream, error) {
	current, instrument, err := source.resolvePublicID(ctx, instrumentID)
	if err != nil || limit <= 0 {
		return OrderBookStream{}, ErrUnavailable
	}
	socket := source.socketFor(current.profile)
	if socket == nil {
		return OrderBookStream{}, ErrUnavailable
	}
	subscription, err := socket.WatchOrderBook(ctx, exchange.WatchRequest{
		Instrument: instrument,
		Options:    exchange.WatchOptions{Buffer: 256},
	})
	if err != nil || subscription == nil {
		return OrderBookStream{}, fmt.Errorf("watch public order book: %w", errors.Join(ErrUnavailable, err))
	}
	event, err := awaitSubscriptionEvent(ctx, source.venueTimeout, subscription, func(event exchange.BookEvent) bool {
		return event.Kind == exchange.EventSnapshot || event.Kind == exchange.EventFull
	})
	if err != nil {
		_ = subscription.Close()
		return OrderBookStream{}, fmt.Errorf("bootstrap public order book websocket: %w", errors.Join(ErrUnavailable, err))
	}
	bids, asks := event.Bids, event.Asks
	if len(bids) > limit {
		bids = bids[:limit]
	}
	if len(asks) > limit {
		asks = asks[:limit]
	}
	book := exchange.OrderBook{Instrument: event.Instrument, Bids: bids, Asks: asks, Time: event.Time, Sequence: event.Sequence, Page: exchange.PageInfo{Limit: limit}}
	replay := func() (exchange.OrderBook, bool) { return exchange.OrderBook{}, false }
	if replayable, ok := subscription.(exchange.ReplaySubscription[exchange.BookEvent]); ok {
		replay = func() (exchange.OrderBook, bool) {
			event, available := replayable.Replay()
			if !available {
				return exchange.OrderBook{}, false
			}
			bids, asks := event.Bids, event.Asks
			if len(bids) > limit {
				bids = bids[:limit]
			}
			if len(asks) > limit {
				asks = asks[:limit]
			}
			return exchange.OrderBook{Instrument: event.Instrument, Bids: bids, Asks: asks, Time: event.Time, Sequence: event.Sequence, Page: exchange.PageInfo{Limit: limit}}, true
		}
	}
	return OrderBookStream{Instrument: instrument, Snapshot: book, Subscription: subscription, Replay: replay}, nil
}

// WatchCandles starts the venue's forming-candle stream for a portable
// interval. Interval validation remains inside the exchange WebSocket contract.
func (source *Source) WatchCandles(ctx context.Context, instrumentID, interval string) (CandleStream, error) {
	current, instrument, err := source.resolvePublicID(ctx, instrumentID)
	if err != nil {
		return CandleStream{}, err
	}
	socket := source.socketFor(current.profile)
	if socket == nil {
		return CandleStream{}, ErrUnavailable
	}
	subscription, err := socket.WatchCandles(ctx, exchange.WatchCandlesRequest{
		Instrument: instrument,
		Interval:   strings.TrimSpace(interval),
		Options:    exchange.WatchOptions{Buffer: 64},
	})
	if err != nil || subscription == nil {
		return CandleStream{}, fmt.Errorf("watch public candles: %w", errors.Join(ErrUnavailable, err))
	}
	page, err := current.connector.Candles(ctx, exchange.CandlesRequest{Instrument: instrument, Interval: strings.TrimSpace(interval), Limit: 500})
	if err != nil {
		_ = subscription.Close()
		return CandleStream{}, fmt.Errorf("snapshot public candles: %w", errors.Join(ErrUnavailable, err))
	}
	return CandleStream{Instrument: instrument, Snapshot: page, Subscription: subscription}, nil
}

func (source *Source) WatchBBO(ctx context.Context, instrumentID string) (BBOStream, error) {
	current, instrument, err := source.resolvePublicID(ctx, instrumentID)
	if err != nil {
		return BBOStream{}, err
	}
	socket := source.socketFor(current.profile)
	if socket == nil {
		return BBOStream{}, ErrUnavailable
	}
	subscription, err := socket.WatchBBO(ctx, exchange.WatchRequest{Instrument: instrument, Options: exchange.WatchOptions{Buffer: 256}})
	if err != nil || subscription == nil {
		return BBOStream{}, fmt.Errorf("watch public BBO: %w", errors.Join(ErrUnavailable, err))
	}
	event, err := awaitSubscriptionEvent(ctx, source.venueTimeout, subscription, func(event exchange.BBOEvent) bool {
		return event.Bid.Price.IsPositive() && event.Bid.Quantity.IsPositive() && event.Ask.Price.IsPositive() && event.Ask.Quantity.IsPositive()
	})
	if err != nil {
		_ = subscription.Close()
		return BBOStream{}, fmt.Errorf("bootstrap public BBO websocket: %w", errors.Join(ErrUnavailable, err))
	}
	return BBOStream{Instrument: instrument, Snapshot: event, Subscription: subscription}, nil
}

// WatchMarkPrice starts and bootstraps from one perpetual WS topic.
func (source *Source) WatchMarkPrice(ctx context.Context, instrumentID string) (MarkPriceStream, error) {
	current, instrument, err := source.resolvePublicID(ctx, instrumentID)
	if err != nil {
		return MarkPriceStream{}, err
	}
	socket := source.socketFor(current.profile)
	perpSocket, ok := socket.(exchange.PerpMarketWebSocket)
	if !ok || perpSocket == nil {
		return MarkPriceStream{}, ErrUnavailable
	}
	subscription, err := perpSocket.WatchMarkPrice(ctx, exchange.WatchRequest{
		Instrument: instrument,
		Options:    exchange.WatchOptions{Buffer: 64},
	})
	if err != nil || subscription == nil {
		return MarkPriceStream{}, fmt.Errorf("watch public mark price: %w", errors.Join(ErrUnavailable, err))
	}
	mark, err := awaitSubscriptionEvent(ctx, source.venueTimeout, subscription, func(event exchange.MarkPriceEvent) bool {
		return event.Price.IsPositive()
	})
	if err != nil {
		_ = subscription.Close()
		return MarkPriceStream{}, fmt.Errorf("bootstrap public mark websocket: %w", errors.Join(ErrUnavailable, err))
	}
	return MarkPriceStream{Instrument: instrument, Snapshot: mark, Subscription: subscription}, nil
}

func awaitSubscriptionEvent[T any](ctx context.Context, timeout time.Duration, subscription exchange.Subscription[T], accept func(T) bool) (T, error) {
	var zero T
	if subscription == nil || accept == nil {
		return zero, ErrUnavailable
	}
	waitCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	events, failures := subscription.Events(), subscription.Errors()
	for events != nil || failures != nil {
		select {
		case <-waitCtx.Done():
			return zero, waitCtx.Err()
		case event, open := <-events:
			if !open {
				events = nil
				continue
			}
			if accept(event) {
				return event, nil
			}
		case err, open := <-failures:
			if !open {
				failures = nil
				continue
			}
			if err != nil {
				return zero, err
			}
		}
	}
	return zero, ErrUnavailable
}

// WatchDerivativeReference uses an adapter's additive rich-reference
// capability. Bybit and Bitget expose one ticker containing all derivative
// fields; OKX combines its four native reference channels behind this one
// logical subscription. Its initial value deliberately comes from the resident
// FactStore, not another REST request; this method owns only the live transport.
func (source *Source) WatchDerivativeReference(ctx context.Context, instrumentID string) (DerivativeReferenceStream, error) {
	current, instrument, err := source.resolvePublicID(ctx, instrumentID)
	if err != nil {
		return DerivativeReferenceStream{}, err
	}
	socket := source.socketFor(current.profile)
	referenceSocket, ok := socket.(exchange.DerivativeReferenceWebSocket)
	if !ok || referenceSocket == nil {
		return DerivativeReferenceStream{}, ErrUnavailable
	}
	subscription, err := referenceSocket.WatchDerivativeReference(ctx, exchange.WatchRequest{
		Instrument: instrument,
		Options:    exchange.WatchOptions{Buffer: 64},
	})
	if err != nil || subscription == nil {
		return DerivativeReferenceStream{}, fmt.Errorf("watch public derivative reference: %w", errors.Join(ErrUnavailable, err))
	}
	return DerivativeReferenceStream{
		Instrument:   instrument,
		Snapshot:     exchange.DerivativeReferenceEvent{Instrument: instrument},
		Subscription: subscription,
	}, nil
}

// ResolveInstrument reads one public catalog scope without requiring an account
// or probing unrelated venues. The returned directory row contains no native
// SDK handle or credential material.
func (source *Source) ResolveInstrument(ctx context.Context, instrumentID string) (Instrument, error) {
	current, canonical, err := source.resolvePublicID(ctx, instrumentID)
	if err != nil {
		return Instrument{}, err
	}
	snapshot, err := current.catalog.Snapshot(current.accountID)
	if err != nil {
		return Instrument{}, ErrUnavailable
	}
	for _, instrument := range snapshot.Instruments {
		if string(instrument.ID) == instrumentID {
			return Instrument{RouteProfileID: current.profile.ID, Catalog: instrument, Canonical: canonical, Ready: !current.catalog.Status(current.accountID).Stale}, nil
		}
	}
	return Instrument{}, exchange.ErrNotFound
}

func (source *Source) resolvePublicID(ctx context.Context, instrumentID string) (*column, exchange.Instrument, error) {
	if source == nil || ctx == nil || ctx.Err() != nil || source.isClosed() || strings.TrimSpace(instrumentID) == "" {
		return nil, exchange.Instrument{}, ErrUnavailable
	}
	id := domain.InstrumentID(instrumentID)
	if id.Validate() != nil {
		return nil, exchange.Instrument{}, exchange.ErrNotFound
	}
	parts := strings.Split(instrumentID, ":")
	for _, current := range source.columns {
		if current.profile.Venue != parts[0] || parts[1] != string(exchange.EnvironmentLive) ||
			string(current.profile.Product) != id.Product() || current.profile.SettlementAsset != id.Settlement() {
			continue
		}
		venueCtx, cancel := context.WithTimeout(ctx, source.venueTimeout)
		_, err := current.snapshot(venueCtx)
		cancel()
		if err != nil {
			return nil, exchange.Instrument{}, ErrUnavailable
		}
		instrument, err := current.catalog.ResolveLastKnown(ctx, current.accountID, domain.InstrumentID(instrumentID))
		if err == nil {
			return current, instrument, nil
		}
		if current.catalog.Status(current.accountID).Stale {
			return nil, exchange.Instrument{}, ErrUnavailable
		}
		return nil, exchange.Instrument{}, exchange.ErrNotFound
	}
	return nil, exchange.Instrument{}, exchange.ErrNotFound
}

func (source *Source) socketFor(profile routeprofile.Profile) exchange.MarketWebSocket {
	if source == nil {
		return nil
	}
	key := markethub.OwnerKey{Venue: exchange.Venue(profile.Venue), Environment: exchange.EnvironmentLive, NativeFamily: nativeMarketFamily(profile)}
	source.mu.Lock()
	socket := source.sockets[key]
	source.mu.Unlock()
	return socket
}

func (source *Source) resolve(ctx context.Context, query Query) (*column, exchange.Instrument, error) {
	if source == nil || ctx == nil || ctx.Err() != nil || source.isClosed() ||
		strings.TrimSpace(query.Venue) == "" || query.Environment != string(exchange.EnvironmentLive) ||
		(query.Product != string(exchange.ProductSpot) && query.Product != string(exchange.ProductPerp)) ||
		strings.TrimSpace(query.InstrumentID) == "" {
		return nil, exchange.Instrument{}, ErrUnavailable
	}
	for _, current := range source.columns {
		if current.profile.Venue != query.Venue || string(current.profile.Product) != query.Product {
			continue
		}
		venueCtx, cancel := context.WithTimeout(ctx, source.venueTimeout)
		_, err := current.snapshot(venueCtx)
		cancel()
		if err != nil {
			continue
		}
		instrument, err := current.catalog.ResolveLastKnown(ctx, current.accountID, domain.InstrumentID(query.InstrumentID))
		if err == nil {
			return current, instrument, nil
		}
	}
	return nil, exchange.Instrument{}, ErrUnavailable
}

func (current *column) snapshot(ctx context.Context) (catalog.Snapshot, error) {
	cached, cachedErr := current.catalog.Snapshot(current.accountID)
	if cachedErr == nil {
		return cached, nil
	}
	refreshErr := current.refresh(ctx)
	if refreshErr == nil {
		return current.catalog.Snapshot(current.accountID)
	}
	if cachedErr == nil {
		return cached, nil
	}
	return catalog.Snapshot{}, fmt.Errorf("%w: refresh %s: %v", ErrUnavailable, current.profile.ID, refreshErr)
}

func (current *column) refresh(ctx context.Context) error {
	for {
		current.mu.Lock()
		if current.inFlight == nil {
			current.inFlight = make(chan struct{})
			wait := current.inFlight
			current.mu.Unlock()
			load := func(callCtx context.Context) (any, error) { return current.connector.Refresh(callCtx) }
			var value any
			var err error
			if current.requests != nil {
				operation := current.capability.RateLimitOperation
				if operation == "" {
					operation = "instruments"
				}
				value, err = current.requests.Do(ctx, exchange.Venue(current.profile.Venue), operation, current.profile.ID, load)
			} else {
				value, err = load(ctx)
			}
			instruments, _ := value.([]exchange.Instrument)
			if err == nil {
				_, err = current.catalog.Publish(current.accountID, instruments)
			}
			if err == nil && current.onCatalog != nil {
				err = current.onCatalog(current.profile, instruments)
			}
			if err != nil {
				current.catalog.MarkRefreshError(current.accountID)
			}
			current.mu.Lock()
			revisions := current.revisions
			close(wait)
			current.inFlight = nil
			current.mu.Unlock()
			if err == nil && revisions != nil {
				if persistErr := persistCatalogRevision(ctx, revisions, current.profile, instruments); persistErr != nil {
					slog.Warn("public market catalog revision persist failed", "venue", current.profile.Venue, "route_profile", current.profile.ID, "error", persistErr)
				}
			}
			return err
		}
		wait := current.inFlight
		current.mu.Unlock()
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-wait:
			if _, err := current.catalog.Snapshot(current.accountID); err == nil {
				return nil
			}
		}
	}
}

func persistCatalogRevision(ctx context.Context, store CatalogRevisionStore, profile routeprofile.Profile, instruments []exchange.Instrument) error {
	immutable := append([]exchange.Instrument(nil), instruments...)
	sort.Slice(immutable, func(i, j int) bool {
		left, right := immutable[i].Identity, immutable[j].Identity
		if left.NativeID != right.NativeID {
			return left.NativeID < right.NativeID
		}
		return left.NativeSymbol < right.NativeSymbol
	})
	payload, err := json.Marshal(immutable)
	if err != nil {
		return err
	}
	digest := sha256.Sum256(payload)
	return store.SaveCatalogRevision(ctx, CatalogRevision{
		ScopeKey: profile.ID, Fingerprint: hex.EncodeToString(digest[:]),
		ObservedAt: time.Now().UTC(), Instruments: payload,
	})
}

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
	columns := append([]*column(nil), source.columns...)
	source.mu.Unlock()
	if cancel != nil {
		cancel()
	}
	if done != nil {
		<-done
	}
	var closeErr error
	for _, current := range columns {
		closeErr = errors.Join(closeErr, current.connector.Close())
	}
	return closeErr
}

func (source *Source) isClosed() bool {
	source.mu.Lock()
	defer source.mu.Unlock()
	return source.closed
}

func publicAccountID(profile routeprofile.Profile) domain.AccountID {
	sum := sha256.Sum256([]byte(profile.Venue + "\x00" + profile.ID))
	return domain.AccountID("public-market-" + hex.EncodeToString(sum[:8]))
}
