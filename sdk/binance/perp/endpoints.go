package perp

// EndpointProfile is the set of hosts and path shapes a client talks to. It
// survives the removal of binance's demo endpoints because callers still inject
// their own profile — a recorded transport in tests, a proxy in some
// deployments — and that indirection is independent of which environments
// binance publishes.
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

// MainnetEndpointProfile is binance USD-M futures itself. The demo profile it
// replaces was the most expensive of the lot: `demo-fstream` needed a WS
// backend of its own, and its WS-API host was on a third domain again.
func MainnetEndpointProfile() EndpointProfile {
	return EndpointProfile{
		RESTBaseURL:             BaseURL,
		EndpointPrefix:          "/fapi",
		AccountVersion:          "v2",
		WSPublicBaseURL:         WSPublicBaseURL,
		WSMarketBaseURL:         WSMarketBaseURL,
		WSPrivateBaseURL:        WSPrivateBaseURL,
		WSMarketFallbackBaseURL: WSMarketFallbackBaseURL,
		WSAPIBaseURL:            WSAPIBaseURL,
	}
}
