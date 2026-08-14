package factory

import (
	"context"
	"net/http"

	"github.com/QuantProcessing/boltertrader/exchange"
)

// PublicFundingConnector owns only credential-free funding rate discovery.
//
// It is the sibling of PublicInstrumentConnector and stays a separate type on
// purpose: that one answers with a catalog, this one with a rate surface, and
// the registry that leases catalogs is typed to the former. Sharing a type
// would mean one of the two lying about what Refresh returns.
type PublicFundingConnector struct {
	loader exchange.FundingBoardLoader
	scope  exchange.CatalogScope
}

func NewPublicFundingConnector(
	venue exchange.Venue,
	product exchange.Product,
	settlement string,
	environment Environment,
	httpClient *http.Client,
) (*PublicFundingConnector, error) {
	configName, ok := publicConnectorConfigName(venue, product, settlement)
	if !ok || httpClient == nil {
		return nil, invalidConfig("public funding connector profile is unsupported")
	}
	settings := newSettings([]Option{WithEnvironment(environment), WithHTTPClient(httpClient)})
	if err := settings.validate(); err != nil {
		return nil, err
	}
	configuredVenue, configuredProduct, configuredSettlement, ok := catalogIdentityForConfig(configName)
	if !ok || configuredVenue != venue || configuredProduct != product || configuredSettlement != settlement {
		return nil, invalidConfig("public funding connector profile is inconsistent")
	}
	scope, _, ok := catalogSourceForConfig(configName, settings)
	if !ok {
		return nil, invalidConfig("public funding connector source is unavailable")
	}
	loader := fundingBoardLoaderForConfig(configName, settings)
	if loader == nil {
		return nil, invalidConfig("public funding connector loader is unavailable")
	}
	return &PublicFundingConnector{loader: loader, scope: scope}, nil
}

// Refresh reads the venue's whole funding surface.
//
// The snapshot is validated against the scope it was requested for before it is
// returned. A board aggregates venues into one table by scope, so a loader that
// answered for the wrong one would file its rates under a neighbouring venue's
// column — a failure that renders as plausible numbers rather than as an error.
func (connector *PublicFundingConnector) Refresh(ctx context.Context) (exchange.FundingBoardSnapshot, error) {
	if connector == nil || connector.loader == nil {
		return exchange.FundingBoardSnapshot{}, invalidConfig("public funding connector is unavailable")
	}
	snapshot, err := connector.loader.LoadFundingBoard(ctx, connector.scope)
	if err != nil {
		return exchange.FundingBoardSnapshot{}, err
	}
	if err := snapshot.Validate(connector.scope); err != nil {
		return exchange.FundingBoardSnapshot{}, err
	}
	return snapshot, nil
}

// Scope reports which venue, environment and settlement asset this connector
// answers for, so a caller assembling a multi-venue board can label the column
// without re-deriving it from the profile it started with.
func (connector *PublicFundingConnector) Scope() exchange.CatalogScope {
	if connector == nil {
		return exchange.CatalogScope{}
	}
	return connector.scope
}

func (*PublicFundingConnector) Close() error { return nil }
