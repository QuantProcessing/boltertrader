package factoryclient

import (
	"context"
	"net/http"
	"sync"

	"github.com/QuantProcessing/boltertrader/exchange"
	hyperliquidbase "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	hyperliquidperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	hyperliquidspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
)

type hyperliquidSpotClient struct {
	*spotClient
	sdk      *hyperliquidspot.Client
	ws       exchange.SpotWebSocket
	cacheMu  sync.Mutex
	loading  chan struct{}
	metadata map[string]hyperliquidMarketMeta
}

func NewHyperliquidSpot(privateKey string, settings Settings) exchange.SpotClient {
	base := hyperliquidbase.NewClient().WithCredentials(privateKey, nil)
	if settings.AccountAddress != "" {
		base.WithAccount(settings.AccountAddress)
	}
	if settings.Environment == "testnet" {
		base.WithEnvironment(hyperliquidbase.EnvironmentTestnet)
	} else {
		base.WithEnvironment(hyperliquidbase.EnvironmentMainnet)
	}
	if settings.Endpoint != "" {
		base.BaseURL = settings.Endpoint
	}
	base.Http = hyperliquidTrackedHTTPClient(settings.HTTPClient)
	client := &hyperliquidSpotClient{
		spotClient: &spotClient{meta: clientMeta{venue: exchange.VenueHyperliquid, product: exchange.ProductSpot}},
		sdk:        hyperliquidspot.NewClient(base),
	}
	client.ws = newSpotWebSocket(
		newPublicWebSocket(client.meta, newHyperliquidSpotWSBackend(client, privateKey, settings)),
		newHyperliquidSpotPrivateWSBackend(client, privateKey, settings),
	)
	return client
}

type hyperliquidPerpClient struct {
	*perpClient
	sdk      *hyperliquidperp.Client
	ws       exchange.PerpWebSocket
	cacheMu  sync.Mutex
	loading  chan struct{}
	metadata map[string]hyperliquidMarketMeta
}

func NewHyperliquidPerp(privateKey string, settings Settings) exchange.PerpClient {
	base := hyperliquidbase.NewClient().WithCredentials(privateKey, nil)
	if settings.AccountAddress != "" {
		base.WithAccount(settings.AccountAddress)
	}
	if settings.Environment == "testnet" {
		base.WithEnvironment(hyperliquidbase.EnvironmentTestnet)
	} else {
		base.WithEnvironment(hyperliquidbase.EnvironmentMainnet)
	}
	if settings.Endpoint != "" {
		base.BaseURL = settings.Endpoint
	}
	base.Http = hyperliquidTrackedHTTPClient(settings.HTTPClient)
	client := &hyperliquidPerpClient{
		perpClient: &perpClient{meta: clientMeta{venue: exchange.VenueHyperliquid, product: exchange.ProductPerp}},
		sdk:        hyperliquidperp.NewClient(base),
	}
	client.ws = newPerpWebSocket(
		client.meta,
		newHyperliquidPerpWSBackend(client, privateKey, settings),
		newHyperliquidPerpPrivateWSBackend(client, privateKey, settings),
	)
	return client
}

type hyperliquidRequestTracker struct {
	mu     sync.Mutex
	status int
}

func (tracker *hyperliquidRequestTracker) setStatus(status int) {
	tracker.mu.Lock()
	tracker.status = status
	tracker.mu.Unlock()
}

func (tracker *hyperliquidRequestTracker) responseStatus() int {
	if tracker == nil {
		return 0
	}
	tracker.mu.Lock()
	defer tracker.mu.Unlock()
	return tracker.status
}

type hyperliquidRequestTrackerKey struct{}

type hyperliquidTrackingTransport struct {
	base http.RoundTripper
}

func hyperliquidTrackedHTTPClient(input *http.Client) *http.Client {
	source := input
	if source == nil {
		source = http.DefaultClient
	}
	clone := *source
	base := clone.Transport
	if base == nil {
		base = http.DefaultTransport
	}
	clone.Transport = hyperliquidTrackingTransport{base: base}
	return &clone
}

func (transport hyperliquidTrackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := transport.base.RoundTrip(req)
	if tracker, _ := req.Context().Value(hyperliquidRequestTrackerKey{}).(*hyperliquidRequestTracker); tracker != nil && resp != nil {
		tracker.setStatus(resp.StatusCode)
	}
	return resp, err
}

func hyperliquidWithRequestTracker(ctx context.Context) (context.Context, *hyperliquidRequestTracker) {
	tracker := &hyperliquidRequestTracker{}
	return context.WithValue(ctx, hyperliquidRequestTrackerKey{}, tracker), tracker
}
