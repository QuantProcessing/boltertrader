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

func (client *okxSpotClient) WebSocket() exchange.SpotWebSocket { return client.ws }

func (client *okxSpotClient) Close() error { return closeSpotWebSocket(client.ws) }

func NewOKXSpot(apiKey, secretKey, passphrase string, settings Settings) exchange.SpotClient {
	sdkClient := okx.NewClient().WithCredentials(apiKey, secretKey, passphrase)
	if settings.Endpoint != "" {
		sdkClient.WithBaseURL(settings.Endpoint)
	}
	if settings.HTTPClient != nil {
		sdkClient.WithHTTPClient(settings.HTTPClient)
	}
	client := &okxSpotClient{
		spotClient: &spotClient{meta: clientMetaFromSettings(exchange.VenueOKX, exchange.ProductSpot, settings)},
		sdk:        sdkClient,
	}
	wsClient := okx.NewWSClient(context.Background())
	if settings.WebSocketEndpoint != "" {
		wsClient.WithURL(settings.WebSocketEndpoint)
	}
	businessWSClient := okx.NewWSClient(context.Background()).WithBusinessURL()
	if settings.WebSocketEndpoint != "" {
		businessWSClient.WithURL(settings.WebSocketEndpoint)
	}
	privateWSClient := okx.NewWSClient(context.Background()).WithCredentials(apiKey, secretKey, passphrase)
	if settings.WebSocketEndpoint != "" {
		privateWSClient.WithURL(settings.WebSocketEndpoint)
	}
	client.ws = newSpotWebSocket(
		newPublicWebSocket(client.meta, newOKXSpotWSBackendWithClients(client.meta, wsClient, businessWSClient)),
		newOKXSpotPrivateWSBackend(client.meta, privateWSClient, okxSpotInstrumentCodeLoader(client.meta), client.okxSpotTradeMode),
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

func (client *okxPerpClient) WebSocket() exchange.PerpWebSocket { return client.ws }

func (client *okxPerpClient) Close() error { return closePerpWebSocket(client.ws) }

func NewOKXUSDTPerp(apiKey, secretKey, passphrase string, settings Settings) exchange.PerpClient {
	sdkClient := okx.NewClient().WithCredentials(apiKey, secretKey, passphrase)
	if settings.Endpoint != "" {
		sdkClient.WithBaseURL(settings.Endpoint)
	}
	if settings.HTTPClient != nil {
		sdkClient.WithHTTPClient(settings.HTTPClient)
	}
	client := &okxPerpClient{
		perpClient: &perpClient{meta: clientMetaFromSettings(exchange.VenueOKX, exchange.ProductPerp, settings)},
		sdk:        sdkClient,
	}
	wsClient := okx.NewWSClient(context.Background())
	if settings.WebSocketEndpoint != "" {
		wsClient.WithURL(settings.WebSocketEndpoint)
	}
	businessWSClient := okx.NewWSClient(context.Background()).WithBusinessURL()
	if settings.WebSocketEndpoint != "" {
		businessWSClient.WithURL(settings.WebSocketEndpoint)
	}
	privateWSClient := okx.NewWSClient(context.Background()).WithCredentials(apiKey, secretKey, passphrase)
	if settings.WebSocketEndpoint != "" {
		privateWSClient.WithURL(settings.WebSocketEndpoint)
	}
	perpMeta := func(ctx context.Context, instrument exchange.Instrument) (okxContractMeta, error) {
		return client.okxPerpMeta(ctx, "WatchPerpReference", instrument)
	}
	client.ws = newPerpWebSocket(client.meta, newOKXPerpWSBackendWithClients(
		client.meta,
		wsClient,
		businessWSClient,
		perpMeta,
	), newOKXPerpPrivateWSBackend(client.meta, privateWSClient, okxPerpInstrumentCodeLoader(client.meta), perpMeta))
	return client
}
