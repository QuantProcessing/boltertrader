package spot

import (
	"encoding/json"

	"github.com/QuantProcessing/exchanges/sdk/hyperliquid"
)

// SubscribeOrderUpdates
func (c *WebsocketClient) SubscribeOrderUpdates(user string, handler func([]hyperliquid.WsOrderUpdate)) error {
	sub := map[string]string{
		"type": "orderUpdates",
		"user": user,
	}

	return c.Subscribe("orderUpdates", sub, func(msg hyperliquid.WsMessage) {
		var data []hyperliquid.WsOrderUpdate
		if err := json.Unmarshal(msg.Data, &data); err != nil {
			return
		}
		handler(data)
	})
}

// SubscribeUserFills
func (c *WebsocketClient) SubscribeUserFills(user string, handler func(hyperliquid.WsUserFills)) error {
	sub := map[string]string{
		"type": "userFills",
		"user": user,
	}

	return c.Subscribe("userFills", sub, func(msg hyperliquid.WsMessage) {
		var data hyperliquid.WsUserFills
		if err := json.Unmarshal(msg.Data, &data); err != nil {
			return
		}
		if data.User == user {
			handler(data)
		}
	})
}

// SubscribeUserEvents
func (c *WebsocketClient) SubscribeUserEvents(user string, handler func(hyperliquid.WsUserEvent)) error {
	sub := map[string]string{
		"type": "user",
		"user": user,
	}

	return c.Subscribe("user", sub, func(msg hyperliquid.WsMessage) {
		var data hyperliquid.WsUserEvent
		if err := json.Unmarshal(msg.Data, &data); err != nil {
			return
		}
		handler(data)
	})
}
