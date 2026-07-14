package nado

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
)

const (
	AuthRequestID = 1111 // Fixed ID for authentication
)

// WsAccountClient handles private account data subscriptions with authentication
// Read loop has NO timeout since account updates may be infrequent (no trading activity)
type WsAccountClient struct {
	url        string
	Signer     *Signer
	restClient *Client
	subaccount string

	ctx    context.Context
	cancel context.CancelFunc

	mu                sync.Mutex
	connectMu         sync.Mutex
	authMu            sync.Mutex
	writeMu           sync.Mutex
	subscriptionMu    sync.Mutex
	conn              *websocket.Conn
	isConnected       bool
	isAuthenticated   bool
	authenticatedConn *websocket.Conn

	authWaitCh   chan error
	authWaitConn *websocket.Conn
	subWaiters   map[int64]chan error

	subscriptions map[string]*accountSubscription
	stopCh        chan struct{}

	loopsStarted       bool
	loopsDoneCh        chan struct{}
	loopsStartOnce     sync.Once
	recovering         bool
	recoveryGeneration uint64

	onReconnectStarted   func(error)
	onReconnectRecovered func()
	afterWrite           func(interface{})
	callbackDispatcher   *accountCallbackDispatcher

	Logger *zap.SugaredLogger
}

type accountSubscription struct {
	params   StreamParams
	callback func([]byte)
}

func NewWsAccountClient(ctx context.Context, restClient *Client) (*WsAccountClient, error) {
	if restClient == nil {
		return nil, fmt.Errorf("nado ws account client: rest client is required")
	}
	profile := restClient.Profile()
	if err := profile.Validate(); err != nil {
		return nil, err
	}
	if restClient.Signer == nil {
		return nil, ErrCredentialsRequired
	}
	c := &WsAccountClient{
		url:                profile.SubscriptionsWSURL(),
		Signer:             restClient.Signer,
		restClient:         restClient,
		subaccount:         restClient.subaccount,
		subscriptions:      make(map[string]*accountSubscription),
		subWaiters:         make(map[int64]chan error),
		callbackDispatcher: newAccountCallbackDispatcher(),
		Logger:             zap.NewNop().Sugar().Named("nado-account"),
	}
	c.ctx, c.cancel = context.WithCancel(ctx)
	return c, nil
}

func (c *WsAccountClient) SetSubaccount(subaccount string) error {
	if subaccount == "" {
		subaccount = "default"
	}
	if len([]byte(subaccount)) > 12 {
		return fmt.Errorf("nado ws account client: subaccount name exceeds 12 bytes")
	}
	c.subaccount = subaccount
	return nil
}

// SetReconnectHooks registers private-stream lifecycle callbacks. Recovered
// runs only after authentication and every account subscription are restored.
func (c *WsAccountClient) SetReconnectHooks(started func(error), recovered func()) {
	c.mu.Lock()
	c.onReconnectStarted = started
	c.onReconnectRecovered = recovered
	c.mu.Unlock()
}

func (c *WsAccountClient) Connect() error {
	_, err := c.connectAndRestore()
	return err
}

func (c *WsAccountClient) connectAndRestore() (*websocket.Conn, error) {
	c.connectMu.Lock()
	defer c.connectMu.Unlock()
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()

	c.mu.Lock()
	if c.isConnected && c.conn != nil {
		conn := c.conn
		c.mu.Unlock()
		return conn, nil
	}
	verifyRecovery := c.recovering
	previousLoops := c.loopsDoneCh
	c.mu.Unlock()
	if previousLoops != nil {
		<-previousLoops
	}
	c.mu.Lock()

	// Safely close old stopCh
	if c.stopCh != nil {
		select {
		case <-c.stopCh:
		default:
			close(c.stopCh)
		}
	}

	c.stopCh = make(chan struct{})
	c.loopsDoneCh = make(chan struct{})
	c.loopsStarted = false
	c.loopsStartOnce = sync.Once{}

	stopCh := c.stopCh
	loopsDoneCh := c.loopsDoneCh
	c.mu.Unlock()

	// Connect with timeout
	connectCtx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	conn, err := c.connect(connectCtx)
	if err != nil {
		return nil, err
	}

	// Start goroutines once per connection
	c.loopsStartOnce.Do(func() {
		var wg sync.WaitGroup
		wg.Add(3)

		go func() {
			defer wg.Done()
			c.pingLoop(conn, stopCh)
		}()
		go func() {
			defer wg.Done()
			c.readLoop(conn, stopCh)
		}()
		go func() {
			defer wg.Done()
			c.authRenewalLoop(stopCh)
		}()

		// Signal when all loops exit
		go func() {
			wg.Wait()
			close(loopsDoneCh)
		}()
	})

	// Restore subscriptions (will authenticate if needed)
	if err := c.restoreSubscriptionsOnLocked(conn, verifyRecovery); err != nil {
		c.dropConnection(conn)
		return nil, err
	}
	if verifyRecovery && !c.completeReconnectOn(conn) {
		c.dropConnection(conn)
		return nil, fmt.Errorf("nado account websocket recovery did not become ready on the captured connection")
	}
	return conn, nil
}

