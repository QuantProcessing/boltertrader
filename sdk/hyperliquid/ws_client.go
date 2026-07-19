package hyperliquid

import (
	"context"
	"crypto/ecdsa"
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/gorilla/websocket"
)

type WebsocketClient struct {
	URL     string
	Env     Environment
	Conn    *websocket.Conn
	Mu      sync.RWMutex
	WriteMu sync.Mutex
	// subscriptionTxMu serializes the complete subscription transaction,
	// including handler mutation, websocket write/acknowledgement, commit or
	// rollback, and connection install/replay/readiness. Subscription changes
	// are infrequent, and this boundary keeps the in-memory handler/payload state
	// aligned with the wire order without exposing a not-yet-replayed socket.
	subscriptionTxMu sync.Mutex
	// subscriptions maps channel -> stable subscription payload key -> handler.
	subscriptions map[string]map[string]func(WsMessage)
	// subscriptionPayloads maps channel -> stable subscription payload key -> original payload.
	subscriptionPayloads map[string]map[string]any
	// subscriptionRevisions prevents an older failed operation from rolling
	// back a newer subscribe or unsubscribe for the same payload key.
	subscriptionRevisions  map[string]map[string]uint64
	nextSubscriptionRev    uint64
	ReconnectWait          time.Duration
	SubscriptionAckTimeout time.Duration
	Debug                  bool
	Logger                 *zap.SugaredLogger

	PrivateKey  *ecdsa.PrivateKey
	Vault       string
	AccountAddr string

	LastNonce    atomic.Int64
	NextPostID   atomic.Int64
	PostChannels map[int64]chan PostResult
	// postConnections binds each in-flight post to the exact socket that wrote
	// it. A stale socket failure must not complete requests sent by its
	// replacement.
	postConnections map[int64]*websocket.Conn
	postDone        map[int64]chan struct{}

	PingInterval time.Duration

	ctx    context.Context
	cancel context.CancelFunc

	connectMu            sync.Mutex
	pingOnce             sync.Once
	recovering           bool
	wantConnected        bool
	connectionEpoch      uint64
	activeEpoch          uint64
	recoveryGeneration   uint64
	subscriptionAcks     map[*websocket.Conn]map[string]chan error
	onReconnectStarted   func(error)
	onReconnectRecovered func()
	callbackDispatcher   *websocketCallbackDispatcher
}

func NewWebsocketClient(ctx context.Context) *WebsocketClient {
	env := EnvironmentMainnet
	wsURL := wsURLForEnvironment(env)

	// Create cancellable context from parent
	ctx, cancel := context.WithCancel(ctx)

	c := &WebsocketClient{
		URL:                    wsURL,
		Env:                    env,
		subscriptions:          make(map[string]map[string]func(WsMessage)),
		subscriptionPayloads:   make(map[string]map[string]any),
		subscriptionRevisions:  make(map[string]map[string]uint64),
		ReconnectWait:          1 * time.Second,
		SubscriptionAckTimeout: 5 * time.Second,
		Logger:                 zap.NewNop().Sugar().Named("hyperliquid"),
		PostChannels:           make(map[int64]chan PostResult),
		postConnections:        make(map[int64]*websocket.Conn),
		postDone:               make(map[int64]chan struct{}),
		subscriptionAcks:       make(map[*websocket.Conn]map[string]chan error),
		callbackDispatcher:     newWebsocketCallbackDispatcher(),
		PingInterval:           50 * time.Second,
		ctx:                    ctx,
		cancel:                 cancel, // Added cancel func
	}

	return c
}

func (c *WebsocketClient) WithEnvironment(env Environment) *WebsocketClient {
	c.Env = normalizeEnvironment(env)
	c.URL = wsURLForEnvironment(c.Env)
	return c
}

func (c *WebsocketClient) IsMainnet() bool {
	return normalizeEnvironment(c.Env) == EnvironmentMainnet
}

func (c *WebsocketClient) SignL1Action(action any, nonce int64) (SignatureResult, error) {
	return SignL1Action(c.PrivateKey, action, c.Vault, nonce, nil, c.IsMainnet())
}

func (c *WebsocketClient) WithCredentials(privateKey string, vault *string) *WebsocketClient {
	if privateKey != "" {
		pk, err := parsePrivateKey(privateKey)
		if err == nil {
			c.PrivateKey = pk
			if c.AccountAddr == "" {
				c.AccountAddr = crypto.PubkeyToAddress(c.PrivateKey.PublicKey).Hex()
			}
		} else if c.Logger != nil {
			c.Logger.Errorw("Invalid private key", "error", err)
		}
	}
	if vault != nil {
		c.Vault = *vault
	}
	return c
}

func (c *WebsocketClient) WithURL(u string) *WebsocketClient {
	c.URL = u
	return c
}

// SetReconnectHooks registers private-stream lifecycle callbacks. Recovered
// runs only after the current connection acknowledges every replayed
// subscription.
func (c *WebsocketClient) SetReconnectHooks(started func(error), recovered func()) {
	c.Mu.Lock()
	c.onReconnectStarted = started
	c.onReconnectRecovered = recovered
	c.Mu.Unlock()
}

