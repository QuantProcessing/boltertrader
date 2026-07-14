package perp

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"
)

type wsAccountLifecycle struct {
	ctx    context.Context
	cancel context.CancelFunc

	recoveryMu                sync.Mutex
	recoveryWorkerRunning     bool
	recoveryRequestGeneration uint64
}

type WsAccountClient struct {
	*WsClient
	Client       *Client
	ctx          context.Context
	BaseURL      string
	KeepAliveInt time.Duration
	ListenKey    string

	mu                           sync.Mutex
	lifecycleMu                  sync.Mutex
	lifecycleStateMu             sync.Mutex
	lifecycle                    *wsAccountLifecycle
	activeWS                     atomic.Pointer[WSClient]
	recoveryHookMu               sync.Mutex
	recoveryEpoch                uint64
	recoveryRetryWait            time.Duration
	beforeRecoveryClientReplace  func()
	accountUpdateCallbacks       []func(*AccountUpdateEvent)
	orderUpdateCallbacks         []func(*OrderUpdateEvent)
	algoUpdateCallbacks          []func(*AlgoUpdateEvent)
	accountConfigUpdateCallbacks []func(*AccountConfigUpdateEvent)
	onResubscribe                func()
	onReconnectStarted           func(error)
	onReconnectRecovered         func()
	recovering                   bool
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
		Client:            restClient,
		ctx:               ctx,
		BaseURL:           baseURL,
		KeepAliveInt:      50 * time.Minute,
		recoveryRetryWait: time.Second,
	}
	client.lifecycle = client.newLifecycle()
	client.resetWSClient()
	return client
}

func (c *WsAccountClient) newLifecycle() *wsAccountLifecycle {
	ctx, cancel := context.WithCancel(c.ctx)
	return &wsAccountLifecycle{ctx: ctx, cancel: cancel}
}

func (c *WsAccountClient) currentLifecycle() *wsAccountLifecycle {
	c.lifecycleStateMu.Lock()
	defer c.lifecycleStateMu.Unlock()
	return c.lifecycle
}

func (c *WsAccountClient) lifecycleIsCurrent(lifecycle *wsAccountLifecycle) bool {
	if lifecycle == nil || lifecycle.ctx.Err() != nil {
		return false
	}
	c.lifecycleStateMu.Lock()
	defer c.lifecycleStateMu.Unlock()
	return c.lifecycle == lifecycle
}

func (c *WsAccountClient) ensureLifecycleLocked() (*wsAccountLifecycle, bool, error) {
	if err := c.ctx.Err(); err != nil {
		return nil, false, err
	}
	if lifecycle := c.currentLifecycle(); lifecycle != nil && lifecycle.ctx.Err() == nil {
		return lifecycle, false, nil
	}

	lifecycle := c.newLifecycle()
	c.lifecycleStateMu.Lock()
	c.lifecycle = lifecycle
	c.lifecycleStateMu.Unlock()

	c.mu.Lock()
	c.recoveryEpoch++
	c.recovering = false
	c.mu.Unlock()
	return lifecycle, true, nil
}

func (c *WsAccountClient) WithURL(url string) *WsAccountClient {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.BaseURL = url
	if c.WsClient != nil {
		c.WsClient.URL = url
	}
	return c
}

func (c *WsAccountClient) SetOnResubscribe(handler func()) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.onResubscribe = handler
}

// SetReconnectHooks registers private-stream lifecycle callbacks. Recovered is
// invoked only after a fresh listen key and its WebSocket connection are live.
func (c *WsAccountClient) SetReconnectHooks(started func(error), recovered func()) {
	c.mu.Lock()
	c.onReconnectStarted = started
	c.onReconnectRecovered = recovered
	c.mu.Unlock()
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
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	lifecycle, renewed, err := c.ensureLifecycleLocked()
	if err != nil {
		return err
	}
	if renewed {
		if ws := c.WsClient; ws != nil {
			ws.Close()
		}
		c.resetWSClientLocked(lifecycle)
	}
	return c.connectLocked(lifecycle)
}

