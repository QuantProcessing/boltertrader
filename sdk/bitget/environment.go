package sdk

import (
	"fmt"
	"net/url"
	"strings"
)

const (
	ProductTypeUSDTFutures = "USDT-FUTURES"
	ProductTypeUSDCFutures = "USDC-FUTURES"
)

type EnvironmentProfile struct {
	RESTBaseURL     string
	PublicWSURL     string
	PrivateWSURL    string
	PAPTrading      bool
	OfficialTestnet bool
}

func MainnetEnvironmentProfile() EnvironmentProfile {
	return EnvironmentProfile{
		RESTBaseURL:  defaultBaseURL,
		PublicWSURL:  publicWSURL,
		PrivateWSURL: privateWSURL,
	}
}

func DemoEnvironmentProfile() EnvironmentProfile {
	return EnvironmentProfile{
		RESTBaseURL:  defaultBaseURL,
		PublicWSURL:  "wss://wspap.bitget.com/v3/ws/public",
		PrivateWSURL: "wss://wspap.bitget.com/v3/ws/private",
		PAPTrading:   true,
	}
}

func NewTestnetEnvironmentProfile(restBaseURL, publicWSURL, privateWSURL string) (EnvironmentProfile, error) {
	for name, raw := range map[string]string{
		"restBaseURL":  restBaseURL,
		"publicWSURL":  publicWSURL,
		"privateWSURL": privateWSURL,
	} {
		if strings.TrimSpace(raw) == "" {
			return EnvironmentProfile{}, fmt.Errorf("bitget environment profile: %s is required", name)
		}
		parsed, err := url.Parse(raw)
		if err != nil || parsed.Scheme == "" || parsed.Host == "" {
			return EnvironmentProfile{}, fmt.Errorf("bitget environment profile: %s has invalid URL", name)
		}
	}
	return EnvironmentProfile{
		RESTBaseURL:     restBaseURL,
		PublicWSURL:     publicWSURL,
		PrivateWSURL:    privateWSURL,
		OfficialTestnet: true,
	}, nil
}

func (c *Client) WithEnvironmentProfile(profile EnvironmentProfile) *Client {
	c.baseURL = profile.RESTBaseURL
	c.papTrading = profile.PAPTrading
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
