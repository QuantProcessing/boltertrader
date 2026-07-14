package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
	"sync"
	"sync/atomic"
	"time"

	"github.com/gorilla/websocket"
)

type PrivateWSClient struct {
	url       string
	apiKey    string
	secretKey string

	ctx    context.Context
	cancel context.CancelFunc

	connectMu      sync.Mutex
	subscriptionMu sync.Mutex
	hookMu         sync.Mutex
	mu             sync.RWMutex
	writeMu        sync.Mutex
	conn           *websocket.Conn
	authenticated  bool
	reconnecting   bool
	recoveryWait   chan struct{}
	closed         bool
	handlers       map[string]func(json.RawMessage)
	reconnectStart func(error)
	reconnectDone  func()

	recoveryGeneration uint64
	callbackDispatcher *privateWSCallbackDispatcher

	pendingMu              sync.Mutex
	pendingSubscriptions   map[string]bybitSubscriptionWaiter
	subscriptionSequence   atomic.Uint64
	subscriptionAckTimeout time.Duration
}

type bybitSubscriptionWaiter struct {
	conn *websocket.Conn
	ch   chan error
}

type WSOrderMessage struct {
	Topic string        `json:"topic"`
	Data  []OrderRecord `json:"data"`
}

type WSExecutionMessage struct {
	Topic string            `json:"topic"`
	Data  []ExecutionRecord `json:"data"`
}

type WSPositionMessage struct {
	Topic string           `json:"topic"`
	Data  []PositionRecord `json:"data"`
}

type WSWalletMessage struct {
	Topic string            `json:"topic"`
	Data  []WSWalletAccount `json:"data"`
}

type WSWalletAccount struct {
	AccountType string       `json:"accountType"`
	Coins       []WalletCoin `json:"coin"`
}

func NewPrivateWSClient() *PrivateWSClient {
	ctx, cancel := context.WithCancel(context.Background())
	return &PrivateWSClient{
		url:                    "wss://stream.bybit.com/v5/private",
		ctx:                    ctx,
		cancel:                 cancel,
		handlers:               make(map[string]func(json.RawMessage)),
		pendingSubscriptions:   make(map[string]bybitSubscriptionWaiter),
		subscriptionAckTimeout: 5 * time.Second,
		callbackDispatcher:     newPrivateWSCallbackDispatcher(),
	}
}

func (c *PrivateWSClient) WithCredentials(apiKey, secretKey string) *PrivateWSClient {
	c.apiKey = apiKey
	c.secretKey = secretKey
	return c
}

// SetReconnectHooks reports unexpected authenticated-session loss and the
// point at which a fresh authenticated connection has restored every private
// subscription. Initial connection attempts do not invoke these hooks.
func (c *PrivateWSClient) SetReconnectHooks(started func(error), recovered func()) {
	c.mu.Lock()
	c.reconnectStart = started
	c.reconnectDone = recovered
	c.mu.Unlock()
}

func (c *PrivateWSClient) startReconnectLocked() bool {
	if c.closed || c.reconnecting {
		return false
	}
	c.reconnecting = true
	c.recoveryGeneration++
	c.recoveryWait = make(chan struct{})
	return true
}

func (c *PrivateWSClient) finishReconnectLocked() {
	if !c.reconnecting {
		return
	}
	c.reconnecting = false
	if c.recoveryWait != nil {
		close(c.recoveryWait)
		c.recoveryWait = nil
	}
}

func (c *PrivateWSClient) waitForRecovery(ctx context.Context) error {
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
			return fmt.Errorf("bybit private ws: client closed")
		}
		if !reconnecting {
			return nil
		}
		if wait == nil {
			return fmt.Errorf("bybit private ws: recovery state unavailable")
		}
		select {
		case <-wait:
		case <-ctx.Done():
			return ctx.Err()
		case <-c.ctx.Done():
			return fmt.Errorf("bybit private ws: client closed")
		}
	}
}

// lockSubscriptionLifecycle returns with subscriptionMu held. A caller that
// arrives during recovery waits until the saved subscription set is restored,
// so it cannot use the replacement connection before replay completes.
func (c *PrivateWSClient) lockSubscriptionLifecycle(ctx context.Context) error {
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		if err := c.waitForRecovery(ctx); err != nil {
			return err
		}
		c.subscriptionMu.Lock()
		c.mu.RLock()
		closed := c.closed
		reconnecting := c.reconnecting
		c.mu.RUnlock()
		if closed {
			c.subscriptionMu.Unlock()
			return fmt.Errorf("bybit private ws: client closed")
		}
		if reconnecting {
			c.subscriptionMu.Unlock()
			continue
		}
		if err := c.Connect(ctx); err != nil {
			c.subscriptionMu.Unlock()
			return err
		}
		c.mu.RLock()
		closed = c.closed
		reconnecting = c.reconnecting
		c.mu.RUnlock()
		if closed {
			c.subscriptionMu.Unlock()
			return fmt.Errorf("bybit private ws: client closed")
		}
		if reconnecting {
			c.subscriptionMu.Unlock()
			continue
		}
		return nil
	}
}

