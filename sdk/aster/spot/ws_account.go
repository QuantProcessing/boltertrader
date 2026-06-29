package spot

import (
	"context"
	"encoding/json"
	"fmt"
)

type WsAccountClient struct {
	*WsClient
	APIKey     string
	SecretKey  string
	ListenKey  string
	StreamMgr  *UserStreamManager
	restClient *Client
}

func NewWsAccountClient(ctx context.Context, apiKey, secretKey string) *WsAccountClient {
	// Initial URL is base, will be updated when listen key is generated
	client := NewWSClient(ctx, WSBaseURL)

	restClient := NewClient(apiKey, secretKey)
	streamMgr := NewUserStreamManager(restClient)

	ac := &WsAccountClient{
		WsClient:   client,
		APIKey:     apiKey,
		SecretKey:  secretKey,
		StreamMgr:  streamMgr,
		restClient: restClient,
	}

	client.Handler = ac.handleMessage
	return ac
}

func (c *WsAccountClient) Connect() error {
	// 1. Get Listen Key
	listenKey, err := c.StreamMgr.Start(c.ctx)
	if err != nil {
		return fmt.Errorf("failed to start user stream: %w", err)
	}
	c.ListenKey = listenKey

	// 2. Update URL with listenKey
	// e.g. wss://stream.binance.com:9443/ws/<listenKey>
	c.WsClient.URL = fmt.Sprintf("%s/%s", WSBaseURL, listenKey)

	// 3. Connect WebSocket
	if err := c.WsClient.Connect(); err != nil {
		// Stop stream manager if connection fails
		c.StreamMgr.Stop(c.ctx)
		return err
	}

	return nil
}

func (c *WsAccountClient) Close() {
	c.WsClient.Close()
	if c.StreamMgr != nil {
		c.StreamMgr.Stop(context.Background())
	}
}

func (c *WsAccountClient) handleMessage(message []byte) {
	var header WsEventHeader
	if err := json.Unmarshal(message, &header); err != nil {
		return
	}

	c.CallSubscription(header.EventType, message)
}

func (c *WsAccountClient) SubscribeExecutionReport(handler func(*ExecutionReportEvent)) {
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
	c.SetHandler("outboundAccountPosition", func(data []byte) error {
		var event AccountPositionEvent
		if err := json.Unmarshal(data, &event); err != nil {
			return err
		}
		handler(&event)
		return nil
	})
}
