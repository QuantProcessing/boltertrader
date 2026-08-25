package marketowner

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/marketcapability"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
	"github.com/QuantProcessing/boltertrader/gateway/publicmarket"
	"github.com/QuantProcessing/boltertrader/routeprofile"
)

type DerivativeFeedFactory interface {
	BuildFeed(routeprofile.Profile) (exchange.FundingMarketFeed, error)
}

type derivativeRouteFeed struct {
	profile routeprofile.Profile
	feed    exchange.FundingMarketFeed
}

type derivativeVenueOwner struct {
	key    markethub.OwnerKey
	routes []derivativeRouteFeed
}

// DerivativeOwners is the process-wide resident owner set. One element exists
// for each venue/environment/native-family key; settlement route profiles are
// projections managed by that element, never independent lifecycle owners.
type DerivativeOwners struct {
	owners   []derivativeVenueOwner
	markets  *publicmarket.Source
	facts    *markethub.FactStore
	observer markethub.Observer
	mu       sync.Mutex
	closed   bool
}

func NewDerivativeOwners(factory DerivativeFeedFactory, profiles *routeprofile.Registry, capabilities *marketcapability.Registry, markets *publicmarket.Source, facts *markethub.FactStore, observer markethub.Observer) (*DerivativeOwners, error) {
	if factory == nil || profiles == nil || capabilities == nil || markets == nil || facts == nil {
		return nil, fmt.Errorf("derivative Feed Owner dependencies are incomplete")
	}
	byOwner := make(map[markethub.OwnerKey][]routeprofile.Profile)
	for _, profile := range profiles.Profiles() {
		if profile.Product != routeprofile.ProductPerp {
			continue
		}
		key := markethub.OwnerKey{Venue: exchange.Venue(profile.Venue), Environment: exchange.EnvironmentLive, NativeFamily: marketcapability.NativeFamily(exchange.Venue(profile.Venue), exchange.ProductPerp, profile.SettlementAsset)}
		for _, dataset := range []exchange.MarketDataset{exchange.DatasetFunding, exchange.DatasetMark, exchange.DatasetIndex, exchange.DatasetOpenInterest} {
			if _, err := capabilities.Resolve(key.Venue, key.NativeFamily, dataset, false); err != nil {
				return nil, fmt.Errorf("%s %s capability: %w", profile.ID, dataset, err)
			}
		}
		byOwner[key] = append(byOwner[key], profile)
	}
	owners := make([]derivativeVenueOwner, 0, len(byOwner))
	for key, ownerProfiles := range byOwner {
		owner := derivativeVenueOwner{key: key, routes: make([]derivativeRouteFeed, 0, len(ownerProfiles))}
		for _, profile := range ownerProfiles {
			feed, err := factory.BuildFeed(profile)
			if err != nil || feed == nil {
				for _, route := range owner.routes {
					_ = route.feed.Close()
				}
				return nil, fmt.Errorf("build derivative feed %s: %w", profile.ID, err)
			}
			owner.routes = append(owner.routes, derivativeRouteFeed{profile: profile, feed: feed})
		}
		owners = append(owners, owner)
	}
	if len(owners) == 0 {
		return nil, fmt.Errorf("no derivative Feed Owners configured")
	}
	return &DerivativeOwners{owners: owners, markets: markets, facts: facts, observer: observer}, nil
}

func (owners *DerivativeOwners) Run(ctx context.Context) error {
	if owners == nil || ctx == nil {
		return fmt.Errorf("derivative Feed Owners unavailable")
	}
	var group sync.WaitGroup
	for index := range owners.owners {
		group.Add(1)
		go func(owner *derivativeVenueOwner) { defer group.Done(); owners.runOwner(ctx, owner) }(&owners.owners[index])
	}
	group.Wait()
	return ctx.Err()
}

