package perp

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sync"
	"sync/atomic"
	"time"

	"go.uber.org/zap"

	"github.com/QuantProcessing/boltertrader/internal/wsdispatch"

	"github.com/gorilla/websocket"
)

type WSClient struct {
	URL     string
	Conn    *websocket.Conn
	Mu      sync.RWMutex
	WriteMu sync.Mutex

	Logger *zap.SugaredLogger
	Debug  bool

	// isClosed flag
	isClosed bool

	ctx    context.Context
	cancel context.CancelFunc

	wg                sync.WaitGroup
	callbacksInFlight atomic.Int64
	dropDispatch      atomic.Bool

	ReconnectWait        time.Duration
	maxReconnectAttempts int
	reconnectAttempt     int
	pongInterval         time.Duration

	// active subscriptions
	subs map[string]Subscription

	postReconnect func()
	onDisconnect  func(error)
	dispatcher    *wsdispatch.Dispatcher

	// Message handler to be implemented/assigned by the embedding client
	Handler func([]byte)
}

type Subscription struct {
	id       int64
	callback func([]byte) error
}

type WsClient = WSClient

func NewWSClient(ctx context.Context, url string) *WSClient {
	ctx, cancel := context.WithCancel(ctx)
	return &WSClient{
		URL:                  url,
		ReconnectWait:        1 * time.Second,
		Logger:               zap.NewNop().Sugar().Named("binance-perp"),
		Debug:                os.Getenv("DEBUG") == "true" || os.Getenv("DEBUG") == "1",
		maxReconnectAttempts: 10,
		pongInterval:         1 * time.Minute,
		subs:                 make(map[string]Subscription),
		dispatcher:           wsdispatch.NewBoundedDispatcher(wsdispatch.DefaultBufferLimit),
		ctx:                  ctx,
		cancel:               cancel,
	}
}

func NewWsClient(ctx context.Context, url string) *WSClient {
	return NewWSClient(ctx, url)
}

func (c *WSClient) Connect() error {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	if c.isClosed {
		return fmt.Errorf("client is closed")
	}

	if c.Conn != nil {
		return nil
	}

	dialer := websocket.DefaultDialer
	proxyURL := os.Getenv("PROXY")
	if proxyURL != "" {
		parsedURL, err := url.Parse(proxyURL)
		if err == nil {
			dialer = &websocket.Dialer{
				Proxy:            http.ProxyURL(parsedURL),
				HandshakeTimeout: 45 * time.Second,
			}
			c.Logger.Debugw("Using configured proxy")
		} else {
			c.Logger.Errorw("Invalid proxy URL")
		}
	}

	// Use internal 10 second timeout
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	conn, _, err := dialer.DialContext(ctx, c.URL, nil)
	if err != nil {
		return err
	}

	c.Conn = conn

	c.wg.Add(3)
	go c.setupPingHandlers(conn)
	go c.readLoop(conn)
	go c.keepAlive(conn)

	return nil
}

func (c *WSClient) setupPingHandlers(conn *websocket.Conn) {
	defer c.wg.Done()

	conn.SetPingHandler(func(appData string) error {
		c.Logger.Debugw("Received ping message", "data", appData)
		c.WriteMu.Lock()
		err := conn.WriteMessage(websocket.PongMessage, []byte(appData))
		c.WriteMu.Unlock()
		return err
	})
}

// keepAlive sends unsolicited Pongs as heartbeats if needed, or just relies on reacting to Pings?
// The original code had a loop sending Pongs. We'll strict copy that behavior.
func (c *WSClient) keepAlive(conn *websocket.Conn) {
	defer c.wg.Done()

	ticker := time.NewTicker(c.pongInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.WriteMu.Lock()
			err := conn.WriteMessage(websocket.PongMessage, []byte{})
			c.WriteMu.Unlock()
			if err != nil {
				c.Logger.Errorw("Failed to send pong", "error", err)
				return
			}
		}
	}
}

