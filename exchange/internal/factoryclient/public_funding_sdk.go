package factoryclient

import (
	"github.com/QuantProcessing/boltertrader/exchange"
	binanceperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	bitget "github.com/QuantProcessing/boltertrader/sdk/bitget"
	bybit "github.com/QuantProcessing/boltertrader/sdk/bybit"
	hyperliquidbase "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	lighter "github.com/QuantProcessing/boltertrader/sdk/lighter"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
)

// Credential-free SDK construction, shared by the funding board and funding
// history loaders.
//
// Each venue spells "point this client at the demo endpoints and give it my
// HTTP client" differently, and each spelling is about ten lines. Two loader
// families times seven venues is fourteen copies of that, which is exactly the
// drift the board's shared helpers were introduced to prevent: the failure mode
// is not a compile error but a history loader quietly reading mainnet while the
// board beside it reads testnet.
//
// Gate is absent because it already has newGateSDK, which takes empty
// credentials for precisely this purpose.

func newBinancePerpPublicSDK(operation string, settings Settings) (*binanceperp.Client, error) {
	sdk := binanceperp.NewClient()
	profile, err := binanceperp.EndpointProfileForEnvironment(binanceperp.Environment(settings.Environment))
	if err != nil {
		return nil, binancePerpInvalidRequest(operation, err.Error())
	}
	sdk.WithEndpointProfile(profile)
	if settings.Endpoint != "" {
		sdk.WithBaseURL(settings.Endpoint)
	}
	if settings.HTTPClient != nil {
		sdk.HTTPClient = settings.HTTPClient
	}
	return sdk, nil
}

func newOKXPublicSDK(settings Settings) *okx.Client {
	sdk := okx.NewClient()
	if settings.Environment == string(exchange.EnvironmentDemo) {
		sdk.WithEnvironment(okx.Simulated)
	} else {
		sdk.WithEnvironment(okx.Production)
	}
	if settings.Endpoint != "" {
		sdk.WithBaseURL(settings.Endpoint)
	}
	if settings.HTTPClient != nil {
		sdk.WithHTTPClient(settings.HTTPClient)
	}
	return sdk
}

func newBybitPublicSDK(settings Settings) *bybit.Client {
	profile := bybit.MainnetEnvironmentProfile()
	switch settings.Environment {
	case string(exchange.EnvironmentDemo):
		profile = bybit.DemoEnvironmentProfile()
	case string(exchange.EnvironmentTestnet):
		profile = bybit.TestnetEnvironmentProfile()
	}
	if settings.Endpoint != "" {
		profile.RESTBaseURL = settings.Endpoint
	}
	sdk := bybit.NewClient().WithEnvironmentProfile(profile)
	if settings.HTTPClient != nil {
		sdk.WithHTTPClient(settings.HTTPClient)
	}
	return sdk
}

func newBitgetPublicSDK(settings Settings) *bitget.Client {
	profile := bitget.MainnetEnvironmentProfile()
	if settings.Environment == string(exchange.EnvironmentDemo) {
		profile = bitget.DemoEnvironmentProfile()
	}
	if settings.Endpoint != "" {
		profile.RESTBaseURL = settings.Endpoint
	}
	sdk := bitget.NewClient().WithEnvironmentProfile(profile)
	if settings.HTTPClient != nil {
		sdk.WithHTTPClient(settings.HTTPClient)
	}
	return sdk
}

func newHyperliquidPublicSDK(settings Settings) *hyperliquidbase.Client {
	base := hyperliquidbase.NewClient()
	if settings.Environment == string(exchange.EnvironmentTestnet) {
		base.WithEnvironment(hyperliquidbase.EnvironmentTestnet)
	} else {
		base.WithEnvironment(hyperliquidbase.EnvironmentMainnet)
	}
	if settings.Endpoint != "" {
		base.BaseURL = settings.Endpoint
	}
	if settings.HTTPClient != nil {
		base.Http = settings.HTTPClient
	}
	return base
}

func newLighterPublicSDK(settings Settings) *lighter.Client {
	sdk := lighter.NewClient()
	if settings.Environment == string(exchange.EnvironmentTestnet) {
		sdk.WithEnvironment(lighter.EnvironmentTestnet)
	} else {
		sdk.WithEnvironment(lighter.EnvironmentMainnet)
	}
	if settings.Endpoint != "" {
		sdk.WithBaseURL(settings.Endpoint)
	}
	sdk.HTTPClient = lighterTrackedHTTPClient(settings.HTTPClient)
	return sdk
}
