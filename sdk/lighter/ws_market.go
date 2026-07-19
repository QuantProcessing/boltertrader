package lighter

import "fmt"

// Public Channel Subscriptions

// SubscribeOrderBook subscribes to the order book channel for a specific market
func (c *WebsocketClient) SubscribeOrderBook(marketId int, cb func([]byte)) error {
	channel := fmt.Sprintf("order_book/%d", marketId)
	return c.Subscribe(channel, nil, cb)
}

// SubscribeTicker subscribes to the best bid/offer ticker channel for a specific market
func (c *WebsocketClient) SubscribeTicker(marketId int, cb func([]byte)) error {
	channel := fmt.Sprintf("ticker/%d", marketId)
	return c.Subscribe(channel, nil, cb)
}

// SubscribeMarketStats subscribes to market stats for a specific market
func (c *WebsocketClient) SubscribeMarketStats(marketId int, cb func([]byte)) error {
	channel := fmt.Sprintf("market_stats/%d", marketId)
	return c.Subscribe(channel, nil, cb)
}

// SubscribeAllMarketStats subscribes to market stats for all markets
func (c *WebsocketClient) SubscribeAllMarketStats(cb func([]byte)) error {
	channel := "market_stats/all"
	return c.Subscribe(channel, nil, cb)
}

// SubscribeSpotMarketStats subscribes to spot market stats for a specific market
func (c *WebsocketClient) SubscribeSpotMarketStats(marketId int, cb func([]byte)) error {
	channel := fmt.Sprintf("spot_market_stats/%d", marketId)
	return c.Subscribe(channel, nil, cb)
}

// SubscribeAllSpotMarketStats subscribes to spot market stats for all spot markets
func (c *WebsocketClient) SubscribeAllSpotMarketStats(cb func([]byte)) error {
	channel := "spot_market_stats/all"
	return c.Subscribe(channel, nil, cb)
}

// SubscribeTrades subscribes to the trades channel for a specific market
func (c *WebsocketClient) SubscribeTrades(marketId int, cb func([]byte)) error {
	channel := fmt.Sprintf("trade/%d", marketId)
	return c.Subscribe(channel, nil, cb)
}

// SubscribeCandle subscribes to live OHLCV updates for a market and resolution.
func (c *WebsocketClient) SubscribeCandle(marketId int, resolution string, cb func([]byte)) error {
	channel := fmt.Sprintf("candle/%d/%s", marketId, resolution)
	return c.Subscribe(channel, nil, cb)
}

// SubscribeHeight subscribes to blockchain height updates
func (c *WebsocketClient) SubscribeHeight(cb func([]byte)) error {
	channel := "height"
	return c.Subscribe(channel, nil, cb)

}

// Unsubscribe methods

func (c *WebsocketClient) UnsubscribeOrderBook(marketId int) error {
	channel := fmt.Sprintf("order_book/%d", marketId)
	return c.Unsubscribe(channel)
}

func (c *WebsocketClient) UnsubscribeTicker(marketId int) error {
	channel := fmt.Sprintf("ticker/%d", marketId)
	return c.Unsubscribe(channel)
}

func (c *WebsocketClient) UnsubscribeMarketStats(marketId int) error {
	channel := fmt.Sprintf("market_stats/%d", marketId)
	return c.Unsubscribe(channel)
}

func (c *WebsocketClient) UnsubscribeAllMarketStats() error {
	return c.Unsubscribe("market_stats/all")
}

func (c *WebsocketClient) UnsubscribeSpotMarketStats(marketId int) error {
	channel := fmt.Sprintf("spot_market_stats/%d", marketId)
	return c.Unsubscribe(channel)
}

func (c *WebsocketClient) UnsubscribeTrades(marketId int) error {
	channel := fmt.Sprintf("trade/%d", marketId)
	return c.Unsubscribe(channel)
}

func (c *WebsocketClient) UnsubscribeCandle(marketId int, resolution string) error {
	channel := fmt.Sprintf("candle/%d/%s", marketId, resolution)
	return c.Unsubscribe(channel)
}
