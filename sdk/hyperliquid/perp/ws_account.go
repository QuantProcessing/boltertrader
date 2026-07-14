package perp

import (
	"encoding/json"
	"strings"

	"github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

// SubscribeOrderUpdates
func (c *WebsocketClient) SubscribeOrderUpdates(user string, handler func([]hyperliquid.WsOrderUpdate)) error {
	return c.subscribeOrderUpdates(user, handler, false)
}

func (c *WebsocketClient) SubscribeOrderUpdatesConfirmed(user string, handler func([]hyperliquid.WsOrderUpdate)) error {
	return c.subscribeOrderUpdates(user, handler, true)
}

func (c *WebsocketClient) subscribeOrderUpdates(user string, handler func([]hyperliquid.WsOrderUpdate), confirmed bool) error {
	sub := map[string]string{
		"type": "orderUpdates",
		"user": user,
	}

	wrapped := func(msg hyperliquid.WsMessage) {
		var data []hyperliquid.WsOrderUpdate
		if err := json.Unmarshal(msg.Data, &data); err != nil {
			return
		}
		handler(data)
	}
	if confirmed {
		return c.SubscribeConfirmed("orderUpdates", sub, wrapped)
	}
	return c.Subscribe("orderUpdates", sub, wrapped)
}

// SubscribeUserFills
func (c *WebsocketClient) SubscribeUserFills(user string, handler func(hyperliquid.WsUserFills)) error {
	return c.subscribeUserFills(user, handler, false)
}

func (c *WebsocketClient) SubscribeUserFillsConfirmed(user string, handler func(hyperliquid.WsUserFills)) error {
	return c.subscribeUserFills(user, handler, true)
}

func (c *WebsocketClient) subscribeUserFills(user string, handler func(hyperliquid.WsUserFills), confirmed bool) error {
	sub := map[string]any{
		"type":            "userFills",
		"user":            user,
		"aggregateByTime": false,
	}

	wrapped := func(msg hyperliquid.WsMessage) {
		var data hyperliquid.WsUserFills
		if err := json.Unmarshal(msg.Data, &data); err != nil {
			return
		}
		if strings.EqualFold(data.User, user) {
			handler(data)
		}
	}
	if confirmed {
		return c.SubscribeConfirmed("userFills", sub, wrapped)
	}
	return c.Subscribe("userFills", sub, wrapped)
}

// SubscribeClearinghouseState subscribes to the current per-dex account state.
// An empty dex selects Hyperliquid's canonical perp dex.
func (c *WebsocketClient) SubscribeClearinghouseState(user, dex string, handler func(PerpPosition)) error {
	return c.subscribeClearinghouseState(user, dex, handler, false)
}

func (c *WebsocketClient) SubscribeClearinghouseStateConfirmed(user, dex string, handler func(PerpPosition)) error {
	return c.subscribeClearinghouseState(user, dex, handler, true)
}

func (c *WebsocketClient) subscribeClearinghouseState(user, dex string, handler func(PerpPosition), confirmed bool) error {
	sub := map[string]string{
		"type": "clearinghouseState",
		"user": user,
		"dex":  dex,
	}

	wrapped := func(msg hyperliquid.WsMessage) {
		var data struct {
			User               string       `json:"user"`
			Dex                string       `json:"dex"`
			ClearinghouseState PerpPosition `json:"clearinghouseState"`
		}
		if err := json.Unmarshal(msg.Data, &data); err != nil {
			return
		}
		if !strings.EqualFold(data.User, user) || data.Dex != dex {
			return
		}
		handler(data.ClearinghouseState)
	}
	if confirmed {
		return c.SubscribeConfirmed("clearinghouseState", sub, wrapped)
	}
	return c.Subscribe("clearinghouseState", sub, wrapped)
}

// SubscribeUserEvents
func (c *WebsocketClient) SubscribeUserEvents(user string, handler func(hyperliquid.WsUserEvent)) error {
	sub := map[string]string{
		"type": "userEvents",
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

// SubscribeWebData2
func (c *WebsocketClient) SubscribeWebData2(user string, handler func(PerpPosition)) error {
	return c.subscribeWebData2(user, handler, false)
}

func (c *WebsocketClient) SubscribeWebData2Confirmed(user string, handler func(PerpPosition)) error {
	return c.subscribeWebData2(user, handler, true)
}

func (c *WebsocketClient) subscribeWebData2(user string, handler func(PerpPosition), confirmed bool) error {
	sub := map[string]string{
		"type": "webData2",
		"user": user,
	}

	wrapped := func(msg hyperliquid.WsMessage) {
		position, matched := decodeWebData2Message(msg.Data, user)
		if !matched {
			return
		}
		handler(position)
	}
	if confirmed {
		return c.SubscribeConfirmed("webData2", sub, wrapped)
	}
	return c.Subscribe("webData2", sub, wrapped)
}

func decodeWebData2Message(raw json.RawMessage, user string) (PerpPosition, bool) {
	var wrapper struct {
		User               string       `json:"user"`
		ClearinghouseState PerpPosition `json:"clearinghouseState"`
	}
	if err := json.Unmarshal(raw, &wrapper); err != nil || !strings.EqualFold(wrapper.User, user) {
		return PerpPosition{}, false
	}
	return wrapper.ClearinghouseState, true
}

func (c *WebsocketClient) UnsubscribeOrderUpdates(user string) error {
	return c.Unsubscribe("orderUpdates", map[string]string{"type": "orderUpdates", "user": user})
}

func (c *WebsocketClient) UnsubscribeUserFills(user string) error {
	return c.Unsubscribe("userFills", map[string]any{
		"type":            "userFills",
		"user":            user,
		"aggregateByTime": false,
	})
}

func (c *WebsocketClient) UnsubscribeClearinghouseState(user, dex string) error {
	return c.Unsubscribe("clearinghouseState", map[string]string{
		"type": "clearinghouseState",
		"user": user,
		"dex":  dex,
	})
}

func (c *WebsocketClient) UnsubscribeWebData2(user string) error {
	return c.Unsubscribe("webData2", map[string]string{"type": "webData2", "user": user})
}
