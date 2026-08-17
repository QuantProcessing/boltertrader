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
	wsProfile := binancespot.MainnetEndpointProfile()
	sdkClient.WithBaseURL(wsProfile.RESTBaseURL)
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
	meta := clientMetaFromSettings(exchange.VenueBinance, exchange.ProductSpot, settings)
	client := &binanceSpotClient{
		spotClient:   &spotClient{meta: meta},
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
	wsProfile := binanceperp.MainnetEndpointProfile()
	sdkClient.WithEndpointProfile(wsProfile)
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
	meta := clientMetaFromSettings(exchange.VenueBinance, exchange.ProductPerp, settings)
	client := &binancePerpClient{
		perpClient: &perpClient{meta: meta},
		sdk:        sdkClient,
	}
	wsCtx := context.Background()
	wsMarketClient := binanceperp.NewWsMarketClientWithEndpointProfile(wsCtx, wsProfile)
	publicBackend := newBinancePerpWSBackend(
		wsMarketClient,
	)
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
