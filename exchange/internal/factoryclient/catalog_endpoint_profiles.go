package factoryclient

import (
	"github.com/QuantProcessing/boltertrader/exchange"
	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
	binanceperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	binancespot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
	bitget "github.com/QuantProcessing/boltertrader/sdk/bitget"
	bybit "github.com/QuantProcessing/boltertrader/sdk/bybit"
	gate "github.com/QuantProcessing/boltertrader/sdk/gate"
	hyperliquid "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	"github.com/QuantProcessing/boltertrader/sdk/lighter"
	nadosdk "github.com/QuantProcessing/boltertrader/sdk/nado"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
)

// ApplyDefaultCatalogEndpoints fills a Catalog SourceDescriptor with the same
// SDK endpoint profile that the corresponding product client uses by default.
// It performs no network I/O; it only reads local SDK endpoint constants and
// profile constructors.
//
// Each case used to branch on the environment first. Those branches are gone
// with the endpoints they chose, and the descriptor a caller gets back is the
// venue's real one — there is no longer an argument that could make it anything
// else, which is the point.
func ApplyDefaultCatalogEndpoints(configName string, descriptor *exchange.SourceDescriptor) {
	if descriptor == nil {
		return
	}
	switch configName {
	case "binance-spot":
		profile := binancespot.MainnetEndpointProfile()
		if descriptor.Endpoint == "" {
			descriptor.Endpoint = profile.RESTBaseURL
		}
		if descriptor.WebSocketEndpoint == "" {
			descriptor.WebSocketEndpoint = profile.WSBaseURL
		}
	case "binance-usd-perp":
		profile := binanceperp.MainnetEndpointProfile()
		if descriptor.Endpoint == "" {
			descriptor.Endpoint = profile.RESTBaseURL
		}
		if descriptor.WebSocketEndpoint == "" {
			descriptor.WebSocketEndpoint = profile.WSPublicBaseURL
		}
	case "lighter-spot", "lighter-perp":
		if descriptor.Endpoint == "" {
			descriptor.Endpoint = lighter.MainnetAPIURL
		}
		if descriptor.WebSocketEndpoint == "" {
			descriptor.WebSocketEndpoint = lighter.MainnetWSURL
		}
	case "okx-spot", "okx-usdt-perp":
		endpoints := okx.DefaultEndpointURLs()
		if descriptor.Endpoint == "" {
			descriptor.Endpoint = endpoints.REST
		}
		if descriptor.WebSocketEndpoint == "" {
			descriptor.WebSocketEndpoint = endpoints.WSPublic
		}
	case "hyperliquid-spot", "hyperliquid-perp":
		if descriptor.Endpoint == "" {
			descriptor.Endpoint = hyperliquid.MainnetAPIURL
		}
		if descriptor.WebSocketEndpoint == "" {
			descriptor.WebSocketEndpoint = hyperliquid.MainnetWSURL
		}
	case "bybit-spot", "bybit-usdt-perp", "bybit-usdc-perp":
		profile := bybit.MainnetEnvironmentProfile()
		if descriptor.Endpoint == "" {
			descriptor.Endpoint = profile.RESTBaseURL
		}
		if descriptor.WebSocketEndpoint == "" {
			if configName == "bybit-spot" {
				descriptor.WebSocketEndpoint = profile.PublicSpotWSURL
			} else {
				descriptor.WebSocketEndpoint = profile.PublicLinearWSURL
			}
		}
	case "bitget-spot", "bitget-usdt-perp", "bitget-usdc-perp":
		profile := bitget.MainnetEnvironmentProfile()
		if descriptor.Endpoint == "" {
			descriptor.Endpoint = profile.RESTBaseURL
		}
		if descriptor.WebSocketEndpoint == "" {
			descriptor.WebSocketEndpoint = profile.PublicWSURL
		}
	case "gate-spot", "gate-usdt-perp":
		profile := gate.MainnetEnvironmentProfile()
		if descriptor.Endpoint == "" {
			descriptor.Endpoint = profile.RESTBaseURL
		}
		if descriptor.WebSocketEndpoint == "" {
			if configName == "gate-spot" {
				descriptor.WebSocketEndpoint = profile.SpotWSURL
			} else {
				descriptor.WebSocketEndpoint = profile.FuturesUSDTWSURL
			}
		}
	case "aster-spot", "aster-usdt-perp":
		product := astercommon.ProductSpot
		if configName == "aster-usdt-perp" {
			product = astercommon.ProductPerp
		}
		profile, err := astercommon.NewProfile(product)
		if err != nil {
			return
		}
		if descriptor.Endpoint == "" {
			descriptor.Endpoint = profile.RESTURL()
		}
		if descriptor.WebSocketEndpoint == "" {
			descriptor.WebSocketEndpoint = profile.PublicWSURL()
		}
	case "nado-spot", "nado-usdt0-perp":
		profile, err := nadosdk.NewProfile()
		if err != nil {
			return
		}
		if descriptor.Endpoint == "" {
			descriptor.Endpoint = profile.Endpoint(nadosdk.EndpointGatewayV1)
		}
		if descriptor.WebSocketEndpoint == "" {
			descriptor.WebSocketEndpoint = profile.Endpoint(nadosdk.EndpointSubscriptionsWS)
		}
	}
}
