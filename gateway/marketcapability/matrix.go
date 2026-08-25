// Package marketcapability contains the static, testable description of each
// venue's native public-market transport. Runtime health and BolterTrader's hot
// subscription policy live elsewhere.
package marketcapability

import (
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

const (
	FamilySpot           = "spot"
	FamilyLinear         = "linear"
	FamilyUSDTFutures    = "usdt-futures"
	FamilyUSDCFutures    = "usdc-futures"
	FamilySwap           = "swap"
	FamilyPerpetual      = "perpetual"
	FamilyUSDT0Perpetual = "usdt0-perpetual"
)

// Matrix returns a copy so callers cannot mutate global capability truth.
func Matrix() []exchange.MarketCapability {
	capabilities := make([]exchange.MarketCapability, 0, 96)
	add := func(venue exchange.Venue, family string, dataset exchange.MarketDataset, transport exchange.MarketTransport, granularity exchange.MarketGranularity, cadence time.Duration, topics int, snapshot, sequence bool, operation string) {
		capabilities = append(capabilities, exchange.MarketCapability{
			Venue: venue, NativeMarketFamily: family, Dataset: dataset,
			Transport: transport, Granularity: granularity, NominalCadence: cadence,
			MaxTopicsPerConnection: topics, HasSnapshot: snapshot, HasSourceSequence: sequence,
			RateLimitOperation: operation,
		})
	}
	addHot := func(venue exchange.Venue, family string, maxTopics int) {
		add(venue, family, exchange.DatasetBBO, exchange.TransportWebSocket, exchange.GranularitySingleInstrument, 0, maxTopics, false, false, "")
		add(venue, family, exchange.DatasetDepth, exchange.TransportWebSocket, exchange.GranularitySingleInstrument, 0, maxTopics, false, true, "")
		add(venue, family, exchange.DatasetCandle, exchange.TransportWebSocket, exchange.GranularitySingleInstrument, 0, maxTopics, false, false, "")
	}
	for _, venue := range []exchange.Venue{exchange.VenueBinance, exchange.VenueAster, exchange.VenueLighter, exchange.VenueHyperliquid, exchange.VenueGate, exchange.VenueNado, exchange.VenueBybit, exchange.VenueBitget, exchange.VenueOKX} {
		addHot(venue, FamilySpot, 100)
		add(venue, FamilySpot, exchange.DatasetCatalog, exchange.TransportREST, exchange.GranularityAllMarket, time.Minute, 0, true, false, "instruments")
	}

	for _, venue := range []exchange.Venue{exchange.VenueBinance, exchange.VenueAster} {
		add(venue, FamilyPerpetual, exchange.DatasetMark, exchange.TransportWebSocket, exchange.GranularityAllMarket, time.Second, 0, true, false, "")
		add(venue, FamilyPerpetual, exchange.DatasetIndex, exchange.TransportWebSocket, exchange.GranularityAllMarket, time.Second, 0, true, false, "")
		add(venue, FamilyPerpetual, exchange.DatasetFunding, exchange.TransportWebSocket, exchange.GranularityAllMarket, time.Second, 0, true, false, "")
		add(venue, FamilyPerpetual, exchange.DatasetOpenInterest, exchange.TransportREST, exchange.GranularityPollOnly, time.Minute, 0, true, false, "open_interest")
		addHot(venue, FamilyPerpetual, 1024)
	}
	for _, item := range []struct {
		venue  exchange.Venue
		family string
	}{
		{exchange.VenueLighter, FamilyPerpetual}, {exchange.VenueHyperliquid, FamilyPerpetual},
	} {
		for _, dataset := range []exchange.MarketDataset{exchange.DatasetMark, exchange.DatasetIndex, exchange.DatasetFunding, exchange.DatasetOpenInterest} {
			add(item.venue, item.family, dataset, exchange.TransportWebSocket, exchange.GranularityAllMarket, 0, 0, true, false, "")
		}
		addHot(item.venue, item.family, 1024)
	}
	for _, dataset := range []exchange.MarketDataset{exchange.DatasetMark, exchange.DatasetIndex, exchange.DatasetFunding, exchange.DatasetOpenInterest} {
		add(exchange.VenueGate, FamilyPerpetual, dataset, exchange.TransportWebSocket, exchange.GranularityMultiSymbol, 0, 0, true, false, "")
	}
	addHot(exchange.VenueGate, FamilyPerpetual, 200)

	add(exchange.VenueNado, FamilyUSDT0Perpetual, exchange.DatasetFunding, exchange.TransportWebSocket, exchange.GranularityAllMarket, 0, 0, true, false, "")
	for _, dataset := range []exchange.MarketDataset{exchange.DatasetMark, exchange.DatasetIndex, exchange.DatasetOpenInterest} {
		add(exchange.VenueNado, FamilyUSDT0Perpetual, dataset, exchange.TransportREST, exchange.GranularityMultiSymbol, 15*time.Second, 0, true, false, "contracts")
	}
	addHot(exchange.VenueNado, FamilyUSDT0Perpetual, 100)

	for _, dataset := range []exchange.MarketDataset{exchange.DatasetMark, exchange.DatasetIndex, exchange.DatasetFunding, exchange.DatasetOpenInterest} {
		add(exchange.VenueBybit, FamilyLinear, dataset, exchange.TransportREST, exchange.GranularityAllMarket, 15*time.Second, 0, true, false, "linear_tickers")
		add(exchange.VenueBybit, FamilyLinear, dataset, exchange.TransportWebSocket, exchange.GranularitySingleInstrument, 0, 100, false, false, "")
	}
	addHot(exchange.VenueBybit, FamilyLinear, 100)

	for _, family := range []string{FamilyUSDTFutures, FamilyUSDCFutures} {
		for _, dataset := range []exchange.MarketDataset{exchange.DatasetMark, exchange.DatasetIndex, exchange.DatasetFunding, exchange.DatasetOpenInterest} {
			add(exchange.VenueBitget, family, dataset, exchange.TransportREST, exchange.GranularityAllMarket, 15*time.Second, 0, true, false, "all_tickers")
			add(exchange.VenueBitget, family, dataset, exchange.TransportWebSocket, exchange.GranularitySingleInstrument, 0, 50, false, false, "")
		}
		addHot(exchange.VenueBitget, family, 50)
	}

	for _, dataset := range []exchange.MarketDataset{exchange.DatasetFunding, exchange.DatasetMark, exchange.DatasetOpenInterest, exchange.DatasetIndex} {
		operation := map[exchange.MarketDataset]string{
			exchange.DatasetFunding: "funding_rates", exchange.DatasetMark: "mark_prices",
			exchange.DatasetOpenInterest: "open_interest", exchange.DatasetIndex: "index_tickers",
		}[dataset]
		add(exchange.VenueOKX, FamilySwap, dataset, exchange.TransportREST, exchange.GranularityAllMarket, 15*time.Second, 0, true, false, operation)
		add(exchange.VenueOKX, FamilySwap, dataset, exchange.TransportWebSocket, exchange.GranularitySingleInstrument, 0, 100, false, false, "")
	}
	addHot(exchange.VenueOKX, FamilySwap, 100)

	owners := map[[2]string]struct{}{}
	for _, capability := range capabilities {
		if capability.NativeMarketFamily != FamilySpot {
			owners[[2]string{string(capability.Venue), capability.NativeMarketFamily}] = struct{}{}
		}
	}
	for owner := range owners {
		add(exchange.Venue(owner[0]), owner[1], exchange.DatasetCatalog, exchange.TransportREST, exchange.GranularityAllMarket, time.Minute, 0, true, false, "instruments")
	}

	sort.Slice(capabilities, func(i, j int) bool {
		left, right := capabilities[i], capabilities[j]
		if left.Venue != right.Venue {
			return left.Venue < right.Venue
		}
		if left.NativeMarketFamily != right.NativeMarketFamily {
			return left.NativeMarketFamily < right.NativeMarketFamily
		}
		if left.Dataset != right.Dataset {
			return left.Dataset < right.Dataset
		}
		return left.Transport < right.Transport
	})
	return capabilities
}

// NativeFamily maps route projections onto the exchange's physical connection
// boundary. It is shared by catalog resolution and Feed Owner construction so
// route-specific code cannot accidentally invent a second owner key.
func NativeFamily(venue exchange.Venue, product exchange.Product, settlement string) string {
	if product == exchange.ProductSpot {
		return FamilySpot
	}
	switch venue {
	case exchange.VenueBybit:
		return FamilyLinear
	case exchange.VenueBitget:
		if strings.EqualFold(strings.TrimSpace(settlement), "USDC") {
			return FamilyUSDCFutures
		}
		return FamilyUSDTFutures
	case exchange.VenueOKX:
		return FamilySwap
	case exchange.VenueNado:
		return FamilyUSDT0Perpetual
	default:
		return FamilyPerpetual
	}
}

type OwnerKey struct {
	Venue        exchange.Venue
	NativeFamily string
}

// Registry is the validated runtime view of Matrix. Production Feed Owners
// resolve every upstream path through this registry; Matrix is no longer only
// documentation exercised by tests.
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

func DefaultRegistry() (*Registry, error) { return NewRegistry(Matrix()) }

func (registry *Registry) Resolve(venue exchange.Venue, family string, dataset exchange.MarketDataset, instrumentScoped bool) (exchange.MarketCapability, error) {
	if registry == nil {
		return exchange.MarketCapability{}, fmt.Errorf("market capability registry is unavailable")
	}
	candidates := registry.byOwner[OwnerKey{Venue: venue, NativeFamily: family}][dataset]
	if len(candidates) == 0 {
		return exchange.MarketCapability{}, fmt.Errorf("market capability is unsupported")
	}
	preference := []exchange.MarketGranularity{exchange.GranularityAllMarket, exchange.GranularityMultiSymbol, exchange.GranularityPollOnly, exchange.GranularitySingleInstrument}
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

func For(venue exchange.Venue, family string) []exchange.MarketCapability {
	all := Matrix()
	result := make([]exchange.MarketCapability, 0)
	for _, capability := range all {
		if capability.Venue == venue && capability.NativeMarketFamily == family {
			result = append(result, capability)
		}
	}
	return result
}
