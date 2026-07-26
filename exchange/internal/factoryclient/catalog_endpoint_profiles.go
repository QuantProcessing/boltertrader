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
func ApplyDefaultCatalogEndpoints(configName string, environment string, descriptor *exchange.SourceDescriptor) {
	if descriptor == nil {
		return
	}
	switch configName {
	case "binance-spot":
		if descriptor.Endpoint == "" {
			profile, _ := binancespot.EndpointProfileForEnvironment(binancespot.Environment(environment))
			descriptor.Endpoint = profile.RESTBaseURL
		}
		if descriptor.WebSocketEndpoint == "" {
			profile, _ := binancespot.EndpointProfileForEnvironment(binancespot.Environment(environment))
			descriptor.WebSocketEndpoint = profile.WSBaseURL
		}
	case "binance-usd-perp":
		if descriptor.Endpoint == "" {
			profile, _ := binanceperp.EndpointProfileForEnvironment(binanceperp.Environment(environment))
			descriptor.Endpoint = profile.RESTBaseURL
		}
		if descriptor.WebSocketEndpoint == "" {
			profile, _ := binanceperp.EndpointProfileForEnvironment(binanceperp.Environment(environment))
			descriptor.WebSocketEndpoint = profile.WSPublicBaseURL
		}
	case "lighter-spot", "lighter-perp":
		if descriptor.Endpoint == "" {
			descriptor.Endpoint = lighter.MainnetAPIURL
			if environment == string(exchange.EnvironmentTestnet) {
				descriptor.Endpoint = lighter.TestnetAPIURL
			}
		}
		if descriptor.WebSocketEndpoint == "" {
			descriptor.WebSocketEndpoint = lighter.MainnetWSURL
			if environment == string(exchange.EnvironmentTestnet) {
				descriptor.WebSocketEndpoint = lighter.TestnetWSURL
			}
		}
	case "okx-spot", "okx-usdt-perp":
		okxEnvironment := okx.Production
		if environment == string(exchange.EnvironmentDemo) {
			okxEnvironment = okx.Simulated
		}
		endpoints, err := okx.DefaultEndpointURLs(okxEnvironment, okx.DemoHostProfileGlobal)
		if err != nil {
			return
		}
		if descriptor.Endpoint == "" {
			descriptor.Endpoint = endpoints.REST
		}
		if descriptor.WebSocketEndpoint == "" {
			descriptor.WebSocketEndpoint = endpoints.WSPublic
		}
	case "hyperliquid-spot", "hyperliquid-perp":
		if descriptor.Endpoint == "" {
			descriptor.Endpoint = hyperliquid.MainnetAPIURL
			if environment == string(exchange.EnvironmentTestnet) {
				descriptor.Endpoint = hyperliquid.TestnetAPIURL
			}
		}
		if descriptor.WebSocketEndpoint == "" {
			descriptor.WebSocketEndpoint = hyperliquid.MainnetWSURL
			if environment == string(exchange.EnvironmentTestnet) {
				descriptor.WebSocketEndpoint = hyperliquid.TestnetWSURL
			}
		}
	case "bybit-spot", "bybit-usdt-perp", "bybit-usdc-perp":
		profile := bybit.MainnetEnvironmentProfile()
		if environment == string(exchange.EnvironmentDemo) {
			profile = bybit.DemoEnvironmentProfile()
		}
		if environment == string(exchange.EnvironmentTestnet) {
			profile = bybit.TestnetEnvironmentProfile()
		}
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
		if environment == string(exchange.EnvironmentDemo) {
			profile = bitget.DemoEnvironmentProfile()
		}
		if descriptor.Endpoint == "" {
			descriptor.Endpoint = profile.RESTBaseURL
		}
		if descriptor.WebSocketEndpoint == "" {
			descriptor.WebSocketEndpoint = profile.PublicWSURL
		}
	case "gate-spot", "gate-usdt-perp":
		profile := gate.MainnetEnvironmentProfile()
		if environment == string(exchange.EnvironmentTestnet) {
			profile = gate.TestnetEnvironmentProfile()
		}
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
	case "aster-spot":
		asterEnvironment := astercommon.EnvironmentProduction
		if environment == string(exchange.EnvironmentTestnet) || environment == string(exchange.EnvironmentDemo) {
			asterEnvironment = astercommon.EnvironmentTestnet
		}
		profile, err := astercommon.NewProfile(asterEnvironment, astercommon.ProductSpot)
		if err != nil {
			return
		}
		if descriptor.Endpoint == "" {
			descriptor.Endpoint = profile.RESTURL()
		}
		if descriptor.WebSocketEndpoint == "" {
			descriptor.WebSocketEndpoint = profile.PublicWSURL()
		}
	case "aster-usdt-perp":
		asterEnvironment := astercommon.EnvironmentProduction
		if environment == string(exchange.EnvironmentTestnet) || environment == string(exchange.EnvironmentDemo) {
			asterEnvironment = astercommon.EnvironmentTestnet
		}
		profile, err := astercommon.NewProfile(asterEnvironment, astercommon.ProductPerp)
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
		nadoEnvironment := nadosdk.EnvironmentMainnet
		if environment == string(exchange.EnvironmentTestnet) || environment == string(exchange.EnvironmentDemo) {
			nadoEnvironment = nadosdk.EnvironmentTestnet
		}
		profile, err := nadosdk.NewProfile(nadoEnvironment)
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
