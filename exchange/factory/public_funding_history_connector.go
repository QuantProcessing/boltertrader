package factory

import (
	"context"
	"net/http"

	"github.com/QuantProcessing/boltertrader/exchange"
)

// PublicFundingHistoryConnector owns credential-free reads of one venue's
// settled funding rates.
//
// It stays separate from the resident DerivativeFeed because history is an
// explicitly bounded, single-instrument read, while current derivative facts
// are a continuously maintained native-family projection.
//
// Not every venue reaches this far. NewPublicFundingHistoryConnector fails for
// the ones with no history endpoint rather than constructing a connector that
// answers empty, so "unsupported" is discovered when the connector is built and
// can be reported once, instead of on every read.
type PublicFundingHistoryConnector struct {
	loader exchange.FundingHistoryLoader
	scope  exchange.CatalogScope
}

func NewPublicFundingHistoryConnector(
	venue exchange.Venue,
	product exchange.Product,
	settlement string,
	environment Environment,
	httpClient *http.Client,
) (*PublicFundingHistoryConnector, error) {
	configName, ok := publicConnectorConfigName(venue, product, settlement)
	if !ok || httpClient == nil {
		return nil, invalidConfig("public funding history connector profile is unsupported")
	}
	settings := newSettings([]Option{WithEnvironment(environment), WithHTTPClient(httpClient)})
	if err := settings.validate(); err != nil {
		return nil, err
	}
	configuredVenue, configuredProduct, configuredSettlement, ok := catalogIdentityForConfig(configName)
	if !ok || configuredVenue != venue || configuredProduct != product || configuredSettlement != settlement {
		return nil, invalidConfig("public funding history connector profile is inconsistent")
	}
	scope, _, ok := catalogSourceForConfig(configName, settings)
	if !ok {
		return nil, invalidConfig("public funding history connector source is unavailable")
	}
	loader := fundingHistoryLoaderForConfig(configName, settings)
	if loader == nil {
		return nil, invalidConfig("public funding history connector loader is unavailable")
	}
	return &PublicFundingHistoryConnector{loader: loader, scope: scope}, nil
}

// Load reads one instrument's settled funding rates over the requested window.
//
// The scope is stamped onto the request here rather than accepted from the
// caller, so that the validation on the way back is a real check: the loader is
// handed the connector's own scope and the snapshot must come back carrying it.
// Letting the caller name a scope would make Validate compare a value against
// itself.
func (connector *PublicFundingHistoryConnector) Load(ctx context.Context, request exchange.FundingHistoryRequest) (exchange.FundingHistorySnapshot, error) {
	if connector == nil || connector.loader == nil {
		return exchange.FundingHistorySnapshot{}, invalidConfig("public funding history connector is unavailable")
	}
	request.Scope = connector.scope
	snapshot, err := connector.loader.LoadFundingHistory(ctx, request)
	if err != nil {
		return exchange.FundingHistorySnapshot{}, err
	}
	if err := snapshot.Validate(request); err != nil {
		return exchange.FundingHistorySnapshot{}, err
	}
	return snapshot, nil
}

// Scope reports which venue, environment and settlement asset this connector
// answers for, so a caller plotting two legs on one chart can label each line
// without re-deriving it from the profile it started with.
func (connector *PublicFundingHistoryConnector) Scope() exchange.CatalogScope {
	if connector == nil {
		return exchange.CatalogScope{}
	}
	return connector.scope
}

func (*PublicFundingHistoryConnector) Close() error { return nil }
