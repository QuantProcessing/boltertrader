package spot

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

	"github.com/gorilla/websocket"
	"go.uber.org/zap"

	"github.com/QuantProcessing/boltertrader/internal/wsdispatch"
	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
)

type WSClient struct {
	endpoint        string
	Conn            *websocket.Conn
	Mu              sync.RWMutex
	WriteMu         sync.Mutex
	subMu           sync.Mutex
	reconnectHookMu sync.Mutex
	connEpoch       uint64

	Logger *zap.SugaredLogger
	Debug  bool

	isClosed bool
	ctx      context.Context
	cancel   context.CancelFunc
	wg       sync.WaitGroup

	ReconnectWait        time.Duration
	maxReconnectAttempts int
	reconnectAttempt     int
	pongInterval         time.Duration

	subs     map[string]Subscription
	handlers map[string]func([]byte) error

	Handler              func([]byte)
	onReconnectStarted   func(error)
	onReconnectRecovered func()
	recovering           bool
	recoveryGeneration   uint64
	callbackDispatcher   *wsCallbackDispatcher
}

const websocketWriteTimeout = 2 * time.Second

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
		ReconnectWait:        time.Second,
		Logger:               zap.NewNop().Sugar().Named("aster-spot-ws"),
		Debug:                os.Getenv("DEBUG") == "true" || os.Getenv("DEBUG") == "1",
		maxReconnectAttempts: 10,
		pongInterval:         3 * time.Minute,
		subs:                 make(map[string]Subscription),
		handlers:             make(map[string]func([]byte) error),
		ctx:                  ctx,
		cancel:               cancel,
		callbackDispatcher:   newWSCallbackDispatcher(),
	}
}

func (c *WSClient) Connect() error {
	c.Mu.Lock()
	if c.isClosed {
		c.Mu.Unlock()
		return fmt.Errorf("aster spot websocket: client is closed")
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
		return fmt.Errorf("aster spot websocket: client is closed")
	}
	if c.Conn != nil {
		c.Mu.Unlock()
		_ = conn.Close()
		return nil
	}
	c.Conn = conn
	c.connEpoch++
	generation := c.recoveryGeneration
	recovering := c.recovering
	dispatcher := c.callbackDispatcher
	for stream, subscription := range c.subs {
		subscription.sent = false
		c.subs[stream] = subscription
	}
	c.wg.Add(2)
	c.Mu.Unlock()
	if dispatcher != nil {
		dispatcher.activateConnection(generation, conn, recovering)
	}

	go c.readLoop(conn)
	go c.keepAlive(conn)

	if err := c.restoreConnection(conn); err != nil {
		c.Logger.Errorw("failed to restore websocket subscriptions", "error", err)
		c.clearConnection(conn)
		_ = conn.Close()
		return fmt.Errorf("aster spot websocket: restore subscriptions: %w", err)
	}
	c.Mu.Lock()
	if c.Conn == conn {
		c.reconnectAttempt = 0
	}
	c.Mu.Unlock()
	return nil
}

func (c *WSClient) setURL(rawURL string) error {
	if err := validateWebSocketURL(rawURL); err != nil {
		return err
	}
	c.Mu.Lock()
	defer c.Mu.Unlock()
	if c.isClosed {
		return fmt.Errorf("aster spot websocket: client is closed")
	}
	if c.Conn != nil {
		return fmt.Errorf("aster spot websocket: cannot change endpoint while connected")
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
		return fmt.Errorf("aster spot websocket: client is closed")
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
		return fmt.Errorf("aster spot websocket: invalid endpoint")
	}
	return nil
}

func (c *WSClient) setupPingHandlers(conn *websocket.Conn) {
	conn.SetPingHandler(func(appData string) error {
		return c.writeMessageOnConn(conn, websocket.PongMessage, []byte(appData))
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
			err := c.writeMessageOnConn(conn, websocket.PongMessage, nil)
			if err != nil {
				c.Logger.Errorw("failed to send pong", "error", err)
				return
			}
		}
	}
}

func (c *WSClient) readLoop(conn *websocket.Conn) {
	defer c.wg.Done()
	var readErr error
	defer func() {
		c.Mu.Lock()
		owned := c.Conn == conn
		if owned {
			c.Conn = nil
			c.connEpoch++
			for stream, subscription := range c.subs {
				subscription.sent = false
				c.subs[stream] = subscription
			}
		}
		closed := c.isClosed
		c.Mu.Unlock()
		_ = conn.Close()
		if owned && !closed {
			c.beginReconnectOn(conn, readErr)
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
				c.Logger.Errorw("websocket unexpected close", "error", err)
			}
			return
		}
		message = bytes.TrimSpace(message)
		if len(message) > 0 {
			c.Mu.RLock()
			handler := c.Handler
			dispatcher := c.callbackDispatcher
			c.Mu.RUnlock()
			if handler == nil {
				continue
			}
			copied := append([]byte(nil), message...)
			if dispatcher != nil {
				if !dispatcher.enqueueData(conn, wsCallback{run: func() { handler(copied) }}) {
					readErr = fmt.Errorf("aster spot websocket: callback queue overflow")
					return
				}
				continue
			}
			handler(copied)
		}
	}
}