func (c *WebsocketClient) Connect() error {
	c.subscriptionTxMu.Lock()
	defer c.subscriptionTxMu.Unlock()

	c.Mu.Lock()
	if c.wantConnected {
		if c.Conn != nil {
			c.Mu.Unlock()
			return nil
		}
		if c.recovering {
			c.Mu.Unlock()
			return fmt.Errorf("hyperliquid websocket: recovery in progress")
		}
	}
	c.connectionEpoch++
	epoch := c.connectionEpoch
	c.wantConnected = true
	c.Mu.Unlock()
	conn, err := c.connectSocket(epoch)
	if err != nil {
		return err
	}
	// Disconnect intentionally retains subscription handlers/payloads so a
	// failed adapter startup can retry. A fresh manual Connect must replay that
	// retained set just as automatic reconnect does; otherwise the socket looks
	// healthy while pre-existing public market streams remain silent.
	if err := c.resubscribeAllOnLocked(conn); err != nil {
		c.Disconnect()
		return fmt.Errorf("hyperliquid websocket: replay retained subscriptions: %w", err)
	}
	return nil
}

func (c *WebsocketClient) connectSocket(epoch uint64) (*websocket.Conn, error) {
	c.connectMu.Lock()
	defer c.connectMu.Unlock()

	c.Mu.RLock()
	if !c.wantConnected || c.connectionEpoch != epoch {
		c.Mu.RUnlock()
		return nil, fmt.Errorf("websocket connection request superseded")
	}
	if c.Conn != nil {
		conn := c.Conn
		c.Mu.RUnlock()
		return conn, nil
	}
	c.Mu.RUnlock()

	// Use internal 10 second timeout for dialing
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	dialer, err := websocketDialerForURL(c.URL)
	if err != nil {
		return nil, err
	}
	conn, _, err := dialer.DialContext(ctx, c.URL, nil)
	if err != nil {
		return nil, err
	}

	// Set initial read deadline
	if err := conn.SetReadDeadline(time.Now().Add(60 * time.Second)); err != nil {
		conn.Close()
		return nil, err
	}

	c.Mu.Lock()
	if !c.wantConnected || c.connectionEpoch != epoch {
		c.Mu.Unlock()
		_ = conn.Close()
		return nil, fmt.Errorf("websocket connection request superseded")
	}
	if c.Conn != nil {
		current := c.Conn
		c.Mu.Unlock()
		_ = conn.Close()
		return current, nil
	}
	c.Conn = conn
	c.activeEpoch = epoch
	recovering := c.recovering
	recoveryGeneration := c.recoveryGeneration
	dispatcher := c.callbackDispatcher
	if dispatcher != nil {
		dispatcher.activateConnection(recoveryGeneration, conn, recovering)
	}
	c.Mu.Unlock()

	go c.readLoop(conn)
	c.pingOnce.Do(func() { go c.pingLoop() })

	return conn, nil
}

func (c *WebsocketClient) readLoop(conn *websocket.Conn) {
	var readErr error
	defer func() { c.handleDisconnect(conn, readErr) }()

	for {
		select {
		case <-c.ctx.Done():
			c.Logger.Debug("Read loop stopping due to context cancellation")
			return
		default:
			_, message, err := conn.ReadMessage()
			if err != nil {
				// Check if context was canceled (normal shutdown) during read
				if c.ctx.Err() != nil {
					c.Logger.Debug("Read loop stopping due to context cancellation")
					return
				}

				readErr = err
				c.Logger.Debugw("websocket read error", "error", err)
				return
			}
			// Extend read deadline on any message received
			_ = conn.SetReadDeadline(time.Now().Add(60 * time.Second))
			c.handleMessage(conn, message)
		}
	}
}

func (c *WebsocketClient) handleDisconnect(conn *websocket.Conn, err error) {
	if c.ctx.Err() != nil {
		return
	}
	if err == nil {
		err = fmt.Errorf("hyperliquid websocket: connection lost")
	}
	c.failConnection(conn, err)
}

func (c *WebsocketClient) pingLoop() {
	ticker := time.NewTicker(c.PingInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			// Hyperliquid requires JSON ping: {"method": "ping"}.
			if err := c.SendCommand(map[string]string{"method": "ping"}); err != nil {
				c.Logger.Debugw("websocket ping skipped", "error", err)
			}
		}
	}
}

