package standx

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"
)

type WsAccountClient struct {
	client   *Client
	wsClient *WSClient
	token    string
	IsAuth   bool

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.RWMutex
	handlers map[string]func(data []byte)
	authDone chan error

	Logger *zap.SugaredLogger
}

const (
	standxAccountChannelOrder    = "order"
	standxAccountChannelPosition = "position"
	standxAccountChannelBalance  = "balance"
	standxAccountChannelTrade    = "trade"

	standxAccountChannelList = "order, position, balance, trade"
)

func NewWsAccountClient(ctx context.Context, client *Client) *WsAccountClient {
	ctx, cancel := context.WithCancel(ctx)
	logger := zap.NewNop().Sugar().Named("standx-ws-account")
	return &WsAccountClient{
		client:   client,
		ctx:      ctx,
		cancel:   cancel,
		wsClient: NewWSClient(ctx, MarketStreamURL, logger),
		handlers: make(map[string]func([]byte)),
		authDone: make(chan error, 1),
		Logger:   logger,
	}
}

func (c *WsAccountClient) Connect() error {
	c.wsClient.SetHandler(c.HandleMsg)
	c.wsClient.OnReconnect = c.onReconnect
	c.wsClient.Connect()
	return nil
}

func (c *WsAccountClient) onReconnect() error {
	c.Logger.Info("Different from MarketClient, AccountClient must Re-Auth first")

	// 1. Re-Auth with Retry (Refresh token if needed)
	c.mu.Lock()
	c.IsAuth = false
	c.mu.Unlock()

	if err := c.doAuthWithRetry(); err != nil {
		c.Logger.Error("Re-Auth failed: ", err)
		return err
	}

	// 2. Restore Subscriptions
	c.Logger.Info("Restoring account subscriptions...")
	c.mu.RLock()
	keys := make([]string, 0, len(c.handlers))
	for k := range c.handlers {
		keys = append(keys, k)
	}
	c.mu.RUnlock()

	for _, channel := range keys {
		if !isStandXAccountChannel(channel) {
			return fmt.Errorf("cannot restore unsupported standx account channel %q after reconnect; supported channels: %s", channel, standxAccountChannelList)
		}
		c.Logger.Infow("account channel restored by auth", "channel", channel)
	}
	return nil
}

func (c *WsAccountClient) Auth() error {
	// Try Auth (with retry on invalid token)
	return c.doAuthWithRetry()
}

func (c *WsAccountClient) doAuthWithRetry() error {
	// First Attempt
	err := c.performAuth()
	if err == nil {
		return nil
	}

	// If error is related to invalid token (auth failed), invalidate and retry
	// "auth failed" is generic, but if we suspect token issues (e.g. expired but not locally), we retry.
	// For safety, we only retry if it looks like an auth error.
	c.Logger.Warn("Auth failed, refreshing token and retrying", zap.Error(err))
	c.client.InvalidateToken()

	// Reset IsAuth to false explicitly (though performAuth checks before sending)
	c.mu.Lock()
	c.IsAuth = false
	c.mu.Unlock()

	return c.performAuth()
}

func (c *WsAccountClient) performAuth() error {
	c.mu.RLock()
	if c.IsAuth {
		c.mu.RUnlock()
		return nil
	}
	c.mu.RUnlock()

	token, err := c.client.GetToken(c.ctx)
	if err != nil {
		return err
	}

	req := SubscriptionAuthRequest{
		Auth: SubscribeAuthParams{
			Token:   token,
			Streams: standxAccountAuthStreams(),
		},
	}

	c.mu.Lock()
	// Clear channel
	select {
	case <-c.authDone:
	default:
	}
	c.mu.Unlock()

	if err := c.wsClient.WriteJSON(req); err != nil {
		return err
	}

	select {
	case err := <-c.authDone:
		return err
	case <-c.ctx.Done():
		return c.ctx.Err()
	case <-time.After(10 * time.Second):
		return fmt.Errorf("auth timeout")
	}
}

