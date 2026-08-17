package factoryclient

import (
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
// These used to also select an environment, and the reason to share them was
// that a history loader could otherwise end up quietly reading mainnet while the
// board beside it read testnet. That failure is gone with the environments, but
// the sharing is still worth keeping: every venue spells "use my HTTP client"
// and "override the base URL" differently, and two loader families times six
// venues is twelve places for a settings field to be silently dropped. Lighter
// is the clearest case — its HTTP client must go through
// lighterTrackedHTTPClient or the venue's request budget stops being counted.
//
// Gate is absent because it already has newGateSDK, which takes empty
// credentials for precisely this purpose.

func newBinancePerpPublicSDK(settings Settings) *binanceperp.Client {
	sdk := binanceperp.NewClient()
	sdk.WithEndpointProfile(binanceperp.MainnetEndpointProfile())
	if settings.Endpoint != "" {
		sdk.WithBaseURL(settings.Endpoint)
	}
	if settings.HTTPClient != nil {
		sdk.HTTPClient = settings.HTTPClient
	}
	return sdk
}

func newOKXPublicSDK(settings Settings) *okx.Client {
	sdk := okx.NewClient()
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
	if settings.Endpoint != "" {
		sdk.WithBaseURL(settings.Endpoint)
	}
	sdk.HTTPClient = lighterTrackedHTTPClient(settings.HTTPClient)
	return sdk
}
