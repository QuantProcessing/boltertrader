package spot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strings"
	"sync"
	"time"

	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
)

type WsAccountClient struct {
	*WsClient
	StreamMgr  *UserStreamManager
	restClient *Client

	mu            sync.RWMutex
	listenKey     string
	userStreamURL func(string) string

	executionReportCallbacks []func(*ExecutionReportEvent)
	accountPositionCallbacks []func(*AccountPositionEvent)
}

func NewWsAccountClient(ctx context.Context, profile astercommon.Profile, security *astercommon.SecurityContext) (*WsAccountClient, error) {
	restClient, err := NewClient(profile, security)
	if err != nil {
		return nil, err
	}
	transport := newWSClient(ctx, strings.TrimSuffix(profile.UserWSURL(), "/")+"/ws")
	client := &WsAccountClient{
		WsClient:   transport,
		StreamMgr:  NewUserStreamManager(restClient),
		restClient: restClient,
		userStreamURL: func(listenKey string) string {
			return strings.TrimSuffix(profile.UserWSURL(), "/") + "/ws/" + url.PathEscape(listenKey)
		},
	}
	transport.Handler = client.handleMessage
	client.StreamMgr.SetRenewHandler(client.handleListenKeyRenewed)
	client.registerHandlers()
	return client, nil
}

func (c *WsAccountClient) Connect() error {
	if c.IsConnected() {
		return nil
	}
	listenKey, err := c.StreamMgr.Start(c.ctx)
	if err != nil {
		return fmt.Errorf("aster spot account websocket: start user stream: %w", err)
	}
	c.setListenKey(listenKey)
	if err := c.WsClient.setURL(c.userStreamURL(listenKey)); err != nil {
		c.stopStreamManager()
		return err
	}
	if err := c.WsClient.Connect(); err != nil {
		c.stopStreamManager()
		return err
	}
	return nil
}

func (c *WsAccountClient) Close() {
	c.WsClient.Close()
	c.stopStreamManager()
}

func (c *WsAccountClient) ListenKey() string {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.listenKey
}

func (c *WsAccountClient) handleListenKeyRenewed(listenKey string) {
	c.setListenKey(listenKey)
	if err := c.WsClient.reconnectTo(c.userStreamURL(listenKey)); err != nil {
		c.Logger.Errorw("failed to reconnect renewed Aster Spot user stream", "error", err)
	}
}

func (c *WsAccountClient) setListenKey(listenKey string) {
	c.mu.Lock()
	c.listenKey = listenKey
	c.mu.Unlock()
}

func (c *WsAccountClient) stopStreamManager() {
	if c.StreamMgr == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := c.StreamMgr.Stop(ctx); err != nil {
		c.Logger.Warnw("failed to close Aster Spot user stream", "error", err)
	}
	c.setListenKey("")
}

func (c *WsAccountClient) handleMessage(message []byte) {
	var header WsEventHeader
	if err := json.Unmarshal(message, &header); err != nil {
		return
	}
	c.CallSubscription(header.EventType, message)
}

func (c *WsAccountClient) registerHandlers() {
	c.SetHandler("executionReport", c.handleExecutionReport)
	c.SetHandler("outboundAccountPosition", c.handleAccountPosition)
}

func (c *WsAccountClient) SubscribeExecutionReport(handler func(*ExecutionReportEvent)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.executionReportCallbacks = append(c.executionReportCallbacks, handler)
}

func (c *WsAccountClient) SubscribeAccountPosition(handler func(*AccountPositionEvent)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accountPositionCallbacks = append(c.accountPositionCallbacks, handler)
}

func (c *WsAccountClient) handleExecutionReport(data []byte) error {
	var event ExecutionReportEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}
	c.mu.RLock()
	callbacks := append([]func(*ExecutionReportEvent){}, c.executionReportCallbacks...)
	c.mu.RUnlock()
	for _, callback := range callbacks {
		if callback != nil {
			callback(&event)
		}
	}
	return nil
}

func (c *WsAccountClient) handleAccountPosition(data []byte) error {
	var event AccountPositionEvent
	if err := json.Unmarshal(data, &event); err != nil {
		return err
	}
	c.mu.RLock()
	callbacks := append([]func(*AccountPositionEvent){}, c.accountPositionCallbacks...)
	c.mu.RUnlock()
	for _, callback := range callbacks {
		if callback != nil {
			callback(&event)
		}
	}
	return nil
}
