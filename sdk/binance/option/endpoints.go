package option

// REST and WS endpoints for Binance European Options API.
const (
	BaseURL       = "https://eapi.binance.com"
	WSBaseURL     = "wss://nbstream.binance.com/eoptions"
	WSPublicPath  = "/ws" // wss://nbstream.binance.com/eoptions/ws/<stream>
	WSPrivatePath = "/ws" // user data stream uses /ws/<listenKey>
)

// REST endpoint paths.
const (
	EndpointExchangeInfo = "/eapi/v1/exchangeInfo"
	EndpointMark         = "/eapi/v1/mark"
	EndpointDepth        = "/eapi/v1/depth"
	EndpointTicker       = "/eapi/v1/ticker"
	EndpointKlines       = "/eapi/v1/klines"

	EndpointOrder         = "/eapi/v1/order"
	EndpointOpenOrders    = "/eapi/v1/openOrders"
	EndpointHistoryOrders = "/eapi/v1/historyOrders"

	EndpointAccount  = "/eapi/v1/account"
	EndpointPosition = "/eapi/v1/position"

	EndpointListenKey = "/eapi/v1/listenKey"
)
