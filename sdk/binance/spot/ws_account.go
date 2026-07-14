package spot

import (
	"encoding/json"
	"fmt"
	"sync"
	"time"

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
	recoveryMu               sync.Mutex
	recoveryHookMu           sync.Mutex
	executionReportCallbacks []func(*ExecutionReportEvent)
	accountPositionCallbacks []func(*AccountPositionEvent)
	subscribed               bool
	subscribedConnEpoch      uint64
	recovering               bool
	recoveryEpoch            uint64
	onReconnectStarted       func(error)
	onReconnectRecovered     func()
	beforeRecoveryComplete   func()

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
	c.mu.Lock()
	subscribed := c.subscribed
	connEpoch := c.subscribedConnEpoch
	c.mu.Unlock()
	return subscribed && c.wsAPI.connectionEpochCurrent(connEpoch)
}

// SetReconnectHooks registers private-stream lifecycle callbacks. Recovered is
// invoked only after userDataStream.subscribe.signature succeeds again.
func (c *WsAccountClient) SetReconnectHooks(started func(error), recovered func()) {
	c.mu.Lock()
	c.onReconnectStarted = started
	c.onReconnectRecovered = recovered
	c.mu.Unlock()
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
	c.wsAPI.SetOnDisconnect(c.handleDisconnect)
	c.wsAPI.SetPostReconnect(c.restoreSubscription)

	// Ensure WsAPI is connected
	if !c.wsAPI.IsConnected() {
		if err := c.wsAPI.Connect(); err != nil {
			return fmt.Errorf("failed to connect ws-api: %w", err)
		}
	}

	return c.subscribeUserData()
}

func (c *WsAccountClient) subscribeUserData() error {
	c.mu.Lock()
	recoveryEpoch := c.recoveryEpoch
	c.mu.Unlock()
	return c.subscribeUserDataAt(recoveryEpoch)
}

func (c *WsAccountClient) subscribeUserDataAt(recoveryEpoch uint64) error {
	connectionEpoch, connected := c.wsAPI.connectionEpochSnapshot()
	if !connected {
		return fmt.Errorf("userDataStream.subscribe.signature failed: websocket is not connected")
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

	c.mu.Lock()
	if c.recoveryEpoch != recoveryEpoch || !c.wsAPI.connectionEpochCurrent(connectionEpoch) {
		c.mu.Unlock()
		return fmt.Errorf("userDataStream.subscribe.signature superseded by a newer connection generation")
	}
	c.subscribed = true
	c.subscribedConnEpoch = connectionEpoch
	c.mu.Unlock()
	c.Logger.Info("Subscribed to user data stream via WS-API (signature mode)")
	return nil
}

func (c *WsAccountClient) handleDisconnect(err error) {
	c.recoveryHookMu.Lock()
	defer c.recoveryHookMu.Unlock()

	// A transport failure supersedes every pushed event buffered by the failed
	// replacement. Keep the next generation paused until its Recovered hook has
	// completed synchronously.
	c.wsAPI.resetPushedEvents()
	c.wsAPI.pausePushedEvents()

	c.mu.Lock()
	c.recoveryEpoch++
	c.subscribed = false
	c.subscribedConnEpoch = 0
	if c.recovering {
		c.mu.Unlock()
		return
	}
	c.recovering = true
	handler := c.onReconnectStarted
	c.mu.Unlock()
	if handler != nil {
		handler(err)
	}
}

func (c *WsAccountClient) restoreSubscription() {
	c.recoveryMu.Lock()
	defer c.recoveryMu.Unlock()

	for {
		c.mu.Lock()
		recovering := c.recovering
		attemptEpoch := c.recoveryEpoch
		c.mu.Unlock()
		if !recovering || !c.wsAPI.IsConnected() {
			return
		}
		if err := c.subscribeUserDataAt(attemptEpoch); err != nil {
			c.Logger.Errorw("Failed to restore user data subscription", "error", err)
			c.mu.Lock()
			current := c.recovering && c.recoveryEpoch == attemptEpoch
			c.mu.Unlock()
			if !current {
				return
			}
			select {
			case <-c.wsAPI.ctx.Done():
				return
			case <-time.After(c.wsAPI.ReconnectWait):
			}
			continue
		}
		if c.beforeRecoveryComplete != nil {
			c.beforeRecoveryComplete()
		}
		if !c.completeReconnect(attemptEpoch) {
			return
		}
		return
	}
}

func (c *WsAccountClient) completeReconnect(epoch uint64) bool {
	c.recoveryHookMu.Lock()
	defer c.recoveryHookMu.Unlock()

	// A stale completion must not touch the dispatch barrier owned by a newer
	// active recovery generation.
	c.mu.Lock()
	activeAttempt := c.recovering && c.recoveryEpoch == epoch
	c.mu.Unlock()
	if !activeAttempt {
		return false
	}

	// Establish the dispatch barrier before validating the generation. Close can
	// invalidate this attempt without taking recoveryHookMu; its dispatcher Reset
	// then prevents a stale hook or buffered callback from starting.
	c.wsAPI.pausePushedEvents()
	c.mu.Lock()
	if !c.recovering || c.recoveryEpoch != epoch || !c.subscribed || !c.wsAPI.connectionEpochCurrent(c.subscribedConnEpoch) {
		recoveryStillActive := c.recovering
		c.mu.Unlock()
		if !recoveryStillActive {
			c.wsAPI.resetPushedEvents()
		}
		return false
	}
	c.recovering = false
	handler := c.onReconnectRecovered
	c.mu.Unlock()
	c.wsAPI.resumePushedEvents(func() {
		c.mu.Lock()
		current := c.recoveryEpoch == epoch && !c.recovering && c.subscribed
		c.mu.Unlock()
		if current && handler != nil {
			handler()
		}
	})
	return true
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
	callbacks := append([]func(*ExecutionReportEvent){}, c.executionReportCallbacks...)
	c.mu.Unlock()
	for _, cb := range callbacks {
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
	callbacks := append([]func(*AccountPositionEvent){}, c.accountPositionCallbacks...)
	c.mu.Unlock()
	for _, cb := range callbacks {
		cb(&event)
	}
}

func (c *WsAccountClient) Close() {
	// Don't close the shared wsAPI — that's managed by the adapter
	c.mu.Lock()
	c.recoveryEpoch++
	c.recovering = false
	c.subscribed = false
	c.subscribedConnEpoch = 0
	c.mu.Unlock()
	c.wsAPI.resetPushedEvents()
}
