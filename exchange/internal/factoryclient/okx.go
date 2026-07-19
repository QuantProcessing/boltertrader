package factoryclient

import (
	"context"
	"sync"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
)

type okxSpotClient struct {
	*spotClient
	sdk       *okx.Client
	ws        exchange.SpotWebSocket
	cacheMu   sync.Mutex
	tradeMode string
	loading   chan struct{}
}

func NewOKXSpot(apiKey, secretKey, passphrase string, settings Settings) exchange.SpotClient {
	sdkClient := okx.NewClient().WithCredentials(apiKey, secretKey, passphrase)
	if settings.Environment == "demo" {
		sdkClient.WithEnvironment(okx.Simulated)
	} else {
		sdkClient.WithEnvironment(okx.Production)
	}
	if settings.Endpoint != "" {
		sdkClient.WithBaseURL(settings.Endpoint)
	}
	if settings.HTTPClient != nil {
		sdkClient.WithHTTPClient(settings.HTTPClient)
	}
	client := &okxSpotClient{
		spotClient: &spotClient{meta: clientMeta{venue: exchange.VenueOKX, product: exchange.ProductSpot}},
		sdk:        sdkClient,
	}
	wsClient := okx.NewWSClient(context.Background())
	if settings.Environment == "demo" {
		wsClient.WithEnvironment(okx.Simulated)
	} else {
		wsClient.WithEnvironment(okx.Production)
	}
	if settings.WebSocketEndpoint != "" {
		wsClient.WithURL(settings.WebSocketEndpoint)
	}
	businessWSClient := okx.NewWSClient(context.Background()).WithBusinessURL()
	if settings.Environment == "demo" {
		businessWSClient.WithEnvironment(okx.Simulated)
	} else {
		businessWSClient.WithEnvironment(okx.Production)
	}
	if settings.WebSocketEndpoint != "" {
		businessWSClient.WithURL(settings.WebSocketEndpoint)
	}
	privateWSClient := okx.NewWSClient(context.Background()).WithCredentials(apiKey, secretKey, passphrase)
	if settings.Environment == "demo" {
		privateWSClient.WithEnvironment(okx.Simulated)
	} else {
		privateWSClient.WithEnvironment(okx.Production)
	}
	if settings.WebSocketEndpoint != "" {
		privateWSClient.WithURL(settings.WebSocketEndpoint)
	}
	client.ws = newSpotWebSocket(
		newPublicWebSocket(client.meta, newOKXSpotWSBackendWithClients(wsClient, businessWSClient)),
		newOKXSpotPrivateWSBackend(privateWSClient, okxSpotInstrumentCodeLoader(sdkClient), client.okxSpotTradeMode),
	)
	return client
}

type okxPerpClient struct {
	*perpClient
	sdk       *okx.Client
	ws        exchange.PerpWebSocket
	cacheMu   sync.Mutex
	contracts map[string]okxContractMeta
	loading   chan struct{}
}

func NewOKXUSDTPerp(apiKey, secretKey, passphrase string, settings Settings) exchange.PerpClient {
	sdkClient := okx.NewClient().WithCredentials(apiKey, secretKey, passphrase)
	if settings.Environment == "demo" {
		sdkClient.WithEnvironment(okx.Simulated)
	} else {
		sdkClient.WithEnvironment(okx.Production)
	}
	if settings.Endpoint != "" {
		sdkClient.WithBaseURL(settings.Endpoint)
	}
	if settings.HTTPClient != nil {
		sdkClient.WithHTTPClient(settings.HTTPClient)
	}
	client := &okxPerpClient{
		perpClient: &perpClient{meta: clientMeta{venue: exchange.VenueOKX, product: exchange.ProductPerp}},
		sdk:        sdkClient,
	}
	wsClient := okx.NewWSClient(context.Background())
	if settings.Environment == "demo" {
		wsClient.WithEnvironment(okx.Simulated)
	} else {
		wsClient.WithEnvironment(okx.Production)
	}
	if settings.WebSocketEndpoint != "" {
		wsClient.WithURL(settings.WebSocketEndpoint)
	}
	businessWSClient := okx.NewWSClient(context.Background()).WithBusinessURL()
	if settings.Environment == "demo" {
		businessWSClient.WithEnvironment(okx.Simulated)
	} else {
		businessWSClient.WithEnvironment(okx.Production)
	}
	if settings.WebSocketEndpoint != "" {
		businessWSClient.WithURL(settings.WebSocketEndpoint)
	}
	privateWSClient := okx.NewWSClient(context.Background()).WithCredentials(apiKey, secretKey, passphrase)
	if settings.Environment == "demo" {
		privateWSClient.WithEnvironment(okx.Simulated)
	} else {
		privateWSClient.WithEnvironment(okx.Production)
	}
	if settings.WebSocketEndpoint != "" {
		privateWSClient.WithURL(settings.WebSocketEndpoint)
	}
	perpMeta := func(ctx context.Context, instrument string) (okxContractMeta, error) {
		return client.okxPerpMeta(ctx, "WatchPerpReference", instrument)
	}
	client.ws = newPerpWebSocket(client.meta, newOKXPerpWSBackendWithClients(
		wsClient,
		businessWSClient,
		perpMeta,
	), newOKXPerpPrivateWSBackend(privateWSClient, okxPerpInstrumentCodeLoader(sdkClient), perpMeta))
	return client
}