func (c *WebsocketClient) handleMessage(conn *websocket.Conn, message []byte) {
	c.Logger.Debugw("received message", "msg", string(message))
	var msg WsMessage
	if err := json.Unmarshal(message, &msg); err != nil {
		c.Logger.Errorw("error unmarshaling message", "error", err)
		return
	}

	if msg.Channel == "subscriptionResponse" {
		c.handleSubscriptionResponse(conn, msg.Data)
		return
	}

	if msg.Channel == "error" {
		c.failSubscriptionAcks(conn, fmt.Errorf("subscription rejected: %s", string(msg.Data)))
		return
	}

	if msg.Channel == "pong" {
		return
	}
	if msg.Channel == "post" {
		// Post responses complete an in-flight write request. They are control
		// traffic, not a user callback, and must not wait behind user backpressure.
		c.handlePostResponse(conn, msg)
	}

	c.Mu.RLock()
	if c.Conn != conn {
		c.Mu.RUnlock()
		return
	}
	channelHandlers, ok := c.subscriptions[msg.Channel]
	keys := make([]string, 0, len(channelHandlers))
	for key := range channelHandlers {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	callbacks := make([]websocketCallback, 0, len(keys))
	messageCopy := WsMessage{
		Channel: msg.Channel,
		Data:    append(json.RawMessage(nil), msg.Data...),
	}
	for _, key := range keys {
		handler := channelHandlers[key]
		if handler == nil {
			continue
		}
		callbacks = append(callbacks, websocketCallback{
			kind: websocketCallbackData,
			conn: conn,
			run: func() {
				handler(messageCopy)
			},
		})
	}
	dispatcher := c.callbackDispatcher
	c.Mu.RUnlock()

	if !ok {
		c.Logger.Debugw("no handler for channel", "channel", msg.Channel)
		return
	}
	if dispatcher != nil && !dispatcher.enqueueData(conn, callbacks) {
		c.failConnection(conn, fmt.Errorf("hyperliquid websocket: callback queue overflow for %s", msg.Channel))
	}
}

type subscriptionMutation struct {
	channel             string
	key                 string
	revision            uint64
	previousHandler     func(WsMessage)
	hadPreviousHandler  bool
	previousRevision    uint64
	hadPreviousRevision bool
}

// Subscribe registers a handler for a specific channel name and optionally sends a subscription request.
func (c *WebsocketClient) Subscribe(channel string, subscription any, handler func(WsMessage)) error {
	c.subscriptionTxMu.Lock()
	defer c.subscriptionTxMu.Unlock()
	return c.subscribeLocked(channel, subscription, handler)
}

func (c *WebsocketClient) subscribeLocked(channel string, subscription any, handler func(WsMessage)) error {
	mutation, conn, err := c.beginSubscription(channel, subscription, handler)
	if err != nil {
		return err
	}
	if subscription == nil {
		return nil
	}
	req := WsSubscribeRequest{Method: "subscribe", Subscription: subscription}
	if err := c.sendCommandOn(conn, req); err != nil {
		c.rollbackSubscription(mutation)
		return err
	}
	c.commitSubscriptionPayload(mutation, subscription)
	return nil
}

// SubscribeConfirmed registers a handler and waits for the current connection
// to acknowledge the exact subscription payload. It is intended for private
// stream startup, where returning before server confirmation would let the
// runtime treat an unestablished execution stream as healthy.
func (c *WebsocketClient) SubscribeConfirmed(channel string, subscription any, handler func(WsMessage)) error {
	c.subscriptionTxMu.Lock()
	defer c.subscriptionTxMu.Unlock()
	if subscription == nil {
		return c.subscribeLocked(channel, nil, handler)
	}
	mutation, conn, err := c.beginSubscription(channel, subscription, handler)
	if err != nil {
		return err
	}
	waiter, err := c.registerSubscriptionAck(conn, mutation.key)
	if err != nil {
		c.rollbackSubscription(mutation)
		return err
	}
	req := WsSubscribeRequest{Method: "subscribe", Subscription: subscription}
	if err := c.sendCommandOn(conn, req); err != nil {
		c.removeSubscriptionAck(conn, mutation.key, waiter)
		c.rollbackSubscription(mutation)
		return err
	}
	if err := c.waitSubscriptionAck(conn, mutation.key, waiter); err != nil {
		c.rollbackSubscription(mutation)
		return fmt.Errorf("subscribe %s: %w", channel, err)
	}
	c.commitSubscriptionPayload(mutation, subscription)
	return nil
}

// Unsubscribe removes a handler and optionally sends unsubscribe command
func (c *WebsocketClient) Unsubscribe(channel string, subscription any) error {
	c.subscriptionTxMu.Lock()
	defer c.subscriptionTxMu.Unlock()
	key := subscriptionKey(subscription)
	c.Mu.Lock()
	mutation := c.newSubscriptionMutationLocked(channel, key)
	if handlers := c.subscriptions[channel]; handlers != nil {
		delete(handlers, key)
		if len(handlers) == 0 {
			delete(c.subscriptions, channel)
		}
	}
	conn := c.Conn
	c.Mu.Unlock()

	if subscription != nil {
		req := WsSubscribeRequest{
			Method:       "unsubscribe",
			Subscription: subscription,
		}
		if err := c.sendCommandOn(conn, req); err != nil {
			// If the exact socket has already gone away, there is no remote
			// subscription left to preserve. Keep the transport error for API
			// compatibility, but remove the local desired state so a later
			// Connect cannot replay a subscription the caller tried to remove.
			// A failure on the still-current socket remains ambiguous and must
			// roll back transactionally.
			if c.isCurrentConnection(conn) {
				c.rollbackSubscription(mutation)
			} else {
				c.commitUnsubscription(mutation)
			}
			return err
		}
	}
	c.commitUnsubscription(mutation)
	return nil
}

func (c *WebsocketClient) beginSubscription(channel string, subscription any, handler func(WsMessage)) (*subscriptionMutation, *websocket.Conn, error) {
	key := subscriptionKey(subscription)
	c.Mu.Lock()
	defer c.Mu.Unlock()
	if isOpaqueAccountChannel(channel) {
		if handlers := c.subscriptions[channel]; len(handlers) != 0 {
			if _, sameIdentity := handlers[key]; !sameIdentity {
				return nil, nil, fmt.Errorf("hyperliquid websocket: channel %s cannot multiplex account identities", channel)
			}
		}
	}
	mutation := c.newSubscriptionMutationLocked(channel, key)
	if c.subscriptions[channel] == nil {
		c.subscriptions[channel] = make(map[string]func(WsMessage))
	}
	c.subscriptions[channel][key] = handler
	return mutation, c.Conn, nil
}

func (c *WebsocketClient) newSubscriptionMutationLocked(channel, key string) *subscriptionMutation {
	mutation := &subscriptionMutation{channel: channel, key: key}
	if handlers := c.subscriptions[channel]; handlers != nil {
		mutation.previousHandler, mutation.hadPreviousHandler = handlers[key]
	}
	if revisions := c.subscriptionRevisions[channel]; revisions != nil {
		mutation.previousRevision, mutation.hadPreviousRevision = revisions[key]
	}
	c.nextSubscriptionRev++
	mutation.revision = c.nextSubscriptionRev
	if c.subscriptionRevisions[channel] == nil {
		c.subscriptionRevisions[channel] = make(map[string]uint64)
	}
	c.subscriptionRevisions[channel][key] = mutation.revision
	return mutation
}

func (c *WebsocketClient) rollbackSubscription(mutation *subscriptionMutation) {
	if mutation == nil {
		return
	}
	c.Mu.Lock()
	defer c.Mu.Unlock()
	if c.subscriptionRevisions[mutation.channel][mutation.key] != mutation.revision {
		return
	}
	if mutation.hadPreviousHandler {
		if c.subscriptions[mutation.channel] == nil {
			c.subscriptions[mutation.channel] = make(map[string]func(WsMessage))
		}
		c.subscriptions[mutation.channel][mutation.key] = mutation.previousHandler
	} else if handlers := c.subscriptions[mutation.channel]; handlers != nil {
		delete(handlers, mutation.key)
		if len(handlers) == 0 {
			delete(c.subscriptions, mutation.channel)
		}
	}
	if mutation.hadPreviousRevision {
		c.subscriptionRevisions[mutation.channel][mutation.key] = mutation.previousRevision
	} else if revisions := c.subscriptionRevisions[mutation.channel]; revisions != nil {
		delete(revisions, mutation.key)
		if len(revisions) == 0 {
			delete(c.subscriptionRevisions, mutation.channel)
		}
	}
}

func (c *WebsocketClient) commitSubscriptionPayload(mutation *subscriptionMutation, subscription any) {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	if mutation == nil || c.subscriptionRevisions[mutation.channel][mutation.key] != mutation.revision {
		return
	}
	if c.subscriptionPayloads[mutation.channel] == nil {
		c.subscriptionPayloads[mutation.channel] = make(map[string]any)
	}
	c.subscriptionPayloads[mutation.channel][mutation.key] = subscription
}

func (c *WebsocketClient) commitUnsubscription(mutation *subscriptionMutation) {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	if mutation == nil || c.subscriptionRevisions[mutation.channel][mutation.key] != mutation.revision {
		return
	}
	if payloads := c.subscriptionPayloads[mutation.channel]; payloads != nil {
		delete(payloads, mutation.key)
		if len(payloads) == 0 {
			delete(c.subscriptionPayloads, mutation.channel)
		}
	}
	if revisions := c.subscriptionRevisions[mutation.channel]; revisions != nil {
		delete(revisions, mutation.key)
		if len(revisions) == 0 {
			delete(c.subscriptionRevisions, mutation.channel)
		}
	}
}

func isOpaqueAccountChannel(channel string) bool {
	return channel == "orderUpdates" || channel == "user"
}

// SendCommand sends a raw JSON command
func (c *WebsocketClient) SendCommand(cmd any) error {
	return c.sendCommandOn(c.currentConnection(), cmd)
}

func (c *WebsocketClient) sendCommandOn(conn *websocket.Conn, cmd any) error {
	return c.sendCommandOnContext(context.Background(), conn, cmd)
}

func (c *WebsocketClient) sendCommandOnContext(ctx context.Context, conn *websocket.Conn, cmd any) error {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.ensureCurrentConnection(conn); err != nil {
		return err
	}
	c.WriteMu.Lock()
	defer c.WriteMu.Unlock()

	if err := ctx.Err(); err != nil {
		return err
	}
	if err := c.ensureCurrentConnection(conn); err != nil {
		return err
	}

	c.Logger.Debugw("sending command", "command_type", wsCommandDebugSummary(cmd))
	// The logger and connection validation above may block. Check once more at
	// the last safe point before starting the websocket write; after WriteJSON
	// begins the request may already be visible to the venue and is not
	// retractable.
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := conn.WriteJSON(cmd); err != nil {
		return err
	}
	return c.ensureCurrentConnection(conn)
}

func wsCommandDebugSummary(cmd any) string {
	return fmt.Sprintf("%T", cmd)
}

func (c *WebsocketClient) Close() {
	// Cancel context to stop loops
	c.cancel()
	c.Disconnect()
	if c.callbackDispatcher != nil {
		c.callbackDispatcher.stop()
	}
}

// Disconnect closes only the current socket and suppresses reconnect for that
// socket. Unlike Close it keeps the client context alive, allowing an adapter
// startup transaction that failed subscription acknowledgement to roll back
// completely and retry on a fresh connection.
func (c *WebsocketClient) Disconnect() {
	c.Mu.Lock()
	conn := c.Conn
	c.Conn = nil
	c.recovering = false
	c.wantConnected = false
	c.activeEpoch = 0
	c.connectionEpoch++
	c.recoveryGeneration++
	dispatcher := c.callbackDispatcher
	if dispatcher != nil {
		dispatcher.reset()
	}
	c.Mu.Unlock()
	disconnectErr := fmt.Errorf("websocket disconnected")
	c.failAllSubscriptionAcks(disconnectErr)
	c.failAllPostRequests(disconnectErr)
	if conn != nil {
		_ = conn.Close()
	}
}

func (c *WebsocketClient) reconnect(epoch uint64) {
	for {
		if !c.connectionWanted(epoch) {
			return
		}
		timer := time.NewTimer(c.ReconnectWait)
		select {
		case <-c.ctx.Done():
			timer.Stop()
			return
		case <-timer.C:
		}
		if !c.connectionWanted(epoch) {
			return
		}
		c.Logger.Infow("reconnecting...")
		c.subscriptionTxMu.Lock()
		conn, err := c.connectSocket(epoch)
		if err != nil {
			c.subscriptionTxMu.Unlock()
			c.Logger.Errorw("reconnect failed", "error", err)
			continue
		}
		if err := c.resubscribeAllOnLocked(conn); err != nil {
			c.subscriptionTxMu.Unlock()
			c.Logger.Errorw("subscription replay failed", "error", err)
			c.dropConnection(conn)
			continue
		}
		recovered := c.completeReconnect(conn)
		c.subscriptionTxMu.Unlock()
		if recovered {
			return
		}
	}
}

func (c *WebsocketClient) connectionWanted(epoch uint64) bool {
	c.Mu.RLock()
	defer c.Mu.RUnlock()
	return c.wantConnected && c.connectionEpoch == epoch
}

func (c *WebsocketClient) resubscribeAll() error {
	return c.resubscribeAllOn(c.currentConnection())
}

func (c *WebsocketClient) resubscribeAllOn(conn *websocket.Conn) error {
	c.subscriptionTxMu.Lock()
	defer c.subscriptionTxMu.Unlock()
	return c.resubscribeAllOnLocked(conn)
}

func (c *WebsocketClient) resubscribeAllOnLocked(conn *websocket.Conn) error {
	if err := c.ensureCurrentConnection(conn); err != nil {
		return err
	}
	c.Mu.RLock()
	type replay struct {
		channel      string
		key          string
		subscription any
	}
	var payloads []replay
	for channel, byKey := range c.subscriptionPayloads {
		for key, subscription := range byKey {
			payloads = append(payloads, replay{channel: channel, key: key, subscription: subscription})
		}
	}
	c.Mu.RUnlock()

	for _, payload := range payloads {
		c.Logger.Infow("resubscribing", "channel", payload.channel)
		ack, err := c.registerSubscriptionAck(conn, payload.key)
		if err != nil {
			return fmt.Errorf("resubscribe %s: %w", payload.channel, err)
		}
		req := WsSubscribeRequest{
			Method:       "subscribe",
			Subscription: payload.subscription,
		}
		if err := c.sendCommandOn(conn, req); err != nil {
			c.removeSubscriptionAck(conn, payload.key, ack)
			return fmt.Errorf("resubscribe %s: %w", payload.channel, err)
		}
		if err := c.waitSubscriptionAck(conn, payload.key, ack); err != nil {
			return fmt.Errorf("resubscribe %s acknowledgement: %w", payload.channel, err)
		}
	}
	return c.ensureCurrentConnection(conn)
}

func (c *WebsocketClient) currentConnection() *websocket.Conn {
	c.Mu.RLock()
	defer c.Mu.RUnlock()
	return c.Conn
}

func (c *WebsocketClient) isCurrentConnection(conn *websocket.Conn) bool {
	if conn == nil {
		return false
	}
	c.Mu.RLock()
	defer c.Mu.RUnlock()
	return c.Conn == conn
}

func (c *WebsocketClient) ensureCurrentConnection(conn *websocket.Conn) error {
	if conn == nil {
		return fmt.Errorf("websocket not connected")
	}
	c.Mu.RLock()
	current := c.Conn
	c.Mu.RUnlock()
	if current != conn {
		return fmt.Errorf("websocket connection changed during recovery")
	}
	return nil
}

func (c *WebsocketClient) dropConnection(conn *websocket.Conn) {
	if conn == nil {
		return
	}
	c.failConnection(conn, fmt.Errorf("websocket connection dropped during recovery"))
}

func (c *WebsocketClient) registerSubscriptionAck(conn *websocket.Conn, key string) (chan error, error) {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	if conn == nil || c.Conn != conn {
		return nil, fmt.Errorf("websocket connection changed during recovery")
	}
	byKey := c.subscriptionAcks[conn]
	if byKey == nil {
		byKey = make(map[string]chan error)
		c.subscriptionAcks[conn] = byKey
	}
	if _, exists := byKey[key]; exists {
		return nil, fmt.Errorf("subscription acknowledgement already pending for %s", key)
	}
	waiter := make(chan error, 1)
	byKey[key] = waiter
	return waiter, nil
}

func (c *WebsocketClient) waitSubscriptionAck(conn *websocket.Conn, key string, waiter chan error) error {
	timeout := c.SubscriptionAckTimeout
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case err := <-waiter:
		// The waiter is registered and resolved against this exact connection.
		// Once its ACK has arrived, a subsequent socket rotation belongs to the
		// reconnect lifecycle and must not retroactively fail the subscription.
		return err
	case <-c.ctx.Done():
		c.removeSubscriptionAck(conn, key, waiter)
		return c.ctx.Err()
	case <-timer.C:
		c.removeSubscriptionAck(conn, key, waiter)
		return fmt.Errorf("timed out after %s", timeout)
	}
}

