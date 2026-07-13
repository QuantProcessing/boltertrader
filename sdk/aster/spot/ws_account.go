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

func (c *WsAccountClient) SubscribeExecutionReport(handler func(*ExecutionReportEvent)) {
	if handler == nil {
		c.SetHandler("executionReport", nil)
		return
	}
	c.SetHandler("executionReport", func(data []byte) error {
		var event ExecutionReportEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		handler(&event)
		return nil
	})
}

func (c *WsAccountClient) SubscribeAccountPosition(handler func(*AccountPositionEvent)) {
	if handler == nil {
		c.SetHandler("outboundAccountPosition", nil)
		return
	}
	c.SetHandler("outboundAccountPosition", func(data []byte) error {
		var event AccountPositionEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		handler(&event)
		return nil
	})
}
