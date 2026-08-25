package factory

import (
	"context"
	"net/http"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/exchange/internal/factoryclient"
)

// PublicMarketConnector is the credential-free REST surface used by Gateway
// before any account exists. The wrapper deliberately exposes no order or
// account methods even though some venue adapters share an implementation with
// their authenticated clients.
type PublicMarketConnector struct {
	catalog exchange.InstrumentCatalog
	market  exchange.MarketREST
	perp    exchange.PerpREST
	mark    exchange.PerpMarkPriceREST
	socket  exchange.SpotWebSocket
	close   func() error
	scope   exchange.CatalogScope
	funding bool
}

// NewPublicMarketConnector builds one read-only connector for a supported
// venue/product/settlement profile. Construction performs no network I/O and
// requires no credential material.
func NewPublicMarketConnector(
	venue exchange.Venue,
	product exchange.Product,
	settlement string,
	environment Environment,
	httpClient *http.Client,
) (*PublicMarketConnector, error) {
	configName, ok := publicConnectorConfigName(venue, product, settlement)
	if !ok || httpClient == nil {
		return nil, invalidConfig("public market connector profile is unsupported")
	}
	settings := newSettings([]Option{WithEnvironment(environment), WithHTTPClient(httpClient)})
	if err := settings.validate(); err != nil {
		return nil, err
	}
	configuredVenue, configuredProduct, configuredSettlement, ok := catalogIdentityForConfig(configName)
	if !ok || configuredVenue != venue || configuredProduct != product || configuredSettlement != settlement {
		return nil, invalidConfig("public market connector profile is inconsistent")
	}
	scope, descriptor, ok := catalogSourceForConfig(configName, settings)
	if !ok {
		return nil, invalidConfig("public market connector source is unavailable")
	}
	loader := instrumentLoaderForConfig(configName, settings)
	if loader == nil {
		return nil, invalidConfig("public market connector loader is unavailable")
	}
	// Public connectors still need the same canonical directory as authenticated
	// clients. In particular Hyperliquid REST reads resolve the explicit
	// Instrument through that directory before using its native HIP-3 coin name.
	// Keep one private catalog per connector: Refresh populates it once and the
	// Gateway then republishes the returned snapshot into its own public
	// projection. This avoids both a second venue lookup and a process-global
	// catalog shared across unrelated route profiles.
	instrumentCatalog := exchange.NewDefaultMemoryCatalog()
	if err := instrumentCatalog.RegisterSource(scope, descriptor, loader); err != nil {
		return nil, invalidConfig("public market connector catalog is unavailable")
	}
	settings.catalog = instrumentCatalog
	market, perp, mark, socket, closeClient := publicMarketClientForConfig(configName, settings)
	if market == nil || closeClient == nil {
		return nil, invalidConfig("public market connector client is unavailable")
	}
	return &PublicMarketConnector{
		catalog: instrumentCatalog, market: market, perp: perp, mark: mark, socket: socket, close: closeClient,
		scope: scope, funding: derivativeSnapshotLoaderForConfig(configName, settings) != nil,
	}, nil
}

func (connector *PublicMarketConnector) Refresh(ctx context.Context) ([]exchange.Instrument, error) {
	if connector == nil || connector.catalog == nil {
		return nil, invalidConfig("public market connector is unavailable")
	}
	snapshot, err := connector.catalog.Refresh(ctx, connector.scope)
	if err != nil {
		return nil, err
	}
	return append([]exchange.Instrument(nil), snapshot.Instruments...), nil
}

func (connector *PublicMarketConnector) OrderBook(ctx context.Context, request exchange.OrderBookRequest) (exchange.OrderBook, error) {
	if connector == nil || connector.market == nil {
		return exchange.OrderBook{}, invalidConfig("public market connector is unavailable")
	}
	return connector.market.OrderBook(ctx, request)
}

func (connector *PublicMarketConnector) Candles(ctx context.Context, request exchange.CandlesRequest) (exchange.CandlePage, error) {
	if connector == nil || connector.market == nil {
		return exchange.CandlePage{}, invalidConfig("public market connector is unavailable")
	}
	return connector.market.Candles(ctx, request)
}

func (connector *PublicMarketConnector) FundingRate(ctx context.Context, request exchange.FundingRateRequest) (exchange.FundingRate, bool, error) {
	if connector == nil || !connector.funding || connector.perp == nil {
		return exchange.FundingRate{}, false, nil
	}
	rate, err := connector.perp.FundingRate(ctx, request)
	return rate, true, err
}