// SetReconnectHooks registers private-stream lifecycle callbacks. Recovered is
// emitted only after a replacement connection has restored its subscriptions.
func (c *WSClient) SetReconnectHooks(started func(error), recovered func()) {
	c.Mu.Lock()
	c.onReconnectStarted = started
	c.onReconnectRecovered = recovered
	c.Mu.Unlock()
}

func (c *WSClient) beginReconnect(err error) {
	c.beginReconnectOn(nil, err)
}

func (c *WSClient) beginReconnectOn(conn *websocket.Conn, err error) {
	if err == nil {
		err = fmt.Errorf("aster spot websocket: connection lost")
	}
	c.reconnectHookMu.Lock()
	defer c.reconnectHookMu.Unlock()
	c.Mu.Lock()
	if c.isClosed {
		c.Mu.Unlock()
		return
	}
	if c.recovering {
		generation := c.recoveryGeneration
		dispatcher := c.callbackDispatcher
		c.Mu.Unlock()
		if dispatcher != nil && conn != nil {
			dispatcher.discardReplacement(generation, conn)
		}
		return
	}
	c.recovering = true
	c.recoveryGeneration++
	generation := c.recoveryGeneration
	handler := c.onReconnectStarted
	dispatcher := c.callbackDispatcher
	c.Mu.Unlock()
	if dispatcher != nil {
		dispatcher.beginGap(generation, func() {
			if handler != nil {
				handler(err)
			}
		})
	} else if handler != nil {
		handler(err)
	}
}

func (c *WSClient) completeReconnect(conn *websocket.Conn) bool {
	c.reconnectHookMu.Lock()
	defer c.reconnectHookMu.Unlock()
	c.Mu.Lock()
	if c.isClosed || c.Conn != conn {
		c.Mu.Unlock()
		return false
	}
	for _, subscription := range c.subs {
		if !subscription.sent {
			c.Mu.Unlock()
			return false
		}
	}
	if !c.recovering {
		c.Mu.Unlock()
		return true
	}
	generation := c.recoveryGeneration
	handler := c.onReconnectRecovered
	dispatcher := c.callbackDispatcher
	if dispatcher != nil && !dispatcher.enqueueRecovered(generation, conn, handler) {
		c.Mu.Unlock()
		return false
	}
	c.recovering = false
	c.Mu.Unlock()
	if dispatcher == nil && handler != nil {
		handler()
	}
	return true
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
		c.Logger.Error("maximum websocket reconnection attempts reached")
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
		c.Logger.Errorw("websocket reconnection failed", "error", err)
		go c.reconnect()
	}
}

func (c *WSClient) restoreConnection(conn *websocket.Conn) error {
	c.subMu.Lock()
	if err := c.resubscribeLocked(conn); err != nil {
		c.subMu.Unlock()
		return err
	}
	c.subMu.Unlock()
	if !c.completeReconnect(conn) {
		return fmt.Errorf("aster spot websocket: replacement connection changed during restoration")
	}
	return nil
}

func (c *WSClient) resubscribe(conn *websocket.Conn) error {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	return c.resubscribeLocked(conn)
}

func (c *WSClient) resubscribeLocked(conn *websocket.Conn) error {
	c.Mu.RLock()
	if c.isClosed || c.Conn != conn {
		c.Mu.RUnlock()
		return fmt.Errorf("aster spot websocket: replacement connection is no longer current")
	}
	streams := make([]string, 0, len(c.subs))
	for stream := range c.subs {
		streams = append(streams, stream)
	}
	c.Mu.RUnlock()
	sort.Strings(streams)
	for _, stream := range streams {
		if err := c.sendSubscriptionLocked(stream, conn); err != nil {
			return err
		}
	}
	return nil
}

func (c *WSClient) WriteJSON(value interface{}) error {
	c.Mu.RLock()
	conn := c.Conn
	closed := c.isClosed
	c.Mu.RUnlock()
	if conn == nil || closed {
		return fmt.Errorf("aster spot websocket: connection not established")
	}
	return c.writeJSONOnConn(conn, value)
}

func (c *WSClient) writeJSONOnConn(conn *websocket.Conn, value interface{}) error {
	if c.Debug {
		c.Logger.Debugw("sending websocket control message", "type", fmt.Sprintf("%T", value))
	}
	return c.writeOnConn(conn, func() error { return conn.WriteJSON(value) })
}

