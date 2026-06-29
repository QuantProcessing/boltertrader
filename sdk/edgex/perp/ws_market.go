package perp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

type WsMarketClient struct {
	URL     string
	Conn    *websocket.Conn
	Mu      sync.RWMutex
	WriteMu sync.Mutex
	Logger  *zap.SugaredLogger

	Done                 chan struct{}
	isShuttingDown       bool
	maxReconnectAttempts int
	reconnectAttempt     int
	ReconnectWait        time.Duration

	latencyTestInterval time.Duration
	latencyTestStart    int64

	// Subscription tracking
	// Map channel name to callback function
	subs map[string]func([]byte) error
	ctx  context.Context
}

func NewWsMarketClient(ctx context.Context) *WsMarketClient {
	return &WsMarketClient{
		URL:                  WSBaseURL + "/public/ws",
		Logger:               zap.NewNop().Sugar().Named("edgex-market"),
		subs:                 make(map[string]func([]byte) error),
		Done:                 make(chan struct{}),
		maxReconnectAttempts: 10,
		ReconnectWait:        1 * time.Second,
		latencyTestInterval:  1 * time.Minute,
		ctx:                  ctx,
	}
}

func (c *WsMarketClient) Connect() error {
	c.Mu.Lock()
	defer c.Mu.Unlock()

	if c.Conn != nil {
		return nil
	}

	c.Logger.Debugw("Connecting to EdgeX Market WS", "url", c.URL)
	// Use internal 10 second timeout
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.URL, nil)
	if err != nil {
		return err
	}

	c.Conn = conn
	c.Done = make(chan struct{})

	go c.latencyTest()
	go c.readLoop()

	return nil
}

func (c *WsMarketClient) latencyTest() {
	ticker := time.NewTicker(c.latencyTestInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.Done:
			return
		case <-ticker.C:
			c.Mu.RLock()
			conn := c.Conn
			c.Mu.RUnlock()
			if conn == nil {
				return // connection closed, stop latency test
			}
			c.latencyTestStart = time.Now().UnixMilli()
			c.WriteMu.Lock()
			err := conn.WriteJSON(map[string]string{
				"type": "ping",
				"time": fmt.Sprintf("%d", c.latencyTestStart),
			})
			c.WriteMu.Unlock()
			if err != nil {
				c.Logger.Debugw("latency ping failed", "error", err)
				return
			}
		}
	}
}

func (c *WsMarketClient) readLoop() {
	defer func() {
		c.Mu.Lock()
		c.Conn = nil
		c.Mu.Unlock()

		if !c.isShuttingDown {
			go c.reconnect()
		}
	}()

	for {
		select {
		case <-c.Done:
			return
		default:
		}

		c.Mu.RLock()
		conn := c.Conn
		c.Mu.RUnlock()

		if conn == nil {
			return
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				c.Logger.Errorw("websocket unexpected close error", "error", err)
			} else if err.Error() == "EOF" || websocket.IsCloseError(err, websocket.CloseNormalClosure) {
				c.Logger.Infow("WebSocket connection closed normally")
			} else {
				c.Logger.Errorw("websocket read error", "error", err)
			}
			return
		}
		c.handleMessage(message)
	}
}