func (c *WsAccountClient) connect(ctx context.Context) (*websocket.Conn, error) {
	conn, _, err := websocket.Dial(ctx, c.url, &websocket.DialOptions{
		CompressionMode: 1,
	})
	if err != nil {
		return nil, err
	}
	c.mu.Lock()
	c.conn = conn
	c.isConnected = true
	c.isAuthenticated = false // Reset auth on new connection
	c.authenticatedConn = nil
	c.authWaitCh = nil
	c.authWaitConn = nil
	if c.callbackDispatcher != nil {
		c.callbackDispatcher.activateConnection(c.recoveryGeneration, conn, c.recovering)
	}
	c.mu.Unlock()
	c.Logger.Infow("Connected to Nado WebSocket (Account)")
	return conn, nil
}

func (c *WsAccountClient) pingLoop(conn *websocket.Conn, stopCh <-chan struct{}) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-stopCh:
			c.Logger.Debug("Ping loop exiting (connection lost)")
			return
		case <-ticker.C:
			if err := c.ensureCurrentConnection(conn); err != nil {
				return
			}
			ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
			if err := conn.Ping(ctx); err != nil {
				c.Logger.Errorw("Ping error", "error", err)
			} else {
				c.Logger.Debug("Ping sent successfully")
			}
			cancel()
		}
	}
}

func (c *WsAccountClient) readLoop(conn *websocket.Conn, stopCh chan struct{}) {
	var readErr error

	defer func() {
		c.mu.Lock()
		ownsConnection := c.conn == conn
		if ownsConnection {
			c.conn = nil
			c.isConnected = false
			c.isAuthenticated = false
			c.authenticatedConn = nil
			c.failWaitersLocked(conn, fmt.Errorf("nado account websocket connection lost"))
		}

		// Stop only the loops that belong to this socket.
		select {
		case <-stopCh:
		default:
			close(stopCh)
		}
		if c.stopCh == stopCh {
			c.stopCh = nil
		}
		manualClose := c.ctx.Err() != nil || !ownsConnection
		c.mu.Unlock()
		if conn != nil {
			_ = conn.Close(websocket.StatusNormalClosure, "")
		}

		if !manualClose {
			if c.beginReconnect(conn, readErr) {
				go c.reconnect()
			}
		}
	}()

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			// Account data has NO timeout (may be idle for long periods)
			_, msg, err := conn.Read(c.ctx)

			if err != nil {
				// Context canceled is expected during normal shutdown
				if c.ctx.Err() != nil {
					c.Logger.Debug("Read loop stopping due to context cancellation")
					return
				}
				readErr = err
				c.Logger.Errorw("Read error", "error", err)
				return
			}

			c.Logger.Debug("Received account message")
			if err := c.handleSocketMessageFrom(conn, msg); err != nil {
				readErr = err
				return
			}
		}
	}
}

