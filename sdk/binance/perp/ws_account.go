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
	ctx          context.Context
	BaseURL      string
	KeepAliveInt time.Duration
	ListenKey    string

	mu                           sync.Mutex
	recoveryMu                   sync.Mutex
	accountUpdateCallbacks       []func(*AccountUpdateEvent)
	orderUpdateCallbacks         []func(*OrderUpdateEvent)
	algoUpdateCallbacks          []func(*AlgoUpdateEvent)
	accountConfigUpdateCallbacks []func(*AccountConfigUpdateEvent)
	onResubscribe                func()
}

func NewWsAccountClient(ctx context.Context, apiKey, apiSecret string) *WsAccountClient {
	return NewWsAccountClientWithEndpointProfile(ctx, apiKey, apiSecret, USDMMProductionEndpoints)
}

func NewDemoWsAccountClient(ctx context.Context, apiKey, apiSecret string) *WsAccountClient {
	return NewWsAccountClientWithEndpointProfile(ctx, apiKey, apiSecret, USDMMDemoEndpoints)
}

func NewCoinMWsAccountClient(ctx context.Context, apiKey, apiSecret string) *WsAccountClient {
	return newWsAccountClient(ctx, NewCoinMClient().WithCredentials(apiKey, apiSecret), CoinMWSPrivateBaseURL)
}

func NewWsAccountClientWithEndpointProfile(ctx context.Context, apiKey, apiSecret string, profile EndpointProfile) *WsAccountClient {
	restClient := NewClient().WithEndpointProfile(profile).WithCredentials(apiKey, apiSecret)
	return newWsAccountClient(ctx, restClient, endpointOrDefault(profile.WSPrivateBaseURL, WSPrivateBaseURL))
}

func newWsAccountClient(ctx context.Context, restClient *Client, baseURL string) *WsAccountClient {
	client := &WsAccountClient{
		Client:       restClient,
		ctx:          ctx,
		BaseURL:      baseURL,
		WsClient:     NewWSClient(ctx, baseURL),
		KeepAliveInt: 50 * time.Minute,
	}
	client.resetWSClient()
	return client
}

func (c *WsAccountClient) WithURL(url string) *WsAccountClient {
	c.BaseURL = url
	c.WsClient.URL = url
	return c
}

func (c *WsAccountClient) SetOnResubscribe(handler func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onResubscribe = handler
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

func (c *WsAccountClient) SubscribeAlgoUpdate(callback func(*AlgoUpdateEvent)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.algoUpdateCallbacks = append(c.algoUpdateCallbacks, callback)
}

func (c *WsAccountClient) SubscribeAccountConfigUpdate(callback func(*AccountConfigUpdateEvent)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accountConfigUpdateCallbacks = append(c.accountConfigUpdateCallbacks, callback)
}

func (c *WsAccountClient) Connect() error {
	if c.WsClient == nil || c.WsClient.isClosed {
		c.resetWSClient()
	}

	// 创建 listen key 时使用带超时的子 context
	ctxAPI, cancelAPI := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancelAPI()
	listenKey, err := c.Client.CreateListenKey(ctxAPI)
	if err != nil {
		return err
	}
	c.ListenKey = listenKey

	// Configure WsClient with listenKey URL
	c.WsClient.URL = c.BaseURL + "/" + listenKey

	// Register handlers
	c.SetHandler("ACCOUNT_UPDATE", c.handleAccountUpdate)
	c.SetHandler("ORDER_TRADE_UPDATE", c.handleOrderUpdate)
	c.SetHandler("ALGO_UPDATE", c.handleAlgoUpdate)
	c.SetHandler("ACCOUNT_CONFIG_UPDATE", c.handleAccountConfigUpdate)
	c.SetHandler("listenKeyExpired", c.handleListenKeyExpired)
	c.SetPostReconnect(func() {
		go c.resubscribe()
	})

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

func (c *WsAccountClient) handleAlgoUpdate(data []byte) error {
	var event AlgoUpdateEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cb := range c.algoUpdateCallbacks {
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
	c.Logger.Info("ListenKey expired")
	go c.resubscribe()
	return nil
}

func (c *WsAccountClient) resubscribe() {
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()

	c.WsClient.Close()
	c.resetWSClient()
	if err := c.Connect(); err != nil {
		c.WsClient.Logger.Errorw("Failed to resubscribe user stream", "error", err)
		return
	}
	c.mu.Lock()
	handler := c.onResubscribe
	c.mu.Unlock()
	if handler != nil {
		handler()
	}
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
				fmt.Printf("Failed to keep alive listen key: %v\n", err)
				go c.resubscribe()
				return
			} else {
				c.WsClient.Logger.Debug("Keep alive listen key successfully")
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

func (c *WsAccountClient) resetWSClient() {
	c.WsClient = NewWSClient(c.ctx, c.BaseURL)
	c.WsClient.Logger = zap.NewNop().Sugar().Named("binance-account")
	c.WsClient.Handler = c.handleMessage
	c.WsClient.SetPostReconnect(func() {
		go c.resubscribe()
	})
}