func (c *WSClient) readLoop(conn *websocket.Conn) {
	defer c.wg.Done()
	connectedAt := time.Now()
	var readErr error

	defer func() {
		_ = conn.Close()
		// Only reset attempt counter if connection lived >5s (not immediately kicked)
		if time.Since(connectedAt) > 5*time.Second {
			c.Mu.Lock()
			c.reconnectAttempt = 0
			c.Mu.Unlock()
		}

		c.Mu.Lock()
		owned := c.Conn == conn
		if owned {
			c.Conn = nil
		}
		isClosed := c.isClosed
		c.Mu.Unlock()

		if owned && !isClosed {
			if readErr == nil {
				readErr = fmt.Errorf("binance perp websocket: connection lost")
			}
			c.Mu.RLock()
			handler := c.onDisconnect
			c.Mu.RUnlock()
			if handler != nil && !c.runCallbackIfOpen(func() {
				handler(readErr)
			}) {
				return
			}
			c.reconnect()
		}
	}()

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
			readErr = err
			if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
				c.Logger.Errorw("websocket unexpected close error", "error", err)
			}
			return
		}

		// Trim space
		message = bytes.TrimSpace(message)
		if len(message) == 0 {
			continue
		}

		if err := c.dispatchMessageChecked(message); err != nil {
			readErr = fmt.Errorf("binance perp websocket callback dispatch: %w", err)
			c.ResetDispatch()
			_ = conn.Close()
			return
		}
	}
}

func (c *WSClient) dispatchMessage(message []byte) {
	_ = c.dispatchMessageChecked(message)
}

func (c *WSClient) dispatchMessageChecked(message []byte) error {
	if c.dropDispatch.Load() {
		return nil
	}
	handler := c.Handler
	if handler == nil {
		return nil
	}
	msg := append([]byte(nil), message...)
	return c.dispatcher.DispatchChecked(func() {
		if c.dropDispatch.Load() {
			return
		}
		c.runCallbackIfOpen(func() {
			handler(msg)
		})
	})
}

func (c *WSClient) runCallbackIfOpen(callback func()) bool {
	if callback == nil {
		return true
	}
	c.Mu.RLock()
	if c.isClosed {
		c.Mu.RUnlock()
		return false
	}
	c.callbacksInFlight.Add(1)
	c.Mu.RUnlock()
	defer c.callbacksInFlight.Add(-1)
	callback()
	return true
}

func (c *WSClient) PauseDispatch() {
	c.dispatcher.Pause()
}

func (c *WSClient) ResumeDispatch(beforeDrain func()) {
	c.dispatcher.Resume(beforeDrain)
}

// ResetDispatch drops callbacks buffered for a superseded connection and
// returns dispatch to its running state. A callback already executing is
// allowed to finish.
func (c *WSClient) ResetDispatch() {
	c.dispatcher.Reset()
}

// pauseSourceDispatchForRecovery closes the old generation's admission gate,
// waits for an already accepted callback to finish, drops callbacks that were
// queued behind it, then runs beforeRecovery. The source remains fail-closed;
// a replacement WSClient owns a separate dispatcher and is paused before its
// Connect call.
func (c *WSClient) pauseSourceDispatchForRecovery(beforeRecovery func()) {
	c.dropDispatch.Store(true)
	c.PauseDispatch()
	c.ResumeDispatch(func() {
		c.ResetDispatch()
		if beforeRecovery != nil {
			beforeRecovery()
		}
	})
}

func (c *WSClient) stopSourceDispatchForRecovery() {
	c.dropDispatch.Store(true)
	c.PauseDispatch()
}

