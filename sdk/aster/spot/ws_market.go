package spot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

type WsMarketClient struct {
	*WsClient
}

func NewWsMarketClient(ctx context.Context) *WsMarketClient {
	// Market data is public, no auth needed usually
	// Use Combined Stream URL for multiplexing
	client := NewWSClient(ctx, WSBaseURL)
	mc := &WsMarketClient{WsClient: client}

	client.Handler = mc.handleMessage
	return mc
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

	eventName, ok := SingleEventMap[event.EventType]
	if ok {
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

// SubscribeBookTicker
func (c *WsMarketClient) SubscribeBookTicker(symbol string, handler func(*BookTickerEvent) error) error {
	stream := fmt.Sprintf("%s@bookTicker", strings.ToLower(symbol))

	// Register handler for this stream/event
	c.SetHandler("bookTicker", func(data []byte) error {
		var event BookTickerEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		if event.Symbol != symbol {
			return nil // Filter if needed
		}
		return handler(&event)
	})

	return c.Subscribe(stream, nil)
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

func (c *WsMarketClient) UnsubscribeIncrementOrderBook(symbol string, interval string) error {
	channel := fmt.Sprintf("%s@depth@%s", symbol, interval)
	return c.Unsubscribe(channel)
}

// SubscribeLimitOrderBook
func (c *WsMarketClient) SubscribeLimitOrderBook(symbol string, depth int, speed string, handler func(*DepthEvent) error) error {
	// <symbol>@depth<levels>@<speed>
	// e.g. btcusdt@depth20@100ms
	stream := fmt.Sprintf("%s@depth%d@%s", strings.ToLower(symbol), depth, speed)

	c.SetHandler("depthUpdate", func(data []byte) error {
		var event DepthEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		if event.Symbol != symbol {
			return nil
		}
		return handler(&event)
	})

	return c.Subscribe(stream, nil)
}

// UnsubscribeLimitOrderBook
func (c *WsMarketClient) UnsubscribeLimitOrderBook(symbol string, depth int, speed string) error {
	stream := fmt.Sprintf("%s@depth%d@%s", strings.ToLower(symbol), depth, speed)
	return c.Unsubscribe(stream)
}

// SubscribeKline
func (c *WsMarketClient) SubscribeKline(symbol string, interval string, handler func(*KlineEvent) error) error {
	stream := fmt.Sprintf("%s@kline_%s", strings.ToLower(symbol), interval)

	c.SetHandler("kline", func(data []byte) error {
		var event KlineEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		if event.Symbol != symbol {
			return nil
		}
		return handler(&event)
	})

	return c.Subscribe(stream, nil)
}

// UnsubscribeKline
func (c *WsMarketClient) UnsubscribeKline(symbol string, interval string) error {
	stream := fmt.Sprintf("%s@kline_%s", strings.ToLower(symbol), interval)
	return c.Unsubscribe(stream)
}

// SubscribeAggTrade
func (c *WsMarketClient) SubscribeAggTrade(symbol string, handler func(*AggTradeEvent) error) error {
	stream := fmt.Sprintf("%s@aggTrade", strings.ToLower(symbol))

	c.SetHandler("aggTrade", func(data []byte) error {
		var event AggTradeEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		if event.Symbol != symbol {
			return nil
		}
		return handler(&event)
	})

	return c.Subscribe(stream, nil)
}

// UnsubscribeAggTrade
func (c *WsMarketClient) UnsubscribeAggTrade(symbol string) error {
	stream := fmt.Sprintf("%s@aggTrade", strings.ToLower(symbol))
	return c.Unsubscribe(stream)
}