type wsAuthRequest struct {
	ReqID string `json:"req_id,omitempty"`
	Op    string `json:"op"`
	Args  []any  `json:"args"`
}

type wsCommandRequest struct {
	ReqID string   `json:"req_id,omitempty"`
	Op    string   `json:"op"`
	Args  []string `json:"args"`
}

func DecodeOrderMessage(payload []byte) (*WSOrderMessage, error) {
	var msg WSOrderMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func DecodeExecutionMessage(payload []byte) (*WSExecutionMessage, error) {
	var msg WSExecutionMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func DecodePositionMessage(payload []byte) (*WSPositionMessage, error) {
	var msg WSPositionMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func DecodeWalletMessage(payload []byte) (*WSWalletMessage, error) {
	var msg WSWalletMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func (c *PrivateWSClient) Subscribe(ctx context.Context, topic string, handler func(json.RawMessage)) error {
	if err := c.lockSubscriptionLifecycle(ctx); err != nil {
		return err
	}

	c.mu.Lock()
	conn := c.conn
	previousHandler, hadPreviousHandler := c.handlers[topic]
	c.handlers[topic] = handler
	c.mu.Unlock()
	if conn == nil {
		c.mu.Lock()
		c.restoreHandlerLocked(topic, previousHandler, hadPreviousHandler)
		c.mu.Unlock()
		c.subscriptionMu.Unlock()
		return fmt.Errorf("bybit private ws: not connected")
	}

	if err := c.subscribeOnConn(ctx, conn, topic); err != nil {
		c.mu.Lock()
		c.restoreHandlerLocked(topic, previousHandler, hadPreviousHandler)
		recoverExisting := len(c.handlers) > 0
		c.mu.Unlock()
		startRecovery := c.detachSubscriptionConnection(conn, err, recoverExisting)
		c.subscriptionMu.Unlock()
		if startRecovery != nil {
			startRecovery()
		}
		return err
	}
	c.subscriptionMu.Unlock()
	return nil
}

func (c *PrivateWSClient) Unsubscribe(ctx context.Context, topic string) error {
	_ = ctx
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()
	c.mu.Lock()
	delete(c.handlers, topic)
	c.mu.Unlock()

	if err := c.writeJSON(wsCommandRequest{Op: "unsubscribe", Args: []string{topic}}); err != nil && err.Error() != "bybit private ws: not connected" {
		return err
	}
	return nil
}

func (c *PrivateWSClient) Close() error {
	c.mu.Lock()
	c.closed = true
	c.finishReconnectLocked()
	conn := c.conn
	c.conn = nil
	c.authenticated = false
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

func (c *PrivateWSClient) Connect(ctx context.Context) error {
	c.connectMu.Lock()
	defer c.connectMu.Unlock()

	c.mu.RLock()
	closed := c.closed
	conn := c.conn
	authenticated := c.authenticated
	c.mu.RUnlock()
	if closed {
		return fmt.Errorf("bybit private ws: client closed")
	}
	if conn != nil && authenticated {
		return nil
	}
	if conn != nil {
		c.clearConnection(conn)
	}

	conn, _, err := websocketDialerFromEnvironment().DialContext(ctx, c.url, nil)
	if err != nil {
		return err
	}

	authCh := make(chan error, 1)
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		_ = conn.Close()
		return fmt.Errorf("bybit private ws: client closed")
	}
	c.conn = conn
	c.authenticated = false
	generation := c.recoveryGeneration
	reconnecting := c.reconnecting
	dispatcher := c.callbackDispatcher
	c.mu.Unlock()
	if dispatcher != nil {
		dispatcher.activateConnection(generation, conn, reconnecting)
	}
	go c.readLoop(conn, authCh)
	go c.pingLoop(conn)

	if err := c.sendAuth(conn); err != nil {
		c.clearConnection(conn)
		return err
	}

	var authErr error
	select {
	case authErr = <-authCh:
	case <-time.After(5 * time.Second):
		authErr = fmt.Errorf("bybit private ws: auth timeout")
	case <-ctx.Done():
		authErr = ctx.Err()
	}
	if authErr != nil {
		c.clearConnection(conn)
		return authErr
	}
	c.mu.RLock()
	ready := c.conn == conn && c.authenticated && !c.closed
	c.mu.RUnlock()
	if !ready {
		c.clearConnection(conn)
		return fmt.Errorf("bybit private ws: authentication connection lost")
	}
	return nil
}

func (c *PrivateWSClient) sendAuth(conn *websocket.Conn) error {
	expires := time.Now().Add(10 * time.Second).UnixMilli()
	signature := sign(c.secretKey, fmt.Sprintf("GET/realtime%d", expires))
	return c.writeJSONConn(conn, wsAuthRequest{
		Op:   "auth",
		Args: []any{c.apiKey, expires, signature},
	})
}

func (c *PrivateWSClient) writeJSON(v any) error {
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()
	if conn == nil {
		return fmt.Errorf("bybit private ws: not connected")
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteJSON(v)
}

func (c *PrivateWSClient) writeJSONConn(conn *websocket.Conn, v any) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteJSON(v)
}

func (c *PrivateWSClient) pingLoop(conn *websocket.Conn) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
		}
		c.mu.RLock()
		active := c.conn == conn
		c.mu.RUnlock()
		if !active {
			return
		}

		c.writeMu.Lock()
		err := conn.WriteJSON(map[string]string{"op": "ping"})
		c.writeMu.Unlock()
		if err != nil {
			return
		}
	}
}

func (c *PrivateWSClient) readLoop(conn *websocket.Conn, authCh chan<- error) {
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			c.handlePrivateWSConnectionLoss(conn, authCh, err)
			return
		}

		var authResp struct {
			Success bool   `json:"success"`
			RetMsg  string `json:"ret_msg"`
			Op      string `json:"op"`
			Topic   string `json:"topic"`
			ReqID   string `json:"req_id"`
		}
		if err := json.Unmarshal(payload, &authResp); err != nil {
			continue
		}

		if authResp.Op == "auth" {
			var authErr error
			if authResp.Success {
				c.mu.Lock()
				if c.conn == conn && !c.closed {
					c.authenticated = true
				} else {
					authErr = fmt.Errorf("bybit private ws: authentication connection lost")
				}
				c.mu.Unlock()
			} else {
				authErr = fmt.Errorf("bybit private ws: auth failed: %s", authResp.RetMsg)
			}
			select {
			case authCh <- authErr:
			default:
			}
			continue
		}
		if authResp.Op == "subscribe" && authResp.ReqID != "" {
			var subscribeErr error
			if !authResp.Success {
				subscribeErr = fmt.Errorf("bybit private ws: subscribe failed: %s", authResp.RetMsg)
			}
			c.resolveSubscriptionWaiter(conn, authResp.ReqID, subscribeErr)
			continue
		}

		if authResp.Topic == "" {
			continue
		}

		c.mu.RLock()
		handler := c.handlers[authResp.Topic]
		dispatcher := c.callbackDispatcher
		c.mu.RUnlock()
		if handler != nil {
			copied := append(json.RawMessage(nil), payload...)
			if dispatcher != nil {
				if !dispatcher.enqueueData(conn, privateWSCallback{run: func() { handler(copied) }}) {
					c.handlePrivateWSConnectionLoss(conn, authCh, fmt.Errorf("bybit private ws: callback queue overflow for %s", authResp.Topic))
					return
				}
				continue
			}
			handler(copied)
		}
	}
}

