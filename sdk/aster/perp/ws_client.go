package perp

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/QuantProcessing/boltertrader/internal/wsdispatch"
	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"

	"github.com/gorilla/websocket"
)

type WSClient struct {
	endpoint string
	Conn     *websocket.Conn
	Mu       sync.RWMutex
	WriteMu  sync.Mutex

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
	subs     map[string]Subscription
	handlers map[string]func([]byte) error

	// Message handler to be implemented/assigned by the embedding client
	Handler func([]byte)
}

type WsClient = WSClient

type Subscription struct {
	id       int64
	callback func([]byte) error
	sent     bool
}

func newWSClient(ctx context.Context, rawURL string) *WSClient {
	ctx, cancel := context.WithCancel(ctx)
	return &WSClient{
		endpoint:             rawURL,
		ReconnectWait:        1 * time.Second,
		Logger:               zap.NewNop().Sugar().Named("aster-perp"),
		Debug:                os.Getenv("DEBUG") == "true" || os.Getenv("DEBUG") == "1",
		maxReconnectAttempts: 10,
		pongInterval:         1 * time.Minute,
		subs:                 make(map[string]Subscription),
		handlers:             make(map[string]func([]byte) error),
		ctx:                  ctx,
		cancel:               cancel,
	}
}

func (c *WSClient) Connect() error {
	c.Mu.Lock()
	if c.isClosed {
		c.Mu.Unlock()
		return fmt.Errorf("aster perp websocket: client is closed")
	}
	if c.Conn != nil {
		c.Mu.Unlock()
		return nil
	}
	rawURL := c.endpoint
	c.Mu.Unlock()

	dialer, err := astercommon.WebSocketDialer(rawURL, os.Getenv("PROXY"))
	if err != nil {
		return err
	}

	// Use internal 10 second timeout
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	conn, _, err := dialer.DialContext(ctx, rawURL, nil)
	if err != nil {
		return astercommon.NewTransportError(http.MethodGet, "websocket", err)
	}
	c.setupPingHandlers(conn)

	c.Mu.Lock()
	if c.isClosed {
		c.Mu.Unlock()
		_ = conn.Close()
		return fmt.Errorf("aster perp websocket: client is closed")
	}
	if c.Conn != nil {
		c.Mu.Unlock()
		_ = conn.Close()
		return nil
	}
	c.Conn = conn
	c.reconnectAttempt = 0
	c.wg.Add(2)
	c.Mu.Unlock()

	go c.readLoop(conn)
	go c.keepAlive(conn)

	if err := c.resubscribe(); err != nil {
		c.Logger.Errorw("failed to restore websocket subscriptions", "error", err)
		_ = conn.Close()
	}
	return nil
}

func (c *WSClient) setURL(rawURL string) error {
	if err := validateWebSocketURL(rawURL); err != nil {
		return err
	}
	c.Mu.Lock()
	defer c.Mu.Unlock()
	if c.isClosed {
		return fmt.Errorf("aster perp websocket: client is closed")
	}
	if c.Conn != nil {
		return fmt.Errorf("aster perp websocket: cannot change endpoint while connected")
	}
	c.endpoint = rawURL
	return nil
}

func (c *WSClient) reconnectTo(rawURL string) error {
	if err := validateWebSocketURL(rawURL); err != nil {
		return err
	}
	c.Mu.Lock()
	if c.isClosed {
		c.Mu.Unlock()
		return fmt.Errorf("aster perp websocket: client is closed")
	}
	c.endpoint = rawURL
	conn := c.Conn
	c.Mu.Unlock()
	if conn != nil {
		return conn.Close()
	}
	return c.Connect()
}

func validateWebSocketURL(rawURL string) error {
	parsed, err := url.Parse(rawURL)
	if err != nil || parsed.Host == "" || (parsed.Scheme != "ws" && parsed.Scheme != "wss") {
		return fmt.Errorf("aster perp websocket: invalid endpoint")
	}
	return nil
}

func (c *WSClient) setupPingHandlers(conn *websocket.Conn) {
	conn.SetPingHandler(func(appData string) error {
		c.WriteMu.Lock()
		err := conn.WriteMessage(websocket.PongMessage, []byte(appData))
		c.WriteMu.Unlock()
		return err
	})
}

