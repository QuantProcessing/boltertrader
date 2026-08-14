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

// Snapshot is the whole board: one column per funding-capable route profile.
type Snapshot struct {
	Mode       string
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
		exchangefactory.Environment(profile.NativeEnvironment),
		factory.httpClient,
	)
	if err != nil {
		return nil, ErrBoardUnavailable
	}
	return connector, nil
}

type Source struct {
	mu           sync.Mutex
	columns      []*column
	mode         routeprofile.Mode
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

// NewSource enumerates the perpetual route profiles for a mode and opens one
// credential-free connector per profile that has a funding surface.
//
// The board follows the gateway's runtime mode rather than always reading
// mainnet. Live rates under a test-mode gateway would read as an ordinary
// board while describing a market the arbitrage product cannot trade against,
// which is the failure mode this codebase keeps designing out: plausible
// numbers instead of a visible gap. A sparse testnet column shows up as
// unavailable, which is the truth.
//
// A profile the factory rejects is not a column. That is how aster stays off
// the board: the capability matrix records that it publishes no funding, so
// "aster has no rates" and "aster is down" never become the same rendering.
func NewSource(factory Factory, trusted *routeprofile.Registry, mode routeprofile.Mode) (*Source, error) {
	return newSource(factory, trusted, mode, sourceOptions{
		interval:     defaultRefreshInterval,
		maxAge:       defaultMaxAge,
		venueTimeout: defaultVenueTimeout,
		now:          time.Now,
	})
}

func newSource(factory Factory, trusted *routeprofile.Registry, mode routeprofile.Mode, options sourceOptions) (*Source, error) {
	if factory == nil {
		return nil, ErrBoardUnavailable
	}
	if trusted == nil {
		trusted = routeprofile.DefaultRegistry()
	}
	if options.interval <= 0 || options.maxAge <= options.interval || options.venueTimeout <= 0 || options.now == nil {
		return nil, ErrBoardUnavailable
	}
	profiles := trusted.Profiles(mode)
	if len(profiles) == 0 {
		return nil, ErrBoardUnavailable
	}
	columns := make([]*column, 0, len(profiles))
	for _, profile := range profiles {
		if profile.Product != routeprofile.ProductPerp {
			continue
		}
		connector, err := factory.Build(profile)
		if err != nil || connector == nil {
			continue
		}
		columns = append(columns, &column{profile: profile, connector: connector})
	}
	if len(columns) == 0 {
		// Every supported mode has funding venues. None means the profile
		// registry or the factory is misconfigured, and a board that silently
		// served an empty table forever would look like a market-wide outage.
		return nil, ErrBoardUnavailable
	}
	return &Source{
		columns:      columns,
		mode:         mode,
		interval:     options.interval,
		maxAge:       options.maxAge,
		venueTimeout: options.venueTimeout,
		now:          options.now,
	}, nil
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
			Environment:    string(current.profile.NativeEnvironment),
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
	return Snapshot{Mode: string(source.mode), ObservedAt: observedAt, Venues: venues}
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
	}
	return closeErr
}