func (c *WebsocketClient) removeSubscriptionAck(conn *websocket.Conn, key string, waiter chan error) {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	byKey := c.subscriptionAcks[conn]
	if byKey == nil || byKey[key] != waiter {
		return
	}
	delete(byKey, key)
	if len(byKey) == 0 {
		delete(c.subscriptionAcks, conn)
	}
}

func (c *WebsocketClient) handleSubscriptionResponse(conn *websocket.Conn, data json.RawMessage) {
	// Hyperliquid's canonical wire shape puts the original subscription object
	// directly in subscriptionResponse.data. Older fixtures and some proxies
	// wrapped it as {method,subscription}; accept that shape only as a backward-
	// compatible fallback.
	key, err := subscriptionKeyFromRaw(data)
	if err != nil {
		c.failSubscriptionAcks(conn, fmt.Errorf("decode subscription acknowledgement: %w", err))
		return
	}
	var response WsSubscriptionResponse
	if unmarshalErr := json.Unmarshal(data, &response); unmarshalErr == nil && len(response.Subscription) != 0 {
		key, err = subscriptionKeyFromRaw(response.Subscription)
		if err != nil {
			c.failSubscriptionAcks(conn, fmt.Errorf("decode acknowledged subscription: %w", err))
			return
		}
		if response.Method == "unsubscribe" {
			// Unsubscribe is fire-and-forget in this client. A delayed
			// confirmation can share the same payload key as a new subscribe;
			// it must not consume or reject the new subscribe waiter.
			return
		}
		if response.Method != "" && response.Method != "subscribe" {
			err = fmt.Errorf("unexpected subscription acknowledgement method %q", response.Method)
		} else if response.Success != nil && !*response.Success {
			err = fmt.Errorf("subscription rejected")
		} else if response.Status == "error" || response.Status == "rejected" || response.Status == "failed" {
			err = fmt.Errorf("subscription %s", response.Status)
		} else if len(response.Error) != 0 && string(response.Error) != "null" && string(response.Error) != `""` {
			err = fmt.Errorf("subscription rejected: %s", string(response.Error))
		}
	}
	c.resolveSubscriptionAck(conn, key, err)
}

