package exchange

import (
	"sort"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/routeprofile"
)

const (
	NativeFamilySpot           = "spot"
	NativeFamilyLinear         = "linear"
	NativeFamilyUSDTFutures    = "usdt-futures"
	NativeFamilyUSDCFutures    = "usdc-futures"
	NativeFamilySwap           = "swap"
	NativeFamilyPerpetual      = "perpetual"
	NativeFamilyUSDT0Perpetual = "usdt0-perpetual"
)

// MarketCapabilities returns a copy of the implemented native market support.
func MarketCapabilities() []MarketCapability {
	capabilities := make([]MarketCapability, 0, 96)
	add := func(venue Venue, family string, dataset MarketDataset, transport MarketTransport, granularity MarketGranularity, cadence time.Duration, topics int, snapshot, sequence bool, operation string) {
		capabilities = append(capabilities, MarketCapability{
			Venue: venue, NativeMarketFamily: family, Dataset: dataset,
			Transport: transport, Granularity: granularity, NominalCadence: cadence,
			MaxTopicsPerConnection: topics, HasSnapshot: snapshot, HasSourceSequence: sequence,
			RateLimitOperation: operation,
		})
	}
	addHot := func(venue Venue, family string, maxTopics int) {
		add(venue, family, DatasetBBO, TransportWebSocket, GranularitySingleInstrument, 0, maxTopics, false, false, "")
		add(venue, family, DatasetDepth, TransportWebSocket, GranularitySingleInstrument, 0, maxTopics, true, true, "")
		add(venue, family, DatasetCandle, TransportWebSocket, GranularitySingleInstrument, 0, maxTopics, false, false, "")
	}
	addHotWithoutDepth := func(venue Venue, family string, maxTopics int) {
		add(venue, family, DatasetBBO, TransportWebSocket, GranularitySingleInstrument, 0, maxTopics, false, false, "")
		add(venue, family, DatasetCandle, TransportWebSocket, GranularitySingleInstrument, 0, maxTopics, false, false, "")
	}
	for _, profile := range routeprofile.DefaultProfiles() {
		if profile.Product != routeprofile.ProductSpot {
			continue
		}
		venue := Venue(profile.Venue)
		if venue == VenueNado {
			// Nado book_depth is delta-only and officially requires a REST
			// MarketLiquidity join. The pure-WS browser depth contract therefore
			// advertises it as unsupported instead of hiding that REST dependency.
			addHotWithoutDepth(venue, NativeFamilySpot, 100)
		} else {
			addHot(venue, NativeFamilySpot, 100)
		}
		add(venue, NativeFamilySpot, DatasetCatalog, TransportREST, GranularityAllMarket, time.Minute, 0, true, false, "instruments")
	}

	for _, venue := range []Venue{VenueBinance, VenueAster} {
		add(venue, NativeFamilyPerpetual, DatasetMark, TransportWebSocket, GranularityAllMarket, time.Second, 0, true, false, "")
		add(venue, NativeFamilyPerpetual, DatasetIndex, TransportWebSocket, GranularityAllMarket, time.Second, 0, true, false, "")
		add(venue, NativeFamilyPerpetual, DatasetFunding, TransportWebSocket, GranularityAllMarket, time.Second, 0, true, false, "")
		addHot(venue, NativeFamilyPerpetual, 1024)
	}
	for _, item := range []struct {
		venue  Venue
		family string
	}{
		{VenueLighter, NativeFamilyPerpetual}, {VenueHyperliquid, NativeFamilyPerpetual},
	} {
		for _, dataset := range []MarketDataset{DatasetMark, DatasetIndex, DatasetFunding, DatasetOpenInterest} {
			add(item.venue, item.family, dataset, TransportWebSocket, GranularityAllMarket, 0, 0, true, false, "")
		}
		addHot(item.venue, item.family, 1024)
	}
	for _, dataset := range []MarketDataset{DatasetMark, DatasetIndex, DatasetFunding, DatasetOpenInterest} {
		add(VenueGate, NativeFamilyPerpetual, dataset, TransportWebSocket, GranularityMultiSymbol, 0, 0, true, false, "")
	}
	addHot(VenueGate, NativeFamilyPerpetual, 200)

	add(VenueNado, NativeFamilyUSDT0Perpetual, DatasetFunding, TransportWebSocket, GranularityAllMarket, 0, 0, true, false, "")
	for _, dataset := range []MarketDataset{DatasetMark, DatasetIndex, DatasetOpenInterest} {
		add(VenueNado, NativeFamilyUSDT0Perpetual, dataset, TransportREST, GranularityMultiSymbol, 15*time.Second, 0, true, false, "contracts")
	}
	addHotWithoutDepth(VenueNado, NativeFamilyUSDT0Perpetual, 100)

	for _, dataset := range []MarketDataset{DatasetMark, DatasetIndex, DatasetFunding, DatasetOpenInterest} {
		add(VenueBybit, NativeFamilyLinear, dataset, TransportREST, GranularityAllMarket, 15*time.Second, 0, true, false, "linear_tickers")
		add(VenueBybit, NativeFamilyLinear, dataset, TransportWebSocket, GranularitySingleInstrument, 0, 100, false, false, "")
	}
	addHot(VenueBybit, NativeFamilyLinear, 100)

	for _, family := range []string{NativeFamilyUSDTFutures, NativeFamilyUSDCFutures} {
		// Bitget's all-tickers response contains mark, index and funding data,
		// but not open interest. OI exists only on the instrument ticker stream;
		// advertising it as an all-market REST fact would turn a broad owner into
		// an accidental N-symbol poller.
		for _, dataset := range []MarketDataset{DatasetMark, DatasetIndex, DatasetFunding} {
			add(VenueBitget, family, dataset, TransportREST, GranularityAllMarket, 15*time.Second, 0, true, false, "all_tickers")
		}
		for _, dataset := range []MarketDataset{DatasetMark, DatasetIndex, DatasetFunding, DatasetOpenInterest} {
			add(VenueBitget, family, dataset, TransportWebSocket, GranularitySingleInstrument, 0, 50, false, false, "")
		}
		addHot(VenueBitget, family, 50)
	}

	for _, dataset := range []MarketDataset{DatasetFunding, DatasetMark, DatasetOpenInterest, DatasetIndex} {
		operation := map[MarketDataset]string{
			DatasetFunding: "funding_rates", DatasetMark: "mark_prices",
			DatasetOpenInterest: "open_interest", DatasetIndex: "index_tickers",
		}[dataset]
		add(VenueOKX, NativeFamilySwap, dataset, TransportREST, GranularityAllMarket, 15*time.Second, 0, true, false, operation)
		add(VenueOKX, NativeFamilySwap, dataset, TransportWebSocket, GranularitySingleInstrument, 0, 100, false, false, "")
	}
	addHot(VenueOKX, NativeFamilySwap, 100)

	owners := map[[2]string]struct{}{}
	for _, capability := range capabilities {
		if capability.NativeMarketFamily != NativeFamilySpot {
			owners[[2]string{string(capability.Venue), capability.NativeMarketFamily}] = struct{}{}
		}
	}
	for owner := range owners {
		add(Venue(owner[0]), owner[1], DatasetCatalog, TransportREST, GranularityAllMarket, time.Minute, 0, true, false, "instruments")
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

// NativeMarketFamily maps route projections onto the exchange's physical connection
// boundary. It is shared by catalog resolution and Feed Owner construction so
// route-specific code cannot accidentally invent a second owner key.
func NativeMarketFamily(venue Venue, product Product, settlement string) string {
	if _, err := routeprofile.DefaultRegistry().ResolveLegacy(string(venue), string(product), settlement); err != nil {
		return ""
	}
	if product == ProductSpot {
		return NativeFamilySpot
	}
	switch venue {
	case VenueBybit:
		return NativeFamilyLinear
	case VenueBitget:
		if strings.EqualFold(strings.TrimSpace(settlement), "USDC") {
			return NativeFamilyUSDCFutures
		}
		return NativeFamilyUSDTFutures
	case VenueOKX:
		return NativeFamilySwap
	case VenueNado:
		return NativeFamilyUSDT0Perpetual
	default:
		return NativeFamilyPerpetual
	}
}

func MarketCapabilitiesFor(venue Venue, family string) []MarketCapability {
	all := MarketCapabilities()
	result := make([]MarketCapability, 0)
	for _, capability := range all {
		if capability.Venue == venue && capability.NativeMarketFamily == family {
			result = append(result, capability)
		}
	}
	return result
}
