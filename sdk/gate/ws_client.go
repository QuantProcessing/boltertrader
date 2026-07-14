package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type wsRequest struct {
	ID      uint64  `json:"id,omitempty"`
	Time    int64   `json:"time"`
	Channel string  `json:"channel"`
	Event   string  `json:"event"`
	Payload any     `json:"payload,omitempty"`
	Auth    *WSAuth `json:"auth,omitempty"`
}

type WSAuth struct {
	Method string `json:"method"`
	Key    string `json:"KEY"`
	Sign   string `json:"SIGN"`
}

type wsSubscription struct {
	channel string
	payload []string
	handler func(json.RawMessage)
}

type WSClient struct {
	url       string
	product   string
	apiKey    string
	secretKey string
	now       func() time.Time

	ctx    context.Context
	cancel context.CancelFunc

	mu             sync.RWMutex
	lifecycleMu    sync.Mutex
	hookMu         sync.Mutex
	writeMu        sync.Mutex
	conn           *websocket.Conn
	reconnecting   bool
	recoveryWait   chan struct{}
	closed         bool
	subs           map[string]wsSubscription
	handlers       map[string]func(json.RawMessage)
	reconnectStart func(error)
	reconnectDone  func()

	recoveryGeneration uint64
	callbackDispatcher *gateWSCallbackDispatcher

	subscriptionMu         sync.Mutex
	subscriptionWaiters    map[uint64]gateSubscriptionWaiter
	subscriptionSequence   atomic.Uint64
	subscriptionAckTimeout time.Duration
}

type gateSubscriptionWaiter struct {
	conn    *websocket.Conn
	channel string
	event   string
	ch      chan error
}

func NewWSClient(product string) (*WSClient, error) {
	ctx, cancel := context.WithCancel(context.Background())
	url, err := wsURLForProduct(product)
	if err != nil {
		cancel()
		return nil, err
	}
	return &WSClient{
		url:                    url,
		product:                product,
		now:                    time.Now,
		ctx:                    ctx,
		cancel:                 cancel,
		subs:                   make(map[string]wsSubscription),
		handlers:               make(map[string]func(json.RawMessage)),
		subscriptionWaiters:    make(map[uint64]gateSubscriptionWaiter),
		subscriptionAckTimeout: 5 * time.Second,
		callbackDispatcher:     newGateWSCallbackDispatcher(),
	}, nil
}

func MustNewWSClient(product string) *WSClient {
	client, err := NewWSClient(product)
	if err != nil {
		panic(err)
	}
	return client
}

func (c *WSClient) WithCredentials(apiKey, secretKey string) *WSClient {
	c.apiKey = apiKey
	c.secretKey = secretKey
	return c
}

func (c *WSClient) WithURL(rawURL string) *WSClient {
	c.url = rawURL
	return c
}

func (c *WSClient) WithClock(now func() time.Time) *WSClient {
	if now != nil {
		c.now = now
	}
	return c
}

// SetReconnectHooks reports an unexpected websocket loss and the point at
// which the replacement connection has successfully rewritten every saved
// subscription. Initial connections do not invoke these hooks.
func (c *WSClient) SetReconnectHooks(started func(error), recovered func()) {
	c.mu.Lock()
	c.reconnectStart = started
	c.reconnectDone = recovered
	c.mu.Unlock()
}

func (c *WSClient) startReconnectLocked() bool {
	if c.closed || c.reconnecting {
		return false
	}
	c.reconnecting = true
	c.recoveryGeneration++
	c.recoveryWait = make(chan struct{})
	return true
}

func (c *WSClient) finishReconnectLocked() {
	if !c.reconnecting {
		return
	}
	c.reconnecting = false
	if c.recoveryWait != nil {
		close(c.recoveryWait)
		c.recoveryWait = nil
	}
}

