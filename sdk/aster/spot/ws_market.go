package spot

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
)

type WsMarketClient struct {
	*WsClient
	profile astercommon.Profile
}

func NewWsMarketClient(ctx context.Context, profile astercommon.Profile) (*WsMarketClient, error) {
	if profile.Product() != astercommon.ProductSpot {
		return nil, fmt.Errorf("aster spot market websocket: profile product is %q", profile.Product())
	}
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	transport := newWSClient(ctx, strings.TrimSuffix(profile.PublicWSURL(), "/")+"/ws")
	client := &WsMarketClient{WsClient: transport, profile: profile}
	transport.Handler = client.handleMessage
	return client, nil
}

func (c *WsMarketClient) handleMessage(message []byte) {
	message = bytes.TrimSpace(message)
	if len(message) == 0 {
		return
	}
	if c.Debug {
		c.Logger.Debugw("received websocket message", "bytes", len(message))
	}

	if message[0] == '{' {
		var combined struct {
			Stream string          `json:"stream"`
			Data   json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(message, &combined); err == nil && combined.Stream != "" && len(combined.Data) > 0 {
			if combined.Data[0] == '[' {
				c.handleArrayMessage(combined.Data)
			} else {
				c.handleObjectMessage(combined.Data)
			}
			return
		}
	}
	if message[0] == '[' {
		c.handleArrayMessage(message)
		return
	}
	c.handleObjectMessage(message)
}

func (c *WsMarketClient) handleArrayMessage(message []byte) {
	var events []struct {
		EventType string `json:"e"`
		EventTime int64  `json:"E"`
	}
	if err := json.Unmarshal(message, &events); err != nil || len(events) == 0 {
		return
	}
	var key string
	switch events[0].EventType {
	case "24hrMiniTicker":
		key = "!miniTicker@arr"
	case "24hrTicker":
		key = "!ticker@arr"
	}
	if key != "" {
		c.CallSubscription(key, message)
	}
}

func (c *WsMarketClient) handleObjectMessage(message []byte) {
	var header struct {
		EventType string `json:"e"`
		EventTime int64  `json:"E"`
		Symbol    string `json:"s"`
	}
	if err := json.Unmarshal(message, &header); err != nil || header.Symbol == "" {
		return
	}
	symbol := strings.ToLower(header.Symbol)
	eventType := header.EventType
	if eventType == "" {
		var bookTicker struct {
			UpdateID *int64 `json:"u"`
			BidPrice string `json:"b"`
			AskPrice string `json:"a"`
		}
		if err := json.Unmarshal(message, &bookTicker); err != nil || bookTicker.UpdateID == nil || bookTicker.BidPrice == "" || bookTicker.AskPrice == "" {
			return
		}
		eventType = "bookTicker"
	}

	var key string
	switch eventType {
	case "bookTicker":
		key = symbol + "@bookTicker"
	case "depthUpdate":
		key = symbol + "@depth"
	case "aggTrade":
		key = symbol + "@aggTrade"
	case "trade":
		key = symbol + "@trade"
	case "kline":
		var event struct {
			Kline struct {
				Interval string `json:"i"`
			} `json:"k"`
		}
		if err := json.Unmarshal(message, &event); err == nil && event.Kline.Interval != "" {
			key = symbol + "@kline_" + event.Kline.Interval
		}
	case "24hrTicker":
		key = symbol + "@ticker"
	case "24hrMiniTicker":
		key = symbol + "@miniTicker"
	}
	if key != "" {
		c.CallSubscription(key, message)
	}
}

func (c *WsMarketClient) SubscribeBookTicker(symbol string, handler func(*BookTickerEvent) error) error {
	normalized, err := c.normalizeSymbol(symbol)
	if err != nil {
		return err
	}
	stream := strings.ToLower(normalized) + "@bookTicker"
	return c.Subscribe(stream, func(data []byte) error {
		var event BookTickerEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		return handler(&event)
	})
}

func (c *WsMarketClient) SubscribeIncrementOrderBook(symbol, interval string, handler func(*WsDepthEvent) error) error {
	normalized, err := c.normalizeSymbol(symbol)
	if err != nil {
		return err
	}
	interval = strings.ToLower(strings.TrimSpace(interval))
	if interval != "" && interval != "100ms" {
		return fmt.Errorf("aster spot depth stream: unsupported interval %q", interval)
	}
	prefix := strings.ToLower(normalized) + "@depth"
	stream := prefix
	if interval != "" {
		stream += "@" + interval
	}
	if existing := c.subscriptionWithPrefix(prefix); existing != "" && existing != stream {
		return fmt.Errorf("aster spot depth stream: %s already subscribed", existing)
	}
	c.SetHandler(prefix, func(data []byte) error {
		var event WsDepthEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		return handler(&event)
	})
	return c.Subscribe(stream, nil)
}

func (c *WsMarketClient) UnsubscribeIncrementOrderBook(symbol, interval string) error {
	normalized, err := c.normalizeSymbol(symbol)
	if err != nil {
		return err
	}
	interval = strings.ToLower(strings.TrimSpace(interval))
	prefix := strings.ToLower(normalized) + "@depth"
	stream := prefix
	if interval != "" {
		stream += "@" + interval
	}
	err = c.Unsubscribe(stream)
	c.SetHandler(prefix, nil)
	return err
}

func (c *WsMarketClient) SubscribeLimitOrderBook(symbol string, depth int, speed string, handler func(*DepthEvent) error) error {
	if depth != 5 && depth != 10 && depth != 20 {
		return fmt.Errorf("aster spot partial depth stream: unsupported depth %d", depth)
	}
	normalized, err := c.normalizeSymbol(symbol)
	if err != nil {
		return err
	}
	speed = strings.ToLower(strings.TrimSpace(speed))
	if speed != "" && speed != "100ms" {
		return fmt.Errorf("aster spot partial depth stream: unsupported speed %q", speed)
	}
	prefix := strings.ToLower(normalized) + "@depth"
	stream := fmt.Sprintf("%s%d", prefix, depth)
	if speed != "" {
		stream += "@" + speed
	}
	if existing := c.subscriptionWithPrefix(prefix); existing != "" && existing != stream {
		return fmt.Errorf("aster spot depth stream: %s already subscribed", existing)
	}
	c.SetHandler(prefix, func(data []byte) error {
		var event DepthEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		return handler(&event)
	})
	return c.Subscribe(stream, nil)
}

func (c *WsMarketClient) UnsubscribeLimitOrderBook(symbol string, depth int, speed string) error {
	normalized, err := c.normalizeSymbol(symbol)
	if err != nil {
		return err
	}
	prefix := strings.ToLower(normalized) + "@depth"
	stream := fmt.Sprintf("%s%d", prefix, depth)
	if speed = strings.ToLower(strings.TrimSpace(speed)); speed != "" {
		stream += "@" + speed
	}
	err = c.Unsubscribe(stream)
	c.SetHandler(prefix, nil)
	return err
}

func (c *WsMarketClient) SubscribeKline(symbol, interval string, handler func(*KlineEvent) error) error {
	normalized, err := c.normalizeSymbol(symbol)
	if err != nil {
		return err
	}
	interval = strings.TrimSpace(interval)
	if interval == "" {
		return fmt.Errorf("aster spot kline stream: interval is required")
	}
	stream := fmt.Sprintf("%s@kline_%s", strings.ToLower(normalized), interval)
	return c.Subscribe(stream, func(data []byte) error {
		var event KlineEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		return handler(&event)
	})
}

func (c *WsMarketClient) UnsubscribeKline(symbol, interval string) error {
	normalized, err := c.normalizeSymbol(symbol)
	if err != nil {
		return err
	}
	return c.Unsubscribe(fmt.Sprintf("%s@kline_%s", strings.ToLower(normalized), strings.TrimSpace(interval)))
}

func (c *WsMarketClient) SubscribeAggTrade(symbol string, handler func(*AggTradeEvent) error) error {
	normalized, err := c.normalizeSymbol(symbol)
	if err != nil {
		return err
	}
	stream := strings.ToLower(normalized) + "@aggTrade"
	return c.Subscribe(stream, func(data []byte) error {
		var event AggTradeEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		return handler(&event)
	})
}

func (c *WsMarketClient) UnsubscribeAggTrade(symbol string) error {
	normalized, err := c.normalizeSymbol(symbol)
	if err != nil {
		return err
	}
	return c.Unsubscribe(strings.ToLower(normalized) + "@aggTrade")
}

func (c *WsMarketClient) SubscribeTrade(symbol string, handler func(*TradeEvent) error) error {
	normalized, err := c.normalizeSymbol(symbol)
	if err != nil {
		return err
	}
	stream := strings.ToLower(normalized) + "@trade"
	return c.Subscribe(stream, func(data []byte) error {
		var event TradeEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		return handler(&event)
	})
}

func (c *WsMarketClient) UnsubscribeTrade(symbol string) error {
	normalized, err := c.normalizeSymbol(symbol)
	if err != nil {
		return err
	}
	return c.Unsubscribe(strings.ToLower(normalized) + "@trade")
}

func (c *WsMarketClient) normalizeSymbol(symbol string) (string, error) {
	return astercommon.NormalizeSymbol(c.profile, symbol)
}
