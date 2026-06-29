package perp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

type WsAccountClient struct {
	*WsClient
	Client       *Client
	KeepAliveInt time.Duration
	ListenKey    string

	mu                           sync.Mutex
	accountUpdateCallbacks       []func(*AccountUpdateEvent)
	orderUpdateCallbacks         []func(*OrderUpdateEvent)
	accountConfigUpdateCallbacks []func(*AccountConfigUpdateEvent)
}

func NewWsAccountClient(ctx context.Context, apiKey, apiSecret string) *WsAccountClient {
	client := &WsAccountClient{
		Client:       NewClient().WithCredentials(apiKey, apiSecret),
		WsClient:     NewWSClient(ctx, WSBaseURL),
		KeepAliveInt: 50 * time.Minute,
	}
	client.WsClient.Logger = zap.NewNop().Sugar().Named("aster-account")
	client.WsClient.Handler = client.handleMessage
	return client
}

func (c *WsAccountClient) WithURL(url string) *WsAccountClient {
	c.WsClient.URL = url
	return c
}

func (c *WsAccountClient) SubscribeAccountUpdate(callback func(*AccountUpdateEvent)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accountUpdateCallbacks = append(c.accountUpdateCallbacks, callback)
}

func (c *WsAccountClient) SubscribeOrderUpdate(callback func(*OrderUpdateEvent)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.orderUpdateCallbacks = append(c.orderUpdateCallbacks, callback)
}

func (c *WsAccountClient) SubscribeAccountConfigUpdate(callback func(*AccountConfigUpdateEvent)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accountConfigUpdateCallbacks = append(c.accountConfigUpdateCallbacks, callback)
}

func (c *WsAccountClient) Connect() error {
	// 创建 listen key 时使用带超时的子 context
	ctxAPI, cancelAPI := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancelAPI()
	listenKey, err := c.Client.CreateListenKey(ctxAPI)
	if err != nil {
		return err
	}
	c.ListenKey = listenKey

	// Configure WsClient compatibility field with listenKey URL
	c.WithURL(WSBaseURL + "/" + listenKey)

	// Register handlers
	c.SetHandler("ACCOUNT_UPDATE", c.handleAccountUpdate)
	c.SetHandler("ORDER_TRADE_UPDATE", c.handleOrderUpdate)
	c.SetHandler("ACCOUNT_CONFIG_UPDATE", c.handleAccountConfigUpdate)
	c.SetHandler("listenKeyExpired", c.handleListenKeyExpired)

	// Connect WebSocket
	if err := c.WsClient.Connect(); err != nil {
		return fmt.Errorf("failed to connect user stream: %w", err)
	}

	go c.keepAlive()

	return nil
}

func (c *WsAccountClient) handleMessage(message []byte) {
	c.Logger.Debugw("Received", "msg", string(message))

	// Use generic map to handle various types
	var raw map[string]interface{}
	if err := json.Unmarshal(message, &raw); err != nil {
		c.Logger.Error("Failed to unmarshal message", "error", err)
		return
	}

	eventType, ok := raw["e"].(string)
	if !ok || eventType == "" {
		// Log raw message if no type found
		// c.Logger.Warn("No event type found (e)", "msg", string(message))
		return
	}

	c.Logger.Info("Parsed Event Type", "type", eventType)
	c.CallSubscription(eventType, message)
}

func (c *WsAccountClient) handleAccountUpdate(data []byte) error {
	var event AccountUpdateEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cb := range c.accountUpdateCallbacks {
		cb(&event)
	}
	return nil
}

func (c *WsAccountClient) handleOrderUpdate(data []byte) error {
	var event OrderUpdateEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cb := range c.orderUpdateCallbacks {
		cb(&event)
	}
	return nil
}

func (c *WsAccountClient) handleAccountConfigUpdate(data []byte) error {
	var event AccountConfigUpdateEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cb := range c.accountConfigUpdateCallbacks {
		cb(&event)
	}
	return nil
}

func (c *WsAccountClient) handleListenKeyExpired(data []byte) error {
	c.Logger.Warn("ListenKey expired, reconnecting")

	// Disconnect the WS connection without cancelling the lifecycle context.
	// Close() would set isClosed=true and cancel ctx, making Connect() impossible.
	c.Mu.Lock()
	if c.Conn != nil {
		c.Conn.Close()
		c.Conn = nil
	}
	c.Mu.Unlock()

	// Fetch a new listenKey
	ctxAPI, cancelAPI := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancelAPI()
	listenKey, err := c.Client.CreateListenKey(ctxAPI)
	if err != nil {
		c.Logger.Error("failed to create new listenKey on expiry", "error", err)
		return err
	}
	c.ListenKey = listenKey
	c.WithURL(WSBaseURL + "/" + listenKey)

	// Reconnect with the new URL (readLoop exit will trigger reconnect)
	if err := c.WsClient.Connect(); err != nil {
		c.Logger.Error("failed to reconnect after listenKey expiry", "error", err)
		return err
	}

	c.Logger.Info("reconnected with new listenKey")
	return nil
}

func (c *WsAccountClient) keepAlive() {
	ticker := time.NewTicker(c.KeepAliveInt)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			if err := c.Client.KeepAliveListenKey(c.ctx); err != nil {
				c.Logger.Warn("failed to keep alive listen key", "error", err)
			} else {
				c.Logger.Debug("keep alive listen key ok")
			}
		}
	}
}

func (c *WsAccountClient) Close() {
	if c.cancel != nil {
		c.cancel()
	}
	c.WsClient.Close()
}
