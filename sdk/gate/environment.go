package sdk

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	defaultTestnetRESTBaseURL      = "https://api-testnet.gateapi.io/api/v4"
	defaultSpotWSURL               = "wss://api.gateio.ws/ws/v4/"
	defaultFuturesUSDTWSURL        = "wss://fx-ws.gateio.ws/v4/ws/usdt"
	defaultTestnetSpotWSURL        = "wss://ws-testnet.gate.com/v4/ws/spot"
	defaultTestnetFuturesUSDTWSURL = "wss://ws-testnet.gate.com/v4/ws/futures/usdt"
)

const (
	ProductSpot        = "spot"
	ProductFuturesUSDT = "futures_usdt"
)

type EnvironmentProfile struct {
	RESTBaseURL      string
	SpotWSURL        string
	FuturesUSDTWSURL string
	OfficialTestnet  bool
}

func MainnetEnvironmentProfile() EnvironmentProfile {
	return EnvironmentProfile{
		RESTBaseURL:      defaultRESTBaseURL,
		SpotWSURL:        defaultSpotWSURL,
		FuturesUSDTWSURL: defaultFuturesUSDTWSURL,
	}
}

func TestnetEnvironmentProfile() EnvironmentProfile {
	return EnvironmentProfile{
		RESTBaseURL:      defaultTestnetRESTBaseURL,
		SpotWSURL:        defaultTestnetSpotWSURL,
		FuturesUSDTWSURL: defaultTestnetFuturesUSDTWSURL,
		OfficialTestnet:  true,
	}
}

func NewTestnetEnvironmentProfile(restBaseURL, spotWSURL, futuresUSDTWSURL string) (EnvironmentProfile, error) {
	for name, raw := range map[string]string{
		"restBaseURL":      restBaseURL,
		"spotWSURL":        spotWSURL,
		"futuresUSDTWSURL": futuresUSDTWSURL,
	} {
		if err := validateURL(name, raw); err != nil {
			return EnvironmentProfile{}, err
		}
	}
	return EnvironmentProfile{
		RESTBaseURL:      strings.TrimRight(restBaseURL, "/"),
		SpotWSURL:        spotWSURL,
		FuturesUSDTWSURL: futuresUSDTWSURL,
		OfficialTestnet:  true,
	}, nil
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

func validateURL(name, raw string) error {
	if strings.TrimSpace(raw) == "" {
		return fmt.Errorf("gate environment profile: %s is required", name)
	}
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme == "" || parsed.Host == "" {
		return fmt.Errorf("gate environment profile: %s has invalid URL", name)
	}
	return nil
}
