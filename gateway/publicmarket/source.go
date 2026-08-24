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
	"github.com/QuantProcessing/boltertrader/routeprofile"
)

var ErrUnavailable = errors.New("public market unavailable")

const (
	defaultRefreshInterval = time.Minute
	defaultMaxAge          = 2 * time.Minute
	defaultVenueTimeout    = 10 * time.Second
)

// Connector is the complete method set a public market route may expose.
// The two boolean results distinguish an unsupported optional facet from a
// supported facet whose venue request failed.
type Connector interface {
	Refresh(context.Context) ([]exchange.Instrument, error)
	OrderBook(context.Context, exchange.OrderBookRequest) (exchange.OrderBook, error)
	Candles(context.Context, exchange.CandlesRequest) (exchange.CandlePage, error)
	FundingRate(context.Context, exchange.FundingRateRequest) (exchange.FundingRate, bool, error)
	FundingRateHistory(context.Context, exchange.FundingRateHistoryRequest) (exchange.FundingRatePage, bool, error)
	MarkPrice(context.Context, exchange.Instrument) (exchange.MarkPriceEvent, bool, error)
	WebSocket() exchange.SpotWebSocket
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
}

type Snapshot struct {
	Revision    string
	ObservedAt  time.Time
	Instruments []Instrument
}

type Query struct {
	Venue        string
	Environment  string
	Product      string
	InstrumentID string
}

type Fact struct {
	Query          Query
	Instrument     exchange.Instrument
	Book           exchange.OrderBook
	Mark           exchange.MarkPriceEvent
	HasMark        bool
	Funding        exchange.FundingRate
	HasFunding     bool
	FundingHistory exchange.FundingRatePage
}

type CandleResult struct {
	Query      Query
	Instrument exchange.Instrument
	Page       exchange.CandlePage
}

// OrderBookStream is a REST snapshot fenced by a live public subscription.
// The subscription is opened first, so updates cannot fall into the gap
// between reading the snapshot and starting the stream.
type OrderBookStream struct {
	Instrument   exchange.Instrument
	Snapshot     exchange.OrderBook
	Subscription exchange.Subscription[exchange.BookEvent]
}

// CandleStream carries the venue's native forming-bar updates. Historical bars
// remain a bounded REST read; only the newest bar belongs on this stream.
type CandleStream struct {
	Instrument   exchange.Instrument
	Subscription exchange.Subscription[exchange.CandleEvent]
}

// MarkPriceStream joins an immediate REST mark to the venue's public perpetual
// stream. It is credential-free: the account-specific position remains on the
// private plane while every viewer may share the same mark.
type MarkPriceStream struct {
	Instrument   exchange.Instrument
	Snapshot     exchange.MarkPriceEvent
	Subscription exchange.Subscription[exchange.MarkPriceEvent]
}

type Source struct {
	columns      []*column
	interval     time.Duration
	venueTimeout time.Duration
	now          func() time.Time

	mu        sync.Mutex
	closed    bool
	running   bool
	runCancel context.CancelFunc
	runDone   chan struct{}
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

	mu       sync.Mutex
	inFlight chan struct{}
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
	return &Source{columns: columns, interval: options.interval, venueTimeout: options.venueTimeout, now: options.now}, nil
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

	combined := Snapshot{Instruments: make([]Instrument, 0)}
	contributors := make([]string, 0, len(source.columns))
	for result := range results {
		if result.err != nil {
			slog.Warn("public market catalog column unavailable",
				"venue", result.column.profile.Venue,
				"route_profile", result.column.profile.ID,
				"error", result.err,
			)
			contributors = append(contributors, result.column.profile.ID+"\x00unavailable")
			continue
		}
		contributors = append(contributors, result.column.profile.ID+"\x00"+result.snapshot.Revision)
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
		switch {
		case entry.NativeID != "" && identity.NativeID != "":
			if entry.NativeID == identity.NativeID {
				return instrument, true
			}
		case entry.NativeSymbol != "":
			if entry.NativeSymbol == identity.NativeSymbol {
				return instrument, true
			}
		}
	}
	return Instrument{}, false
}