func (c *WsAccountClient) connectLocked(lifecycle *wsAccountLifecycle) error {
	if !c.lifecycleIsCurrent(lifecycle) {
		return context.Canceled
	}
	if err := lifecycle.ctx.Err(); err != nil {
		return err
	}
	ws := c.WsClient
	if ws == nil || wsClientIsClosed(ws) {
		ws = c.resetWSClientLocked(lifecycle)
	}

	// 创建 listen key 时使用带超时的子 context
	ctxAPI, cancelAPI := context.WithTimeout(lifecycle.ctx, 10*time.Second)
	defer cancelAPI()
	listenKey, err := c.Client.CreateListenKey(ctxAPI)
	if err != nil {
		return err
	}
	c.ListenKey = listenKey

	// Configure WsClient with listenKey URL
	ws.URL = c.BaseURL + "/" + listenKey

	// Register handlers
	ws.SetHandler("ACCOUNT_UPDATE", c.handleAccountUpdate)
	ws.SetHandler("ORDER_TRADE_UPDATE", c.handleOrderUpdate)
	ws.SetHandler("ALGO_UPDATE", c.handleAlgoUpdate)
	ws.SetHandler("ACCOUNT_CONFIG_UPDATE", c.handleAccountConfigUpdate)
	ws.SetHandler("listenKeyExpired", func(data []byte) error {
		return c.handleListenKeyExpired(lifecycle, ws, data)
	})
	ws.SetPostReconnect(func() {
		go c.resubscribeLifecycle(lifecycle, ws)
	})

	// Connect WebSocket
	if err := ws.Connect(); err != nil {
		return fmt.Errorf("failed to connect user stream: %w", err)
	}

	go c.keepAlive(lifecycle, ws)

	return nil
}

func (c *WsAccountClient) handleMessage(message []byte) {
	c.lifecycleMu.Lock()
	ws := c.WsClient
	c.lifecycleMu.Unlock()
	c.handleWSMessage(ws, message)
}

