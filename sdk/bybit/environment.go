package sdk

import "fmt"

const (
	SettleCoinUSDT = "USDT"
	SettleCoinUSDC = "USDC"
)

type EnvironmentProfile struct {
	RESTBaseURL        string
	PublicSpotWSURL    string
	PublicLinearWSURL  string
	PublicInverseWSURL string
	PublicOptionWSURL  string
	PrivateWSURL       string
	TradeWSURL         string
	SupportsWSTrade    bool
}

func MainnetEnvironmentProfile() EnvironmentProfile {
	return EnvironmentProfile{
		RESTBaseURL:        defaultBaseURL,
		PublicSpotWSURL:    publicWSURLSpot,
		PublicLinearWSURL:  publicWSURLLinear,
		PublicInverseWSURL: publicWSURLInverse,
		PublicOptionWSURL:  publicWSURLOption,
		PrivateWSURL:       "wss://stream.bybit.com/v5/private",
		TradeWSURL:         "wss://stream.bybit.com/v5/trade",
		SupportsWSTrade:    true,
	}
}

func DemoEnvironmentProfile() EnvironmentProfile {
	return EnvironmentProfile{
		RESTBaseURL:        "https://api-demo.bybit.com",
		PublicSpotWSURL:    publicWSURLSpot,
		PublicLinearWSURL:  publicWSURLLinear,
		PublicInverseWSURL: publicWSURLInverse,
		PublicOptionWSURL:  publicWSURLOption,
		PrivateWSURL:       "wss://stream-demo.bybit.com/v5/private",
		SupportsWSTrade:    false,
	}
}

func TestnetEnvironmentProfile() EnvironmentProfile {
	return EnvironmentProfile{
		RESTBaseURL:        "https://api-testnet.bybit.com",
		PublicSpotWSURL:    "wss://stream-testnet.bybit.com/v5/public/spot",
		PublicLinearWSURL:  "wss://stream-testnet.bybit.com/v5/public/linear",
		PublicInverseWSURL: "wss://stream-testnet.bybit.com/v5/public/inverse",
		PublicOptionWSURL:  "wss://stream-testnet.bybit.com/v5/public/option",
		PrivateWSURL:       "wss://stream-testnet.bybit.com/v5/private",
		TradeWSURL:         "wss://stream-testnet.bybit.com/v5/trade",
		SupportsWSTrade:    true,
	}
}

func (p EnvironmentProfile) PublicWSURL(category string) string {
	switch category {
	case "spot":
		return p.PublicSpotWSURL
	case "inverse":
		return p.PublicInverseWSURL
	case "option":
		return p.PublicOptionWSURL
	default:
		return p.PublicLinearWSURL
	}
}

func (c *Client) WithEnvironmentProfile(profile EnvironmentProfile) *Client {
	return c.WithBaseURL(profile.RESTBaseURL)
}

func NewPublicWSClientWithProfile(profile EnvironmentProfile, category string) *PublicWSClient {
	client := NewPublicWSClient(category)
	client.url = profile.PublicWSURL(category)
	return client
}

func NewPrivateWSClientWithProfile(profile EnvironmentProfile) *PrivateWSClient {
	client := NewPrivateWSClient()
	client.url = profile.PrivateWSURL
	return client
}

func NewTradeWSClientWithProfile(profile EnvironmentProfile) (*TradeWSClient, error) {
	if !profile.SupportsWSTrade || profile.TradeWSURL == "" {
		return nil, fmt.Errorf("bybit trade ws: environment profile does not support WS Trade")
	}
	client := NewTradeWSClient()
	client.url = profile.TradeWSURL
	return client, nil
}