func (c *WSClient) waitForRecovery(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		c.mu.RLock()
		closed := c.closed
		reconnecting := c.reconnecting
		wait := c.recoveryWait
		c.mu.RUnlock()
		if closed {
			return fmt.Errorf("gate ws: client closed")
		}
		if !reconnecting {
			return nil
		}
		if wait == nil {
			return fmt.Errorf("gate ws: recovery state unavailable")
		}
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		case <-c.ctx.Done():
			return fmt.Errorf("gate ws: client closed")
		}
	}
}

// lockSubscriptionLifecycle returns with lifecycleMu held. A caller that
// arrives during recovery waits until the saved subscription set is restored,
// so it cannot use the replacement connection before replay completes.
func (c *WSClient) lockSubscriptionLifecycle(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := c.waitForRecovery(ctx); err != nil {
			return err
		}
		c.lifecycleMu.Lock()
		c.mu.RLock()
		closed := c.closed
		reconnecting := c.reconnecting
		c.mu.RUnlock()
		if closed {
			c.lifecycleMu.Unlock()
			return fmt.Errorf("gate ws: client closed")
		}
		if reconnecting {
			c.lifecycleMu.Unlock()
			continue
		}
		if err := c.Connect(ctx); err != nil {
			c.lifecycleMu.Unlock()
			return err
		}
		c.mu.RLock()
		closed = c.closed
		reconnecting = c.reconnecting
		c.mu.RUnlock()
		if closed {
			c.lifecycleMu.Unlock()
			return fmt.Errorf("gate ws: client closed")
		}
		if reconnecting {
			c.lifecycleMu.Unlock()
			continue
		}
		return nil
	}
}

func (c *WSClient) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return fmt.Errorf("gate ws: client closed")
	}
	if c.conn != nil {
		return nil
	}

	dialer, err := websocketDialerForURL(c.url)
	if err != nil {
		return err
	}
	conn, _, err := dialer.DialContext(ctx, c.url, nil)
	if err != nil {
		return err
	}
	conn.SetPingHandler(func(appData string) error {
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(5*time.Second))
	})
	c.conn = conn
	if c.callbackDispatcher != nil {
		c.callbackDispatcher.activateConnection(c.recoveryGeneration, conn, c.reconnecting)
	}
	go c.readLoop(conn)
	return nil
}

func (c *WSClient) Subscribe(ctx context.Context, channel string, payload []string, handler func(json.RawMessage)) error {
	if err := c.lockSubscriptionLifecycle(ctx); err != nil {
		return err
	}

	req, err := c.subscribeRequest(channel, "subscribe", payload)
	if err != nil {
		c.lifecycleMu.Unlock()
		return err
	}

	key := wsKey(channel, payload)
	c.mu.Lock()
	conn := c.conn
	previousSub, hadPreviousSub := c.subs[key]
	previousHandler, hadPreviousHandler := c.handlers[key]
	c.subs[key] = wsSubscription{channel: channel, payload: append([]string(nil), payload...), handler: handler}
	c.handlers[key] = handler
	c.mu.Unlock()
	if conn == nil {
		c.mu.Lock()
		c.restoreSubscriptionLocked(key, previousSub, hadPreviousSub, previousHandler, hadPreviousHandler)
		c.mu.Unlock()
		c.lifecycleMu.Unlock()
		return fmt.Errorf("gate ws: not connected")
	}

	if err := c.subscribeOnConn(ctx, conn, req); err != nil {
		c.mu.Lock()
		c.restoreSubscriptionLocked(key, previousSub, hadPreviousSub, previousHandler, hadPreviousHandler)
		recoverExisting := len(c.subs) > 0
		c.mu.Unlock()
		startRecovery := c.detachSubscriptionConnection(conn, err, recoverExisting)
		c.lifecycleMu.Unlock()
		if startRecovery != nil {
			startRecovery()
		}
		return err
	}
	c.lifecycleMu.Unlock()
	return nil
}