func (c *WSClient) keepAlive(conn *websocket.Conn) {
	defer c.wg.Done()

	ticker := time.NewTicker(c.pongInterval)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.Mu.RLock()
			current := c.Conn == conn && !c.isClosed
			c.Mu.RUnlock()
			if !current {
				return
			}
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
	defer func() {
		c.Mu.Lock()
		owned := c.Conn == conn
		if owned {
			c.Conn = nil
			for stream, subscription := range c.subs {
				subscription.sent = false
				c.subs[stream] = subscription
			}
		}
		closed := c.isClosed
		c.Mu.Unlock()
		if owned && !closed {
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

		if c.Handler != nil {
			c.Handler(message)
		}
	}
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

	select {
	case <-c.ctx.Done():
		return
	case <-time.After(backoff):
	}

	if err := c.Connect(); err != nil {
		c.Logger.Errorw("Reconnection failed", "error", err)
		go c.reconnect()
	}
}

func (c *WSClient) resubscribe() error {
	c.Mu.RLock()
	streams := make([]string, 0, len(c.subs))
	for stream, subscription := range c.subs {
		if subscription.id != 0 {
			streams = append(streams, stream)
		}
	}
	c.Mu.RUnlock()
	sort.Strings(streams)
	for _, stream := range streams {
		if err := c.sendSubscription(stream); err != nil {
			return err
		}
	}
	return nil
}

func (c *WSClient) WriteJSON(v interface{}) error {
	c.WriteMu.Lock()
	defer c.WriteMu.Unlock()

	c.Mu.RLock()
	conn := c.Conn
	closed := c.isClosed
	c.Mu.RUnlock()

	if conn == nil || closed {
		return fmt.Errorf("aster perp websocket: connection not established")
	}

	if c.Debug {
		c.Logger.Debugw("sending websocket control message", "type", fmt.Sprintf("%T", v))
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
	conn := c.Conn
	c.Conn = nil
	c.Mu.Unlock()

	if c.cancel != nil {
		c.cancel()
	}
	if conn != nil {
		_ = conn.Close()
	}
	c.wg.Wait()
}

// Subscribe sends a subscription request
func (c *WSClient) Subscribe(stream string, handler func([]byte) error) error {
	stream = strings.TrimSpace(stream)
	if stream == "" {
		return fmt.Errorf("aster perp websocket: stream is required")
	}
	c.Mu.Lock()
	subscription, exists := c.subs[stream]
	if !exists || subscription.id == 0 {
		subscription.id = wsdispatch.GenerateRandomID()
	}
	if handler != nil {
		subscription.callback = handler
	}
	c.subs[stream] = subscription
	connected := c.Conn != nil && !c.isClosed
	alreadySent := subscription.sent
	c.Mu.Unlock()

	if alreadySent {
		return nil
	}
	if !connected {
		return fmt.Errorf("aster perp websocket: connection not established")
	}
	return c.sendSubscription(stream)
}

func (c *WSClient) sendSubscription(stream string) error {
	c.Mu.Lock()
	subscription, exists := c.subs[stream]
	if !exists || subscription.id == 0 || subscription.sent {
		c.Mu.Unlock()
		return nil
	}
	if c.Conn == nil || c.isClosed {
		c.Mu.Unlock()
		return fmt.Errorf("aster perp websocket: connection not established")
	}
	subscription.sent = true
	c.subs[stream] = subscription
	c.Mu.Unlock()

	req := map[string]interface{}{
		"method": "SUBSCRIBE",
		"params": []string{stream},
		"id":     subscription.id,
	}
	if err := c.WriteJSON(req); err != nil {
		c.Mu.Lock()
		if current, ok := c.subs[stream]; ok && current.id == subscription.id {
			current.sent = false
			c.subs[stream] = current
		}
		c.Mu.Unlock()
		return err
	}
	return nil
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
	if handler == nil {
		delete(c.handlers, stream)
		return
	}
	c.handlers[stream] = handler
}

func (c *WSClient) CallSubscription(key string, message []byte) {
	c.Mu.RLock()
	sub := c.subs[key]
	handler := c.handlers[key]
	c.Mu.RUnlock()
	for _, callback := range []func([]byte) error{sub.callback, handler} {
		if callback == nil {
			continue
		}
		if err := callback(message); err != nil {
			c.Logger.Errorw("websocket callback failed", "stream", key, "error", err)
		}
	}
}

func (c *WSClient) subscriptionWithPrefix(prefix string) string {
	c.Mu.RLock()
	defer c.Mu.RUnlock()
	for stream := range c.subs {
		if strings.HasPrefix(stream, prefix) {
			return stream
		}
	}
	return ""
}

func (c *WSClient) IsConnected() bool {
	c.Mu.RLock()
	defer c.Mu.RUnlock()
	return c.Conn != nil && !c.isClosed
}