func (c *WebsocketClient) resolveSubscriptionAck(conn *websocket.Conn, key string, result error) {
	c.Mu.Lock()
	byKey := c.subscriptionAcks[conn]
	waiter := byKey[key]
	if waiter != nil {
		delete(byKey, key)
		if len(byKey) == 0 {
			delete(c.subscriptionAcks, conn)
		}
	}
	c.Mu.Unlock()
	if waiter != nil {
		waiter <- result
	}
}

func (c *WebsocketClient) failSubscriptionAcks(conn *websocket.Conn, err error) {
	if conn == nil {
		return
	}
	c.Mu.Lock()
	byKey := c.subscriptionAcks[conn]
	delete(c.subscriptionAcks, conn)
	c.Mu.Unlock()
	for _, waiter := range byKey {
		waiter <- err
	}
}

func (c *WebsocketClient) failAllSubscriptionAcks(err error) {
	c.Mu.Lock()
	all := c.subscriptionAcks
	c.subscriptionAcks = make(map[*websocket.Conn]map[string]chan error)
	c.Mu.Unlock()
	for _, byKey := range all {
		for _, waiter := range byKey {
			waiter <- err
		}
	}
}

func subscriptionKeyFromRaw(raw json.RawMessage) (string, error) {
	var subscription any
	if err := json.Unmarshal(raw, &subscription); err != nil {
		return "", err
	}
	return subscriptionKey(subscription), nil
}

