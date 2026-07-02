package okx

import "fmt"

const (
	DemoRESTBaseURL    = "https://openapi.okx.com"
	DemoEEARESTBaseURL = "https://eea.okx.com"

	WSDemoBaseURL         = "wss://wspap.okx.com:8443/ws/v5/public"
	WSDemoPrivateBaseURL  = "wss://wspap.okx.com:8443/ws/v5/private"
	WSDemoBusinessBaseURL = "wss://wspap.okx.com:8443/ws/v5/business"

	WSDemoEEABaseURL         = "wss://wseeapap.okx.com:8443/ws/v5/public"
	WSDemoEEAPrivateBaseURL  = "wss://wseeapap.okx.com:8443/ws/v5/private"
	WSDemoEEABusinessBaseURL = "wss://wseeapap.okx.com:8443/ws/v5/business"
)

// DemoHostProfile selects the official OKX Demo host family. OKX publishes
// different Demo hosts for global and regional docs, so SDK callers can select
// the appropriate profile instead of recompiling endpoint constants.
type DemoHostProfile string

const (
	DemoHostProfileGlobal DemoHostProfile = "global"
	DemoHostProfileEEA    DemoHostProfile = "eea"
	DemoHostProfileCustom DemoHostProfile = "custom"
)

type EndpointURLs struct {
	REST       string
	WSPublic   string
	WSPrivate  string
	WSBusiness string
}

func defaultEnvironment(env Environment) Environment {
	if env == "" {
		return Production
	}
	return env
}

func defaultDemoHostProfile(profile DemoHostProfile) DemoHostProfile {
	if profile == "" {
		return DemoHostProfileGlobal
	}
	return profile
}

func DefaultEndpointURLs(env Environment, profile DemoHostProfile) (EndpointURLs, error) {
	switch defaultEnvironment(env) {
	case Production:
		return EndpointURLs{
			REST:       BaseURL,
			WSPublic:   WSBaseURL,
			WSPrivate:  WSPrivateBaseURL,
			WSBusiness: WSBusinessBaseURL,
		}, nil
	case Simulated:
		switch defaultDemoHostProfile(profile) {
		case DemoHostProfileGlobal:
			return EndpointURLs{
				REST:       DemoRESTBaseURL,
				WSPublic:   WSDemoBaseURL,
				WSPrivate:  WSDemoPrivateBaseURL,
				WSBusiness: WSDemoBusinessBaseURL,
			}, nil
		case DemoHostProfileEEA:
			return EndpointURLs{
				REST:       DemoEEARESTBaseURL,
				WSPublic:   WSDemoEEABaseURL,
				WSPrivate:  WSDemoEEAPrivateBaseURL,
				WSBusiness: WSDemoEEABusinessBaseURL,
			}, nil
		case DemoHostProfileCustom:
			return EndpointURLs{}, fmt.Errorf("okx: custom demo host profile requires explicit REST/WS URL overrides")
		default:
			return EndpointURLs{}, fmt.Errorf("okx: unknown demo host profile %q", profile)
		}
	default:
		return EndpointURLs{}, fmt.Errorf("okx: unknown environment %q", env)
	}
}