func (owners *DerivativeOwners) runOwner(ctx context.Context, owner *derivativeVenueOwner) {
	backoff := time.Second
	var reconnects uint64
	for ctx.Err() == nil {
		owners.observe(owner.key, "connecting", reconnects)
		if err := owners.bootstrapOwner(ctx, owner); err != nil {
			owners.observe(owner.key, "unavailable", reconnects)
			if !waitContext(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		owners.observe(owner.key, "live", reconnects)
		backoff = time.Second
		attempt, cancel := context.WithCancel(ctx)
		results := make(chan error, len(owner.routes))
		for index := range owner.routes {
			route := &owner.routes[index]
			go func() {
				results <- route.feed.Stream(attempt, func(patch exchange.FundingMarketPatch) {
					if projected, err := owners.project(attempt, owner.key, route.profile, patch); err == nil {
						_ = owners.facts.Apply(projected)
					}
				})
			}()
		}
		err := <-results
		cancel()
		for range owner.routes[1:] {
			<-results
		}
		if ctx.Err() != nil {
			return
		}
		reconnects++
		owners.observe(owner.key, "degraded", reconnects)
		if owners.observer != nil {
			owners.observer.RecordMarketGap(string(owner.key.Venue), owner.key.NativeFamily, "derivative", true)
		}
		_ = err
		if !waitContext(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff)
	}
}

func (owners *DerivativeOwners) bootstrapOwner(ctx context.Context, owner *derivativeVenueOwner) error {
	results := make(chan error, len(owner.routes))
	for index := range owner.routes {
		route := &owner.routes[index]
		go func() {
			requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
			defer cancel()
			patch, err := route.feed.Bootstrap(requestCtx)
			if err == nil {
				var projected exchange.DerivativePatch
				projected, err = owners.project(requestCtx, owner.key, route.profile, patch)
				if err == nil {
					err = owners.facts.Apply(projected)
				}
			}
			results <- err
		}()
	}
	var result error
	for range owner.routes {
		result = errors.Join(result, <-results)
	}
	return result
}

func (owners *DerivativeOwners) project(ctx context.Context, owner markethub.OwnerKey, profile routeprofile.Profile, patch exchange.FundingMarketPatch) (exchange.DerivativePatch, error) {
	if err := patch.Validate(); err != nil {
		return exchange.DerivativePatch{}, err
	}
	catalog, err := owners.markets.Snapshot(ctx)
	if err != nil {
		return exchange.DerivativePatch{}, err
	}
	facts := make([]exchange.DerivativeFact, 0, len(patch.Facts))
	for _, native := range patch.Facts {
		entry := exchange.FundingBoardEntry{NativeSymbol: native.Instrument.NativeSymbol, NativeID: native.Instrument.NativeID}
		instrument, ok := catalog.MatchFundingEntry(profile.ID, entry)
		if !ok {
			continue
		}
		facts = append(facts, exchange.DerivativeFact{
			InstrumentID: string(instrument.Catalog.ID), RouteProfileID: profile.ID,
			Instrument: instrument.Canonical.Identity, Environment: exchange.EnvironmentLive,
			FundingRate: native.FundingRate, FundingIntervalHours: native.FundingIntervalHours,
			NextFundingTime: native.NextFundingTime, MarkPrice: native.MarkPrice, IndexPrice: native.IndexPrice,
			OpenInterest: native.OpenInterest, OpenInterestNotional: native.OpenInterestNotional,
			OpenInterestNotionalAsset: native.OpenInterestNotionalAsset,
			FundingObservedAt:         native.FundingObservedAt, MarkObservedAt: native.MarkObservedAt,
			IndexObservedAt: native.IndexObservedAt, OpenInterestObservedAt: native.OpenInterestObservedAt,
		})
	}
	if len(patch.Facts) > 0 && len(facts) == 0 {
		return exchange.DerivativePatch{}, fmt.Errorf("derivative catalog projection unavailable")
	}
	kind := exchange.EventDelta
	if patch.Kind == exchange.FundingMarketPatchSnapshot {
		kind = exchange.EventSnapshot
	}
	return exchange.DerivativePatch{
		Kind: kind, Scope: patch.Scope, NativeFamily: owner.NativeFamily, ObservedAt: patch.ObservedAt,
		SourceSequence: strconv.FormatUint(patch.Sequence, 10), Fields: derivativeFields(patch.Fields), Facts: facts,
	}, nil
}

func derivativeFields(fields exchange.FundingMarketField) exchange.DerivativeField {
	var result exchange.DerivativeField
	for _, item := range []struct {
		old  exchange.FundingMarketField
		next exchange.DerivativeField
	}{
		{exchange.FundingMarketFieldIdentity, exchange.DerivativeFieldIdentity},
		{exchange.FundingMarketFieldFunding, exchange.DerivativeFieldFunding},
		{exchange.FundingMarketFieldFundingInterval, exchange.DerivativeFieldFundingInterval},
		{exchange.FundingMarketFieldNextFundingTime, exchange.DerivativeFieldNextFundingTime},
		{exchange.FundingMarketFieldMarkPrice, exchange.DerivativeFieldMark},
		{exchange.FundingMarketFieldIndexPrice, exchange.DerivativeFieldIndex},
		{exchange.FundingMarketFieldOpenInterest, exchange.DerivativeFieldOpenInterest},
		{exchange.FundingMarketFieldOpenInterestNotional, exchange.DerivativeFieldOpenInterestNotional},
	} {
		if fields.Has(item.old) {
			result |= item.next
		}
	}
	return result
}

func (owners *DerivativeOwners) observe(key markethub.OwnerKey, state string, reconnects uint64) {
	if owners.observer != nil {
		owners.observer.ObserveMarketFeed(string(key.Venue), key.NativeFamily, "derivative", state, reconnects)
	}
}

func (owners *DerivativeOwners) Close() error {
	if owners == nil {
		return nil
	}
	owners.mu.Lock()
	if owners.closed {
		owners.mu.Unlock()
		return nil
	}
	owners.closed = true
	var result error
	for _, owner := range owners.owners {
		for _, route := range owner.routes {
			result = errors.Join(result, route.feed.Close())
		}
	}
	owners.mu.Unlock()
	return result
}

func waitContext(ctx context.Context, delay time.Duration) bool {
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func nextBackoff(current time.Duration) time.Duration {
	current *= 2
	if current > 30*time.Second {
		return 30 * time.Second
	}
	return current
}