func (c *WebsocketClient) completeReconnect(conn *websocket.Conn) bool {
	c.Mu.Lock()
	if c.Conn != conn || conn == nil {
		c.Mu.Unlock()
		return false
	}
	if !c.recovering {
		c.Mu.Unlock()
		return true
	}
	c.recovering = false
	generation := c.recoveryGeneration
	handler := c.onReconnectRecovered
	dispatcher := c.callbackDispatcher
	c.Mu.Unlock()
	if dispatcher != nil {
		dispatcher.enqueueRecovered(generation, conn, handler)
	}
	return true
}

func (c *WebsocketClient) failConnection(conn *websocket.Conn, cause error) {
	if conn == nil {
		return
	}
	if cause == nil {
		cause = fmt.Errorf("hyperliquid websocket: connection lost")
	}
	c.failSubscriptionAcks(conn, cause)
	c.failPostRequests(conn, cause)

	c.Mu.Lock()
	if c.Conn != conn {
		c.Mu.Unlock()
		return
	}
	c.Conn = nil
	epoch := c.activeEpoch
	c.activeEpoch = 0
	startReconnect := c.wantConnected && !c.recovering && c.ctx.Err() == nil
	if startReconnect {
		c.recovering = true
		c.recoveryGeneration++
	}
	generation := c.recoveryGeneration
	recovering := c.recovering
	started := c.onReconnectStarted
	dispatcher := c.callbackDispatcher
	if dispatcher != nil {
		if startReconnect {
			dispatcher.beginGap(generation, func() {
				if started != nil {
					started(cause)
				}
			})
		} else if recovering {
			dispatcher.discardReplacement(generation, conn)
		}
	}
	c.Mu.Unlock()
	_ = conn.Close()
	if startReconnect {
		go c.reconnect(epoch)
	}
}