func (source *Source) MarketData(ctx context.Context, query Query, depthLimit, fundingHistoryLimit int) (Fact, error) {
	current, instrument, err := source.resolve(ctx, query)
	if err != nil {
		return Fact{}, err
	}
	book, err := current.connector.OrderBook(ctx, exchange.OrderBookRequest{Instrument: instrument, Limit: depthLimit})
	if err != nil {
		return Fact{}, fmt.Errorf("read public order book: %w", err)
	}
	fact := Fact{Query: query, Instrument: instrument, Book: book}
	if fact.Mark, fact.HasMark, err = current.connector.MarkPrice(ctx, instrument); err != nil {
		// Mark price enriches a book; it is not the book. A venue temporarily
		// refusing this optional endpoint must leave depth usable and the mark
		// field absent, which is already the public DTO's representation.
		fact.Mark = exchange.MarkPriceEvent{}
		fact.HasMark = false
	}
	if fact.Funding, fact.HasFunding, err = current.connector.FundingRate(ctx, exchange.FundingRateRequest{Instrument: instrument}); err != nil {
		fact.Funding = exchange.FundingRate{}
		fact.HasFunding = false
	}
	if fact.HasFunding {
		var supported bool
		fact.FundingHistory, supported, err = current.connector.FundingRateHistory(ctx, exchange.FundingRateHistoryRequest{Instrument: instrument, Limit: fundingHistoryLimit})
		if err != nil {
			fact.FundingHistory = exchange.FundingRatePage{}
		}
		if !supported {
			fact.FundingHistory = exchange.FundingRatePage{}
		}
	}
	return fact, nil
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
	socket := current.connector.WebSocket()
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
	book, err := current.connector.OrderBook(ctx, exchange.OrderBookRequest{Instrument: instrument, Limit: limit})
	if err != nil {
		_ = subscription.Close()
		return OrderBookStream{}, fmt.Errorf("snapshot public order book: %w", err)
	}
	return OrderBookStream{Instrument: instrument, Snapshot: book, Subscription: subscription}, nil
}

// WatchCandles starts the venue's forming-candle stream for a portable
// interval. Interval validation remains inside the exchange WebSocket contract.
func (source *Source) WatchCandles(ctx context.Context, instrumentID, interval string) (CandleStream, error) {
	current, instrument, err := source.resolvePublicID(ctx, instrumentID)
	if err != nil {
		return CandleStream{}, err
	}
	socket := current.connector.WebSocket()
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
	return CandleStream{Instrument: instrument, Subscription: subscription}, nil
}

// WatchMarkPrice starts the perpetual mark stream before taking its REST
// snapshot, so a price move cannot disappear between the two operations.
func (source *Source) WatchMarkPrice(ctx context.Context, instrumentID string) (MarkPriceStream, error) {
	current, instrument, err := source.resolvePublicID(ctx, instrumentID)
	if err != nil {
		return MarkPriceStream{}, err
	}
	socket := current.connector.WebSocket()
	perpSocket, ok := socket.(exchange.PerpWebSocket)
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
	mark, supported, err := current.connector.MarkPrice(ctx, instrument)
	if err != nil || !supported {
		_ = subscription.Close()
		return MarkPriceStream{}, fmt.Errorf("snapshot public mark price: %w", errors.Join(ErrUnavailable, err))
	}
	return MarkPriceStream{Instrument: instrument, Snapshot: mark, Subscription: subscription}, nil
}

func (source *Source) resolvePublicID(ctx context.Context, instrumentID string) (*column, exchange.Instrument, error) {
	if source == nil || ctx == nil || ctx.Err() != nil || source.isClosed() || strings.TrimSpace(instrumentID) == "" {
		return nil, exchange.Instrument{}, ErrUnavailable
	}
	for _, current := range source.columns {
		venueCtx, cancel := context.WithTimeout(ctx, source.venueTimeout)
		_, err := current.snapshot(venueCtx)
		cancel()
		if err != nil {
			continue
		}
		instrument, err := current.catalog.ResolveLastKnown(ctx, current.accountID, domain.InstrumentID(instrumentID))
		if err == nil {
			return current, instrument, nil
		}
	}
	return nil, exchange.Instrument{}, ErrUnavailable
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
			instruments, err := current.connector.Refresh(ctx)
			if err == nil {
				_, err = current.catalog.Publish(current.accountID, instruments)
			} else {
				current.catalog.MarkRefreshError(current.accountID)
			}
			current.mu.Lock()
			close(wait)
			current.inFlight = nil
			current.mu.Unlock()
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
