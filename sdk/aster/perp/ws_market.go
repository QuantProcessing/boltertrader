package perp

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"go.uber.org/zap"

	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
)

type WsMarketClient struct {
	*WsClient
	profile astercommon.Profile
}

func NewWsMarketClient(ctx context.Context, profile astercommon.Profile) (*WsMarketClient, error) {
	if profile.Product() != astercommon.ProductPerp {
		return nil, fmt.Errorf("aster perp market websocket: profile product is %q", profile.Product())
	}
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	client := &WsMarketClient{
		WsClient: newWSClient(ctx, strings.TrimSuffix(profile.PublicWSURL(), "/")+"/ws"),
		profile:  profile,
	}
	client.WsClient.Logger = zap.NewNop().Sugar().Named("aster-market")
	client.Handler = client.handleMessage
	return client, nil
}

func (c *WsMarketClient) handleMessage(message []byte) {
	c.Logger.Debugw("received websocket message", "bytes", len(message))

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
	} else if event.EventType == "depthUpdate" && event.Symbol != "" {
		keys = append(keys, fmt.Sprintf("%s@depth", strings.ToLower(event.Symbol)))
	} else if eventName, ok := SingleEventMap[event.EventType]; ok {
		stream := fmt.Sprintf("%s@%s", strings.ToLower(event.Symbol), eventName)
		keys = append(keys, stream)
	} else if event.EventType == "" && event.Symbol != "" {
		var bookTicker struct {
			UpdateID *int64 `json:"u"`
			BidPrice string `json:"b"`
			AskPrice string `json:"a"`
		}
		if err := json.Unmarshal(message, &bookTicker); err == nil && bookTicker.UpdateID != nil && bookTicker.BidPrice != "" && bookTicker.AskPrice != "" {
			keys = append(keys, fmt.Sprintf("%s@bookTicker", strings.ToLower(event.Symbol)))
		}
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
		c.Logger.Debugw("no routing keys generated for event", "eventType", event.EventType)
	}
}