func subscriptionKey(subscription any) string {
	if subscription == nil {
		return "__default__"
	}
	data, err := json.Marshal(subscription)
	if err != nil {
		return fmt.Sprintf("%#v", subscription)
	}
	// Hyperliquid account identifiers are Ethereum addresses. The server may
	// return a checksummed request address in lower case, and the official SDK
	// also treats user subscription identifiers case-insensitively. Normalize
	// only the user identity field; market/channel fields remain exact.
	var canonical any
	if err := json.Unmarshal(data, &canonical); err == nil {
		normalizeSubscriptionIdentity(canonical)
		if normalized, marshalErr := json.Marshal(canonical); marshalErr == nil {
			return string(normalized)
		}
	}
	return string(data)
}

func normalizeSubscriptionIdentity(value any) {
	switch typed := value.(type) {
	case map[string]any:
		subscriptionType, _ := typed["type"].(string)
		if subscriptionType == "spotState" {
			if alias, ok := typed["ignorePortfolioMargin"]; ok {
				if _, hasOfficialField := typed["isPortfolioMargin"]; !hasOfficialField {
					typed["isPortfolioMargin"] = alias
					delete(typed, "ignorePortfolioMargin")
				}
			}
		}
		if subscriptionType == "l2Book" {
			if value, exists := typed["nSigFigs"]; exists && value == nil {
				delete(typed, "nSigFigs")
			}
			if value, exists := typed["mantissa"]; exists && value == nil {
				delete(typed, "mantissa")
			}
			if value, exists := typed["fast"]; exists && value == false {
				delete(typed, "fast")
			}
		}
		for key, child := range typed {
			if key == "user" {
				if user, ok := child.(string); ok {
					typed[key] = strings.ToLower(user)
					continue
				}
			}
			normalizeSubscriptionIdentity(child)
		}
	case []any:
		for _, child := range typed {
			normalizeSubscriptionIdentity(child)
		}
	}
}

func (c *WebsocketClient) GetNextNonce() int64 {
	for {
		last := c.LastNonce.Load()
		candidate := time.Now().UnixMilli()

		if candidate <= last {
			candidate = last + 1
		}
		if c.LastNonce.CompareAndSwap(last, candidate) {
			return candidate
		}
	}
}

func (c *WebsocketClient) PostAction(action any, sig SignatureResult, nonce int64) (chan PostResult, error) {
	return c.PostActionContext(context.Background(), action, sig, nonce)
}

