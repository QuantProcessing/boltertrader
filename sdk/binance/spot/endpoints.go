package spot

// EndpointProfile is the set of hosts a client talks to. It survives the
// removal of binance's demo endpoints because callers still inject their own
// profile — a recorded transport in tests, a proxy in some deployments — and
// that indirection is independent of which environments binance publishes.
type EndpointProfile struct {
	RESTBaseURL  string
	WSBaseURL    string
	WSAPIBaseURL string
}

// MainnetEndpointProfile is binance spot itself. It is the only profile the SDK
// ships now: `demo-api.binance.com` was a separate host with its own listen-key
// lifecycle and its own outages, and keeping a second set of constants pointed
// at it meant every endpoint change had to be made twice.
func MainnetEndpointProfile() EndpointProfile {
	return EndpointProfile{
		RESTBaseURL:  BaseURL,
		WSBaseURL:    WSBaseURL,
		WSAPIBaseURL: WSAPIBaseURL,
	}
}
