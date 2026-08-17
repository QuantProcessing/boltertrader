package sdk

const (
	defaultSpotWSURL        = "wss://api.gateio.ws/ws/v4/"
	defaultFuturesUSDTWSURL = "wss://fx-ws.gateio.ws/v4/ws/usdt"
)

const (
	ProductSpot        = "spot"
	ProductFuturesUSDT = "futures_usdt"
)

// EnvironmentProfile is the set of hosts one gate client talks to.
//
// It used to also carry an OfficialTestnet flag, so that callers could tell a
// profile aimed at gate's own testnet apart from one aimed at a local stub. With
// testnet gone there is only one venue to reach and the distinction has nothing
// left to distinguish, so a profile is now addresses and nothing else.
type EnvironmentProfile struct {
	RESTBaseURL      string
	SpotWSURL        string
	FuturesUSDTWSURL string
}

func MainnetEnvironmentProfile() EnvironmentProfile {
	return EnvironmentProfile{
		RESTBaseURL:      defaultRESTBaseURL,
		SpotWSURL:        defaultSpotWSURL,
		FuturesUSDTWSURL: defaultFuturesUSDTWSURL,
	}
}

func (c *Client) WithEnvironmentProfile(profile EnvironmentProfile) *Client {
	return c.WithBaseURL(profile.RESTBaseURL)
}

func NewWSClientWithProfile(profile EnvironmentProfile, product string) (*WSClient, error) {
	client, err := NewWSClient(product)
	if err != nil {
		return nil, err
	}
	switch product {
	case ProductSpot:
		client.url = profile.SpotWSURL
	case ProductFuturesUSDT:
		client.url = profile.FuturesUSDTWSURL
	}
	return client, nil
}
