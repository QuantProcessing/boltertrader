package factory

import (
	"net/http"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/exchange/internal/factoryclient"
)

// NewPublicFundingMarketFeed builds one credential-free, route-profile scoped
// realtime funding/mark/index/OI feed. The returned feed owns its WebSocket and
// any bounded REST enrichment workers.
func NewPublicFundingMarketFeed(
	venue exchange.Venue,
	product exchange.Product,
	settlement string,
	environment Environment,
	httpClient *http.Client,
) (exchange.FundingMarketFeed, error) {
	configName, ok := publicConnectorConfigName(venue, product, settlement)
	if !ok || httpClient == nil {
		return nil, invalidConfig("public funding market feed profile is unsupported")
	}
	settings := newSettings([]Option{WithEnvironment(environment), WithHTTPClient(httpClient)})
	if err := settings.validate(); err != nil {
		return nil, err
	}
	scope, _, ok := catalogSourceForConfig(configName, settings)
	if !ok {
		return nil, invalidConfig("public funding market feed scope is unavailable")
	}
	loader := fundingBoardLoaderForConfig(configName, settings)
	if loader == nil {
		return nil, invalidConfig("public funding market feed loader is unavailable")
	}
	return factoryclient.NewFundingMarketFeed(scope, loader, factorySettingsForConfig(configName, settings)), nil
}
