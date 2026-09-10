// Package marketcapability applies Gateway runtime policy to exchange support.
// Native protocol facts live in exchange; health is observed by running owners.
package marketcapability

import (
	"fmt"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

const defaultRealtimeFreshness = 5 * time.Second

// FreshnessWindow converts a venue's native cadence into the amount of silence
// a consumer may tolerate before a realtime value is no longer trustworthy.
// Three missed observations distinguishes a delayed packet from a dead feed;
// the floor also avoids flapping on event-driven streams whose matrix cadence
// is intentionally unspecified.
func FreshnessWindow(capability exchange.MarketCapability) time.Duration {
	window := 3 * capability.NominalCadence
	if window < defaultRealtimeFreshness {
		return defaultRealtimeFreshness
	}
	return window
}

type OwnerKey struct {
	Venue        exchange.Venue
	NativeFamily string
}

// Registry validates exchange support and selects runnable Gateway strategies.
// Feed Owners resolve every upstream path through this registry.
type Registry struct {
	byOwner map[OwnerKey]map[exchange.MarketDataset][]exchange.MarketCapability
	all     []exchange.MarketCapability
}

func NewRegistry(capabilities []exchange.MarketCapability) (*Registry, error) {
	registry := &Registry{byOwner: make(map[OwnerKey]map[exchange.MarketDataset][]exchange.MarketCapability), all: append([]exchange.MarketCapability(nil), capabilities...)}
	seen := make(map[[5]string]struct{}, len(capabilities))
	for _, capability := range capabilities {
		if err := capability.Validate(); err != nil {
			return nil, err
		}
		identity := [5]string{string(capability.Venue), capability.NativeMarketFamily, string(capability.Dataset), string(capability.Transport), string(capability.Granularity)}
		if _, duplicate := seen[identity]; duplicate {
			return nil, fmt.Errorf("duplicate market capability")
		}
		seen[identity] = struct{}{}
		owner := OwnerKey{Venue: capability.Venue, NativeFamily: capability.NativeMarketFamily}
		if registry.byOwner[owner] == nil {
			registry.byOwner[owner] = make(map[exchange.MarketDataset][]exchange.MarketCapability)
		}
		registry.byOwner[owner][capability.Dataset] = append(registry.byOwner[owner][capability.Dataset], capability)
	}
	if len(registry.all) == 0 {
		return nil, fmt.Errorf("market capability matrix is empty")
	}
	return registry, nil
}

func DefaultRegistry() (*Registry, error) { return NewRegistry(exchange.MarketCapabilities()) }

func (registry *Registry) Resolve(venue exchange.Venue, family string, dataset exchange.MarketDataset, instrumentScoped bool) (exchange.MarketCapability, error) {
	if registry == nil {
		return exchange.MarketCapability{}, fmt.Errorf("market capability registry is unavailable")
	}
	candidates := registry.byOwner[OwnerKey{Venue: venue, NativeFamily: family}][dataset]
	if len(candidates) == 0 {
		return exchange.MarketCapability{}, fmt.Errorf("market capability is unsupported")
	}
	// A broad Feed Owner must never fall back to a single-instrument path. That
	// silently changes one upstream operation into N operations as the catalog
	// grows. Instrument-scoped consumers may reuse a broad feed, but not vice
	// versa.
	preference := []exchange.MarketGranularity{exchange.GranularityAllMarket, exchange.GranularityMultiSymbol, exchange.GranularityPollOnly}
	if instrumentScoped {
		preference = []exchange.MarketGranularity{exchange.GranularitySingleInstrument, exchange.GranularityAllMarket, exchange.GranularityMultiSymbol, exchange.GranularityPollOnly}
	}
	for _, granularity := range preference {
		for _, capability := range candidates {
			if capability.Granularity == granularity {
				return capability, nil
			}
		}
	}
	return exchange.MarketCapability{}, fmt.Errorf("market capability has no runnable strategy")
}

func (registry *Registry) All() []exchange.MarketCapability {
	if registry == nil {
		return nil
	}
	return append([]exchange.MarketCapability(nil), registry.all...)
}

// HotCapacity returns the native owner's per-connection instrument budget.
// A broad or poll-only owner does not consume per-instrument ticker topics, so
// it receives a deliberately large logical budget; the adapter still shards
// topic-based datasets according to their own matrix rows.
func (registry *Registry) HotCapacity(venue exchange.Venue, family string) int {
	if registry == nil {
		return 0
	}
	owner := registry.byOwner[OwnerKey{Venue: venue, NativeFamily: family}]
	capacity := 0
	for _, dataset := range []exchange.MarketDataset{exchange.DatasetMark, exchange.DatasetBBO} {
		for _, capability := range owner[dataset] {
			if capability.Granularity != exchange.GranularitySingleInstrument || capability.MaxTopicsPerConnection <= 0 {
				continue
			}
			if capacity == 0 || capability.MaxTopicsPerConnection < capacity {
				capacity = capability.MaxTopicsPerConnection
			}
		}
	}
	if capacity == 0 {
		return 1 << 20
	}
	return capacity
}