func (c *WsMarketClient) handleMessage(message []byte) {
	c.Mu.RLock()
	defer c.Mu.RUnlock()

	var msgStruct struct {
		Channel string `json:"channel"`
		Type    string `json:"type"`
	}
	if err := json.Unmarshal(message, &msgStruct); err != nil {
		c.Logger.Errorw("Failed to unmarshal message", "error", err)
		return
	}

	// Handle ping
	if msgStruct.Type == "ping" {
		c.Logger.Debugw("Received ping", "msg", string(message))
		conn := c.Conn
		if conn == nil {
			return
		}
		c.WriteMu.Lock()
		conn.WriteJSON(map[string]string{
			"type": "pong",
			"time": fmt.Sprintf("%d", time.Now().UnixMilli()),
		})
		c.WriteMu.Unlock()
		return
	}

	// handle latency test response
	if msgStruct.Type == "pong" {
		c.Logger.Debugw("Received pong", "msg", string(message))
		latency := time.Now().UnixMilli() - c.latencyTestStart
		c.Logger.Debugw("Latency", "ms", latency)
		return
	}

	// Handle subscription responses or other system messages if necessary
	if msgStruct.Type == "subscribe" || msgStruct.Type == "unsubscribe" {
		c.Logger.Debugw("Received system message", "msg", string(message))
		return
	}

	if callback, ok := c.subs[msgStruct.Channel]; ok {
		if err := callback(message); err != nil {
			c.Logger.Errorw("Failed to process message", "channel", msgStruct.Channel, "error", err)
		}
	} else {
		// Only log if it's not a system message we already handled
		c.Logger.Debugw("No callback registered for channel", "channel", msgStruct.Channel)
	}
}

func (c *WsMarketClient) reconnect() {
	c.Mu.Lock()
	if c.isShuttingDown {
		c.Mu.Unlock()
		return
	}
	c.reconnectAttempt++
	attempt := c.reconnectAttempt
	c.Mu.Unlock()

	if attempt > c.maxReconnectAttempts {
		c.Logger.Errorw("Max reconnection attempts reached, giving up", "attempts", c.maxReconnectAttempts)
		return
	}

	// Exponential backoff
	backoff := time.Duration(1<<uint(attempt-1)) * c.ReconnectWait
	if backoff > 30*time.Second {
		backoff = 30 * time.Second
	}

	c.Logger.Infow("Reconnecting...", "backoff", backoff, "attempt", attempt, "max", c.maxReconnectAttempts)

	select {
	case <-c.Done:
		return
	case <-time.After(backoff):
	}

	if err := c.Connect(); err != nil {
		c.Logger.Errorw("Reconnection attempt failed", "attempt", attempt, "error", err)
		go c.reconnect()
		return
	}

	c.Logger.Infow("Reconnected successfully, resubscribing...")

	// Resubscribe to all channels
	c.Mu.RLock()
	channels := make([]string, 0, len(c.subs))
	for ch := range c.subs {
		channels = append(channels, ch)
	}
	c.Mu.RUnlock()

	for _, ch := range channels {
		msg := map[string]string{
			"type":    "subscribe",
			"channel": ch,
		}
		c.WriteMu.Lock()
		if err := c.Conn.WriteJSON(msg); err != nil {
			c.Logger.Errorw("Failed to resubscribe", "channel", ch, "error", err)
		}
		c.WriteMu.Unlock()
	}
}

// Subscribe registers a callback for a channel and sends the subscription message
func (c *WsMarketClient) Subscribe(channel string, callback func([]byte) error) error {
	c.Mu.Lock()
	c.subs[channel] = callback
	c.Mu.Unlock()

	if c.Conn == nil {
		return fmt.Errorf("websocket not connected")
	}

	msg := map[string]string{
		"type":    "subscribe",
		"channel": channel,
	}

	c.WriteMu.Lock()
	defer c.WriteMu.Unlock()
	return c.Conn.WriteJSON(msg)
}

func (c *WsMarketClient) Unsubscribe(channel string) error {
	c.Mu.Lock()
	delete(c.subs, channel)
	c.Mu.Unlock()

	if c.Conn == nil {
		return nil
	}

	msg := map[string]string{
		"type":    "unsubscribe",
		"channel": channel,
	}

	c.WriteMu.Lock()
	defer c.WriteMu.Unlock()
	return c.Conn.WriteJSON(msg)
}

func (c *WsMarketClient) Close() {
	c.Mu.Lock()
	c.isShuttingDown = true
	c.Mu.Unlock()

	if c.Done != nil {
		close(c.Done)
		c.Done = nil
	}
	if c.Conn != nil {
		c.Conn.Close()
	}
}

