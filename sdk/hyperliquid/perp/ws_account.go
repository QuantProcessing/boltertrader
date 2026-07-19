package perp

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
)

// SubscribeOrderUpdates
func (c *WebsocketClient) SubscribeOrderUpdates(user string, handler func([]hyperliquid.WsOrderUpdate)) error {
	return c.subscribeOrderUpdates(user, handler, nil, false)
}

func (c *WebsocketClient) SubscribeOrderUpdatesConfirmed(user string, handler func([]hyperliquid.WsOrderUpdate)) error {
	return c.subscribeOrderUpdates(user, handler, nil, true)
}

func (c *WebsocketClient) SubscribeOrderUpdatesWithErrors(user string, handler func([]hyperliquid.WsOrderUpdate), onDecodeError func(error)) error {
	return c.subscribeOrderUpdates(user, handler, onDecodeError, false)
}

func (c *WebsocketClient) SubscribeOrderUpdatesConfirmedWithErrors(user string, handler func([]hyperliquid.WsOrderUpdate), onDecodeError func(error)) error {
	return c.subscribeOrderUpdates(user, handler, onDecodeError, true)
}

func (c *WebsocketClient) subscribeOrderUpdates(user string, handler func([]hyperliquid.WsOrderUpdate), onDecodeError func(error), confirmed bool) error {
	sub := map[string]string{
		"type": "orderUpdates",
		"user": user,
	}

	wrapped := func(msg hyperliquid.WsMessage) {
		data, err := decodeOrderUpdatesMessage(msg.Data)
		if err != nil {
			if onDecodeError != nil {
				onDecodeError(err)
			}
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
	return c.subscribeUserFills(user, handler, nil, false)
}

func (c *WebsocketClient) SubscribeUserFillsConfirmed(user string, handler func(hyperliquid.WsUserFills)) error {
	return c.subscribeUserFills(user, handler, nil, true)
}

func (c *WebsocketClient) SubscribeUserFillsWithErrors(user string, handler func(hyperliquid.WsUserFills), onDecodeError func(error)) error {
	return c.subscribeUserFills(user, handler, onDecodeError, false)
}

func (c *WebsocketClient) SubscribeUserFillsConfirmedWithErrors(user string, handler func(hyperliquid.WsUserFills), onDecodeError func(error)) error {
	return c.subscribeUserFills(user, handler, onDecodeError, true)
}

func (c *WebsocketClient) subscribeUserFills(user string, handler func(hyperliquid.WsUserFills), onDecodeError func(error), confirmed bool) error {
	sub := map[string]any{
		"type":            "userFills",
		"user":            user,
		"aggregateByTime": false,
	}

	wrapped := func(msg hyperliquid.WsMessage) {
		data, matched, err := decodeUserFillsMessage(msg.Data, user)
		if !matched {
			return
		}
		if err != nil {
			if onDecodeError != nil {
				onDecodeError(err)
			}
			return
		}
		handler(data)
	}
	if confirmed {
		return c.SubscribeConfirmed("userFills", sub, wrapped)
	}
	return c.Subscribe("userFills", sub, wrapped)
}

func decodeOrderUpdatesMessage(raw json.RawMessage) ([]hyperliquid.WsOrderUpdate, error) {
	var data []hyperliquid.WsOrderUpdate
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, fmt.Errorf("decode orderUpdates payload: %w", err)
	}
	return data, nil
}

func decodeUserFillsMessage(raw json.RawMessage, user string) (hyperliquid.WsUserFills, bool, error) {
	var envelope struct {
		User  string          `json:"user"`
		Fills json.RawMessage `json:"fills"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return hyperliquid.WsUserFills{}, true, fmt.Errorf("decode userFills envelope: %w", err)
	}
	if strings.TrimSpace(envelope.User) == "" {
		return hyperliquid.WsUserFills{}, true, fmt.Errorf("decode userFills envelope: user is required")
	}
	if !strings.EqualFold(envelope.User, user) {
		return hyperliquid.WsUserFills{}, false, nil
	}
	if len(envelope.Fills) == 0 || string(envelope.Fills) == "null" {
		return hyperliquid.WsUserFills{}, true, fmt.Errorf("decode userFills payload: fills is required")
	}
	var data hyperliquid.WsUserFills
	if err := json.Unmarshal(raw, &data); err != nil {
		return hyperliquid.WsUserFills{}, true, fmt.Errorf("decode userFills payload: %w", err)
	}
	return data, true, nil
}

// SubscribeClearinghouseState subscribes to the current per-dex account state.
// An empty dex selects Hyperliquid's canonical perp dex.
func (c *WebsocketClient) SubscribeClearinghouseState(user, dex string, handler func(PerpPosition)) error {
	return c.subscribeClearinghouseState(user, dex, handler, nil, false)
}

func (c *WebsocketClient) SubscribeClearinghouseStateConfirmed(user, dex string, handler func(PerpPosition)) error {
	return c.subscribeClearinghouseState(user, dex, handler, nil, true)
}

func (c *WebsocketClient) SubscribeClearinghouseStateWithErrors(user, dex string, handler func(PerpPosition), onDecodeError func(error)) error {
	return c.subscribeClearinghouseState(user, dex, handler, onDecodeError, false)
}

func (c *WebsocketClient) SubscribeClearinghouseStateConfirmedWithErrors(user, dex string, handler func(PerpPosition), onDecodeError func(error)) error {
	return c.subscribeClearinghouseState(user, dex, handler, onDecodeError, true)
}

func (c *WebsocketClient) subscribeClearinghouseState(user, dex string, handler func(PerpPosition), onDecodeError func(error), confirmed bool) error {
	sub := map[string]string{
		"type": "clearinghouseState",
		"user": user,
		"dex":  dex,
	}

	wrapped := func(msg hyperliquid.WsMessage) {
		state, matched, err := decodeClearinghouseStateMessage(msg.Data, user, dex)
		if !matched {
			return
		}
		if err != nil {
			if onDecodeError != nil {
				onDecodeError(err)
			}
			return
		}
		handler(state)
	}
	if confirmed {
		return c.SubscribeConfirmed("clearinghouseState", sub, wrapped)
	}
	return c.Subscribe("clearinghouseState", sub, wrapped)
}

func decodeClearinghouseStateMessage(raw json.RawMessage, user, dex string) (PerpPosition, bool, error) {
	var envelope struct {
		User               string          `json:"user"`
		Dex                string          `json:"dex"`
		ClearinghouseState json.RawMessage `json:"clearinghouseState"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return PerpPosition{}, true, fmt.Errorf("decode clearinghouseState envelope: %w", err)
	}
	if strings.TrimSpace(envelope.User) == "" {
		return PerpPosition{}, true, fmt.Errorf("decode clearinghouseState envelope: user is required")
	}
	if !strings.EqualFold(envelope.User, user) || envelope.Dex != dex {
		return PerpPosition{}, false, nil
	}
	if len(envelope.ClearinghouseState) == 0 || string(envelope.ClearinghouseState) == "null" {
		return PerpPosition{}, true, fmt.Errorf("decode clearinghouseState payload: clearinghouseState is required")
	}
	var state PerpPosition
	if err := json.Unmarshal(envelope.ClearinghouseState, &state); err != nil {
		return PerpPosition{}, true, fmt.Errorf("decode clearinghouseState payload: %w", err)
	}
	return state, true, nil
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
