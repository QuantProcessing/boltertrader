package boltertrader

import (
	"context"
	"fmt"

	"github.com/QuantProcessing/funding-trader/backend/internal/config"
	"github.com/QuantProcessing/funding-trader/backend/internal/marketdata"
)

// PublicCatalogSource deliberately excludes every Gateway private read/write
// method. PublicMarketProvider can discover safe account-scoped instruments,
// but cannot submit or cancel orders.
type PublicCatalogSource interface {
	DiscoverAccounts(context.Context) (AccountDiscovery, error)
	ListInstruments(context.Context, string) ([]InstrumentResource, error)
}

// PublicMarketSource is implemented by the dedicated GET/public-WS client.
// Its method set is the compile-time capability boundary for market ingestion.
type PublicMarketSource interface {
	CurrentFunding(context.Context, marketdata.Instrument) (marketdata.FundingRate, error)
	FundingHistory(context.Context, marketdata.FundingRateHistoryRequest) (marketdata.FundingRatePage, error)
	OrderBook(context.Context, marketdata.OrderBookRequest) (marketdata.OrderBook, error)
	BBO(context.Context, marketdata.Instrument) (marketdata.BBO, error)
	MarkPrice(context.Context, marketdata.Instrument) (marketdata.MarkPrice, error)
	WatchOrderBook(context.Context, marketdata.Instrument) (marketdata.Subscription[marketdata.OrderBook], error)
	WatchBBO(context.Context, marketdata.Instrument) (marketdata.Subscription[marketdata.BBO], error)
	WatchMarkPrice(context.Context, marketdata.Instrument) (marketdata.Subscription[marketdata.MarkPrice], error)
	WatchFundingRate(context.Context, marketdata.Instrument) (marketdata.Subscription[marketdata.FundingRate], error)
}

func NewMarketDataProvider(
	catalog PublicCatalogSource,
	public PublicMarketSource,
	mode config.Mode,
) (*marketdata.Provider, error) {
	source, err := NewPublicMarketProvider(catalog, public, mode)
	if err != nil {
		return nil, err
	}
	provider, err := marketdata.NewProvider(source, source, source, source.Close)
	if err != nil {
		return nil, fmt.Errorf("create marketdata provider: %w", err)
	}
	return provider, nil
}
