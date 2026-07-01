package perp

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

const (
	DemoBaseURL          = "https://demo-fapi.binance.com"
	DemoWSPublicBaseURL  = "wss://demo-fstream.binance.com/ws"
	DemoWSMarketBaseURL  = "wss://demo-fstream.binance.com/ws"
	DemoWSPrivateBaseURL = "wss://demo-fstream.binance.com/ws"
	DemoWSAPIBaseURL     = "wss://testnet.binancefuture.com/ws-fapi/v1"
)

var USDMMProductionEndpoints = EndpointProfile{
	RESTBaseURL:             BaseURL,
	EndpointPrefix:          "/fapi",
	AccountVersion:          "v2",
	WSPublicBaseURL:         WSPublicBaseURL,
	WSMarketBaseURL:         WSMarketBaseURL,
	WSPrivateBaseURL:        WSPrivateBaseURL,
	WSMarketFallbackBaseURL: WSMarketFallbackBaseURL,
	WSAPIBaseURL:            WSAPIBaseURL,
}

var USDMMDemoEndpoints = EndpointProfile{
	RESTBaseURL:             DemoBaseURL,
	EndpointPrefix:          "/fapi",
	AccountVersion:          "v2",
	WSPublicBaseURL:         DemoWSPublicBaseURL,
	WSMarketBaseURL:         DemoWSMarketBaseURL,
	WSPrivateBaseURL:        DemoWSPrivateBaseURL,
	WSMarketFallbackBaseURL: DemoWSMarketBaseURL,
	WSAPIBaseURL:            DemoWSAPIBaseURL,
}