func (c *WsAccountClient) beginReconnect(conn *websocket.Conn, err error) bool {
	if err == nil {
		err = fmt.Errorf("nado account websocket: connection lost")
	}
	c.mu.Lock()
	if c.recovering {
		generation := c.recoveryGeneration
		dispatcher := c.callbackDispatcher
		c.mu.Unlock()
		if dispatcher != nil {
			dispatcher.discardReplacement(generation, conn)
		}
		return false
	}
	c.recovering = true
	c.recoveryGeneration++
	generation := c.recoveryGeneration
	handler := c.onReconnectStarted
	dispatcher := c.callbackDispatcher
	c.mu.Unlock()
	if dispatcher != nil {
		dispatcher.beginGap(generation, func() {
			if handler != nil {
				handler(err)
			}
		})
	} else if handler != nil {
		handler(err)
	}
	return true
}

func (c *WsAccountClient) completeReconnectOn(conn *websocket.Conn) bool {
	c.mu.Lock()
	if conn == nil || c.conn != conn || !c.isConnected {
		c.mu.Unlock()
		return false
	}
	if c.subscriptionsNeedAuthenticationLocked() && (!c.isAuthenticated || c.authenticatedConn != conn) {
		c.mu.Unlock()
		return false
	}
	if !c.recovering {
		c.mu.Unlock()
		return true
	}
	generation := c.recoveryGeneration
	handler := c.onReconnectRecovered
	dispatcher := c.callbackDispatcher
	if dispatcher != nil && !dispatcher.enqueueRecovered(generation, conn, handler) {
		c.mu.Unlock()
		return false
	}
	c.recovering = false
	c.mu.Unlock()
	if dispatcher == nil && handler != nil {
		handler()
	}
	return true
}

func (c *WsAccountClient) reconnect() {
	c.Logger.Warn("Connection lost, attempting to reconnect...")

	backoff := time.Second
	const maxBackoff = 30 * time.Second

	attempt := 0
	for {
		select {
		case <-c.ctx.Done():
			c.Logger.Info("Reconnect cancelled due to context done")
			return
		case <-time.After(backoff):
			attempt++
			c.Logger.Infow("Reconnecting", "attempt", attempt, "backoff", backoff)
			if _, err := c.connectAndRestore(); err == nil {
				c.Logger.Infow("Reconnected successfully", "attempts", attempt)
				return
			} else {
				c.Logger.Warnw("Reconnect attempt failed", "attempt", attempt, "error", err)
				backoff *= 2
				if backoff > maxBackoff {
					backoff = maxBackoff
				}
			}
		}
	}
}

func (c *WsAccountClient) Close() {
	c.cancel()
	if c.callbackDispatcher != nil {
		c.callbackDispatcher.stop()
	}
	c.disconnect()
}

func (c *WsAccountClient) disconnect() {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	c.dropConnection(conn)
}

func (c *WsAccountClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isConnected
}

func (c *WsAccountClient) ensureCurrentConnection(conn *websocket.Conn) error {
	if conn == nil {
		return fmt.Errorf("not connected")
	}
	c.mu.Lock()
	current := c.conn
	connected := c.isConnected
	c.mu.Unlock()
	if current != conn || !connected {
		return fmt.Errorf("nado account websocket connection changed during recovery")
	}
	return nil
}

