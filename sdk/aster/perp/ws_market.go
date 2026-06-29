package perp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"
)

type WsMarketClient struct {
	*WsClient
}

func NewWsMarketClient(ctx context.Context) *WsMarketClient {
	client := &WsMarketClient{
		WsClient: NewWSClient(ctx, WSBaseURL+"/public/ws"),
	}
	client.WsClient.Logger = zap.NewNop().Sugar().Named("aster-market")
	client.Handler = client.handleMessage
	return client
}

func (c *WsMarketClient) handleMessage(message []byte) {
	c.Logger.Debugw("Received", "msg", string(message))

	// trim space
	message = bytes.TrimSpace(message)
	if len(message) == 0 {
		return
	}

	if message[0] == '[' {
		c.handleArrayMessage(message)
	} else {
		c.handleObjectMessage(message)
	}
}

func (c *WsMarketClient) handleArrayMessage(message []byte) {
	var events []struct {
		EventType string `json:"e"`
		EventTime int64  `json:"E"`
	}
	if err := json.Unmarshal(message, &events); err != nil {
		c.Logger.Errorw("error unmarshalling array message", "error", err)
		return
	}

	if len(events) == 0 {
		return
	}

	// use first event type
	eventType := events[0].EventType
	if eventType == "" {
		c.Logger.Debug("event type not found in array message")
		return
	}

	if eventType == "markPriceUpdate" {
		c.CallSubscription("!markPrice@arr", message)
		c.CallSubscription("!markPrice@arr@1s", message)
		c.CallSubscription("!markPrice@arr@3s", message)
		return
	}

	key := fmt.Sprintf("!%s@arr", eventType)
	c.CallSubscription(key, message)
}

func (c *WsMarketClient) handleObjectMessage(message []byte) {
	var event struct {
		EventType string `json:"e"`
		EventTime int64  `json:"E"`
		Symbol    string `json:"s"`
		// kline specific
		Kline struct {
			Interval string `json:"i"`
		} `json:"k"`
	}
	if err := json.Unmarshal(message, &event); err != nil {
		c.Logger.Errorw("error unmarshalling object message", "error", err)
		return
	}

	// collect all potential keys
	var keys []string

	if event.EventType == "markPriceUpdate" && event.Symbol != "" {
		symbol := strings.ToLower(event.Symbol)
		keys = append(keys, fmt.Sprintf("%s@markPrice", symbol))
		keys = append(keys, fmt.Sprintf("%s@markPrice@1s", symbol))
		keys = append(keys, fmt.Sprintf("%s@markPrice@3s", symbol))
	} else if eventName, ok := SingleEventMap[event.EventType]; ok {
		stream := fmt.Sprintf("%s@%s", strings.ToLower(event.Symbol), eventName)
		keys = append(keys, stream)
	}

	// special handle !bookTicker
	if event.EventType == "bookTicker" {
		keys = append(keys, "!bookTicker")
	}

	// special handle kline
	if event.EventType == "kline" && event.Symbol != "" && event.Kline.Interval != "" {
		stream := fmt.Sprintf("%s@kline_%s", strings.ToLower(event.Symbol), event.Kline.Interval)
		keys = append(keys, stream)
	}

	// dispatch
	for _, key := range keys {
		c.CallSubscription(key, message)
	}

	if len(keys) == 0 {
		c.Logger.Debugw("No routing keys generated for event", "msg", string(message))
	}
}

// SubscribeMarkPrice latest mark price
// interval default 1s, option 3s
func (c *WsMarketClient) SubscribeMarkPrice(symbol string, interval string, callback func(*WsMarkPriceEvent) error) error {
	if interval == "" {
		interval = "1s"
	}
	channel := fmt.Sprintf("%s@markPrice@%s", symbol, interval)
	return c.Subscribe(channel, func(data []byte) error {
		var wsData WsMarkPriceEvent
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(&wsData)
	})
}