// SubscribeMarkPrice latest mark price
// interval default 1s, option 3s
func (c *WsMarketClient) SubscribeMarkPrice(symbol string, interval string, callback func(*WsMarkPriceEvent) error) error {
	symbol, err := c.normalizeSymbol(symbol)
	if err != nil {
		return err
	}
	if interval == "" {
		interval = "1s"
	}
	channel := fmt.Sprintf("%s@markPrice@%s", strings.ToLower(symbol), interval)
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

// SubscribeIncrementOrderBook subscribes to diff depth. Empty interval selects
// the documented 250ms default; explicit options are 100ms and 500ms.
func (c *WsMarketClient) SubscribeIncrementOrderBook(symbol string, interval string, callback func(*WsDepthEvent) error) error {
	symbol, err := c.normalizeSymbol(symbol)
	if err != nil {
		return err
	}
	prefix := strings.ToLower(symbol) + "@depth"
	channel, err := perpDepthStream(prefix, 0, interval)
	if err != nil {
		return err
	}
	if existing := c.subscriptionWithPrefix(prefix); existing != "" && existing != channel {
		return fmt.Errorf("aster perp depth stream: %s already subscribed", existing)
	}
	c.SetHandler(prefix, func(data []byte) error {
		var wsData WsDepthEvent
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(&wsData)
	})
	return c.Subscribe(channel, nil)
}

// SubscribeLimitOrderBook subscribes to 5, 10, or 20 levels. Empty interval
// selects the documented 250ms default; explicit options are 100ms and 500ms.
func (c *WsMarketClient) SubscribeLimitOrderBook(symbol string, levels int, interval string, callback func(*WsDepthEvent) error) error {
	if levels != 5 && levels != 10 && levels != 20 {
		return fmt.Errorf("aster perp partial depth stream: unsupported levels %d", levels)
	}
	symbol, err := c.normalizeSymbol(symbol)
	if err != nil {
		return err
	}
	prefix := strings.ToLower(symbol) + "@depth"
	channel, err := perpDepthStream(prefix, levels, interval)
	if err != nil {
		return err
	}
	if existing := c.subscriptionWithPrefix(prefix); existing != "" && existing != channel {
		return fmt.Errorf("aster perp depth stream: %s already subscribed", existing)
	}
	c.SetHandler(prefix, func(data []byte) error {
		var wsData WsDepthEvent
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(&wsData)
	})
	return c.Subscribe(channel, nil)
}

// SubscribeBookTicker Optimal bid/ask price
func (c *WsMarketClient) SubscribeBookTicker(symbol string, callback func(*WsBookTickerEvent) error) error {
	symbol, err := c.normalizeSymbol(symbol)
	if err != nil {
		return err
	}
	channel := fmt.Sprintf("%s@bookTicker", strings.ToLower(symbol))
	return c.Subscribe(channel, func(data []byte) error {
		var wsData WsBookTickerEvent
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(&wsData)
	})
}

func (c *WsMarketClient) SubscribeAggTrade(symbol string, callback func(*WsAggTradeEvent) error) error {
	symbol, err := c.normalizeSymbol(symbol)
	if err != nil {
		return err
	}
	channel := fmt.Sprintf("%s@aggTrade", strings.ToLower(symbol))
	return c.Subscribe(channel, func(data []byte) error {
		var wsData WsAggTradeEvent
		if err := json.Unmarshal(data, &wsData); err != nil {
			return err
		}
		return callback(&wsData)
	})
}

func (c *WsMarketClient) SubscribeKline(symbol string, interval string, callback func(*WsKlineEvent) error) error {
	symbol, err := c.normalizeSymbol(symbol)
	if err != nil {
		return err
	}
	channel := fmt.Sprintf("%s@kline_%s", strings.ToLower(symbol), interval)
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
	symbol, err := c.normalizeSymbol(symbol)
	if err != nil {
		return err
	}
	if interval == "" {
		interval = "1s"
	}
	channel := fmt.Sprintf("%s@markPrice@%s", strings.ToLower(symbol), interval)
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
	symbol, err := c.normalizeSymbol(symbol)
	if err != nil {
		return err
	}
	prefix := strings.ToLower(symbol) + "@depth"
	channel, err := perpDepthStream(prefix, 0, interval)
	if err != nil {
		return err
	}
	if existing := c.subscriptionWithPrefix(prefix); existing != "" && existing != channel {
		return fmt.Errorf("aster perp depth stream: active stream is %s", existing)
	}
	err = c.Unsubscribe(channel)
	c.SetHandler(prefix, nil)
	return err
}

func (c *WsMarketClient) UnsubscribeLimitOrderBook(symbol string, levels int, interval string) error {
	if levels != 5 && levels != 10 && levels != 20 {
		return fmt.Errorf("aster perp partial depth stream: unsupported levels %d", levels)
	}
	symbol, err := c.normalizeSymbol(symbol)
	if err != nil {
		return err
	}
	prefix := strings.ToLower(symbol) + "@depth"
	channel, err := perpDepthStream(prefix, levels, interval)
	if err != nil {
		return err
	}
	if existing := c.subscriptionWithPrefix(prefix); existing != "" && existing != channel {
		return fmt.Errorf("aster perp depth stream: active stream is %s", existing)
	}
	err = c.Unsubscribe(channel)
	c.SetHandler(prefix, nil)
	return err
}

func (c *WsMarketClient) UnsubscribeBookTicker(symbol string) error {
	symbol, err := c.normalizeSymbol(symbol)
	if err != nil {
		return err
	}
	channel := fmt.Sprintf("%s@bookTicker", strings.ToLower(symbol))
	return c.Unsubscribe(channel)
}

func (c *WsMarketClient) UnsubscribeAggTrade(symbol string) error {
	symbol, err := c.normalizeSymbol(symbol)
	if err != nil {
		return err
	}
	channel := fmt.Sprintf("%s@aggTrade", strings.ToLower(symbol))
	return c.Unsubscribe(channel)
}

func (c *WsMarketClient) UnsubscribeKline(symbol string, interval string) error {
	symbol, err := c.normalizeSymbol(symbol)
	if err != nil {
		return err
	}
	channel := fmt.Sprintf("%s@kline_%s", strings.ToLower(symbol), interval)
	return c.Unsubscribe(channel)
}

func (c *WsMarketClient) normalizeSymbol(symbol string) (string, error) {
	return astercommon.NormalizeSymbol(c.profile, symbol)
}

func perpDepthStream(prefix string, levels int, interval string) (string, error) {
	interval = strings.ToLower(strings.TrimSpace(interval))
	switch interval {
	case "", "100ms", "500ms":
	default:
		return "", fmt.Errorf("aster perp depth stream: unsupported interval %q", interval)
	}
	stream := prefix
	if levels > 0 {
		stream += fmt.Sprintf("%d", levels)
	}
	if interval != "" {
		stream += "@" + interval
	}
	return stream, nil
}
