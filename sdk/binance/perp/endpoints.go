package perp

import "fmt"

type Environment string

const (
	EnvironmentLive Environment = "live"
	EnvironmentDemo Environment = "demo"
)

type EndpointProfile struct {
	RESTBaseURL             string
	EndpointPrefix          string
	AccountVersion          string
	WSPublicBaseURL         string
	WSMarketBaseURL         string
	WSPrivateBaseURL        string
	WSMarketFallbackBaseURL string
	WSAPIBaseURL            string
}

func endpointOrDefault(value, fallback string) string {
	if value != "" {
		return value
	}
	return fallback
}

const (
	DemoBaseURL          = "https://demo-fapi.binance.com"
	DemoWSPublicBaseURL  = "wss://demo-fstream.binance.com/ws"
	DemoWSMarketBaseURL  = "wss://demo-fstream.binance.com/ws"
	DemoWSPrivateBaseURL = "wss://demo-fstream.binance.com/ws"
	DemoWSAPIBaseURL     = "wss://testnet.binancefuture.com/ws-fapi/v1"
)

var USDMMProductionEndpoints = EndpointProfile{
	RESTBaseURL:             BaseURL,
	EndpointPrefix:          "/fapi",
	AccountVersion:          "v2",
	WSPublicBaseURL:         WSPublicBaseURL,
	WSMarketBaseURL:         WSMarketBaseURL,
	WSPrivateBaseURL:        WSPrivateBaseURL,
	WSMarketFallbackBaseURL: WSMarketFallbackBaseURL,
	WSAPIBaseURL:            WSAPIBaseURL,
}

var USDMMDemoEndpoints = EndpointProfile{
	RESTBaseURL:             DemoBaseURL,
	EndpointPrefix:          "/fapi",
	AccountVersion:          "v2",
	WSPublicBaseURL:         DemoWSPublicBaseURL,
	WSMarketBaseURL:         DemoWSMarketBaseURL,
	WSPrivateBaseURL:        DemoWSPrivateBaseURL,
	WSMarketFallbackBaseURL: DemoWSMarketBaseURL,
	WSAPIBaseURL:            DemoWSAPIBaseURL,
}

func DefaultEnvironment(env Environment) Environment {
	if env == "" {
		return EnvironmentLive
	}
	return env
}

func EndpointProfileForEnvironment(env Environment) (EndpointProfile, error) {
	switch DefaultEnvironment(env) {
	case EnvironmentLive:
		return USDMMProductionEndpoints, nil
	case EnvironmentDemo:
		return USDMMDemoEndpoints, nil
	default:
		return EndpointProfile{}, fmt.Errorf("binance perp: unknown environment %q", env)
	}
}
