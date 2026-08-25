package factory

import (
	"net/http"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/exchange/internal/factoryclient"
)

// NewPublicDerivativeFeed builds the canonical adapter output used by the
// Gateway Feed Owner. Raw venue funding/ticker structures stay behind this
// factory boundary.
func NewPublicDerivativeFeed(
	venue exchange.Venue,
	product exchange.Product,
	settlement string,
	environment Environment,
	httpClient *http.Client,
	routeProfileIDs []string,
	nativeFamily string,
	resolver exchange.DerivativeInstrumentResolver,
	capabilities []exchange.MarketCapability,
	requests exchange.MarketRequestCoordinator,
) (exchange.DerivativeFeed, error) {
	configName, ok := publicConnectorConfigName(venue, product, settlement)
	if !ok || httpClient == nil || len(routeProfileIDs) == 0 || nativeFamily == "" || resolver == nil {
		return nil, invalidConfig("public derivative feed profile is unsupported")
	}
	settings := newSettings([]Option{WithEnvironment(environment), WithHTTPClient(httpClient)})
	if err := settings.validate(); err != nil {
		return nil, err
	}
	scope, _, ok := catalogSourceForConfig(configName, settings)
	if !ok {
		return nil, invalidConfig("public derivative feed scope is unavailable")
	}
	adapterSettings := factorySettingsForConfig(configName, settings)
	loader := derivativeSnapshotLoaderForConfig(configName, settings)
	// Bybit's one linear request contains both USDT and USDC rows. A physical
	// owner with both projections must retain the full response and let catalog
	// resolution route each native symbol exactly once.
	if venue == exchange.VenueBybit && len(routeProfileIDs) > 1 {
		scope.Settlement = ""
		loader = factoryclient.NewBybitDerivativeSnapshotLoader("", adapterSettings)
	}
	if loader == nil {
		return nil, invalidConfig("public derivative feed loader is unavailable")
	}
	adapterSettings.MarketCapabilities = append([]exchange.MarketCapability(nil), capabilities...)
	adapterSettings.MarketRequests = requests
	return factoryclient.NewDerivativeFeed(scope, routeProfileIDs, nativeFamily, loader, resolver, adapterSettings), nil
}
