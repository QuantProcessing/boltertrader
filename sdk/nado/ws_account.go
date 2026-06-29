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
	privateKey string
	subaccount string

	ctx    context.Context
	cancel context.CancelFunc

	mu              sync.Mutex
	writeMu         sync.Mutex
	conn            *websocket.Conn
	isConnected     bool
	isAuthenticated bool

	authWaitCh chan error

	subscriptions map[string]*accountSubscription
	stopCh        chan struct{}

	loopsStarted   bool
	loopsDoneCh    chan struct{}
	loopsStartOnce sync.Once

	Logger *zap.SugaredLogger
}

type accountSubscription struct {
	params   StreamParams
	callback func([]byte)
}

func NewWsAccountClient(ctx context.Context) *WsAccountClient {
	c := &WsAccountClient{
		url:           WsSubscriptionsURL,
		subaccount:    "default",
		subscriptions: make(map[string]*accountSubscription),
		Logger:        zap.NewNop().Sugar().Named("nado-account"),
	}
	c.ctx, c.cancel = context.WithCancel(ctx)
	return c
}

func (c *WsAccountClient) WithCredentials(privateKey string) *WsAccountClient {
	c.privateKey = privateKey
	return c
}

func (c *WsAccountClient) SetSubaccount(subaccount string) {
	if subaccount == "" {
		subaccount = "default"
	}
	c.subaccount = subaccount
}

func (c *WsAccountClient) Connect() error {
	c.mu.Lock()

	// Wait for previous loops to exit
	if c.loopsDoneCh != nil {
		c.mu.Unlock()
		<-c.loopsDoneCh
		c.mu.Lock()
	}

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
	if err := c.connect(connectCtx); err != nil {
		return err
	}

	// Start goroutines once per connection
	c.loopsStartOnce.Do(func() {
		var wg sync.WaitGroup
		wg.Add(3)

		go func() {
			defer wg.Done()
			c.pingLoop()
		}()
		go func() {
			defer wg.Done()
			c.readLoop()
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
	c.resubscribeAll()

	return nil
}

func (c *WsAccountClient) connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	conn, _, err := websocket.Dial(ctx, c.url, &websocket.DialOptions{
		CompressionMode: 1,
	})
	if err != nil {
		return err
	}
	c.conn = conn
	c.isConnected = true
	c.isAuthenticated = false // Reset auth on new connection
	c.Logger.Infow("Connected to Nado WebSocket (Account)")
	return nil
}

func (c *WsAccountClient) pingLoop() {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()

	c.mu.Lock()
	stopCh := c.stopCh
	c.mu.Unlock()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-stopCh:
			c.Logger.Debug("Ping loop exiting (connection lost)")
			return
		case <-ticker.C:
			if c.conn != nil {
				ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
				if err := c.conn.Ping(ctx); err != nil {
					c.Logger.Errorw("Ping error", "error", err)
				} else {
					c.Logger.Debug("Ping sent successfully")
				}
				cancel()
			}
		}
	}
}

func (c *WsAccountClient) readLoop() {
	defer func() {
		c.mu.Lock()
		if c.conn != nil {
			c.conn.Close(websocket.StatusNormalClosure, "")
			c.conn = nil
		}
		c.isConnected = false
		c.isAuthenticated = false

		// Safely close stopCh
		if c.stopCh != nil {
			select {
			case <-c.stopCh:
			default:
				close(c.stopCh)
			}
			c.stopCh = nil
		}

		manualClose := c.ctx.Err() != nil
		c.mu.Unlock()

		if !manualClose {
			go c.reconnect()
		}
	}()

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			// Account data has NO timeout (may be idle for long periods)
			_, msg, err := c.conn.Read(c.ctx)

			if err != nil {
				// Context canceled is expected during normal shutdown
				if c.ctx.Err() != nil {
					c.Logger.Debug("Read loop stopping due to context cancellation")
					return
				}
				c.Logger.Errorw("Read error", "error", err)
				return
			}

			c.Logger.Debug("Received message", "msg", string(msg))
			c.handleMessage(msg)
		}
	}
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
			if err := c.Connect(); err == nil {
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
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		c.conn.Close(websocket.StatusNormalClosure, "")
		c.conn = nil
	}
}

func (c *WsAccountClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isConnected
}

func (c *WsAccountClient) Subscribe(stream StreamParams, callback func([]byte)) error {
	c.mu.Lock()
	sub := &accountSubscription{
		params:   stream,
		callback: callback,
	}
	channel := stream.Type
	if stream.ProductId != nil {
		channel = fmt.Sprintf("%s:%d", channel, *stream.ProductId)
	}
	c.subscriptions[channel] = sub
	isConnected := c.isConnected
	c.mu.Unlock()

	if isConnected {
		// Account subscriptions require authentication
		if stream.Type == "order_update" || stream.Type == "fill" || stream.Type == "position_change" {
			if err := c.authenticate(); err != nil {
				return err
			}
		}
		return c.sendSubscribe(stream)
	}
	return nil
}

func (c *WsAccountClient) Unsubscribe(stream StreamParams) error {
	channel := stream.Type
	if stream.ProductId != nil {
		channel = fmt.Sprintf("%s:%d", channel, *stream.ProductId)
	}

	c.mu.Lock()
	delete(c.subscriptions, channel)
	c.mu.Unlock()

	req := SubscriptionRequest{
		Method: "unsubscribe",
		Stream: stream,
		Id:     time.Now().UnixNano(),
	}
	return c.writeJSON(req)
}

func (c *WsAccountClient) sendSubscribe(stream StreamParams) error {
	req := SubscriptionRequest{
		Method: "subscribe",
		Stream: stream,
		Id:     time.Now().UnixNano(),
	}
	return c.writeJSON(req)
}