func (connector *PublicMarketConnector) FundingRateHistory(ctx context.Context, request exchange.FundingRateHistoryRequest) (exchange.FundingRatePage, bool, error) {
	if connector == nil || !connector.funding || connector.perp == nil {
		return exchange.FundingRatePage{}, false, nil
	}
	page, err := connector.perp.FundingRateHistory(ctx, request)
	return page, true, err
}

func (connector *PublicMarketConnector) MarkPrice(ctx context.Context, instrument exchange.Instrument) (exchange.MarkPriceEvent, bool, error) {
	if connector == nil || connector.mark == nil {
		return exchange.MarkPriceEvent{}, false, nil
	}
	mark, err := connector.mark.MarkPrice(ctx, instrument)
	return mark, true, err
}

// WebSocket exposes only the credential-free public stream surface. The
// returned interface includes write methods for historical compatibility, but
// the connector itself still exposes no order method and every stream opened
// here is constructed without credential material.
func (connector *PublicMarketConnector) WebSocket() exchange.SpotWebSocket {
	if connector == nil {
		return nil
	}
	return connector.socket
}

func (connector *PublicMarketConnector) Close() error {
	if connector == nil || connector.close == nil {
		return nil
	}
	return connector.close()
}

func publicMarketClientForConfig(configName string, settings settings) (exchange.MarketREST, exchange.PerpREST, exchange.PerpMarkPriceREST, exchange.SpotWebSocket, func() error) {
	configured := factorySettingsForConfig(configName, settings)
	// The constructors below receive empty credential fields intentionally.
	// Their public REST methods do not sign, and the returned value is sealed
	// behind PublicMarketConnector, whose method set contains no writes.
	var market exchange.MarketREST
	var perp exchange.PerpREST
	var mark exchange.PerpMarkPriceREST
	var socket exchange.SpotWebSocket
	var closeClient func() error
	setSpot := func(client exchange.SpotClient) {
		market, socket, closeClient = client, client.WebSocket(), client.Close
	}
	setPerp := func(client exchange.PerpClient) {
		market, perp, socket, closeClient = client, client, client.WebSocket(), client.Close
		mark, _ = client.(exchange.PerpMarkPriceREST)
	}
	switch configName {
	case "binance-spot":
		setSpot(factoryclient.NewBinanceSpot("", "", configured))
	case "binance-usd-perp":
		setPerp(factoryclient.NewBinanceUSDPerp("", "", configured))
	case "okx-spot":
		setSpot(factoryclient.NewOKXSpot("", "", "", configured))
	case "okx-usdt-perp":
		setPerp(factoryclient.NewOKXUSDTPerp("", "", "", configured))
	case "lighter-spot":
		setSpot(factoryclient.NewLighterSpot("", 0, 0, configured))
	case "lighter-perp":
		setPerp(factoryclient.NewLighterPerp("", 0, 0, configured))
	case "hyperliquid-spot":
		setSpot(factoryclient.NewHyperliquidSpot("", configured))
	case "hyperliquid-perp":
		setPerp(factoryclient.NewHyperliquidPerp("", configured))
	case "bybit-spot":
		setSpot(factoryclient.NewBybitSpot("", "", configured))
	case "bybit-usdt-perp":
		setPerp(factoryclient.NewBybitLinearPerp("", "", "USDT", configured))
	case "bybit-usdc-perp":
		setPerp(factoryclient.NewBybitLinearPerp("", "", "USDC", configured))
	case "bitget-spot":
		setSpot(factoryclient.NewBitgetSpot("", "", "", configured))
	case "bitget-usdt-perp":
		setPerp(factoryclient.NewBitgetPerp("", "", "", "USDT-FUTURES", configured))
	case "bitget-usdc-perp":
		setPerp(factoryclient.NewBitgetPerp("", "", "", "USDC-FUTURES", configured))
	case "gate-spot":
		setSpot(factoryclient.NewGateSpot("", "", configured))
	case "gate-usdt-perp":
		setPerp(factoryclient.NewGateUSDTPerp("", "", configured))
	case "aster-spot":
		setSpot(factoryclient.NewAsterPublicSpot(configured))
	case "aster-usdt-perp":
		setPerp(factoryclient.NewAsterPublicPerp(configured))
	case "nado-spot":
		setSpot(factoryclient.NewNadoSpot("", "", configured))
	case "nado-usdt0-perp":
		setPerp(factoryclient.NewNadoUSDT0Perp("", "", configured))
	default:
		return nil, nil, nil, nil, nil
	}
	if market == nil || closeClient == nil {
		return nil, nil, nil, nil, nil
	}
	return market, perp, mark, socket, closeClient
}
