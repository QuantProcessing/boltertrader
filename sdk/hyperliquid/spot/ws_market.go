package spot

import (
	"encoding/json"

	"github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

// Helper to subscribe to L2Book
func (c *WebsocketClient) SubscribeL2Book(coin string, handler func(hyperliquid.WsL2Book)) error {
	sub := map[string]string{
		"type": "l2Book",
		"coin": coin,
	}

	return c.WebsocketClient.Subscribe("l2Book", sub, func(msg hyperliquid.WsMessage) {
		var data hyperliquid.WsL2Book
		if err := json.Unmarshal(msg.Data, &data); err != nil {
			return
		}
		if data.Coin == coin {
			handler(data)
		}
	})
}

// SubscribeTrades
func (c *WebsocketClient) SubscribeTrades(coin string, handler func([]hyperliquid.WsTrade)) error {
	sub := map[string]string{
		"type": "trades",
		"coin": coin,
	}

	return c.WebsocketClient.Subscribe("trades", sub, func(msg hyperliquid.WsMessage) {
		var data []hyperliquid.WsTrade
		if err := json.Unmarshal(msg.Data, &data); err != nil {
			return
		}
		if len(data) > 0 && data[0].Coin == coin {
			handler(data)
		}
	})
}

func (c *WebsocketClient) SubscribeBbo(coin string, handler func(hyperliquid.WsBbo)) error {
	sub := map[string]string{
		"type": "bbo",
		"coin": coin,
	}

	return c.WebsocketClient.Subscribe("bbo", sub, func(msg hyperliquid.WsMessage) {
		var data hyperliquid.WsBbo
		if err := json.Unmarshal(msg.Data, &data); err != nil {
			return
		}
		if data.Coin == coin {
			handler(data)
		}
	})
}

func (c *WebsocketClient) SubscribeAllMids(handler func(hyperliquid.WsAllMids)) error {
	sub := map[string]string{
		"type": "allMids",
	}
	return c.WebsocketClient.Subscribe("allMids", sub, func(msg hyperliquid.WsMessage) {
		var data hyperliquid.WsAllMids
		if err := json.Unmarshal(msg.Data, &data); err != nil {
			return
		}
		handler(data)
	})
}

func (c *WebsocketClient) SubscribeAllMidsWithDex(dex string, handler func(hyperliquid.WsAllMids)) error {
	sub := map[string]string{
		"type": "allMids",
		"dex":  dex,
	}
	return c.WebsocketClient.Subscribe("allMids", sub, func(msg hyperliquid.WsMessage) {
		var data hyperliquid.WsAllMids
		if err := json.Unmarshal(msg.Data, &data); err != nil {
			return
		}
		handler(data)
	})
}

func (c *WebsocketClient) SubscribeCandle(coin string, interval string, handler func(hyperliquid.WsCandle)) error {
	sub := map[string]string{
		"type":     "candle",
		"coin":     coin,
		"interval": interval,
	}

	return c.WebsocketClient.Subscribe("candle", sub, func(msg hyperliquid.WsMessage) {
		var data hyperliquid.WsCandle
		if err := json.Unmarshal(msg.Data, &data); err != nil {
			return
		}
		if data.S == coin && data.I == interval {
			handler(data)
		}
	})
}

// Unsubscribe methods

func (c *WebsocketClient) UnsubscribeL2Book(coin string) error {
	sub := map[string]string{
		"type": "l2Book",
		"coin": coin,
	}
	return c.WebsocketClient.Unsubscribe("l2Book", sub)
}

func (c *WebsocketClient) UnsubscribeTrades(coin string) error {
	sub := map[string]string{
		"type": "trades",
		"coin": coin,
	}
	return c.WebsocketClient.Unsubscribe("trades", sub)
}

func (c *WebsocketClient) UnsubscribeBbo(coin string) error {
	sub := map[string]string{
		"type": "bbo",
		"coin": coin,
	}
	return c.WebsocketClient.Unsubscribe("bbo", sub)
}

func (c *WebsocketClient) UnsubscribeAllMids() error {
	sub := map[string]string{
		"type": "allMids",
	}
	return c.WebsocketClient.Unsubscribe("allMids", sub)
}

func (c *WebsocketClient) UnsubscribeAllMidsWithDex(dex string) error {
	sub := map[string]string{
		"type": "allMids",
		"dex":  dex,
	}
	return c.WebsocketClient.Unsubscribe("allMids", sub)
}

func (c *WebsocketClient) UnsubscribeCandle(coin string, interval string) error {
	sub := map[string]string{
		"type":     "candle",
		"coin":     coin,
		"interval": interval,
	}
	return c.WebsocketClient.Unsubscribe("candle", sub)
}
