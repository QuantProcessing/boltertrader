package perp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
)

type WsAccountClient struct {
	*WsClient
	Client        *Client
	StreamMgr     *PerpUserStreamManager
	KeepAliveInt  time.Duration
	ListenKey     string
	profile       astercommon.Profile
	userStreamURL func(string) string

	mu                           sync.Mutex
	accountUpdateCallbacks       []func(*AccountUpdateEvent)
	orderUpdateCallbacks         []func(*OrderUpdateEvent)
	accountConfigUpdateCallbacks []func(*AccountConfigUpdateEvent)
}

func NewWsAccountClient(ctx context.Context, profile astercommon.Profile, security *astercommon.SecurityContext) (*WsAccountClient, error) {
	restClient, err := NewClient(profile, security)
	if err != nil {
		return nil, err
	}
	client := &WsAccountClient{
		Client:       restClient,
		StreamMgr:    NewPerpUserStreamManager(restClient),
		WsClient:     newWSClient(ctx, strings.TrimSuffix(profile.UserWSURL(), "/")+"/ws"),
		KeepAliveInt: 50 * time.Minute,
		profile:      profile,
		userStreamURL: func(listenKey string) string {
			return strings.TrimSuffix(profile.UserWSURL(), "/") + "/ws/" + url.PathEscape(listenKey)
		},
	}
	client.StreamMgr.KeepAliveInt = client.KeepAliveInt
	client.WsClient.Logger = zap.NewNop().Sugar().Named("aster-account")
	client.WsClient.Handler = client.handleMessage
	client.StreamMgr.SetRenewHandler(client.handleListenKeyRenewed)
	client.registerHandlers()
	return client, nil
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
	if c.IsConnected() {
		return nil
	}
	c.registerHandlers()

	listenKey, err := c.StreamMgr.Start(c.ctx)
	if err != nil {
		return fmt.Errorf("aster perp account websocket: start user stream: %w", err)
	}
	c.setListenKey(listenKey)

	if err := c.WsClient.setURL(c.userStreamURL(listenKey)); err != nil {
		c.stopStreamManager()
		return err
	}

	if err := c.WsClient.Connect(); err != nil {
		c.stopStreamManager()
		return fmt.Errorf("failed to connect user stream: %w", err)
	}
	return nil
}

func (c *WsAccountClient) registerHandlers() {
	c.SetHandler("ACCOUNT_UPDATE", c.handleAccountUpdate)
	c.SetHandler("ORDER_TRADE_UPDATE", c.handleOrderUpdate)
	c.SetHandler("ACCOUNT_CONFIG_UPDATE", c.handleAccountConfigUpdate)
	c.SetHandler("listenKeyExpired", c.handleListenKeyExpired)
}

func (c *WsAccountClient) handleMessage(message []byte) {
	var raw map[string]interface{}
	if err := json.Unmarshal(message, &raw); err != nil {
		c.Logger.Error("Failed to unmarshal message", "error", err)
		return
	}

	eventType, ok := raw["e"].(string)
	if !ok || eventType == "" {
		return
	}

	c.CallSubscription(eventType, message)
}

func (c *WsAccountClient) handleAccountUpdate(data []byte) error {
	var event AccountUpdateEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}
	c.mu.Lock()
	callbacks := append([]func(*AccountUpdateEvent){}, c.accountUpdateCallbacks...)
	c.mu.Unlock()
	for _, cb := range callbacks {
		if cb != nil {
			cb(&event)
		}
	}
	return nil
}

func (c *WsAccountClient) handleOrderUpdate(data []byte) error {
	var event OrderUpdateEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}
	c.mu.Lock()
	callbacks := append([]func(*OrderUpdateEvent){}, c.orderUpdateCallbacks...)
	c.mu.Unlock()
	for _, cb := range callbacks {
		if cb != nil {
			cb(&event)
		}
	}
	return nil
}

func (c *WsAccountClient) handleAccountConfigUpdate(data []byte) error {
	var event AccountConfigUpdateEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}
	c.mu.Lock()
	callbacks := append([]func(*AccountConfigUpdateEvent){}, c.accountConfigUpdateCallbacks...)
	c.mu.Unlock()
	for _, cb := range callbacks {
		if cb != nil {
			cb(&event)
		}
	}
	return nil
}

func (c *WsAccountClient) handleListenKeyExpired(data []byte) error {
	ctxAPI, cancelAPI := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancelAPI()
	_, err := c.StreamMgr.Renew(ctxAPI)
	if err != nil {
		c.Logger.Error("failed to create new listenKey on expiry", "error", err)
		return err
	}
	return nil
}

func (c *WsAccountClient) handleListenKeyRenewed(listenKey string) {
	c.setListenKey(listenKey)
	if err := c.WsClient.reconnectTo(c.userStreamURL(listenKey)); err != nil {
		c.Logger.Errorw("failed to reconnect renewed Aster Perp user stream", "error", err)
	}
}

func (c *WsAccountClient) setListenKey(listenKey string) {
	c.mu.Lock()
	c.ListenKey = listenKey
	c.mu.Unlock()
}

func (c *WsAccountClient) stopStreamManager() {
	if c.StreamMgr == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.StreamMgr.Stop(ctx); err != nil {
		c.Logger.Warnw("failed to close Aster Perp user stream", "error", err)
	}
	c.setListenKey("")
}

func (c *WsAccountClient) Close() {
	c.WsClient.Close()
	c.stopStreamManager()
}