func (c *WSClient) reconnect() {
	c.Mu.Lock()
	if c.isClosed {
		c.Mu.Unlock()
		return
	}
	c.reconnectAttempt++
	attempt := c.reconnectAttempt
	c.Mu.Unlock()

	if attempt > c.maxReconnectAttempts {
		c.Logger.Error("Max reconnection attempts reached")
		return
	}

	backoff := time.Duration(1<<uint(attempt-1)) * c.ReconnectWait
	if backoff > 30*time.Second {
		backoff = 30 * time.Second
	}

	c.Logger.Infow("Reconnecting...", "backoff", backoff)

	select {
	case <-c.ctx.Done():
		return
	case <-time.After(backoff):
	}

	if err := c.Connect(); err != nil {
		c.Logger.Errorw("Reconnection failed", "error", err)
		go c.reconnect()
		return
	}

	// Resubscribe
	c.Mu.RLock()
	subs := make(map[string]Subscription)
	for k, v := range c.subs {
		subs[k] = v
	}
	c.Mu.RUnlock()

	for stream, sub := range subs {
		// If ID is 0, it might be a local handler (pushed stream)
		if sub.id == 0 {
			// No need to send subscribe frame
			continue
		}
		// Rate-limit: max 4 messages/sec (Binance limit: 5/sec)
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(250 * time.Millisecond):
		}
		// Send subscribe frame
		req := map[string]interface{}{
			"method": "SUBSCRIBE",
			"params": []string{stream},
			"id":     sub.id,
		}
		if err := c.WriteJSON(req); err != nil {
			c.Logger.Errorw("Resubscribe failed", "stream", stream, "error", err)
		}
	}

	c.Mu.RLock()
	handler := c.postReconnect
	c.Mu.RUnlock()
	if handler != nil {
		go handler()
	}
}

func (c *WSClient) SetPostReconnect(handler func()) {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	c.postReconnect = handler
}

// SetOnDisconnect sets a callback for unexpected transport loss. Intentional
// Close calls do not invoke it.
func (c *WSClient) SetOnDisconnect(handler func(error)) {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	c.onDisconnect = handler
}

func (c *WSClient) WriteJSON(v interface{}) error {
	c.WriteMu.Lock()
	defer c.WriteMu.Unlock()

	c.Mu.RLock()
	conn := c.Conn
	c.Mu.RUnlock()

	if conn == nil {
		return fmt.Errorf("connection not established")
	}

	c.Logger.Debugw("Sending", "msg", v)

	return conn.WriteJSON(v)
}

func (c *WSClient) Close() {
	c.Mu.Lock()
	if c.isClosed {
		c.Mu.Unlock()
		return
	}
	c.isClosed = true
	if c.Conn != nil {
		c.Conn.Close()
		c.Conn = nil
	}
	c.Mu.Unlock()

	// c.cancel may be nil if Connect was never called successfully.
	if c.cancel != nil {
		c.cancel()
	}
	if c.callbacksInFlight.Load() == 0 {
		c.wg.Wait()
	}
	c.ResetDispatch()
}

// Subscribe sends a subscription request
func (c *WSClient) Subscribe(stream string, handler func([]byte) error) error {
	id := wsdispatch.GenerateRandomID()
	c.Mu.Lock()
	c.subs[stream] = Subscription{
		id:       id,
		callback: handler,
	}
	c.Mu.Unlock()

	req := map[string]interface{}{
		"method": "SUBSCRIBE",
		"params": []string{stream},
		"id":     id,
	}
	return c.WriteJSON(req)
}

// Unsubscribe sends an unsubscribe request
func (c *WSClient) Unsubscribe(stream string) error {
	c.Mu.Lock()
	sub, ok := c.subs[stream]
	if !ok {
		c.Mu.Unlock()
		return nil
	}
	delete(c.subs, stream)
	c.Mu.Unlock()

	req := map[string]interface{}{
		"method": "UNSUBSCRIBE",
		"params": []string{stream},
		"id":     sub.id,
	}
	return c.WriteJSON(req)
}

// SetHandler registers a local handler (no network request)
func (c *WSClient) SetHandler(stream string, handler func([]byte) error) {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	c.subs[stream] = Subscription{
		id:       0,
		callback: handler,
	}
}

func (c *WSClient) SetSubscriptionHandler(stream string, handler func([]byte) error) {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	sub := c.subs[stream]
	sub.callback = handler
	c.subs[stream] = sub
}

func (c *WSClient) CallSubscription(key string, message []byte) bool {
	c.Mu.RLock()
	sub, exist := c.subs[key]
	c.Mu.RUnlock()

	if exist && sub.callback != nil {
		if err := sub.callback(message); err != nil {
			c.Logger.Error("callback error", "error", err)
		}
		return true
	}
	return false
}

func (c *WSClient) IsConnected() bool {
	c.Mu.RLock()
	defer c.Mu.RUnlock()
	return c.Conn != nil && !c.isClosed
}
