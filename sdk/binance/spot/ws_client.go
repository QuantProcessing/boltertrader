package spot

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/QuantProcessing/boltertrader/internal/wsdispatch"

	"github.com/gorilla/websocket"
)

const (
	WSBaseURL = "wss://stream.binance.com:9443/ws"
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

	wg sync.WaitGroup

	ReconnectWait        time.Duration
	maxReconnectAttempts int
	reconnectAttempt     int
	pongInterval         time.Duration

	// active subscriptions
	subs map[string]Subscription

	postReconnect func()
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
		Logger:               zap.NewNop().Sugar().Named("binance-spot-ws"),
		Debug:                os.Getenv("DEBUG") == "true" || os.Getenv("DEBUG") == "1",
		maxReconnectAttempts: 10,
		pongInterval:         3 * time.Minute, // Spot might have different requirement, but 3m is safe-ish. Perp uses 1m.
		subs:                 make(map[string]Subscription),
		dispatcher:           wsdispatch.NewDispatcher(),
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
			c.Logger.Debugw("Using proxy", "url", proxyURL)
		} else {
			c.Logger.Errorw("Invalid proxy URL", "url", proxyURL, "error", err)
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
	c.reconnectAttempt = 0

	// Set handlers before starting read loop
	c.setupPingHandlers(conn)

	c.wg.Add(2)
	go c.readLoop()
	go c.keepAlive()

	return nil
}

// IsConnected checks if the WebSocket connection is established
func (c *WSClient) IsConnected() bool {
	c.Mu.RLock()
	defer c.Mu.RUnlock()
	return c.Conn != nil && !c.isClosed
}

func (c *WSClient) setupPingHandlers(conn *websocket.Conn) {
	conn.SetPingHandler(func(appData string) error {
		c.Logger.Debugw("Received ping message", "data", appData)
		c.WriteMu.Lock()
		err := conn.WriteMessage(websocket.PongMessage, []byte(appData))
		c.WriteMu.Unlock()
		return err
	})
}

func (c *WSClient) keepAlive() {
	defer c.wg.Done()

	ticker := time.NewTicker(c.pongInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.Mu.RLock()
			conn := c.Conn
			c.Mu.RUnlock()

			if conn == nil {
				return
			}

			// Binance Spot typically expects Pong response to server Ping.
			// But sending unsolicited Pong is also allowed as heartbeat.
			c.WriteMu.Lock()
			err := conn.WriteMessage(websocket.PongMessage, []byte{})
			c.WriteMu.Unlock()
			if err != nil {
				c.Logger.Errorw("Failed to send pong", "error", err)
			}
		}
	}
}

func (c *WSClient) readLoop() {
	defer c.wg.Done()

	defer func() {
		c.Mu.Lock()
		c.Conn = nil
		c.Mu.Unlock()

		c.Mu.RLock()
		isClosed := c.isClosed
		c.Mu.RUnlock()

		if !isClosed {
			c.reconnect()
		}
	}()

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
		}

		c.Mu.RLock()
		conn := c.Conn
		c.Mu.RUnlock()

		if conn == nil {
			return
		}

		_, message, err := conn.ReadMessage()
		if err != nil {
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

		c.dispatchMessage(message)
	}
}

func (c *WSClient) dispatchMessage(message []byte) {
	if c.Handler == nil {
		return
	}
	msg := append([]byte(nil), message...)
	c.dispatcher.Dispatch(func() {
		c.Handler(msg)
	})
}

func (c *WSClient) PauseDispatch() {
	c.dispatcher.Pause()
}

func (c *WSClient) ResumeDispatch(beforeDrain func()) {
	c.dispatcher.Resume(beforeDrain)
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
		// If ID is 0, it might be a local handler (pushed stream or user stream where we don't send explicit subscribe)
		if sub.id == 0 {
			continue
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

func (c *WSClient) WriteJSON(v interface{}) error {
	c.WriteMu.Lock()
	defer c.WriteMu.Unlock()

	c.Mu.RLock()
	conn := c.Conn
	c.Mu.RUnlock()

	if conn == nil {
		return fmt.Errorf("connection not established")
	}

	if c.Debug {
		c.Logger.Debugw("Sending", "msg", v)
	}

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

	if c.cancel != nil {
		c.cancel()
	}
	c.wg.Wait()
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
