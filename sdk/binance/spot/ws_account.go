package spot

import (
	"encoding/json"
	"fmt"
	"sync"

	"go.uber.org/zap"
)

// WsAccountClient handles user data stream via WebSocket API.
// Since Feb 20, 2026, Binance deprecated the listenKey system.
// User data events are now received by subscribing on the WS-API connection
// using userDataStream.subscribe.signature with HMAC-SHA256 API key.
//
// This client shares the same WsAPIClient as the order client,
// subscribing to user data events on the same connection used for orders.
type WsAccountClient struct {
	wsAPI     *WsAPIClient // shared with order client
	apiKey    string
	secretKey string

	mu                       sync.Mutex
	executionReportCallbacks []func(*ExecutionReportEvent)
	accountPositionCallbacks []func(*AccountPositionEvent)
	subscribed               bool

	Logger *zap.SugaredLogger
}

// NewWsAccountClient creates a WsAccountClient that uses a shared WsAPIClient.
func NewWsAccountClient(wsAPI *WsAPIClient, apiKey, apiSecret string) *WsAccountClient {
	client := &WsAccountClient{
		wsAPI:     wsAPI,
		apiKey:    apiKey,
		secretKey: apiSecret,
		Logger:    zap.NewNop().Sugar().Named("binance-spot-account"),
	}
	return client
}

func (c *WsAccountClient) IsConnected() bool {
	return c.wsAPI.IsConnected() && c.subscribed
}

func (c *WsAccountClient) SubscribeExecutionReport(callback func(*ExecutionReportEvent)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.executionReportCallbacks = append(c.executionReportCallbacks, callback)
}

func (c *WsAccountClient) SubscribeAccountPosition(callback func(*AccountPositionEvent)) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.accountPositionCallbacks = append(c.accountPositionCallbacks, callback)
}

// Connect subscribes to user data stream on the shared WsAPI connection.
// The WsAPIClient must already be connected before calling this.
func (c *WsAccountClient) Connect() error {
	// Register event handler for pushed user data events
	c.wsAPI.SetEventHandler(c.handlePushedEvent)

	// Ensure WsAPI is connected
	if !c.wsAPI.IsConnected() {
		if err := c.wsAPI.Connect(); err != nil {
			return fmt.Errorf("failed to connect ws-api: %w", err)
		}
	}

	// Subscribe using userDataStream.subscribe.signature (supports HMAC-SHA256)
	ts := Timestamp()
	params := map[string]interface{}{
		"apiKey":    c.apiKey,
		"timestamp": ts,
	}
	q := BuildQueryString(params)
	sig := GenerateSignature(c.secretKey, q)
	params["signature"] = sig

	subID := fmt.Sprintf("uds_%d", ts)
	subReq := map[string]interface{}{
		"id":     subID,
		"method": "userDataStream.subscribe.signature",
		"params": params,
	}

	respData, err := c.wsAPI.SendRequest(subID, subReq)
	if err != nil {
		return fmt.Errorf("userDataStream.subscribe.signature failed: %w", err)
	}

	var subResp struct {
		ID     interface{} `json:"id"`
		Result interface{} `json:"result"`
		Error  *struct {
			Code int    `json:"code"`
			Msg  string `json:"msg"`
		} `json:"error"`
	}
	if err := json.Unmarshal(respData, &subResp); err != nil {
		return fmt.Errorf("failed to parse subscribe response: %w", err)
	}
	if subResp.Error != nil {
		return fmt.Errorf("subscribe error: code=%d, msg=%s", subResp.Error.Code, subResp.Error.Msg)
	}

	c.subscribed = true
	c.Logger.Info("Subscribed to user data stream via WS-API (signature mode)")
	return nil
}

// handlePushedEvent handles user data events pushed by the server.
// Events from the WS-API are wrapped: {"subscriptionId":0,"event":{"e":"executionReport",...}}
func (c *WsAccountClient) handlePushedEvent(message []byte) {
	var wrapper struct {
		SubscriptionID int             `json:"subscriptionId"`
		Event          json.RawMessage `json:"event"`
	}
	if err := json.Unmarshal(message, &wrapper); err != nil {
		c.Logger.Errorw("Failed to unmarshal pushed event", "error", err)
		return
	}

	// If there's no event field, try parsing as a direct event (fallback)
	eventData := wrapper.Event
	if len(eventData) == 0 {
		eventData = message
	}

	var raw map[string]interface{}
	if err := json.Unmarshal(eventData, &raw); err != nil {
		c.Logger.Errorw("Failed to unmarshal event data", "error", err)
		return
	}

	eventType, ok := raw["e"].(string)
	if !ok || eventType == "" {
		return
	}

	c.Logger.Infow("Parsed Event Type", "type", eventType)

	switch eventType {
	case "executionReport":
		c.handleExecutionReport(eventData)
	case "outboundAccountPosition":
		c.handleAccountPosition(eventData)
	}
}

func (c *WsAccountClient) handleExecutionReport(data []byte) {
	var event ExecutionReportEvent
	if err := json.Unmarshal(data, &event); err != nil {
		c.Logger.Errorw("Failed to unmarshal executionReport", "error", err)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cb := range c.executionReportCallbacks {
		cb(&event)
	}
}

func (c *WsAccountClient) handleAccountPosition(data []byte) {
	var event AccountPositionEvent
	if err := json.Unmarshal(data, &event); err != nil {
		c.Logger.Errorw("Failed to unmarshal accountPosition", "error", err)
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	for _, cb := range c.accountPositionCallbacks {
		cb(&event)
	}
}

func (c *WsAccountClient) Close() {
	// Don't close the shared wsAPI — that's managed by the adapter
}
