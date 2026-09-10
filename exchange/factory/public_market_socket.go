package factory

import "github.com/QuantProcessing/boltertrader/exchange"

// Embed only public interfaces so the returned concrete method sets cannot
// expose private capabilities from the underlying account-client implementation.
// Subscriptions pass through intact, including replay, status, errors and close.
type marketSocket struct{ exchange.MarketWebSocket }

type perpMarketSocket struct{ exchange.PerpMarketWebSocket }

type referenceMarketSocket struct {
	perpMarketSocket
	exchange.DerivativeReferenceWebSocket
}

func publicMarketSocket(socket exchange.MarketWebSocket) exchange.MarketWebSocket {
	if socket == nil {
		return nil
	}
	if perp, ok := socket.(exchange.PerpMarketWebSocket); ok {
		public := perpMarketSocket{perp}
		if reference, ok := socket.(exchange.DerivativeReferenceWebSocket); ok {
			return referenceMarketSocket{public, reference}
		}
		return public
	}
	return marketSocket{socket}
}