func (c *WsAccountClient) authenticate() error {
	if c.privateKey == "" {
		c.updateAuthState(true)
		return nil
	}

	// Check if already authenticated
	c.mu.Lock()
	if c.isAuthenticated {
		c.mu.Unlock()
		return nil
	}
	// Prepare waiting channel
	if c.authWaitCh == nil {
		c.authWaitCh = make(chan error, 1)
	}
	waitCh := c.authWaitCh
	c.mu.Unlock()

	// Clean up waitCh after we are done
	defer func() {
		c.mu.Lock()
		c.authWaitCh = nil
		c.mu.Unlock()
	}()

	signer, err := NewSigner(c.privateKey)
	if err != nil {
		return err
	}

	// Auth request with 10 second expiration
	expiration := fmt.Sprintf("%d", time.Now().Add(10*time.Second).UnixMilli())

	txAuth := TxStreamAuth{
		Sender:     BuildSender(signer.GetAddress(), c.subaccount),
		Expiration: expiration,
	}

	verifyingContract := EndpointAddress
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

	if err := c.writeJSON(req); err != nil {
		return err
	}

	// Wait for response
	reqCtx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()

	select {
	case err := <-waitCh:
		return err
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
			if !c.isConnected || !c.isAuthenticated {
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
	signer, err := NewSigner(c.privateKey)
	if err != nil {
		return err
	}
	expiration := fmt.Sprintf("%d", time.Now().Add(24*time.Hour).UnixMilli())
	txAuth := TxStreamAuth{
		Sender:     BuildSender(signer.GetAddress(), c.subaccount),
		Expiration: expiration,
	}
	verifyingContract := EndpointAddress
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

	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	return wsjson.Write(ctx, conn, req)
}

func (c *WsAccountClient) updateAuthState(auth bool) {
	c.mu.Lock()
	c.isAuthenticated = auth
	c.mu.Unlock()
}

func (c *WsAccountClient) resubscribeAll() {
	c.mu.Lock()
	if len(c.subscriptions) == 0 {
		c.mu.Unlock()
		c.Logger.Info("No account subscriptions to restore")
		return
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
		if err := c.authenticate(); err != nil {
			c.Logger.Errorw("Auth failed during resubscribe", "error", err)
			return
		}
	}

	// Restore all subscriptions
	for _, p := range allParams {
		if err := c.sendSubscribe(p); err != nil {
			c.Logger.Errorw("Failed to restore account subscription",
				"type", p.Type,
				"error", err,
			)
		} else {
			c.Logger.Infow("Restored account subscription", "type", p.Type)
		}
	}

	c.Logger.Info("Account subscription restoration completed")
}

func (c *WsAccountClient) writeJSON(v interface{}) error {
	c.writeMu.Lock()
	defer c.writeMu.Unlock()

	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()

	if conn == nil {
		return fmt.Errorf("not connected")
	}

	ctx, cancel := context.WithTimeout(c.ctx, 5*time.Second)
	defer cancel()
	return wsjson.Write(ctx, conn, v)
}

func (c *WsAccountClient) handleMessage(msg []byte) {
	var baseMsg struct {
		Id        int64   `json:"id"`
		Error     *string `json:"error,omitempty"`
		Type      *string `json:"type,omitempty"`
		ProductID *int64  `json:"product_id,omitempty"`
	}
	if err := json.Unmarshal(msg, &baseMsg); err != nil {
		return
	}

	// Handle auth response
	if baseMsg.Id == AuthRequestID {
		var authErr error
		if baseMsg.Error != nil {
			authErr = fmt.Errorf("auth failed: %s", *baseMsg.Error)
			c.updateAuthState(false)
		} else {
			c.updateAuthState(true)
			c.Logger.Debug("Authentication successful")
		}

		c.mu.Lock()
		if c.authWaitCh != nil {
			select {
			case c.authWaitCh <- authErr:
			default:
			}
		}
		c.mu.Unlock()
		return
	}

	if baseMsg.Type == nil {
		c.Logger.Debugw("Received message with no type", "msg", string(msg))
		return
	}

	channel := *baseMsg.Type
	if baseMsg.ProductID != nil {
		channel = fmt.Sprintf("%s:%d", channel, *baseMsg.ProductID)
	}

	c.mu.Lock()
	sub, ok := c.subscriptions[channel]
	if !ok && baseMsg.ProductID != nil {
		// Fallback to wildcard subscription (e.g. "order_update" instead of "order_update:8")
		sub, ok = c.subscriptions[*baseMsg.Type]
	}
	c.mu.Unlock()

	if !ok {
		c.Logger.Warnw("Received message for unknown subscription", "channel", channel)
		return
	}

	// Call callback synchronously to preserve message order
	sub.callback(msg)
}

// getSender helper
func (c *WsAccountClient) getSender() string {
	if c.privateKey == "" {
		return ""
	}
	signer, _ := NewSigner(c.privateKey)
	return BuildSender(signer.GetAddress(), c.subaccount)
}

func (c *WsAccountClient) SubscribeOrders(productId *int64, callback func(*OrderUpdate)) error {
	if c.privateKey == "" {
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
			fmt.Printf("unmarshal order_update error: %v\n", err)
			return
		}
		if callback != nil {
			callback(&res)
		}
	})
}

func (c *WsAccountClient) SubscribeFills(productId *int64, callback func(*Fill)) error {
	if c.privateKey == "" {
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
			fmt.Printf("unmarshal fill error: %v\n", err)
			return
		}
		if callback != nil {
			callback(&res)
		}
	})
}

func (c *WsAccountClient) SubscribePositions(productId *int64, callback func(*PositionChange)) error {
	if c.privateKey == "" {
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
			fmt.Printf("unmarshal position_change error: %v\n", err)
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
