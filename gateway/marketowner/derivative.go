package marketowner

import (
	"context"
	"errors"
	"fmt"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/hotset"
	"github.com/QuantProcessing/boltertrader/gateway/marketcapability"
	"github.com/QuantProcessing/boltertrader/gateway/markethub"
	"github.com/QuantProcessing/boltertrader/routeprofile"
)

type DerivativeFeedFactory interface {
	BuildDerivativeFeed([]routeprofile.Profile, []exchange.MarketCapability, exchange.DerivativeInstrumentResolver) (exchange.DerivativeFeed, error)
}

type derivativeVenueOwner struct {
	key        markethub.OwnerKey
	profiles   []routeprofile.Profile
	feed       exchange.DerivativeFeed
	generation uint64
}

// DerivativeOwners is the process-wide resident owner set. One element exists
// for each venue/environment/native-family key; settlement route profiles are
// projections managed by that element, never independent lifecycle owners.
type DerivativeOwners struct {
	owners   []derivativeVenueOwner
	facts    *markethub.FactStore
	observer markethub.Observer
	mu       sync.Mutex
	closed   bool
}

func NewDerivativeOwners(factory DerivativeFeedFactory, profiles *routeprofile.Registry, capabilities *marketcapability.Registry, resolver exchange.DerivativeInstrumentResolver, facts *markethub.FactStore, observer markethub.Observer) (*DerivativeOwners, error) {
	if factory == nil || profiles == nil || capabilities == nil || resolver == nil || facts == nil {
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
		owner := derivativeVenueOwner{key: key, profiles: append([]routeprofile.Profile(nil), ownerProfiles...)}
		runtimeCapabilities := make([]exchange.MarketCapability, 0, 4)
		for _, dataset := range []exchange.MarketDataset{exchange.DatasetFunding, exchange.DatasetMark, exchange.DatasetIndex, exchange.DatasetOpenInterest} {
			capability, resolveErr := capabilities.Resolve(key.Venue, key.NativeFamily, dataset, false)
			if resolveErr != nil {
				return nil, fmt.Errorf("%s %s capability: %w", ownerProfiles[0].ID, dataset, resolveErr)
			}
			runtimeCapabilities = append(runtimeCapabilities, capability)
		}
		feed, err := factory.BuildDerivativeFeed(ownerProfiles, runtimeCapabilities, resolver)
		if err != nil || feed == nil {
			return nil, fmt.Errorf("build derivative feed %s/%s: %w", key.Venue, key.NativeFamily, err)
		}
		owner.feed = feed
		owners = append(owners, owner)
	}
	if len(owners) == 0 {
		return nil, fmt.Errorf("no derivative Feed Owners configured")
	}
	return &DerivativeOwners{owners: owners, facts: facts, observer: observer}, nil
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
		owner.generation++
		generation := owner.generation
		owners.observe(owner.key, "connecting", reconnects)
		if err := owners.bootstrapOwner(ctx, owner, generation); err != nil {
			owners.observe(owner.key, "unavailable", reconnects)
			if !waitContext(ctx, backoff) {
				return
			}
			backoff = nextBackoff(backoff)
			continue
		}
		owners.observe(owner.key, "live", reconnects)
		backoff = time.Second
		err := owner.feed.Stream(ctx, func(patch exchange.DerivativePatch) {
			patch.Generation = generation
			patch.NativeFamily = owner.key.NativeFamily
			_ = owners.facts.Apply(patch)
		})
		if ctx.Err() != nil {
			return
		}
		reconnects++
		owners.observe(owner.key, "degraded", reconnects)
		if owners.observer != nil {
			owners.observer.RecordMarketGap(string(owner.key.Venue), owner.key.NativeFamily, "derivative_facts", true)
		}
		_ = err
		if !waitContext(ctx, backoff) {
			return
		}
		backoff = nextBackoff(backoff)
	}
}

func (owners *DerivativeOwners) bootstrapOwner(ctx context.Context, owner *derivativeVenueOwner, generation uint64) error {
	requestCtx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	patch, err := owner.feed.Bootstrap(requestCtx)
	if err == nil {
		patch.Generation = generation
		patch.NativeFamily = owner.key.NativeFamily
		err = owners.facts.Apply(patch)
	}
	return err
}

func (owners *DerivativeOwners) observe(key markethub.OwnerKey, state string, reconnects uint64) {
	if owners.observer != nil {
		owners.observer.ObserveMarketFeed(string(key.Venue), key.NativeFamily, "derivative_facts", state, reconnects)
	}
}

// SetHot forwards logical hotset demand to the one broad owner for this native
// family. Feeds without a cadence-sensitive field simply ignore the hint.
func (owners *DerivativeOwners) SetHot(key hotset.Key, active bool) {
	if owners == nil {
		return
	}
	owners.mu.Lock()
	defer owners.mu.Unlock()
	for index := range owners.owners {
		owner := &owners.owners[index]
		if owner.key != key.Owner {
			continue
		}
		if feed, ok := owner.feed.(exchange.DerivativeHotsetFeed); ok {
			feed.SetHotInstrument(key.InstrumentID, active)
		}
		return
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
		result = errors.Join(result, owner.feed.Close())
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
