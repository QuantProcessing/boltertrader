package publicconn

import (
	"context"
	"net/http"

	"github.com/QuantProcessing/boltertrader/exchange"
	exchangefactory "github.com/QuantProcessing/boltertrader/exchange/factory"
	"github.com/QuantProcessing/boltertrader/routeprofile"
)

type ExchangeFactory struct {
	httpClient *http.Client
	trusted    *routeprofile.Registry
}

func NewExchangeFactory(httpClient *http.Client, trusted *routeprofile.Registry) (*ExchangeFactory, error) {
	if httpClient == nil {
		return nil, ErrConnectorUnavailable
	}
	if trusted == nil {
		trusted = routeprofile.DefaultRegistry()
	}
	return &ExchangeFactory{httpClient: httpClient, trusted: trusted}, nil
}

func (factory *ExchangeFactory) Build(ctx context.Context, key routeprofile.Key) (Connector, error) {
	if factory == nil || factory.httpClient == nil || ctx == nil || ctx.Err() != nil {
		return nil, ErrConnectorUnavailable
	}
	profile, err := factory.trusted.Resolve(key.Venue, key.RouteProfileID)
	if err != nil {
		return nil, ErrConnectorUnavailable
	}
	connector, err := exchangefactory.NewPublicInstrumentConnector(
		exchange.Venue(profile.Venue),
		exchange.Product(profile.Product),
		profile.SettlementAsset,
		exchangefactory.EnvironmentLive,
		factory.httpClient,
	)
	if err != nil {
		return nil, ErrConnectorUnavailable
	}
	return connector, nil
}
