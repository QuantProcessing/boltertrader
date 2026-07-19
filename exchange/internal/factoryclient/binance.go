package factoryclient

import (
	"context"

	"github.com/QuantProcessing/boltertrader/exchange"
	binanceperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	binancespot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
)

type binanceSpotClient struct {
	*spotClient
	sdk          *binancespot.Client
	ws           exchange.SpotWebSocket
	commandWSAPI *binancespot.WsAPIClient
	accountWSAPI *binancespot.WsAPIClient
}

func NewBinanceSpot(apiKey, secretKey string, settings Settings) exchange.SpotClient {
	sdkClient := binancespot.NewClient().WithCredentials(apiKey, secretKey)
	wsProfile := binancespot.ProductionEndpoints
	switch settings.Environment {
	case "demo":
		profile, _ := binancespot.EndpointProfileForEnvironment(binancespot.EnvironmentDemo)
		sdkClient.WithBaseURL(profile.RESTBaseURL)
		wsProfile = profile
	default:
		profile, _ := binancespot.EndpointProfileForEnvironment(binancespot.EnvironmentLive)
		sdkClient.WithBaseURL(profile.RESTBaseURL)
		wsProfile = profile
	}
	if settings.Endpoint != "" {
		sdkClient.WithBaseURL(settings.Endpoint)
	}
	if settings.HTTPClient != nil {
		sdkClient.HTTPClient = settings.HTTPClient
	}
	if settings.WebSocketEndpoint != "" {
		wsProfile.WSBaseURL = settings.WebSocketEndpoint
	}
	wsCtx := context.Background()
	commandWSAPI := binancespot.NewWsAPIClient(wsCtx).WithURL(wsProfile.WSAPIBaseURL)
	accountWSAPI := binancespot.NewWsAPIClient(wsCtx).WithURL(wsProfile.WSAPIBaseURL)
	client := &binanceSpotClient{
		spotClient:   &spotClient{meta: clientMeta{venue: exchange.VenueBinance, product: exchange.ProductSpot}},
		sdk:          sdkClient,
		commandWSAPI: commandWSAPI,
		accountWSAPI: accountWSAPI,
	}
	client.ws = newSpotWebSocket(
		newPublicWebSocket(client.meta, newBinanceSpotWSBackend(
			binancespot.NewWsMarketClientWithEndpointProfile(wsCtx, wsProfile),
		)),
		newBinanceSpotPrivateWSBackend(
			commandWSAPI,
			binancespot.NewWsAccountClient(accountWSAPI, apiKey, secretKey),
			apiKey,
			secretKey,
		),
	)
	return client
}

type binancePerpClient struct {
	*perpClient
	sdk *binanceperp.Client
	ws  exchange.PerpWebSocket
}

func NewBinanceUSDPerp(apiKey, secretKey string, settings Settings) exchange.PerpClient {
	sdkClient := binanceperp.NewClient().WithCredentials(apiKey, secretKey)
	wsProfile := binanceperp.USDMMProductionEndpoints
	switch settings.Environment {
	case "demo":
		profile, _ := binanceperp.EndpointProfileForEnvironment(binanceperp.EnvironmentDemo)
		sdkClient.WithEndpointProfile(profile)
		wsProfile = profile
	default:
		profile, _ := binanceperp.EndpointProfileForEnvironment(binanceperp.EnvironmentLive)
		sdkClient.WithEndpointProfile(profile)
		wsProfile = profile
	}
	if settings.Endpoint != "" {
		sdkClient.WithBaseURL(settings.Endpoint)
	}
	if settings.HTTPClient != nil {
		sdkClient.WithHTTPClient(settings.HTTPClient)
	}
	if settings.WebSocketEndpoint != "" {
		wsProfile.WSPublicBaseURL = settings.WebSocketEndpoint
		wsProfile.WSMarketBaseURL = settings.WebSocketEndpoint
		wsProfile.WSMarketFallbackBaseURL = settings.WebSocketEndpoint
	}
	client := &binancePerpClient{
		perpClient: &perpClient{meta: clientMeta{venue: exchange.VenueBinance, product: exchange.ProductPerp}},
		sdk:        sdkClient,
	}
	wsCtx := context.Background()
	wsMarketClient := binanceperp.NewWsMarketClientWithEndpointProfile(wsCtx, wsProfile)
	publicBackend := newBinancePerpWSBackend(
		wsMarketClient,
	)
	if settings.Environment == "demo" {
		publicBackend = newBinancePerpDemoWSBackendWithClient(wsMarketClient)
	}
	client.ws = newPerpWebSocket(
		client.meta,
		publicBackend,
		newBinancePerpPrivateWSBackend(
			binanceperp.NewWsAPIClient(wsCtx).WithURL(wsProfile.WSAPIBaseURL),
			binanceperp.NewWsAccountClientWithEndpointProfile(wsCtx, apiKey, secretKey, wsProfile),
			apiKey,
			secretKey,
		),
	)
	return client
}