// SubscribeAllMarkPrice subscribes to all-market mark price updates, including
// funding rate fields, using Aster's Binance-compatible array stream.
func (c *WsMarketClient) SubscribeAllMarkPrice(interval string, callback func([]*WsMarkPriceEvent) error) error {
	if interval == "" {
		interval = "1s"
	}
	channel := fmt.Sprintf("!markPrice@arr@%s", interval)
	return c.Subscribe(channel, func(data []byte) error {
		var wsData []*WsMarkPriceEvent
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(wsData)
	})
}

// SubscribeIncrementOrderBook interval default 250ms, option 500ms 100ms
// only increment depth
func (c *WsMarketClient) SubscribeIncrementOrderBook(symbol string, interval string, callback func(*WsDepthEvent) error) error {
	channel := fmt.Sprintf("%s@depth@%s", symbol, interval)
	return c.Subscribe(channel, func(data []byte) error {
		var wsData WsDepthEvent
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(&wsData)
	})
}

// SubscribeLimitOrderBook interval default 250ms, option 500ms 100ms
// only limit depth, options: 5  10  20
func (c *WsMarketClient) SubscribeLimitOrderBook(symbol string, levels int, interval string, callback func(*WsDepthEvent) error) error {
	channel := fmt.Sprintf("%s@depth%d@%s", symbol, levels, interval)
	return c.Subscribe(channel, func(data []byte) error {
		var wsData WsDepthEvent
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(&wsData)
	})
}

// SubscribeBookTicker Optimal bid/ask price
func (c *WsMarketClient) SubscribeBookTicker(symbol string, callback func(*WsBookTickerEvent) error) error {
	channel := fmt.Sprintf("%s@bookTicker", symbol)
	return c.Subscribe(channel, func(data []byte) error {
		var wsData WsBookTickerEvent
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(&wsData)
	})
}

func (c *WsMarketClient) SubscribeAggTrade(symbol string, callback func(*WsAggTradeEvent) error) error {
	channel := fmt.Sprintf("%s@aggTrade", symbol)
	return c.Subscribe(channel, func(data []byte) error {
		var wsData WsAggTradeEvent
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(&wsData)
	})
}

func (c *WsMarketClient) SubscribeKline(symbol string, interval string, callback func(*WsKlineEvent) error) error {
	channel := fmt.Sprintf("%s@kline_%s", symbol, interval)
	return c.Subscribe(channel, func(data []byte) error {
		var wsData WsKlineEvent
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(&wsData)
	})
}

// Unsubscribe methods

func (c *WsMarketClient) UnsubscribeMarkPrice(symbol string, interval string) error {
	if interval == "" {
		interval = "1s"
	}
	channel := fmt.Sprintf("%s@markPrice@%s", symbol, interval)
	return c.Unsubscribe(channel)
}

func (c *WsMarketClient) UnsubscribeAllMarkPrice(interval string) error {
	if interval == "" {
		interval = "1s"
	}
	channel := fmt.Sprintf("!markPrice@arr@%s", interval)
	return c.Unsubscribe(channel)
}

func (c *WsMarketClient) UnsubscribeIncrementOrderBook(symbol string, interval string) error {
	channel := fmt.Sprintf("%s@depth@%s", symbol, interval)
	return c.Unsubscribe(channel)
}

func (c *WsMarketClient) UnsubscribeLimitOrderBook(symbol string, levels int, interval string) error {
	channel := fmt.Sprintf("%s@depth%d@%s", symbol, levels, interval)
	return c.Unsubscribe(channel)
}

func (c *WsMarketClient) UnsubscribeBookTicker(symbol string) error {
	channel := fmt.Sprintf("%s@bookTicker", symbol)
	return c.Unsubscribe(channel)
}

func (c *WsMarketClient) UnsubscribeAggTrade(symbol string) error {
	channel := fmt.Sprintf("%s@aggTrade", symbol)
	return c.Unsubscribe(channel)
}

func (c *WsMarketClient) UnsubscribeKline(symbol string, interval string) error {
	channel := fmt.Sprintf("%s@kline_%s", symbol, interval)
	return c.Unsubscribe(channel)
}
