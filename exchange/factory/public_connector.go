package factory

import (
	"context"
	"net/http"

	"github.com/QuantProcessing/boltertrader/exchange"
)

// PublicInstrumentConnector owns only credential-free instrument discovery.
// Private account clients are constructed separately by Gateway runtime groups.
type PublicInstrumentConnector struct {
	loader exchange.InstrumentLoader
	scope  exchange.CatalogScope
}

func NewPublicInstrumentConnector(
	venue exchange.Venue,
	product exchange.Product,
	settlement string,
	environment Environment,
	httpClient *http.Client,
) (*PublicInstrumentConnector, error) {
	configName, ok := publicConnectorConfigName(venue, product, settlement)
	if !ok || httpClient == nil {
		return nil, invalidConfig("public connector profile is unsupported")
	}
	settings := newSettings([]Option{WithEnvironment(environment), WithHTTPClient(httpClient)})
	if err := settings.validate(); err != nil {
		return nil, err
	}
	configuredVenue, configuredProduct, configuredSettlement, ok := catalogIdentityForConfig(configName)
	if !ok || configuredVenue != venue || configuredProduct != product || configuredSettlement != settlement {
		return nil, invalidConfig("public connector profile is inconsistent")
	}
	scope, _, ok := catalogSourceForConfig(configName, settings)
	if !ok {
		return nil, invalidConfig("public connector source is unavailable")
	}
	loader := instrumentLoaderForConfig(configName, settings)
	if loader == nil {
		return nil, invalidConfig("public connector loader is unavailable")
	}
	return &PublicInstrumentConnector{loader: loader, scope: scope}, nil
}

func (connector *PublicInstrumentConnector) Refresh(ctx context.Context) ([]exchange.Instrument, error) {
	if connector == nil || connector.loader == nil {
		return nil, invalidConfig("public connector is unavailable")
	}
	snapshot, err := connector.loader.LoadInstruments(ctx, connector.scope)
	if err != nil {
		return nil, err
	}
	return append([]exchange.Instrument(nil), snapshot.Instruments...), nil
}

func (*PublicInstrumentConnector) Close() error { return nil }

func publicConnectorConfigName(venue exchange.Venue, product exchange.Product, settlement string) (string, bool) {
	switch string(venue) + ":" + string(product) + ":" + settlement {
	case "binance:spot:":
		return "binance-spot", true
	case "binance:perp:USDT":
		return "binance-usd-perp", true
	case "okx:spot:":
		return "okx-spot", true
	case "okx:perp:USDT":
		return "okx-usdt-perp", true
	case "lighter:spot:":
		return "lighter-spot", true
	case "lighter:perp:USDC":
		return "lighter-perp", true
	case "hyperliquid:spot:":
		return "hyperliquid-spot", true
	case "hyperliquid:perp:USDC":
		return "hyperliquid-perp", true
	case "bybit:spot:":
		return "bybit-spot", true
	case "bybit:perp:USDT":
		return "bybit-usdt-perp", true
	case "bybit:perp:USDC":
		return "bybit-usdc-perp", true
	case "bitget:spot:":
		return "bitget-spot", true
	case "bitget:perp:USDT":
		return "bitget-usdt-perp", true
	case "bitget:perp:USDC":
		return "bitget-usdc-perp", true
	case "gate:spot:":
		return "gate-spot", true
	case "gate:perp:USDT":
		return "gate-usdt-perp", true
	case "aster:spot:":
		return "aster-spot", true
	case "aster:perp:USDT":
		return "aster-usdt-perp", true
	case "nado:spot:USDT0":
		return "nado-spot", true
	case "nado:perp:USDT0":
		return "nado-usdt0-perp", true
	default:
		return "", false
	}
}