func (c *WSClient) Unsubscribe(ctx context.Context, channel string, payload []string) error {
	_ = ctx
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	key := wsKey(channel, payload)
	c.mu.Lock()
	delete(c.subs, key)
	delete(c.handlers, key)
	c.mu.Unlock()

	req, err := c.subscribeRequest(channel, "unsubscribe", payload)
	if err != nil {
		return err
	}
	if err := c.writeJSON(req); err != nil && err.Error() != "gate ws: not connected" {
		return err
	}
	return nil
}

func (c *WSClient) Close() error {
	c.mu.Lock()
	c.closed = true
	c.finishReconnectLocked()
	conn := c.conn
	c.conn = nil
	dispatcher := c.callbackDispatcher
	if c.cancel != nil {
		c.cancel()
	}
	c.mu.Unlock()
	if dispatcher != nil {
		dispatcher.stop()
	}
	if conn == nil {
		return nil
	}
	_ = conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(5*time.Second))
	err := conn.Close()
	return err
}

func (c *WSClient) subscribeRequest(channel, event string, payload []string) (wsRequest, error) {
	req := wsRequest{
		Time:    c.now().Unix(),
		Channel: channel,
		Event:   event,
	}
	if len(payload) > 0 {
		req.Payload = append([]string(nil), payload...)
	}
	if !requiresAuth(channel) {
		return req, nil
	}
	if c.apiKey == "" || c.secretKey == "" {
		return wsRequest{}, fmt.Errorf("gate ws: credentials required for %s", channel)
	}
	req.Auth = &WSAuth{
		Method: "api_key",
		Key:    c.apiKey,
		Sign:   sign(c.secretKey, buildWSAuthPayload(channel, event, req.Time)),
	}
	return req, nil
}

func (c *WSClient) writeJSON(v any) error {
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()
	if conn == nil {
		return fmt.Errorf("gate ws: not connected")
	}
	return c.writeJSONConn(conn, v)
}

func (c *WSClient) writeJSONConn(conn *websocket.Conn, v any) error {
	if conn == nil {
		return fmt.Errorf("gate ws: not connected")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteJSON(v)
}

func (c *WSClient) readLoop(conn *websocket.Conn) {
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			c.handleGateWSConnectionLoss(conn, err)
			return
		}

		var env WSEnvelope
		if err := json.Unmarshal(payload, &env); err != nil {
			continue
		}
		if env.Event == "subscribe" {
			c.resolveSubscriptionWaiter(conn, env)
			continue
		}
		if env.Event == "unsubscribe" {
			continue
		}
		if env.Channel == "" {
			continue
		}

		c.mu.RLock()
		handler := c.handlers[wsKey(env.Channel, nil)]
		if handler == nil {
			for _, sub := range c.subs {
				if sub.channel == env.Channel {
					handler = sub.handler
					break
				}
			}
		}
		dispatcher := c.callbackDispatcher
		c.mu.RUnlock()
		if handler != nil {
			copied := append(json.RawMessage(nil), payload...)
			if dispatcher != nil {
				if !dispatcher.enqueueData(conn, gateWSCallback{run: func() { handler(copied) }}) {
					c.handleGateWSConnectionLoss(conn, fmt.Errorf("gate ws: callback queue overflow for %s", env.Channel))
					return
				}
				continue
			}
			handler(copied)
		}
	}
}