func (c *WSClient) writeMessageOnConn(conn *websocket.Conn, messageType int, data []byte) error {
	return c.writeOnConn(conn, func() error { return conn.WriteMessage(messageType, data) })
}

func (c *WSClient) writeOnConn(conn *websocket.Conn, write func() error) error {
	if conn == nil {
		return fmt.Errorf("aster spot websocket: connection changed before write")
	}
	c.Mu.RLock()
	epoch := c.connEpoch
	current := !c.isClosed && c.Conn == conn
	c.Mu.RUnlock()
	if !current {
		return fmt.Errorf("aster spot websocket: connection changed before write")
	}

	c.WriteMu.Lock()
	defer c.WriteMu.Unlock()
	c.Mu.RLock()
	current = !c.isClosed && c.Conn == conn && c.connEpoch == epoch
	c.Mu.RUnlock()
	if !current {
		return fmt.Errorf("aster spot websocket: connection changed before write")
	}
	if err := conn.SetWriteDeadline(time.Now().Add(websocketWriteTimeout)); err != nil {
		_ = conn.Close()
		return err
	}
	if err := write(); err != nil {
		_ = conn.Close()
		return err
	}
	return nil
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
	c.connEpoch++
	dispatcher := c.callbackDispatcher
	c.Mu.Unlock()

	if dispatcher != nil {
		dispatcher.stop()
	}
	if c.cancel != nil {
		c.cancel()
	}
	if conn != nil {
		_ = conn.Close()
	}
	c.wg.Wait()
}

func (c *WSClient) Subscribe(stream string, handler func([]byte) error) error {
	stream = strings.TrimSpace(stream)
	if stream == "" {
		return fmt.Errorf("aster spot websocket: stream is required")
	}
	c.subMu.Lock()
	defer c.subMu.Unlock()
	c.Mu.Lock()
	subscription, exists := c.subs[stream]
	if !exists {
		subscription.id = wsdispatch.GenerateRandomID()
	}
	if handler != nil {
		subscription.callback = handler
	}
	c.subs[stream] = subscription
	conn := c.Conn
	connected := conn != nil && !c.isClosed
	alreadySent := subscription.sent
	c.Mu.Unlock()

	if alreadySent {
		return nil
	}
	if !connected {
		return fmt.Errorf("aster spot websocket: connection not established")
	}
	return c.sendSubscriptionLocked(stream, conn)
}

func (c *WSClient) sendSubscriptionLocked(stream string, conn *websocket.Conn) error {
	c.Mu.Lock()
	subscription, exists := c.subs[stream]
	if !exists || subscription.sent {
		c.Mu.Unlock()
		return nil
	}
	if c.Conn != conn || conn == nil || c.isClosed {
		c.Mu.Unlock()
		return fmt.Errorf("aster spot websocket: connection changed before subscription")
	}
	subscription.sent = true
	c.subs[stream] = subscription
	c.Mu.Unlock()

	request := map[string]interface{}{
		"method": "SUBSCRIBE",
		"params": []string{stream},
		"id":     subscription.id,
	}
	if err := c.writeJSONOnConn(conn, request); err != nil {
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

func (c *WSClient) Unsubscribe(stream string) error {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	c.Mu.Lock()
	subscription, exists := c.subs[stream]
	if !exists {
		c.Mu.Unlock()
		return nil
	}
	delete(c.subs, stream)
	conn := c.Conn
	connected := conn != nil && !c.isClosed
	c.Mu.Unlock()
	if !connected {
		return fmt.Errorf("aster spot websocket: connection not established")
	}
	return c.writeJSONOnConn(conn, map[string]interface{}{
		"method": "UNSUBSCRIBE",
		"params": []string{stream},
		"id":     subscription.id,
	})
}

func (c *WSClient) clearConnection(conn *websocket.Conn) {
	c.subMu.Lock()
	defer c.subMu.Unlock()
	c.Mu.Lock()
	if c.Conn != conn {
		c.Mu.Unlock()
		return
	}
	c.Conn = nil
	c.connEpoch++
	generation := c.recoveryGeneration
	dispatcher := c.callbackDispatcher
	for stream, subscription := range c.subs {
		subscription.sent = false
		c.subs[stream] = subscription
	}
	c.Mu.Unlock()
	if dispatcher != nil {
		dispatcher.discardReplacement(generation, conn)
	}
}

func (c *WSClient) SetHandler(key string, handler func([]byte) error) {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	if handler == nil {
		delete(c.handlers, key)
		return
	}
	c.handlers[key] = handler
}

func (c *WSClient) CallSubscription(key string, message []byte) {
	c.Mu.RLock()
	subscription := c.subs[key]
	handler := c.handlers[key]
	c.Mu.RUnlock()
	for _, callback := range []func([]byte) error{subscription.callback, handler} {
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
