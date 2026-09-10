package fundingboard

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/marketcapability"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
	"github.com/QuantProcessing/boltertrader/routeprofile"
)

// Board is the business projection contract. Public exchange connectivity is
// intentionally absent: implementations read normalized facts and history.
type Board interface {
	Snapshot(context.Context) (Snapshot, error)
	Subscribe(context.Context) (<-chan Change, func(), error)
	History(context.Context, HistoryQuery) (exchange.FundingHistorySnapshot, error)
}

type Projection struct {
	facts        *markethub.FactStore
	profiles     map[string]routeprofile.Profile
	history      map[string]HistoryConnector
	now          func() time.Time
	maxAge       time.Duration
	fieldMaxAge  map[exchange.MarketDataset]time.Duration
	capabilities *marketcapability.Registry
	mu           sync.Mutex
	closed       bool
}

func NewProjection(facts *markethub.FactStore, histories HistoryFactory, profiles *routeprofile.Registry, capabilities *marketcapability.Registry) (*Projection, error) {
	if facts == nil || profiles == nil || capabilities == nil {
		return nil, ErrBoardUnavailable
	}
	projection := &Projection{
		facts: facts, profiles: make(map[string]routeprofile.Profile), history: make(map[string]HistoryConnector), now: time.Now, maxAge: defaultMaxAge, capabilities: capabilities,
		fieldMaxAge: map[exchange.MarketDataset]time.Duration{
			exchange.DatasetFunding: 2 * time.Minute, exchange.DatasetMark: 5 * time.Second,
			exchange.DatasetIndex: 15 * time.Second, exchange.DatasetOpenInterest: 2 * time.Minute,
		},
	}
	for _, profile := range profiles.Profiles() {
		if profile.Product != routeprofile.ProductPerp {
			continue
		}
		projection.profiles[profile.ID] = profile
		if histories != nil {
			if connector, err := histories.BuildHistory(profile); err == nil && connector != nil {
				projection.history[profile.ID] = connector
			}
		}
	}
	if len(projection.profiles) == 0 {
		return nil, ErrBoardUnavailable
	}
	return projection, nil
}

func (projection *Projection) Snapshot(ctx context.Context) (Snapshot, error) {
	if projection == nil || ctx == nil || ctx.Err() != nil {
		return Snapshot{}, ErrBoardUnavailable
	}
	projection.mu.Lock()
	closed := projection.closed
	projection.mu.Unlock()
	if closed {
		return Snapshot{}, ErrBoardUnavailable
	}
	facts, err := projection.facts.Snapshot(ctx)
	if err != nil {
		return Snapshot{}, err
	}
	now := projection.now().UTC()
	byRoute := make(map[string][]exchange.DerivativeFact)
	for _, fact := range facts {
		byRoute[fact.RouteProfileID] = append(byRoute[fact.RouteProfileID], fact)
	}
	result := Snapshot{ObservedAt: now, Venues: make([]VenueSnapshot, 0, len(projection.profiles))}
	for routeID, profile := range projection.profiles {
		venue := VenueSnapshot{Venue: profile.Venue, Environment: string(exchange.EnvironmentLive), Product: string(profile.Product), Settlement: profile.SettlementAsset, RouteProfileID: routeID, Status: VenueUnavailable, Unavailable: true}
		for _, fact := range byRoute[routeID] {
			entry := derivativeBoardEntry(fact)
			entry.StaleFields, entry.MissingFields = derivativeFactFieldState(fact, now, projection.freshnessFor(fact))
			if len(entry.StaleFields) > 0 {
				venue.Stale = true
			}
			venue.Entries = append(venue.Entries, entry)
			last := latestDerivativeObservation(fact)
			if last.After(venue.LastSuccessAt) {
				venue.LastSuccessAt, venue.ObservedAt = last, last
			}
		}
		if !venue.LastSuccessAt.IsZero() && now.Sub(venue.LastSuccessAt) <= projection.maxAge {
			venue.Unavailable = false
			venue.Status = VenueLive
			if venue.Stale {
				venue.Status = VenueDegraded
			}
		}
		sort.Slice(venue.Entries, func(i, j int) bool { return venue.Entries[i].Symbol < venue.Entries[j].Symbol })
		result.Venues = append(result.Venues, venue)
	}
	sort.Slice(result.Venues, func(i, j int) bool { return result.Venues[i].RouteProfileID < result.Venues[j].RouteProfileID })
	return result, nil
}

func (projection *Projection) freshnessFor(fact exchange.DerivativeFact) map[exchange.MarketDataset]time.Duration {
	result := make(map[exchange.MarketDataset]time.Duration, len(projection.fieldMaxAge))
	for dataset, fallback := range projection.fieldMaxAge {
		result[dataset] = fallback
		family := exchange.NativeMarketFamily(fact.Instrument.Venue, fact.Instrument.Product, fact.Instrument.SettleAsset)
		capability, err := projection.capabilities.Resolve(fact.Instrument.Venue, family, dataset, false)
		if err != nil {
			continue
		}
		if window := marketcapability.FreshnessWindow(capability); window > result[dataset] {
			result[dataset] = window
		}
	}
	return result
}