func (c *WsAccountClient) dropConnection(conn *websocket.Conn) {
	if conn == nil {
		return
	}
	c.mu.Lock()
	generation := c.recoveryGeneration
	dispatcher := c.callbackDispatcher
	if c.conn == conn {
		c.conn = nil
		c.isConnected = false
		c.isAuthenticated = false
		c.authenticatedConn = nil
		c.failWaitersLocked(conn, fmt.Errorf("nado account websocket connection closed"))
		if c.stopCh != nil {
			select {
			case <-c.stopCh:
			default:
				close(c.stopCh)
			}
			c.stopCh = nil
		}
	}
	c.mu.Unlock()
	if dispatcher != nil {
		dispatcher.discardReplacement(generation, conn)
	}
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

func (c *WsAccountClient) failWaitersLocked(conn *websocket.Conn, err error) {
	if c.authWaitCh != nil && c.authWaitConn == conn {
		select {
		case c.authWaitCh <- err:
		default:
		}
	}
	for _, waiter := range c.subWaiters {
		select {
		case waiter <- err:
		default:
		}
	}
}

func (c *WsAccountClient) Subscribe(stream StreamParams, callback func([]byte)) error {
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()

	sub := &accountSubscription{
		params:   stream,
		callback: callback,
	}
	channel := stream.Type
	if stream.ProductId != nil {
		channel = fmt.Sprintf("%s:%d", channel, *stream.ProductId)
	}
	c.mu.Lock()
	previous, hadPrevious := c.subscriptions[channel]
	c.subscriptions[channel] = sub
	isConnected := c.isConnected
	conn := c.conn
	c.mu.Unlock()

	if !isConnected {
		return nil
	}

	// Account subscriptions require authentication.
	if stream.Type == "order_update" || stream.Type == "fill" || stream.Type == "position_change" {
		if err := c.authenticateOn(conn); err != nil {
			c.rollbackSubscription(channel, sub, previous, hadPrevious)
			return err
		}
	}
	if err := c.sendSubscribeOn(conn, stream); err != nil {
		c.rollbackSubscription(channel, sub, previous, hadPrevious)
		return err
	}
	return nil
}

func (c *WsAccountClient) rollbackSubscription(channel string, failed, previous *accountSubscription, hadPrevious bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.subscriptions[channel] != failed {
		return
	}
	if hadPrevious {
		c.subscriptions[channel] = previous
		return
	}
	delete(c.subscriptions, channel)
}

func (c *WsAccountClient) Unsubscribe(stream StreamParams) error {
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()

	channel := stream.Type
	if stream.ProductId != nil {
		channel = fmt.Sprintf("%s:%d", channel, *stream.ProductId)
	}

	c.mu.Lock()
	prior, hadPrior := c.subscriptions[channel]
	conn := c.conn
	c.mu.Unlock()

	if err := c.sendUnsubscribeOn(conn, stream); err != nil {
		return err
	}
	c.mu.Lock()
	if current, ok := c.subscriptions[channel]; hadPrior && ok && current == prior {
		delete(c.subscriptions, channel)
	}
	c.mu.Unlock()
	return nil
}

func (c *WsAccountClient) sendSubscribe(stream StreamParams) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	return c.sendSubscribeOn(conn, stream)
}

func (c *WsAccountClient) sendSubscribeOn(conn *websocket.Conn, stream StreamParams) error {
	return c.sendSubscriptionRequestOn(conn, "subscribe", stream)
}

func (c *WsAccountClient) sendUnsubscribeOn(conn *websocket.Conn, stream StreamParams) error {
	return c.sendSubscriptionRequestOn(conn, "unsubscribe", stream)
}

func (c *WsAccountClient) sendSubscriptionRequestOn(conn *websocket.Conn, method string, stream StreamParams) error {
	if err := c.ensureCurrentConnection(conn); err != nil {
		return err
	}
	id := time.Now().UnixNano()
	waiter := make(chan error, 1)
	c.mu.Lock()
	c.subWaiters[id] = waiter
	c.mu.Unlock()
	defer func() {
		c.mu.Lock()
		delete(c.subWaiters, id)
		c.mu.Unlock()
	}()
	req := SubscriptionRequest{
		Method: method,
		Stream: stream,
		Id:     id,
	}
	if err := c.writeJSONOn(conn, req); err != nil {
		return err
	}
	waitCtx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	select {
	case err := <-waiter:
		if err != nil {
			return err
		}
		return nil
	case <-waitCtx.Done():
		return fmt.Errorf("%s %s acknowledgement timeout: %w", method, stream.Type, waitCtx.Err())
	}
}

func (c *WsAccountClient) authenticate() error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	return c.authenticateOn(conn)
}