func (c *WSClient) handleGateWSConnectionLoss(conn *websocket.Conn, cause error) {
	c.mu.Lock()
	exact := c.conn == conn
	if exact {
		c.conn = nil
	}
	startReconnect := exact && c.startReconnectLocked()
	reconnecting := c.reconnecting
	generation := c.recoveryGeneration
	reconnectStart := c.reconnectStart
	dispatcher := c.callbackDispatcher
	c.mu.Unlock()
	c.failSubscriptionWaiters(conn, fmt.Errorf("gate ws: subscription connection lost: %w", cause))
	_ = conn.Close()
	if startReconnect {
		c.hookMu.Lock()
		if dispatcher != nil {
			dispatcher.beginGap(generation, func() {
				if reconnectStart != nil {
					reconnectStart(cause)
				}
			})
		} else if reconnectStart != nil {
			reconnectStart(cause)
		}
		c.hookMu.Unlock()
		go c.reconnect()
		return
	}
	if exact && reconnecting && dispatcher != nil {
		dispatcher.discardReplacement(generation, conn)
	}
}

func (c *WSClient) subscribeOnConn(ctx context.Context, conn *websocket.Conn, req wsRequest) error {
	if conn == nil {
		return fmt.Errorf("gate ws: not connected")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	req.ID = c.subscriptionSequence.Add(1)
	waiter := gateSubscriptionWaiter{
		conn:    conn,
		channel: req.Channel,
		event:   req.Event,
		ch:      make(chan error, 1),
	}
	c.subscriptionMu.Lock()
	c.subscriptionWaiters[req.ID] = waiter
	c.subscriptionMu.Unlock()
	defer func() {
		c.subscriptionMu.Lock()
		delete(c.subscriptionWaiters, req.ID)
		c.subscriptionMu.Unlock()
	}()

	if err := c.writeJSONConn(conn, req); err != nil {
		return err
	}
	timeout := c.subscriptionAckTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()

	select {
	case err := <-waiter.ch:
		if err != nil {
			return fmt.Errorf("gate ws: subscribe failed for %s: %w", req.Channel, err)
		}
		return nil
	case <-timer.C:
		return fmt.Errorf("gate ws: subscription timeout for %s", req.Channel)
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return fmt.Errorf("gate ws: client closed")
	}
}

func (c *WSClient) resolveSubscriptionWaiter(conn *websocket.Conn, env WSEnvelope) bool {
	c.subscriptionMu.Lock()
	waiter, ok := c.subscriptionWaiters[env.ID]
	if ok && waiter.conn == conn && waiter.channel == env.Channel && waiter.event == env.Event {
		delete(c.subscriptionWaiters, env.ID)
	} else {
		ok = false
	}
	c.subscriptionMu.Unlock()
	if !ok {
		return false
	}

	err := gateSubscriptionACKError(env)
	select {
	case waiter.ch <- err:
	default:
	}
	return true
}

func gateSubscriptionACKError(env WSEnvelope) error {
	if env.Error != nil {
		return fmt.Errorf("gate ws: subscribe failed: %d %s", env.Error.Code, env.Error.Message)
	}
	trimmed := strings.TrimSpace(string(env.Result))
	if trimmed == "" || trimmed == "null" {
		return fmt.Errorf("gate ws: subscribe failed: acknowledgement missing result status")
	}
	var result struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(env.Result, &result); err != nil {
		return fmt.Errorf("gate ws: malformed subscribe acknowledgement: %w", err)
	}
	if !strings.EqualFold(result.Status, "success") {
		return fmt.Errorf("gate ws: subscribe failed: status %s", result.Status)
	}
	return nil
}

func (c *WSClient) failSubscriptionWaiters(conn *websocket.Conn, err error) {
	if conn == nil {
		return
	}
	c.subscriptionMu.Lock()
	waiters := make([]gateSubscriptionWaiter, 0)
	for id, waiter := range c.subscriptionWaiters {
		if waiter.conn == conn {
			delete(c.subscriptionWaiters, id)
			waiters = append(waiters, waiter)
		}
	}
	c.subscriptionMu.Unlock()
	for _, waiter := range waiters {
		select {
		case waiter.ch <- err:
		default:
		}
	}
}

func (c *WSClient) restoreSubscriptionLocked(
	key string,
	sub wsSubscription,
	hadSub bool,
	handler func(json.RawMessage),
	hadHandler bool,
) {
	if hadSub {
		c.subs[key] = sub
	} else {
		delete(c.subs, key)
	}
	if hadHandler {
		c.handlers[key] = handler
	} else {
		delete(c.handlers, key)
	}
}

func (c *WSClient) detachSubscriptionConnection(conn *websocket.Conn, cause error, recoverExisting bool) func() {
	if conn == nil {
		return nil
	}
	c.mu.Lock()
	exact := c.conn == conn
	if exact {
		c.conn = nil
	}
	startReconnect := exact && recoverExisting && c.startReconnectLocked()
	reconnecting := c.reconnecting
	generation := c.recoveryGeneration
	reconnectStart := c.reconnectStart
	dispatcher := c.callbackDispatcher
	c.mu.Unlock()
	c.failSubscriptionWaiters(conn, fmt.Errorf("gate ws: subscription connection closed: %w", cause))
	_ = conn.Close()
	if !startReconnect {
		if exact && reconnecting && dispatcher != nil {
			dispatcher.discardReplacement(generation, conn)
		}
		return nil
	}
	return func() {
		c.hookMu.Lock()
		if dispatcher != nil {
			dispatcher.beginGap(generation, func() {
				if reconnectStart != nil {
					reconnectStart(cause)
				}
			})
		} else if reconnectStart != nil {
			reconnectStart(cause)
		}
		c.hookMu.Unlock()
		go c.reconnect()
	}
}

func (c *WSClient) reconnect() {
	for {
		select {
		case <-c.ctx.Done():
			c.mu.Lock()
			c.finishReconnectLocked()
			c.mu.Unlock()
			return
		case <-time.After(time.Second):
		}
		if err := c.Connect(c.ctx); err != nil {
			continue
		}
		c.mu.RLock()
		conn := c.conn
		c.mu.RUnlock()
		if conn == nil {
			continue
		}
		if err := c.resubscribeAll(conn); err != nil {
			c.clearConnection(conn)
			continue
		}

		c.hookMu.Lock()
		c.mu.Lock()
		if c.closed || c.conn != conn {
			c.mu.Unlock()
			c.hookMu.Unlock()
			continue
		}
		generation := c.recoveryGeneration
		reconnectDone := c.reconnectDone
		dispatcher := c.callbackDispatcher
		if dispatcher != nil && !dispatcher.enqueueRecovered(generation, conn, reconnectDone) {
			c.mu.Unlock()
			c.hookMu.Unlock()
			c.clearConnection(conn)
			continue
		}
		c.finishReconnectLocked()
		c.mu.Unlock()
		if dispatcher == nil && reconnectDone != nil {
			reconnectDone()
		}
		c.hookMu.Unlock()
		return
	}
}

func (c *WSClient) resubscribeAll(conn *websocket.Conn) error {
	c.lifecycleMu.Lock()
	defer c.lifecycleMu.Unlock()
	if conn == nil {
		return fmt.Errorf("gate ws: not connected")
	}
	c.mu.RLock()
	keys := make([]string, 0, len(c.subs))
	for key := range c.subs {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	subs := make([]wsSubscription, 0, len(keys))
	for _, key := range keys {
		subs = append(subs, c.subs[key])
	}
	c.mu.RUnlock()

	for _, sub := range subs {
		req, err := c.subscribeRequest(sub.channel, "subscribe", sub.payload)
		if err != nil {
			return err
		}
		if err := c.subscribeOnConn(c.ctx, conn, req); err != nil {
			return err
		}
	}
	return nil
}

func (c *WSClient) clearConnection(conn *websocket.Conn) {
	if conn == nil {
		return
	}
	c.mu.Lock()
	cleared := false
	if c.conn == conn {
		c.conn = nil
		cleared = true
	}
	generation := c.recoveryGeneration
	dispatcher := c.callbackDispatcher
	c.mu.Unlock()
	if cleared && dispatcher != nil {
		dispatcher.discardReplacement(generation, conn)
	}
	c.failSubscriptionWaiters(conn, fmt.Errorf("gate ws: subscription connection closed"))
	_ = conn.Close()
}

func DecodeWSEnvelope(payload []byte) (*WSEnvelope, error) {
	var env WSEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

func DecodeSpotOrderMessage(payload []byte) (*SpotOrderMessage, error) {
	env, err := DecodeWSEnvelope(payload)
	if err != nil {
		return nil, err
	}
	var orders []Order
	if len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, &orders); err != nil {
			return nil, err
		}
	}
	return &SpotOrderMessage{WSEnvelope: *env, Orders: orders}, nil
}

func DecodeSpotBalanceMessage(payload []byte) (*SpotBalanceMessage, error) {
	env, err := DecodeWSEnvelope(payload)
	if err != nil {
		return nil, err
	}
	var balances []SpotBalance
	if len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, &balances); err != nil {
			return nil, err
		}
	}
	return &SpotBalanceMessage{WSEnvelope: *env, Balances: balances}, nil
}

