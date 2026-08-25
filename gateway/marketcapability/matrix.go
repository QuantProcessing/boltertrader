// Package marketcapability contains the static, testable description of each
// venue's native public-market transport. Runtime health and BolterTrader's hot
// subscription policy live elsewhere.
package marketcapability

import (
	"sort"
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

	for _, venue := range []exchange.Venue{exchange.VenueBinance, exchange.VenueAster, exchange.VenueLighter, exchange.VenueHyperliquid, exchange.VenueGate, exchange.VenueNado, exchange.VenueBybit, exchange.VenueBitget, exchange.VenueOKX} {
		family := defaultFamily(venue)
		add(venue, family, exchange.DatasetCatalog, exchange.TransportREST, exchange.GranularityAllMarket, time.Minute, 0, true, false, "instruments")
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

func defaultFamily(venue exchange.Venue) string {
	switch venue {
	case exchange.VenueBybit:
		return FamilyLinear
	case exchange.VenueBitget:
		return FamilyUSDTFutures
	case exchange.VenueOKX:
		return FamilySwap
	case exchange.VenueNado:
		return FamilyUSDT0Perpetual
	default:
		return FamilyPerpetual
	}
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