// PostActionContext is the context-aware form of PostAction. The legacy
// method remains available and keeps its background-context behavior.
func (c *WebsocketClient) PostActionContext(ctx context.Context, action any, sig SignatureResult, nonce int64) (chan PostResult, error) {
	payload := map[string]any{
		"action":    action,
		"nonce":     nonce,
		"signature": sig,
	}
	if c.Vault != "" {
		if actionMap, ok := action.(map[string]any); ok {
			if actionMap["type"] == "usdClassTransfer" {
				actionMap["vaultAddress"] = c.Vault
			} else {
				payload["vaultAddress"] = nil
			}
		} else {
			payload["vaultAddress"] = c.Vault
		}
	}

	return c.PostRequestContext(ctx, WsPostRequestPayload{
		Type:    "action",
		Payload: payload,
	})
}

func (c *WebsocketClient) PostRequest(payload WsPostRequestPayload) (chan PostResult, error) {
	return c.PostRequestContext(context.Background(), payload)
}

// PostRequestContext submits a websocket post and completes its result when
// the response arrives, the exact socket fails, or the caller context ends.
func (c *WebsocketClient) PostRequestContext(ctx context.Context, payload WsPostRequestPayload) (chan PostResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	conn := c.currentConnection()
	if err := c.ensureCurrentConnection(conn); err != nil {
		return nil, err
	}
	id := c.NextPostID.Add(1)
	ch := make(chan PostResult, 1)
	done := make(chan struct{})

	c.Mu.Lock()
	if c.Conn != conn {
		c.Mu.Unlock()
		return nil, fmt.Errorf("websocket connection changed during recovery")
	}
	c.PostChannels[id] = ch
	c.postConnections[id] = conn
	c.postDone[id] = done
	c.Mu.Unlock()

	req := WsPostRequest{
		Method:  "post",
		ID:      id,
		Request: payload,
	}

	if err := c.sendCommandOnContext(ctx, conn, req); err != nil {
		c.discardPostRequest(id)
		return nil, err
	}
	if ctx.Done() != nil {
		go func() {
			select {
			case <-ctx.Done():
				c.completePostRequest(id, PostResult{Error: ctx.Err()})
			case <-done:
			}
		}()
	}

	return ch, nil
}

func (c *WebsocketClient) handlePostResponse(conn *websocket.Conn, msg WsMessage) {
	var data WsPostResponse
	if err := json.Unmarshal(msg.Data, &data); err != nil {
		c.Logger.Errorw("error unmarshaling post response", "error", err)
		return
	}

	c.completePostRequestOn(conn, data.ID, PostResult{Response: data.Response})
}

func (c *WebsocketClient) completePostRequest(id int64, result PostResult) bool {
	c.Mu.Lock()
	ch, ok := c.PostChannels[id]
	if !ok {
		c.Mu.Unlock()
		return false
	}
	done := c.postDone[id]
	delete(c.PostChannels, id)
	delete(c.postConnections, id)
	delete(c.postDone, id)
	c.Mu.Unlock()
	ch <- result
	close(ch)
	if done != nil {
		close(done)
	}
	return true
}

func (c *WebsocketClient) completePostRequestOn(conn *websocket.Conn, id int64, result PostResult) bool {
	c.Mu.Lock()
	if c.postConnections[id] != conn {
		c.Mu.Unlock()
		return false
	}
	ch, ok := c.PostChannels[id]
	done := c.postDone[id]
	if ok {
		delete(c.PostChannels, id)
		delete(c.postConnections, id)
		delete(c.postDone, id)
	}
	c.Mu.Unlock()
	if !ok {
		return false
	}
	ch <- result
	close(ch)
	if done != nil {
		close(done)
	}
	return true
}

func (c *WebsocketClient) discardPostRequest(id int64) {
	c.Mu.Lock()
	ch, ok := c.PostChannels[id]
	done := c.postDone[id]
	if ok {
		delete(c.PostChannels, id)
		delete(c.postConnections, id)
		delete(c.postDone, id)
	}
	c.Mu.Unlock()
	if !ok {
		return
	}
	close(ch)
	if done != nil {
		close(done)
	}
}

func (c *WebsocketClient) failPostRequests(conn *websocket.Conn, cause error) {
	if conn == nil {
		return
	}
	c.failMatchingPostRequests(func(requestConn *websocket.Conn) bool { return requestConn == conn }, cause)
}

func (c *WebsocketClient) failAllPostRequests(cause error) {
	c.failMatchingPostRequests(func(*websocket.Conn) bool { return true }, cause)
}

func (c *WebsocketClient) failMatchingPostRequests(matches func(*websocket.Conn) bool, cause error) {
	if cause == nil {
		cause = fmt.Errorf("hyperliquid websocket: post request interrupted")
	}
	type pendingPost struct {
		ch   chan PostResult
		done chan struct{}
	}
	c.Mu.Lock()
	pending := make([]pendingPost, 0)
	for id, ch := range c.PostChannels {
		if !matches(c.postConnections[id]) {
			continue
		}
		pending = append(pending, pendingPost{ch: ch, done: c.postDone[id]})
		delete(c.PostChannels, id)
		delete(c.postConnections, id)
		delete(c.postDone, id)
	}
	c.Mu.Unlock()
	for _, request := range pending {
		request.ch <- PostResult{Error: cause}
		close(request.ch)
		if request.done != nil {
			close(request.done)
		}
	}
}