func (c *WsAccountClient) SubscribeOrderUpdate(handler func(*Order)) error {
	c.mu.RLock()
	isAuth := c.IsAuth
	c.mu.RUnlock()

	if !isAuth {
		return fmt.Errorf("not authenticated")
	}
	return c.Subscribe("order", func(data []byte) {
		var order Order
		json.Unmarshal(data, &order)
		handler(&order)
	})
}

func (c *WsAccountClient) SubscribePositionUpdate(handler func(*Position)) error {
	c.mu.RLock()
	isAuth := c.IsAuth
	c.mu.RUnlock()

	if !isAuth {
		return fmt.Errorf("not authenticated")
	}
	return c.Subscribe("position", func(data []byte) {
		var position Position
		json.Unmarshal(data, &position)
		handler(&position)
	})
}

func (c *WsAccountClient) SubscribeBalanceUpdate(handler func(*Balance)) error {
	c.mu.RLock()
	isAuth := c.IsAuth
	c.mu.RUnlock()

	if !isAuth {
		return fmt.Errorf("not authenticated")
	}
	return c.Subscribe("balance", func(data []byte) {
		var balance Balance
		json.Unmarshal(data, &balance)
		handler(&balance)
	})
}

func (c *WsAccountClient) SubscribeTradeUpdate(handler func(*Trade)) error {
	c.mu.RLock()
	isAuth := c.IsAuth
	c.mu.RUnlock()

	if !isAuth {
		return fmt.Errorf("not authenticated")
	}
	return c.Subscribe("trade", func(data []byte) {
		var trade Trade
		json.Unmarshal(data, &trade)
		handler(&trade)
	})
}

func (c *WsAccountClient) Subscribe(channel string, handler func([]byte)) error {
	if !isStandXAccountChannel(channel) {
		return fmt.Errorf("unsupported standx account channel %q; supported channels: %s", channel, standxAccountChannelList)
	}

	// Register Handler
	c.mu.Lock()
	c.handlers[channel] = handler
	c.mu.Unlock()

	// Send Subscribe Request
	req := SubscriptionRequest{
		Subscribe: SubscribeParams{
			Channel: channel,
		},
	}
	return c.wsClient.WriteJSON(req)
}

func standxAccountAuthStreams() []SubscribeAuthChannel {
	return []SubscribeAuthChannel{
		{Channel: standxAccountChannelOrder},
		{Channel: standxAccountChannelPosition},
		{Channel: standxAccountChannelBalance},
		{Channel: standxAccountChannelTrade},
	}
}

func isStandXAccountChannel(channel string) bool {
	switch channel {
	case standxAccountChannelOrder,
		standxAccountChannelPosition,
		standxAccountChannelBalance,
		standxAccountChannelTrade:
		return true
	default:
		return false
	}
}

func (c *WsAccountClient) HandleMsg(data []byte) {
	// c.Logger.Info("received ws message", zap.String("data", string(data)))
	var resp WSResponse
	if err := json.Unmarshal(data, &resp); err != nil {
		c.Logger.Error("failed to unmarshal ws response", zap.Error(err))
		return
	}
	if resp.Channel == "auth" {
		// Auth Response
		var authResp WsAuthResponse
		if err := json.Unmarshal(resp.Data, &authResp); err != nil {
			c.Logger.Error("failed to unmarshal auth response", zap.Error(err))
			select {
			case c.authDone <- err:
			default:
			}
			return
		}
		if authResp.Code != 0 {
			c.Logger.Error("auth failed", zap.String("msg", authResp.Msg))
			select {
			case c.authDone <- fmt.Errorf("auth failed: %s", authResp.Msg):
			default:
			}
			return
		}
		c.Logger.Info("auth success")
		c.mu.Lock()
		c.IsAuth = true
		c.mu.Unlock()

		select {
		case c.authDone <- nil:
		default:
		}
		return
	}

	key := resp.Channel
	c.mu.RLock()
	handler, ok := c.handlers[key]
	c.mu.RUnlock()

	if !ok {
		// c.Logger.Debug("no handler for channel", zap.String("channel", key))
		return
	}

	// Pass only the data payload to the handler
	handler(resp.Data)
}

func (c *WsAccountClient) Close() {
	c.cancel()
	c.wsClient.Close()
}
