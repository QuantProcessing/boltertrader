package hyperliquid

import (
	"fmt"
	"net/url"
)

type Environment string

const (
	EnvironmentMainnet Environment = "mainnet"
	EnvironmentTestnet Environment = "testnet"

	MainnetAPIURL = "https://api.hyperliquid.xyz"
	TestnetAPIURL = "https://api.hyperliquid-testnet.xyz"
	MainnetWSURL  = "wss://api.hyperliquid.xyz/ws"
	TestnetWSURL  = "wss://api.hyperliquid-testnet.xyz/ws"
)

func normalizeEnvironment(env Environment) Environment {
	if env == EnvironmentTestnet {
		return EnvironmentTestnet
	}
	return EnvironmentMainnet
}

func restURLForEnvironment(env Environment) string {
	if normalizeEnvironment(env) == EnvironmentTestnet {
		return TestnetAPIURL
	}
	return MainnetAPIURL
}

func wsURLForEnvironment(env Environment) string {
	if normalizeEnvironment(env) == EnvironmentTestnet {
		return TestnetWSURL
	}
	return MainnetWSURL
}

func deriveWSURL(baseURL string) string {
	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		panic(fmt.Sprintf("invalid URL: %v", err))
	}
	parsedURL.Scheme = "wss"
	parsedURL.Path = "/ws"
	return parsedURL.String()
}