func (projection *Projection) Subscribe(ctx context.Context) (<-chan Change, func(), error) {
	if projection == nil || ctx == nil || ctx.Err() != nil {
		return nil, nil, ErrBoardUnavailable
	}
	changes, stopFacts, err := projection.facts.Subscribe(ctx, 16)
	if err != nil {
		return nil, nil, err
	}
	streamCtx, cancel := context.WithCancel(ctx)
	output := make(chan Change, 2)
	output <- Change{Sequence: 0, Kind: ChangeUpsert}
	go func() {
		defer close(output)
		defer stopFacts()
		ticker := time.NewTicker(time.Second)
		defer ticker.Stop()
		var pending bool
		var sequence uint64
		for {
			select {
			case <-streamCtx.Done():
				return
			case change, open := <-changes:
				if !open {
					return
				}
				if change.Sequence > sequence {
					sequence = change.Sequence
				}
				pending = true
			case <-ticker.C:
				if !pending {
					continue
				}
				select {
				case output <- Change{Sequence: sequence, Kind: ChangeUpsert}:
					pending = false
				default:
					return
				}
			}
		}
	}()
	var once sync.Once
	stop := func() { once.Do(func() { cancel(); stopFacts() }) }
	return output, stop, nil
}

func (projection *Projection) History(ctx context.Context, query HistoryQuery) (exchange.FundingHistorySnapshot, error) {
	if projection == nil || ctx == nil || ctx.Err() != nil {
		return exchange.FundingHistorySnapshot{}, ErrBoardUnavailable
	}
	projection.mu.Lock()
	closed := projection.closed
	projection.mu.Unlock()
	if closed {
		return exchange.FundingHistorySnapshot{}, ErrBoardUnavailable
	}
	if query.RouteProfileID == "" || (query.NativeSymbol == "" && query.NativeID == "") {
		return exchange.FundingHistorySnapshot{}, ErrHistoryUnsupported
	}
	if query.Since.IsZero() || !query.Until.After(query.Since) || query.Until.Sub(query.Since) > maxHistorySpan {
		return exchange.FundingHistorySnapshot{}, ErrHistoryWindow
	}
	projection.mu.Lock()
	history := projection.history[query.RouteProfileID]
	projection.mu.Unlock()
	if history == nil {
		return exchange.FundingHistorySnapshot{}, ErrHistoryUnsupported
	}
	requestCtx, cancel := context.WithTimeout(ctx, defaultVenueTimeout)
	defer cancel()
	return history.Load(requestCtx, exchange.FundingHistoryRequest{NativeSymbol: query.NativeSymbol, NativeID: query.NativeID, Since: query.Since, Until: query.Until})
}

func (projection *Projection) Ready() bool {
	if projection == nil {
		return false
	}
	snapshot, err := projection.Snapshot(context.Background())
	if err != nil {
		return false
	}
	for _, venue := range snapshot.Venues {
		if !venue.Unavailable {
			return true
		}
	}
	return false
}

func (projection *Projection) Close() error {
	if projection == nil {
		return nil
	}
	projection.mu.Lock()
	if projection.closed {
		projection.mu.Unlock()
		return nil
	}
	projection.closed = true
	var result error
	for _, history := range projection.history {
		result = errors.Join(result, history.Close())
	}
	projection.mu.Unlock()
	return result
}

func derivativeBoardEntry(fact exchange.DerivativeFact) exchange.FundingBoardEntry {
	entry := exchange.FundingBoardEntry{
		Symbol: fact.Instrument.Symbol, NativeSymbol: fact.Instrument.NativeSymbol, NativeID: fact.Instrument.NativeID,
		BaseAsset: fact.Instrument.BaseAsset, QuoteAsset: fact.Instrument.QuoteAsset,
		IntervalHours: fact.FundingIntervalHours, NextFundingTime: fact.NextFundingTime,
		MarkPrice: fact.MarkPrice, IndexPrice: fact.IndexPrice, OpenInterest: fact.OpenInterest,
		OpenInterestNotional: fact.OpenInterestNotional, OpenInterestNotionalAsset: fact.OpenInterestNotionalAsset,
		FundingObservedAt: fact.FundingObservedAt, MarkObservedAt: fact.MarkObservedAt,
		IndexObservedAt: fact.IndexObservedAt, OpenInterestObservedAt: fact.OpenInterestObservedAt,
	}
	if fact.FundingRate.Valid {
		entry.Rate, entry.RateValid = fact.FundingRate.Value, true
	}
	entry.PriceObservedAt = latestObservation(fact.MarkObservedAt, fact.IndexObservedAt)
	return entry
}

func derivativeFactFieldState(fact exchange.DerivativeFact, now time.Time, maxAge map[exchange.MarketDataset]time.Duration) (stale, missing []string) {
	for _, field := range []struct {
		name    string
		dataset exchange.MarketDataset
		valid   bool
		at      time.Time
	}{
		{"funding", exchange.DatasetFunding, fact.FundingRate.Valid, fact.FundingObservedAt},
		{"mark", exchange.DatasetMark, fact.MarkPrice.Valid, fact.MarkObservedAt},
		{"index", exchange.DatasetIndex, fact.IndexPrice.Valid, fact.IndexObservedAt},
		{"open_interest", exchange.DatasetOpenInterest, fact.OpenInterest.Valid || fact.OpenInterestNotional.Valid, fact.OpenInterestObservedAt},
	} {
		if !field.valid {
			missing = append(missing, field.name)
			continue
		}
		if field.at.IsZero() || now.Sub(field.at) < 0 || now.Sub(field.at) > maxAge[field.dataset] {
			stale = append(stale, field.name)
		}
	}
	return stale, missing
}

func latestDerivativeObservation(fact exchange.DerivativeFact) time.Time {
	latest := fact.FundingObservedAt
	for _, candidate := range []time.Time{fact.MarkObservedAt, fact.IndexObservedAt, fact.OpenInterestObservedAt} {
		if candidate.After(latest) {
			latest = candidate
		}
	}
	return latest.UTC()
}

func latestObservation(left, right time.Time) time.Time {
	if right.After(left) {
		return right.UTC()
	}
	if left.IsZero() {
		return time.Time{}
	}
	return left.UTC()
}

var _ Board = (*Projection)(nil)
