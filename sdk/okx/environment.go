package okx

// EndpointURLs is the host family a client talks to. OKX splits its websocket
// surface three ways — public, private, and "business" (candles and other
// derived streams live there, not on public) — so a client needs all four
// hosts, not just a base URL.
type EndpointURLs struct {
	REST       string
	WSPublic   string
	WSPrivate  string
	WSBusiness string
}

// DefaultEndpointURLs is OKX itself.
//
// It used to take an environment and a demo host profile, because OKX's
// simulated trading published two host families of its own — global and EEA —
// on top of the production pair. Simulated trading is gone, and with it the
// only reason this could fail, so it no longer returns an error: a caller that
// had to handle one would be handling a branch that cannot be taken.
func DefaultEndpointURLs() EndpointURLs {
	return EndpointURLs{
		REST:       BaseURL,
		WSPublic:   WSBaseURL,
		WSPrivate:  WSPrivateBaseURL,
		WSBusiness: WSBusinessBaseURL,
	}
}
