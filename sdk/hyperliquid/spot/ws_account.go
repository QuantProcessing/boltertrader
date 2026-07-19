package spot

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

// SubscribeSpotState subscribes to authoritative Spot balance updates. The
// documented request field is isPortfolioMargin, while the service currently
// acknowledges it as ignorePortfolioMargin. Keep ignorePortfolioMargin false
// when Portfolio Margin-derived availability must remain enabled.
func (c *WebsocketClient) SubscribeSpotState(user string, ignorePortfolioMargin bool, handler func(hyperliquid.SpotClearinghouseState)) error {
	return c.subscribeSpotState(user, ignorePortfolioMargin, handler, nil, false)
}

func (c *WebsocketClient) SubscribeSpotStateConfirmed(user string, ignorePortfolioMargin bool, handler func(hyperliquid.SpotClearinghouseState)) error {
	return c.subscribeSpotState(user, ignorePortfolioMargin, handler, nil, true)
}

// SubscribeSpotStateWithErrors is the error-aware form for callers that must
// fail closed when a matching-user payload no longer satisfies the wire schema.
func (c *WebsocketClient) SubscribeSpotStateWithErrors(user string, ignorePortfolioMargin bool, handler func(hyperliquid.SpotClearinghouseState), onDecodeError func(error)) error {
	return c.subscribeSpotState(user, ignorePortfolioMargin, handler, onDecodeError, false)
}

// SubscribeSpotStateConfirmedWithErrors combines confirmed startup with
// observable matching-user payload decode failures.
func (c *WebsocketClient) SubscribeSpotStateConfirmedWithErrors(user string, ignorePortfolioMargin bool, handler func(hyperliquid.SpotClearinghouseState), onDecodeError func(error)) error {
	return c.subscribeSpotState(user, ignorePortfolioMargin, handler, onDecodeError, true)
}

func (c *WebsocketClient) subscribeSpotState(user string, ignorePortfolioMargin bool, handler func(hyperliquid.SpotClearinghouseState), onDecodeError func(error), confirmed bool) error {
	sub := map[string]any{
		"type":              "spotState",
		"user":              user,
		"isPortfolioMargin": ignorePortfolioMargin,
	}
	wrapped := func(msg hyperliquid.WsMessage) {
		state, matched, err := decodeSpotStateMessage(msg.Data, user)
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
		return c.SubscribeConfirmed("spotState", sub, wrapped)
	}
	return c.Subscribe("spotState", sub, wrapped)
}

func decodeSpotStateMessage(raw json.RawMessage, user string) (hyperliquid.SpotClearinghouseState, bool, error) {
	var envelope struct {
		User      string          `json:"user"`
		SpotState json.RawMessage `json:"spotState"`
	}
	if err := json.Unmarshal(raw, &envelope); err != nil {
		return hyperliquid.SpotClearinghouseState{}, true, fmt.Errorf("decode spotState envelope: %w", err)
	}
	if strings.TrimSpace(envelope.User) == "" {
		return hyperliquid.SpotClearinghouseState{}, true, fmt.Errorf("decode spotState envelope: user is required")
	}
	if !strings.EqualFold(envelope.User, user) {
		return hyperliquid.SpotClearinghouseState{}, false, nil
	}
	if len(envelope.SpotState) == 0 || string(envelope.SpotState) == "null" {
		return hyperliquid.SpotClearinghouseState{}, true, fmt.Errorf("decode spotState payload: spotState is required")
	}
	var shape struct {
		Balances json.RawMessage `json:"balances"`
	}
	if err := json.Unmarshal(envelope.SpotState, &shape); err != nil {
		return hyperliquid.SpotClearinghouseState{}, true, fmt.Errorf("decode spotState payload: %w", err)
	}
	if len(shape.Balances) == 0 || string(shape.Balances) == "null" {
		return hyperliquid.SpotClearinghouseState{}, true, fmt.Errorf("decode spotState payload: balances is required")
	}
	var state hyperliquid.SpotClearinghouseState
	if err := json.Unmarshal(envelope.SpotState, &state); err != nil {
		return hyperliquid.SpotClearinghouseState{}, true, fmt.Errorf("decode spotState payload: %w", err)
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

func (c *WebsocketClient) UnsubscribeSpotState(user string, ignorePortfolioMargin bool) error {
	return c.Unsubscribe("spotState", map[string]any{
		"type":              "spotState",
		"user":              user,
		"isPortfolioMargin": ignorePortfolioMargin,
	})
}
