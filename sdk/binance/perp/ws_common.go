package perp

const (
	WSPublicBaseURL  = "wss://fstream.binance.com/public/ws"
	WSMarketBaseURL  = "wss://fstream.binance.com/market/ws"
	WSPrivateBaseURL = "wss://fstream.binance.com/private/ws"
	WSBaseURL        = WSPublicBaseURL
	// WSMarketFallbackBaseURL is a Binance futures-compatible per-stream host.
	// Keep the official routed endpoints as primary and use this only when a
	// primary route is temporarily unreachable from the caller.
	WSMarketFallbackBaseURL = "wss://fstream.binancefuture.com/ws"
	WSAPIBaseURL            = "wss://ws-fapi.binance.com/ws-fapi/v1"

	CoinMWSPublicBaseURL         = "wss://dstream.binance.com/ws"
	CoinMWSMarketBaseURL         = "wss://dstream.binance.com/ws"
	CoinMWSPrivateBaseURL        = "wss://dstream.binance.com/ws"
	CoinMWSMarketFallbackBaseURL = "wss://dstream.binancefuture.com/ws"
	CoinMWSAPIBaseURL            = "wss://ws-dapi.binance.com/ws-dapi/v1"
)

type MsgDispatcher interface {
	Dispatch(data []byte) error
}