func (c *PrivateWSClient) handlePrivateWSConnectionLoss(conn *websocket.Conn, authCh chan<- error, cause error) {
	c.mu.Lock()
	exact := c.conn == conn
	wasAuthenticated := exact && c.authenticated
	if exact {
		c.conn = nil
		c.authenticated = false
	}
	startReconnect := exact && wasAuthenticated && c.startReconnectLocked()
	reconnecting := c.reconnecting
	generation := c.recoveryGeneration
	reconnectStart := c.reconnectStart
	dispatcher := c.callbackDispatcher
	c.mu.Unlock()
	c.failSubscriptionWaiters(conn, fmt.Errorf("bybit private ws: subscription connection lost: %w", cause))
	_ = conn.Close()
	select {
	case authCh <- cause:
	default:
	}
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

func (c *PrivateWSClient) clearConnection(conn *websocket.Conn) {
	if conn == nil {
		return
	}
	c.mu.Lock()
	cleared := false
	if c.conn == conn {
		c.conn = nil
		c.authenticated = false
		cleared = true
	}
	generation := c.recoveryGeneration
	dispatcher := c.callbackDispatcher
	c.mu.Unlock()
	if cleared && dispatcher != nil {
		dispatcher.discardReplacement(generation, conn)
	}
	c.failSubscriptionWaiters(conn, fmt.Errorf("bybit private ws: subscription connection closed"))
	_ = conn.Close()
}

func (c *PrivateWSClient) restoreHandlerLocked(topic string, handler func(json.RawMessage), existed bool) {
	if existed {
		c.handlers[topic] = handler
		return
	}
	delete(c.handlers, topic)
}

func (c *PrivateWSClient) detachSubscriptionConnection(conn *websocket.Conn, cause error, recoverExisting bool) func() {
	if conn == nil {
		return nil
	}
	c.mu.Lock()
	exact := c.conn == conn
	if exact {
		c.conn = nil
		c.authenticated = false
	}
	startReconnect := exact && recoverExisting && c.startReconnectLocked()
	reconnecting := c.reconnecting
	generation := c.recoveryGeneration
	reconnectStart := c.reconnectStart
	dispatcher := c.callbackDispatcher
	c.mu.Unlock()
	c.failSubscriptionWaiters(conn, fmt.Errorf("bybit private ws: subscription connection closed: %w", cause))
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

func (c *PrivateWSClient) subscribeOnConn(ctx context.Context, conn *websocket.Conn, topic string) error {
	if conn == nil {
		return fmt.Errorf("bybit private ws: not connected")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	reqID := fmt.Sprintf("subscribe-%d", c.subscriptionSequence.Add(1))
	waiter := bybitSubscriptionWaiter{conn: conn, ch: make(chan error, 1)}
	c.pendingMu.Lock()
	c.pendingSubscriptions[reqID] = waiter
	c.pendingMu.Unlock()
	defer func() {
		c.pendingMu.Lock()
		delete(c.pendingSubscriptions, reqID)
		c.pendingMu.Unlock()
	}()

	if err := c.writeJSONConn(conn, wsCommandRequest{ReqID: reqID, Op: "subscribe", Args: []string{topic}}); err != nil {
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
			return fmt.Errorf("bybit private ws: subscribe failed for %s: %w", topic, err)
		}
		return nil
	case <-timer.C:
		return fmt.Errorf("bybit private ws: subscription timeout for %s", topic)
	case <-ctx.Done():
		return ctx.Err()
	case <-c.ctx.Done():
		return fmt.Errorf("bybit private ws: client closed")
	}
}

func (c *PrivateWSClient) resolveSubscriptionWaiter(conn *websocket.Conn, reqID string, err error) bool {
	c.pendingMu.Lock()
	waiter, ok := c.pendingSubscriptions[reqID]
	if ok && waiter.conn == conn {
		delete(c.pendingSubscriptions, reqID)
	} else {
		ok = false
	}
	c.pendingMu.Unlock()
	if !ok {
		return false
	}
	select {
	case waiter.ch <- err:
	default:
	}
	return true
}

func (c *PrivateWSClient) failSubscriptionWaiters(conn *websocket.Conn, err error) {
	if conn == nil {
		return
	}
	c.pendingMu.Lock()
	waiters := make([]bybitSubscriptionWaiter, 0)
	for reqID, waiter := range c.pendingSubscriptions {
		if waiter.conn == conn {
			delete(c.pendingSubscriptions, reqID)
			waiters = append(waiters, waiter)
		}
	}
	c.pendingMu.Unlock()
	for _, waiter := range waiters {
		select {
		case waiter.ch <- err:
		default:
		}
	}
}

func (c *PrivateWSClient) reconnect() {
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
		authenticated := c.authenticated
		c.mu.RUnlock()
		if conn == nil || !authenticated {
			continue
		}
		if err := c.resubscribeAll(conn); err != nil {
			c.clearConnection(conn)
			continue
		}

		c.hookMu.Lock()
		c.mu.Lock()
		if c.closed || c.conn != conn || !c.authenticated {
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

func (c *PrivateWSClient) resubscribeAll(conn *websocket.Conn) error {
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()
	if conn == nil {
		return fmt.Errorf("bybit private ws: not connected")
	}
	c.mu.RLock()
	topics := make([]string, 0, len(c.handlers))
	for topic := range c.handlers {
		topics = append(topics, topic)
	}
	c.mu.RUnlock()
	sort.Strings(topics)

	for _, topic := range topics {
		if err := c.subscribeOnConn(c.ctx, conn, topic); err != nil {
			return err
		}
	}
	return nil
}