// Market Data Subscriptions

func (c *WsMarketClient) SubscribeMetadata(callback func(*WsMetadataEvent)) error {
	return c.Subscribe("metadata", func(data []byte) error {
		var event WsMetadataEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		callback(&event)
		return nil
	})
}

func (c *WsMarketClient) SubscribeTicker(contractId string, callback func(*WsTickerEvent)) error {
	return c.Subscribe(fmt.Sprintf("ticker.%s", contractId), func(data []byte) error {
		var event WsTickerEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		callback(&event)
		return nil
	})
}

func (c *WsMarketClient) SubscribeAllTickers(callback func(*WsTickerEvent)) error {
	return c.Subscribe("ticker.all", func(data []byte) error {
		var event WsTickerEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		callback(&event)
		return nil
	})
}

// SubscribeKline
func (c *WsMarketClient) SubscribeKline(contractId string, priceType PriceType, interval KlineInterval, callback func(*WsKlineEvent)) error {
	return c.Subscribe(fmt.Sprintf("kline.%s.%s.%s", priceType, contractId, interval), func(data []byte) error {
		var event WsKlineEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		callback(&event)
		return nil
	})
}

// SubscribeOrderBook response dataType Snapshot or CHANGED
// depth: 15 200
func (c *WsMarketClient) SubscribeOrderBook(contractId string, depth OrderBookDepth, callback func(*WsDepthEvent)) error {
	return c.Subscribe(fmt.Sprintf("depth.%s.%d", contractId, depth), func(data []byte) error {
		var event WsDepthEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		callback(&event)
		return nil
	})
}

func (c *WsMarketClient) SubscribeTrades(contractId string, callback func(*WsTradeEvent)) error {
	return c.Subscribe(fmt.Sprintf("trades.%s", contractId), func(data []byte) error {
		var event WsTradeEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		callback(&event)
		return nil
	})
}

func (c *WsMarketClient) SubscribeFundingRate(contractId string, callback func(*WsFundingRateEvent)) error {
	return c.Subscribe(fmt.Sprintf("fundingRate.%s", contractId), func(data []byte) error {
		var event WsFundingRateEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		callback(&event)
		return nil
	})
}

func (c *WsMarketClient) SubscribeAllFundingRates(callback func(*WsFundingRateEvent)) error {
	return c.Subscribe("fundingRate.all", func(data []byte) error {
		var event WsFundingRateEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		callback(&event)
		return nil
	})
}

// Unsubscribe methods

func (c *WsMarketClient) UnsubscribeMetadata() error {
	return c.Unsubscribe("metadata")
}

func (c *WsMarketClient) UnsubscribeTicker(contractId string) error {
	return c.Unsubscribe(fmt.Sprintf("ticker.%s", contractId))
}

func (c *WsMarketClient) UnsubscribeAllTickers() error {
	return c.Unsubscribe("ticker.all")
}

func (c *WsMarketClient) UnsubscribeKline(contractId string, priceType PriceType, interval KlineInterval) error {
	return c.Unsubscribe(fmt.Sprintf("kline.%s.%s.%s", priceType, contractId, interval))
}

func (c *WsMarketClient) UnsubscribeOrderBook(contractId string, depth OrderBookDepth) error {
	return c.Unsubscribe(fmt.Sprintf("depth.%s.%d", contractId, depth))
}

func (c *WsMarketClient) UnsubscribeTrades(contractId string) error {
	return c.Unsubscribe(fmt.Sprintf("trades.%s", contractId))
}

func (c *WsMarketClient) UnsubscribeFundingRate(contractId string) error {
	return c.Unsubscribe(fmt.Sprintf("fundingRate.%s", contractId))
}

func (c *WsMarketClient) UnsubscribeAllFundingRates() error {
	return c.Unsubscribe("fundingRate.all")
}
