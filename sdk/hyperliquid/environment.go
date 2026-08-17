package hyperliquid

import (
	"fmt"
	"net/url"
)

// Hyperliquid's mainnet endpoints. These are the only endpoints the SDK can
// reach: the testnet pair, the Environment enum that chose between them, and the
// signing-domain switch that had to agree with that choice are all gone.
const (
	MainnetAPIURL = "https://api.hyperliquid.xyz"
	MainnetWSURL  = "wss://api.hyperliquid.xyz/ws"
)

func deriveWSURL(baseURL string) string {
	parsedURL, err := url.Parse(baseURL)
	if err != nil {
		panic(fmt.Sprintf("invalid URL: %v", err))
	}
	parsedURL.Scheme = "wss"
	parsedURL.Path = "/ws"
	return parsedURL.String()
}