func (c *WsAccountClient) authenticateOn(conn *websocket.Conn) error {
	c.authMu.Lock()
	defer c.authMu.Unlock()

	if c.Signer == nil {
		return ErrCredentialsRequired
	}
	if err := c.ensureCurrentConnection(conn); err != nil {
		return err
	}

	// Check if already authenticated
	c.mu.Lock()
	if c.conn != conn || !c.isConnected {
		c.mu.Unlock()
		return fmt.Errorf("nado account websocket connection changed during authentication")
	}
	if c.isAuthenticated && c.authenticatedConn == conn {
		c.mu.Unlock()
		return nil
	}
	// Prepare waiting channel
	c.authWaitCh = make(chan error, 1)
	c.authWaitConn = conn
	waitCh := c.authWaitCh
	c.mu.Unlock()

	// Clean up waitCh after we are done
	defer func() {
		c.mu.Lock()
		if c.authWaitCh == waitCh && c.authWaitConn == conn {
			c.authWaitCh = nil
			c.authWaitConn = nil
		}
		c.mu.Unlock()
	}()

	signer := c.Signer

	// Auth request with 10 second expiration
	expiration := fmt.Sprintf("%d", time.Now().Add(10*time.Second).UnixMilli())

	txAuth := TxStreamAuth{
		Sender:     BuildSender(signer.GetAddress(), c.subaccount),
		Expiration: expiration,
	}

	verifyingContract, err := c.endpointAddress(c.ctx)
	if err != nil {
		return err
	}
	signature, err := signer.SignStreamAuthentication(txAuth, verifyingContract)
	if err != nil {
		return err
	}

	req := WsAuthRequest{
		Method:    "authenticate",
		Id:        AuthRequestID,
		Tx:        txAuth,
		Signature: signature,
	}

	if err := c.writeJSONOn(conn, req); err != nil {
		return err
	}

	// Wait for response
	reqCtx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()

	select {
	case err := <-waitCh:
		if err != nil {
			return err
		}
		if err := c.ensureCurrentConnection(conn); err != nil {
			return err
		}
		c.mu.Lock()
		authenticated := c.conn == conn && c.isAuthenticated && c.authenticatedConn == conn
		c.mu.Unlock()
		if !authenticated {
			return fmt.Errorf("nado account websocket authentication was not established on captured connection")
		}
		return nil
	case <-reqCtx.Done():
		return fmt.Errorf("auth timeout")
	}
}

func (c *WsAccountClient) authRenewalLoop(stopCh <-chan struct{}) {
	ticker := time.NewTicker(23 * time.Hour)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-stopCh:
			return
		case <-ticker.C:
			c.mu.Lock()
			if !c.isConnected || !c.isAuthenticated || c.authenticatedConn != c.conn {
				c.mu.Unlock()
				continue
			}
			c.mu.Unlock()

			if err := c.sendAuthMessage(); err != nil {
				c.Logger.Errorw("Auth renewal failed", "error", err)
			}
		}
	}
}

func (c *WsAccountClient) sendAuthMessage() error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	return c.sendAuthMessageOn(conn)
}

func (c *WsAccountClient) sendAuthMessageOn(conn *websocket.Conn) error {
	signer := c.Signer
	if signer == nil {
		return ErrCredentialsRequired
	}
	expiration := fmt.Sprintf("%d", time.Now().Add(24*time.Hour).UnixMilli())
	txAuth := TxStreamAuth{
		Sender:     BuildSender(signer.GetAddress(), c.subaccount),
		Expiration: expiration,
	}
	verifyingContract, err := c.endpointAddress(c.ctx)
	if err != nil {
		return err
	}
	signature, err := signer.SignStreamAuthentication(txAuth, verifyingContract)
	if err != nil {
		return err
	}
	req := WsAuthRequest{
		Method:    "authenticate",
		Id:        AuthRequestID,
		Tx:        txAuth,
		Signature: signature,
	}

	return c.writeJSONOn(conn, req)
}

func (c *WsAccountClient) resubscribeAll() error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	return c.resubscribeAllOn(conn)
}

func (c *WsAccountClient) resubscribeAllOn(conn *websocket.Conn) error {
	return c.restoreSubscriptionsOn(conn, true)
}

func (c *WsAccountClient) restoreSubscriptionsOn(conn *websocket.Conn, verifyRecovery bool) error {
	c.subscriptionMu.Lock()
	defer c.subscriptionMu.Unlock()
	return c.restoreSubscriptionsOnLocked(conn, verifyRecovery)
}

