package sdk

const (
	ProductTypeUSDTFutures = "USDT-FUTURES"
	ProductTypeUSDCFutures = "USDC-FUTURES"
)

// EnvironmentProfile is the set of hosts one bitget client talks to.
//
// It used to also carry a PAPTrading flag, which turned on a `paptrading: 1`
// request header and switched the public book topic to a shallower one that
// bitget's paper environment was the only place to need. Both are gone with the
// environment that asked for them; what remains is addresses, and a caller that
// overrides one is pointing at a proxy, not at a different venue.
type EnvironmentProfile struct {
	RESTBaseURL  string
	PublicWSURL  string
	PrivateWSURL string
}

func MainnetEnvironmentProfile() EnvironmentProfile {
	return EnvironmentProfile{
		RESTBaseURL:  defaultBaseURL,
		PublicWSURL:  publicWSURL,
		PrivateWSURL: privateWSURL,
	}
}

func (c *Client) WithEnvironmentProfile(profile EnvironmentProfile) *Client {
	c.baseURL = profile.RESTBaseURL
	return c
}

func NewPublicWSClientWithProfile(profile EnvironmentProfile) *PublicWSClient {
	client := NewPublicWSClient()
	client.url = profile.PublicWSURL
	return client
}

func NewPrivateWSClientWithProfile(profile EnvironmentProfile) *PrivateWSClient {
	client := NewPrivateWSClient()
	client.url = profile.PrivateWSURL
	return client
}