func DecodeSpotUserTradeMessage(payload []byte) (*SpotUserTradeMessage, error) {
	env, err := DecodeWSEnvelope(payload)
	if err != nil {
		return nil, err
	}
	var trades []SpotUserTrade
	if len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, &trades); err != nil {
			return nil, err
		}
	}
	return &SpotUserTradeMessage{WSEnvelope: *env, Trades: trades}, nil
}

func DecodeFuturesOrderMessage(payload []byte) (*FuturesOrderMessage, error) {
	env, err := DecodeWSEnvelope(payload)
	if err != nil {
		return nil, err
	}
	var orders []FuturesOrder
	if len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, &orders); err != nil {
			return nil, err
		}
	}
	return &FuturesOrderMessage{WSEnvelope: *env, Orders: orders}, nil
}

func DecodeFuturesUserTradeMessage(payload []byte) (*FuturesUserTradeMessage, error) {
	env, err := DecodeWSEnvelope(payload)
	if err != nil {
		return nil, err
	}
	var trades []MyFuturesTrade
	if len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, &trades); err != nil {
			return nil, err
		}
	}
	return &FuturesUserTradeMessage{WSEnvelope: *env, Trades: trades}, nil
}

func DecodeFuturesBalanceMessage(payload []byte) (*FuturesBalanceMessage, error) {
	env, err := DecodeWSEnvelope(payload)
	if err != nil {
		return nil, err
	}
	var balances []FuturesBalance
	if len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, &balances); err != nil {
			return nil, err
		}
	}
	return &FuturesBalanceMessage{WSEnvelope: *env, Balances: balances}, nil
}

func DecodeFuturesPositionMessage(payload []byte) (*FuturesPositionMessage, error) {
	env, err := DecodeWSEnvelope(payload)
	if err != nil {
		return nil, err
	}
	var positions []Position
	if len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, &positions); err != nil {
			return nil, err
		}
	}
	return &FuturesPositionMessage{WSEnvelope: *env, Positions: positions}, nil
}

func wsURLForProduct(product string) (string, error) {
	switch product {
	case ProductSpot:
		return defaultSpotWSURL, nil
	case ProductFuturesUSDT:
		return defaultFuturesUSDTWSURL, nil
	default:
		return "", fmt.Errorf("gate ws: unsupported product %q", product)
	}
}

func wsKey(channel string, payload []string) string {
	if len(payload) == 0 {
		return channel
	}
	return channel + "|" + strings.Join(payload, ",")
}

func requiresAuth(channel string) bool {
	switch channel {
	case ChannelSpotOrder, ChannelSpotUserTrade, ChannelSpotBalance,
		ChannelFuturesOrder, ChannelFuturesUserTrade, ChannelFuturesBalance, ChannelFuturesPosition:
		return true
	default:
		return false
	}
}
