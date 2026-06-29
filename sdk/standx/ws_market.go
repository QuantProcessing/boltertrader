package standx

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"go.uber.org/zap"
)

type WsMarketClient struct {
	WsClient *WSClient
	IsAuth   bool

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.RWMutex
	handlers map[string]func(data []byte) error

	Logger *zap.SugaredLogger
}

func NewWsMarketClient(ctx context.Context) *WsMarketClient {
	ctx, cancel := context.WithCancel(ctx)
	logger := zap.NewNop().Sugar().Named("standx-ws-market")
	return &WsMarketClient{
		WsClient: NewWSClient(ctx, MarketStreamURL, logger),
		Logger:   logger,
		ctx:      ctx,
		cancel:   cancel,
		handlers: make(map[string]func([]byte) error),
	}
}

func (c *WsMarketClient) Connect() error {
	c.WsClient.SetHandler(c.HandleMsg)
	c.WsClient.OnReconnect = c.onReconnect
	return c.WsClient.Connect()
}

func (c *WsMarketClient) onReconnect() error {
	c.Logger.Info("Restoring market subscriptions...")
	// Delegate to helper
	return c.restoreSubscriptions()
}

// Helper to restore
func (c *WsMarketClient) restoreSubscriptions() error {
	c.mu.RLock()
	keys := make([]string, 0, len(c.handlers))
	for k := range c.handlers {
		keys = append(keys, k)
	}
	c.mu.RUnlock()

	for _, key := range keys {
		// Key format: "channel:symbol"
		parts := strings.SplitN(key, ":", 2)
		if len(parts) != 2 {
			c.Logger.Warn("Skipping restore for invalid key format: ", key)
			continue
		}
		channel, symbol := parts[0], parts[1]

		c.Logger.Infow("Resubscribing", "channel", channel, "symbol", symbol)

		req := SubscriptionRequest{
			Subscribe: SubscribeParams{
				Channel: channel,
				Symbol:  symbol,
			},
		}
		if err := c.WsClient.WriteJSON(req); err != nil {
			c.Logger.Errorw("Failed to resubscribe", "key", key, "error", err)
			// Continue with others
		}
	}
	return nil
}

func (c *WsMarketClient) SubscribePrice(symbol string, handler func([]byte) error) error {
	return c.Subscribe("price", symbol, handler)
}

func (c *WsMarketClient) SubscribeDepthBook(symbol string, handler func([]byte) error) error {
	return c.Subscribe("depth_book", symbol, handler)
}

func (c *WsMarketClient) SubscribePublicTrade(symbol string, handler func([]byte) error) error {
	return c.Subscribe("public_trade", symbol, handler)
}

func (c *WsMarketClient) HandleMsg(data []byte) {
	var resp WSResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		c.Logger.Error("Failed to unmarshal response: ", err)
		return
	}

	key := fmt.Sprintf("%s:%s", resp.Channel, resp.Symbol)

	handler, ok := c.handlers[key]
	if !ok {
		c.Logger.Error("No handler found for channel: ", resp.Channel)
		return
	}
	if err := handler(resp.Data); err != nil {
		c.Logger.Error("Handler failed: ", err)
	}
}

func (c *WsMarketClient) Subscribe(channel string, symbol string, handler func([]byte) error) error {
	// Register Handler
	key := fmt.Sprintf("%s:%s", channel, symbol)
	c.mu.Lock()
	c.handlers[key] = handler
	c.mu.Unlock()

	// Send Subscribe Request
	req := SubscriptionRequest{
		Subscribe: SubscribeParams{
			Channel: channel,
			Symbol:  symbol,
		},
	}
	return c.WsClient.WriteJSON(req)
}

func (c *WsMarketClient) Close() {
	c.cancel()
	c.WsClient.Close()
}
