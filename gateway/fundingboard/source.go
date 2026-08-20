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
	"net/http"
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
	// One refresh per half-minute, retained for two, matching publicconn's
	// ratio rather than its absolute values: funding settles hourly at the
	// fastest, but mark and index prices move continuously and the board is
	// read to compare venues against each other, so a column two refreshes
	// behind its neighbours is a spread that is not really there.
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
	mu           sync.Mutex
	columns      []*column
	interval     time.Duration
	maxAge       time.Duration
	venueTimeout time.Duration
	now          func() time.Time

	lastAttempt time.Time
	refreshing  bool
	refreshDone chan struct{}
	closed      bool
}

type column struct {
	profile   routeprofile.Profile
	connector Connector

	// nil on a venue that publishes rates but no settled series. Set once at
	// construction and never reassigned, so History may read it without the
	// mutex; entries below is the part that changes.
	history HistoryConnector

	// Guarded by Source.mu.
	entries    []exchange.FundingBoardEntry
	observedAt time.Time
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
// A profile the factory rejects is not a column. That is how aster stays off
// the board: the capability matrix records that it publishes no funding, so
// "aster has no rates" and "aster is down" never become the same rendering.
func NewSource(factory Factory, trusted *routeprofile.Registry) (*Source, error) {
	return newSource(factory, trusted, sourceOptions{
		interval:     defaultRefreshInterval,
		maxAge:       defaultMaxAge,
		venueTimeout: defaultVenueTimeout,
		now:          time.Now,
	})
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
		connector, err := factory.Build(profile)
		if err != nil || connector == nil {
			continue
		}
		columns = append(columns, &column{
			profile:   profile,
			connector: connector,
			history:   buildHistory(factory, profile),
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
	}, nil
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

// Snapshot returns the board, refreshing first when the cached one has aged past
// the refresh interval.
//
// Refresh is lazy rather than a background ticker: the board has no lease to
// scope a loop to, so a ticker would poll a dozen venues forever whether or not
// anybody is looking at the page. One caller does the work, concurrent callers
// wait for it, and the attempt clock advances even when every venue fails so a
// broken venue cannot turn each request into a retry.
//
// An error means the source itself is unusable. A table where every venue
// failed is not an error: it is a snapshot whose columns all say so, which
// tells the operator strictly more than a 503 would.
func (source *Source) Snapshot(ctx context.Context) (Snapshot, error) {
	if source == nil || ctx == nil || ctx.Err() != nil {
		return Snapshot{}, ErrBoardUnavailable
	}

	source.mu.Lock()
	if source.closed {
		source.mu.Unlock()
		return Snapshot{}, ErrBoardUnavailable
	}
	if !source.needsRefreshLocked() {
		projection := source.projectLocked()
		source.mu.Unlock()
		return projection, nil
	}
	if source.refreshing {
		done := source.refreshDone
		source.mu.Unlock()
		select {
		case <-ctx.Done():
			return Snapshot{}, ctx.Err()
		case <-done:
		}
		source.mu.Lock()
		if source.closed {
			source.mu.Unlock()
			return Snapshot{}, ErrBoardUnavailable
		}
		projection := source.projectLocked()
		source.mu.Unlock()
		return projection, nil
	}
	source.refreshing = true
	source.refreshDone = make(chan struct{})
	done := source.refreshDone
	targets := append([]*column(nil), source.columns...)
	source.mu.Unlock()

	results := source.refreshAll(ctx, targets)

	source.mu.Lock()
	source.lastAttempt = source.clock()
	for index, result := range results {
		if result.err != nil {
			continue
		}
		targets[index].entries = result.entries
		targets[index].observedAt = source.lastAttempt
	}
	source.refreshing = false
	close(done)
	projection := source.projectLocked()
	source.mu.Unlock()
	return projection, nil
}

type venueResult struct {
	entries []exchange.FundingBoardEntry
	err     error
}

// refreshAll reads every venue concurrently, each under its own deadline.
//
// The work is detached from the calling request's context on purpose. This
// caller holds the single-flight slot, so abandoning the refresh when the
// browser hangs up would leave every waiter blocked until their own contexts
// expire, and would leave the board unrefreshed for all of them.
func (source *Source) refreshAll(ctx context.Context, targets []*column) []venueResult {
	results := make([]venueResult, len(targets))
	detached := context.WithoutCancel(ctx)

	var group sync.WaitGroup
	for index, current := range targets {
		group.Add(1)
		go func(index int, current *column) {
			defer group.Done()
			venueCtx, cancel := context.WithTimeout(detached, source.venueTimeout)
			defer cancel()
			snapshot, err := current.connector.Refresh(venueCtx)
			if err != nil {
				results[index] = venueResult{err: err}
				return
			}
			results[index] = venueResult{entries: snapshot.Entries}
		}(index, current)
	}
	group.Wait()
	return results
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

	target, entry, warm, err := source.resolveHistory(query)
	if errors.Is(err, ErrHistoryUnknownInstrument) && !warm {
		// The mapping lives in a column that has never been read — the very
		// first chart after startup, or one opened while a venue is down. One
		// refresh is worth attempting before telling the caller the instrument
		// is unlisted, because those two states are indistinguishable from here
		// and only one of them is true. A column that *is* warm and still lacks
		// the symbol genuinely does not list it, and refreshing ten venues over
		// a mistyped symbol would be the wrong trade.
		if _, refreshErr := source.Snapshot(ctx); refreshErr != nil {
			return exchange.FundingHistorySnapshot{}, refreshErr
		}
		target, entry, _, err = source.resolveHistory(query)
	}
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
		if len(current.entries) == 0 {
			return nil, missing, false, ErrHistoryUnknownInstrument
		}
		for _, candidate := range current.entries {
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

func (source *Source) needsRefreshLocked() bool {
	if source.lastAttempt.IsZero() {
		return true
	}
	return source.clock().Sub(source.lastAttempt) >= source.interval
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
		}
		age := observedAt.Sub(current.observedAt)
		switch {
		case current.observedAt.IsZero() || age > source.maxAge:
			venue.Unavailable = true
		default:
			venue.ObservedAt = current.observedAt
			venue.Stale = age > source.interval
			venue.Entries = append([]exchange.FundingBoardEntry(nil), current.entries...)
		}
		venues = append(venues, venue)
	}
	return Snapshot{ObservedAt: observedAt, Venues: venues}
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
	columns := source.columns
	source.mu.Unlock()

	var closeErr error
	for _, current := range columns {
		if err := current.connector.Close(); err != nil {
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