func (c *WsAccountClient) restoreSubscriptionsOnLocked(conn *websocket.Conn, verifyRecovery bool) error {
	if err := c.ensureCurrentConnection(conn); err != nil {
		return err
	}
	c.mu.Lock()
	if len(c.subscriptions) == 0 {
		c.mu.Unlock()
		c.Logger.Info("No account subscriptions to restore")
		return nil
	}

	var allParams []StreamParams
	for _, sub := range c.subscriptions {
		allParams = append(allParams, sub.params)
	}
	c.mu.Unlock()

	c.Logger.Infow("Restoring account subscriptions", "count", len(allParams))

	// Authenticate first if needed
	needAuth := false
	for _, p := range allParams {
		if p.Type == "order_update" || p.Type == "fill" || p.Type == "position_change" {
			needAuth = true
			break
		}
	}

	if needAuth {
		if err := c.authenticateOn(conn); err != nil {
			return fmt.Errorf("authenticate account subscriptions: %w", err)
		}
	}

	// Restore all subscriptions
	for _, p := range allParams {
		if err := c.sendSubscribeOn(conn, p); err != nil {
			return fmt.Errorf("restore account subscription %s: %w", p.Type, err)
		}
		c.Logger.Infow("Restored account subscription", "type", p.Type)
	}

	c.Logger.Info("Account subscription restoration completed")
	if !verifyRecovery {
		return nil
	}
	if err := c.ensureCurrentConnection(conn); err != nil {
		return err
	}
	if needAuth {
		c.mu.Lock()
		authenticated := c.conn == conn && c.isAuthenticated && c.authenticatedConn == conn
		c.mu.Unlock()
		if !authenticated {
			return fmt.Errorf("nado account websocket lost authentication during subscription restoration")
		}
	}
	return nil
}

func (c *WsAccountClient) subscriptionsNeedAuthenticationLocked() bool {
	for _, sub := range c.subscriptions {
		switch sub.params.Type {
		case "order_update", "fill", "position_change":
			return true
		}
	}
	return false
}

func (c *WsAccountClient) writeJSON(v interface{}) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	return c.writeJSONOn(conn, v)
}

