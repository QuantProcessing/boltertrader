package spot

import "fmt"

type Environment string

const (
	EnvironmentLive Environment = "live"
	EnvironmentDemo Environment = "demo"

	DemoBaseURL      = "https://demo-api.binance.com"
	DemoWSBaseURL    = "wss://demo-stream.binance.com:9443/ws"
	DemoWSAPIBaseURL = "wss://demo-ws-api.binance.com/ws-api/v3"
)

type EndpointProfile struct {
	RESTBaseURL  string
	WSBaseURL    string
	WSAPIBaseURL string
}

var ProductionEndpoints = EndpointProfile{
	RESTBaseURL:  BaseURL,
	WSBaseURL:    WSBaseURL,
	WSAPIBaseURL: WSAPIBaseURL,
}

var DemoEndpoints = EndpointProfile{
	RESTBaseURL:  DemoBaseURL,
	WSBaseURL:    DemoWSBaseURL,
	WSAPIBaseURL: DemoWSAPIBaseURL,
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
		return ProductionEndpoints, nil
	case EnvironmentDemo:
		return DemoEndpoints, nil
	default:
		return EndpointProfile{}, fmt.Errorf("binance spot: unknown environment %q", env)
	}
}