func (c *WsAccountClient) handleWSMessage(ws *WsClient, message []byte) {
	if ws == nil {
		return
	}
	ws.Logger.Debugw("Received private account message", "bytes", len(message))

	// Use generic map to handle various types
	var raw map[string]interface{}
	if err := json.Unmarshal(message, &raw); err != nil {
		ws.Logger.Error("Failed to unmarshal message", "error", err)
		return
	}

	eventType, ok := raw["e"].(string)
	if !ok || eventType == "" {
		// Log raw message if no type found
		// c.Logger.Warn("No event type found (e)", "msg", string(message))
		return
	}

	ws.Logger.Info("Parsed Event Type", "type", eventType)
	ws.CallSubscription(eventType, message)
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
	callbacks := append([]func(*OrderUpdateEvent){}, c.orderUpdateCallbacks...)
	c.mu.Unlock()
	for _, cb := range callbacks {
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
	callbacks := append([]func(*AlgoUpdateEvent){}, c.algoUpdateCallbacks...)
	c.mu.Unlock()
	for _, cb := range callbacks {
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
	callbacks := append([]func(*AccountConfigUpdateEvent){}, c.accountConfigUpdateCallbacks...)
	c.mu.Unlock()
	for _, cb := range callbacks {
		cb(&event)
	}
	return nil
}

func (c *WsAccountClient) handleListenKeyExpired(lifecycle *wsAccountLifecycle, ws *WsClient, data []byte) error {
	ws.Logger.Info("ListenKey expired")
	go c.resubscribeLifecycle(lifecycle, ws)
	return nil
}

func (c *WsAccountClient) resubscribe() {
	lifecycle := c.currentLifecycle()
	c.lifecycleMu.Lock()
	ws := c.WsClient
	c.lifecycleMu.Unlock()
	c.resubscribeLifecycle(lifecycle, ws)
}

func (c *WsAccountClient) resubscribeLifecycle(lifecycle *wsAccountLifecycle, sourceWS *WsClient) {
	if !c.lifecycleOwnsWS(lifecycle, sourceWS) {
		return
	}

	lifecycle.recoveryMu.Lock()
	lifecycle.recoveryRequestGeneration++
	if lifecycle.recoveryWorkerRunning {
		lifecycle.recoveryMu.Unlock()
		return
	}
	lifecycle.recoveryWorkerRunning = true
	lifecycle.recoveryMu.Unlock()
	if lifecycle.ctx.Err() != nil {
		c.stopRecoveryWorker(lifecycle)
		return
	}

	c.beginRecoveryForSource(lifecycle, sourceWS, fmt.Errorf("binance perp private stream subscription recovery started"))
	for {
		if !c.lifecycleIsCurrent(lifecycle) || lifecycle.ctx.Err() != nil {
			c.stopRecoveryWorker(lifecycle)
			return
		}
		lifecycle.recoveryMu.Lock()
		requestGeneration := lifecycle.recoveryRequestGeneration
		lifecycle.recoveryMu.Unlock()
		attemptEpoch := c.currentRecoveryEpoch(lifecycle)

		ws, err := c.replaceAndConnectForRecovery(lifecycle)
		if err != nil {
			if !c.lifecycleIsCurrent(lifecycle) || lifecycle.ctx.Err() != nil {
				c.stopRecoveryWorker(lifecycle)
				return
			}
			if ws != nil {
				ws.Logger.Errorw("Failed to resubscribe user stream", "error", err)
			}
			if !c.waitForRecoveryRetry(lifecycle) {
				c.stopRecoveryWorker(lifecycle)
				return
			}
			continue
		}

		if !c.recoveryAttemptIsLive(lifecycle, attemptEpoch, ws) {
			ws.Close()
			if !c.waitForRecoveryRetry(lifecycle) {
				c.stopRecoveryWorker(lifecycle)
				return
			}
			continue
		}

		c.mu.Lock()
		handler := c.onResubscribe
		c.mu.Unlock()
		if handler != nil {
			handler()
		}

		if !c.completeRecoveryAttempt(lifecycle, requestGeneration, attemptEpoch, ws) {
			ws.Close()
			c.beginRecoveryForSource(lifecycle, ws, fmt.Errorf("binance perp private stream recovery retriggered before completion"))
			continue
		}

		// A trigger registered while the recovered callback was running is a
		// newer generation. Keep this worker alive and renew the listen key again;
		// lifecycle callbacks remain ordered recovered(old) -> started(new).
		lifecycle.recoveryMu.Lock()
		if lifecycle.recoveryRequestGeneration != requestGeneration {
			lifecycle.recoveryMu.Unlock()
			ws.Close()
			c.beginRecoveryForSource(lifecycle, ws, fmt.Errorf("binance perp private stream recovery retriggered during completion"))
			continue
		}
		lifecycle.recoveryWorkerRunning = false
		lifecycle.recoveryMu.Unlock()
		return
	}
}

func (c *WsAccountClient) stopRecoveryWorker(lifecycle *wsAccountLifecycle) {
	lifecycle.recoveryMu.Lock()
	lifecycle.recoveryWorkerRunning = false
	lifecycle.recoveryMu.Unlock()
}

func (c *WsAccountClient) lifecycleOwnsWS(lifecycle *wsAccountLifecycle, ws *WsClient) bool {
	if !c.lifecycleIsCurrent(lifecycle) || ws == nil {
		return false
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	return c.lifecycleIsCurrent(lifecycle) && c.WsClient == ws
}

func (c *WsAccountClient) replaceAndConnectForRecovery(lifecycle *wsAccountLifecycle) (*WsClient, error) {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()

	if !c.lifecycleIsCurrent(lifecycle) {
		return nil, context.Canceled
	}
	if ws := c.WsClient; ws != nil {
		ws.Close()
	}
	if err := lifecycle.ctx.Err(); err != nil {
		return nil, err
	}
	if c.beforeRecoveryClientReplace != nil {
		c.beforeRecoveryClientReplace()
	}
	if !c.lifecycleIsCurrent(lifecycle) {
		return nil, context.Canceled
	}
	if err := lifecycle.ctx.Err(); err != nil {
		return nil, err
	}
	ws := c.resetWSClientLocked(lifecycle)
	// The replacement read loop starts inside Connect, so pause before Connect
	// to prevent fresh private data from overtaking the Recovered callback.
	ws.PauseDispatch()
	if err := c.connectLocked(lifecycle); err != nil {
		ws.ResetDispatch()
		return ws, err
	}
	return ws, nil
}

func (c *WsAccountClient) keepAlive(lifecycle *wsAccountLifecycle, ws *WsClient) {
	ticker := time.NewTicker(c.KeepAliveInt)
	defer ticker.Stop()

	for {
		select {
		case <-ws.ctx.Done():
			return
		case <-ticker.C:
			if err := c.Client.KeepAliveListenKey(ws.ctx); err != nil {
				fmt.Printf("Failed to keep alive listen key: %v\n", err)
				go c.resubscribeLifecycle(lifecycle, ws)
				return
			} else {
				ws.Logger.Debug("Keep alive listen key successfully")
			}
		}
	}
}

func (c *WsAccountClient) Close() {
	lifecycle := c.currentLifecycle()
	if lifecycle != nil {
		lifecycle.cancel()
	}
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	c.lifecycleStateMu.Lock()
	current := c.lifecycle
	c.lifecycleStateMu.Unlock()
	if current == lifecycle {
		ws := c.WsClient
		if ws != nil {
			ws.Close()
		}
	}
}

func (c *WsAccountClient) resetWSClient() {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	lifecycle, _, err := c.ensureLifecycleLocked()
	if err != nil {
		return
	}
	c.resetWSClientLocked(lifecycle)
}

func (c *WsAccountClient) resetWSClientLocked(lifecycle *wsAccountLifecycle) *WsClient {
	ws := NewWSClient(lifecycle.ctx, c.BaseURL)
	ws.Logger = zap.NewNop().Sugar().Named("binance-account")
	ws.Handler = func(message []byte) {
		c.handleWSMessage(ws, message)
	}
	ws.SetOnDisconnect(func(err error) {
		c.beginRecoveryForSource(lifecycle, ws, err)
		// Start fresh-listen-key recovery immediately. Waiting for the embedded
		// transport's stale-URL reconnect to succeed can strand the account
		// stream forever when every old-key handshake is rejected.
		go c.resubscribeLifecycle(lifecycle, ws)
	})
	ws.SetPostReconnect(func() {
		go c.resubscribeLifecycle(lifecycle, ws)
	})
	c.WsClient = ws
	c.activeWS.Store(ws)
	return ws
}

func wsClientIsClosed(ws *WsClient) bool {
	ws.Mu.RLock()
	defer ws.Mu.RUnlock()
	return ws.isClosed
}

func (c *WsAccountClient) beginRecovery(err error) {
	lifecycle := c.currentLifecycle()
	c.lifecycleMu.Lock()
	ws := c.WsClient
	c.lifecycleMu.Unlock()
	c.beginRecoveryForSource(lifecycle, ws, err)
}

func (c *WsAccountClient) beginRecoveryFor(lifecycle *wsAccountLifecycle, err error) {
	c.lifecycleMu.Lock()
	ws := c.WsClient
	c.lifecycleMu.Unlock()
	c.beginRecoveryForSource(lifecycle, ws, err)
}

func (c *WsAccountClient) beginRecoveryForSource(lifecycle *wsAccountLifecycle, sourceWS *WsClient, err error) {
	c.recoveryHookMu.Lock()
	defer c.recoveryHookMu.Unlock()
	if !c.lifecycleIsCurrent(lifecycle) || sourceWS != nil && c.activeWS.Load() != sourceWS {
		return
	}

	c.mu.Lock()
	c.recoveryEpoch++
	attemptEpoch := c.recoveryEpoch
	if c.recovering {
		c.mu.Unlock()
		if sourceWS != nil {
			sourceWS.stopSourceDispatchForRecovery()
		}
		return
	}
	c.recovering = true
	handler := c.onReconnectStarted
	c.mu.Unlock()
	beforeRecovery := func() {
		if !c.lifecycleIsCurrent(lifecycle) || sourceWS != nil && c.activeWS.Load() != sourceWS {
			return
		}
		c.mu.Lock()
		current := c.recovering && c.recoveryEpoch == attemptEpoch
		c.mu.Unlock()
		if !current {
			return
		}
		if handler != nil {
			handler(err)
		}
	}
	if sourceWS != nil {
		sourceWS.pauseSourceDispatchForRecovery(beforeRecovery)
	} else {
		beforeRecovery()
	}
}

func (c *WsAccountClient) currentRecoveryEpoch(lifecycle *wsAccountLifecycle) uint64 {
	if !c.lifecycleIsCurrent(lifecycle) {
		return 0
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.recoveryEpoch
}

func (c *WsAccountClient) recoveryAttemptIsLive(lifecycle *wsAccountLifecycle, epoch uint64, ws *WsClient) bool {
	if !c.lifecycleIsCurrent(lifecycle) || ws == nil || !ws.IsConnected() {
		return false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.recovering && c.recoveryEpoch == epoch && c.lifecycleIsCurrent(lifecycle)
}

func (c *WsAccountClient) completeRecoveryAttempt(lifecycle *wsAccountLifecycle, requestGeneration, epoch uint64, ws *WsClient) bool {
	c.recoveryHookMu.Lock()
	defer c.recoveryHookMu.Unlock()
	if !c.lifecycleIsCurrent(lifecycle) {
		return false
	}

	// The generation check and recovery state transition are serialized with
	// request registration. A request that linearized first invalidates this
	// completion; one that arrives during the callback is handled by the worker
	// immediately after this method returns.
	lifecycle.recoveryMu.Lock()
	generationCurrent := lifecycle.recoveryRequestGeneration == requestGeneration
	lifecycle.recoveryMu.Unlock()
	if !generationCurrent {
		return false
	}
	if !c.lifecycleIsCurrent(lifecycle) || ws == nil || !ws.IsConnected() {
		return false
	}
	c.mu.Lock()
	if !c.recovering || c.recoveryEpoch != epoch || !c.lifecycleIsCurrent(lifecycle) {
		c.mu.Unlock()
		return false
	}
	c.recovering = false
	handler := c.onReconnectRecovered
	c.mu.Unlock()
	ws.ResumeDispatch(handler)
	return true
}

func (c *WsAccountClient) waitForRecoveryRetry(lifecycle *wsAccountLifecycle) bool {
	wait := c.recoveryRetryWait
	if wait <= 0 {
		wait = time.Second
	}
	if wait > 30*time.Second {
		wait = 30 * time.Second
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-lifecycle.ctx.Done():
		return false
	case <-timer.C:
		return c.lifecycleIsCurrent(lifecycle)
	}
}