func (c *WsAccountClient) writeJSONOn(conn *websocket.Conn, v interface{}) error {
	if err := c.ensureCurrentConnection(conn); err != nil {
		return err
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	if err := c.ensureCurrentConnection(conn); err != nil {
		return err
	}

	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	if err := wsjson.Write(ctx, conn, v); err != nil {
		return err
	}
	if c.afterWrite != nil {
		c.afterWrite(v)
	}
	return nil
}

func (c *WsAccountClient) handleMessage(msg []byte) {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	c.handleMessageFrom(conn, msg)
}

func (c *WsAccountClient) handleMessageFrom(conn *websocket.Conn, msg []byte) {
	_ = c.handleMessageFromMode(conn, msg, false)
}

func (c *WsAccountClient) handleSocketMessageFrom(conn *websocket.Conn, msg []byte) error {
	return c.handleMessageFromMode(conn, msg, true)
}

func (c *WsAccountClient) handleMessageFromMode(conn *websocket.Conn, msg []byte, asyncCallback bool) error {
	var baseMsg struct {
		Id        int64   `json:"id"`
		Error     *string `json:"error,omitempty"`
		Type      *string `json:"type,omitempty"`
		ProductID *int64  `json:"product_id,omitempty"`
	}
	if err := json.Unmarshal(msg, &baseMsg); err != nil {
		return nil
	}
	c.mu.Lock()
	if conn != nil && c.conn != conn {
		c.mu.Unlock()
		return nil
	}

	// Handle auth response
	if baseMsg.Id == AuthRequestID {
		var authErr error
		if baseMsg.Error != nil {
			authErr = fmt.Errorf("auth failed: %s", *baseMsg.Error)
			c.isAuthenticated = false
			c.authenticatedConn = nil
		} else {
			c.isAuthenticated = true
			c.authenticatedConn = conn
			c.Logger.Debug("Authentication successful")
		}

		if c.authWaitCh != nil && (conn == nil || c.authWaitConn == conn) {
			select {
			case c.authWaitCh <- authErr:
			default:
			}
		}
		c.mu.Unlock()
		return nil
	}
	if baseMsg.Id != 0 {
		waiter := c.subWaiters[baseMsg.Id]
		if waiter != nil {
			var ackErr error
			if baseMsg.Error != nil {
				ackErr = fmt.Errorf("subscription rejected: %s", *baseMsg.Error)
			}
			select {
			case waiter <- ackErr:
			default:
			}
			c.mu.Unlock()
			return nil
		}
	}

	if baseMsg.Type == nil {
		c.mu.Unlock()
		c.Logger.Debug("Received account message with no type")
		return nil
	}

	channel := *baseMsg.Type
	if baseMsg.ProductID != nil {
		channel = fmt.Sprintf("%s:%d", channel, *baseMsg.ProductID)
	}

	sub, ok := c.subscriptions[channel]
	if !ok && baseMsg.ProductID != nil {
		// Fallback to wildcard subscription (e.g. "order_update" instead of "order_update:8")
		sub, ok = c.subscriptions[*baseMsg.Type]
	}
	c.mu.Unlock()

	if !ok {
		c.Logger.Warnw("Received message for unknown subscription", "channel", channel)
		return nil
	}

	callback := sub.callback
	if callback != nil {
		copied := append([]byte(nil), msg...)
		if asyncCallback {
			if c.callbackDispatcher != nil && !c.callbackDispatcher.enqueueData(conn, accountCallback{
				run: func() { callback(copied) },
			}) {
				return fmt.Errorf("nado account websocket: callback queue overflow for %s", channel)
			}
		} else {
			callback(copied)
		}
	}
	return nil
}

func (c *WsAccountClient) endpointAddress(ctx context.Context) (string, error) {
	if c.restClient == nil {
		return "", fmt.Errorf("nado ws account client: rest client is required")
	}
	contract, err := c.restClient.ensureContracts(ctx)
	if err != nil {
		return "", fmt.Errorf("nado ws account auth contracts discovery: %w", err)
	}
	return contract.EndpointAddress, nil
}

// getSender helper
func (c *WsAccountClient) getSender() string {
	if c.Signer == nil {
		return ""
	}
	return BuildSender(c.Signer.GetAddress(), c.subaccount)
}

func (c *WsAccountClient) SubscribeOrders(productId *int64, callback func(*OrderUpdate)) error {
	if c.Signer == nil {
		return ErrCredentialsRequired
	}
	sender := c.getSender()
	params := StreamParams{
		Type:       "order_update",
		ProductId:  productId,
		Subaccount: sender,
	}
	return c.Subscribe(params, func(data []byte) {
		var res OrderUpdate
		if err := json.Unmarshal(data, &res); err != nil {
			c.Logger.Errorw("unmarshal order_update error", "error", err)
			return
		}
		if callback != nil {
			callback(&res)
		}
	})
}

func (c *WsAccountClient) SubscribeFills(productId *int64, callback func(*Fill)) error {
	if c.Signer == nil {
		return ErrCredentialsRequired
	}
	sender := c.getSender()
	params := StreamParams{
		Type:       "fill",
		ProductId:  productId,
		Subaccount: sender,
	}
	return c.Subscribe(params, func(data []byte) {
		var res Fill
		if err := json.Unmarshal(data, &res); err != nil {
			c.Logger.Errorw("unmarshal fill error", "error", err)
			return
		}
		if callback != nil {
			callback(&res)
		}
	})
}

func (c *WsAccountClient) SubscribePositions(productId *int64, callback func(*PositionChange)) error {
	if c.Signer == nil {
		return ErrCredentialsRequired
	}
	sender := c.getSender()
	params := StreamParams{
		Type:       "position_change",
		ProductId:  productId,
		Subaccount: sender,
	}
	return c.Subscribe(params, func(data []byte) {
		var res PositionChange
		if err := json.Unmarshal(data, &res); err != nil {
			c.Logger.Errorw("unmarshal position_change error", "error", err)
			return
		}
		if callback != nil {
			callback(&res)
		}
	})
}

func (c *WsAccountClient) UnsubscribeOrders(productId *int64) error {
	sender := c.getSender()
	if sender == "" {
		return nil
	}
	params := StreamParams{
		Type:       "order_update",
		ProductId:  productId,
		Subaccount: sender,
	}
	return c.Unsubscribe(params)
}

func (c *WsAccountClient) UnsubscribePositions(productId *int64) error {
	sender := c.getSender()
	if sender == "" {
		return nil
	}
	params := StreamParams{
		Type:       "position_change",
		ProductId:  productId,
		Subaccount: sender,
	}
	return c.Unsubscribe(params)
}
