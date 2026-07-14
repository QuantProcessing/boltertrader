package spot

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/internal/wsdispatch"

	"go.uber.org/zap"

	"github.com/gorilla/websocket"
)

const (
	WSAPIBaseURL = "wss://ws-api.binance.com:443/ws-api/v3"
)

type WsAPIClient struct {
	URL             string
	Conn            *websocket.Conn
	Mu              sync.Mutex
	WriteMu         sync.Mutex
	PendingRequests map[string]chan []byte
	PendingMu       sync.Mutex
	Done            chan struct{}
	ReconnectWait   time.Duration
	Logger          *zap.SugaredLogger
	Debug           bool
	isClosed        bool
	connEpoch       uint64
	ctx             context.Context

	// eventHandler handles pushed events (messages without an "id" field),
	// e.g. user data stream events like executionReport.
	eventHandler          func([]byte)
	postReconnect         func()
	onDisconnect          func(error)
	beforeReadLoopCleanup func()
	afterReadLoopCleanup  func()
	beforePostReconnect   func()
	beforeDoneClose       func()
	eventMu               sync.Mutex
	eventDispatcher       *wsdispatch.Dispatcher
}

func NewWsAPIClient(ctx context.Context) *WsAPIClient {
	return &WsAPIClient{
		URL:             WSAPIBaseURL,
		PendingRequests: make(map[string]chan []byte),
		Done:            make(chan struct{}),
		ReconnectWait:   1 * time.Second,
		Logger:          zap.NewNop().Sugar().Named("binance-spot-api"),
		Debug:           os.Getenv("DEBUG") == "true" || os.Getenv("DEBUG") == "1",
		ctx:             ctx,
		eventDispatcher: wsdispatch.NewBoundedDispatcher(wsdispatch.DefaultBufferLimit),
	}
}

func (c *WsAPIClient) WithURL(url string) *WsAPIClient {
	c.URL = url
	return c
}

// SetEventHandler sets the callback for pushed events (messages without "id").
func (c *WsAPIClient) SetEventHandler(handler func([]byte)) {
	c.eventMu.Lock()
	defer c.eventMu.Unlock()
	c.eventHandler = handler
}

func (c *WsAPIClient) SetPostReconnect(handler func()) {
	c.eventMu.Lock()
	defer c.eventMu.Unlock()
	c.postReconnect = handler
}

// SetOnDisconnect sets a callback for unexpected transport loss. Intentional
// Close calls do not invoke it.
func (c *WsAPIClient) SetOnDisconnect(handler func(error)) {
	c.eventMu.Lock()
	defer c.eventMu.Unlock()
	c.onDisconnect = handler
}

func (c *WsAPIClient) Connect() error {
	_, _, err := c.connect(true)
	return err
}

func (c *WsAPIClient) connect(allowRestart bool) (uint64, bool, error) {
	c.Mu.Lock()
	defer c.Mu.Unlock()

	if c.isClosed {
		if !allowRestart {
			return 0, false, fmt.Errorf("binance spot ws-api: client is closed")
		}
		// Public Connect explicitly permits restart after Close. Internal
		// reconnect attempts never reopen an intentionally closed client.
		c.isClosed = false
	}

	if c.Conn != nil {
		return c.connEpoch, false, nil
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
		} else {
			c.Logger.Warnw("Invalid proxy URL")
		}
	}

	// Use internal 10 second timeout
	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	conn, _, err := dialer.DialContext(ctx, c.URL, nil)
	if err != nil {
		return 0, false, err
	}

	c.Conn = conn
	c.Done = make(chan struct{})
	c.connEpoch++
	done := c.Done
	epoch := c.connEpoch

	go c.readLoop(conn, done)

	return epoch, true, nil
}

// IsConnected checks if the WebSocket connection is established
func (c *WsAPIClient) IsConnected() bool {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	return c.Conn != nil && !c.isClosed
}

func (c *WsAPIClient) connectionEpochSnapshot() (uint64, bool) {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	return c.connEpoch, c.Conn != nil && !c.isClosed
}

func (c *WsAPIClient) connectionEpochCurrent(epoch uint64) bool {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	return c.connEpoch == epoch && c.Conn != nil && !c.isClosed
}

func (c *WsAPIClient) readLoop(conn *websocket.Conn, done <-chan struct{}) {
	var readErr error
	defer func() {
		if c.beforeReadLoopCleanup != nil {
			c.beforeReadLoopCleanup()
		}
		_ = conn.Close()
		c.Mu.Lock()
		owned := c.Conn == conn
		if owned {
			c.Conn = nil
			c.connEpoch++
		}
		closed := c.isClosed
		c.Mu.Unlock()
		if c.afterReadLoopCleanup != nil {
			c.afterReadLoopCleanup()
		}

		if owned && !closed {
			if readErr == nil {
				readErr = fmt.Errorf("binance spot ws-api: connection lost")
			}
			c.eventMu.Lock()
			handler := c.onDisconnect
			c.eventMu.Unlock()
			if handler != nil {
				handler(readErr)
			}
			c.reconnect()
		}
	}()

	for {
		select {
		case <-done:
			return
		default:
			_, message, err := conn.ReadMessage()
			if err != nil {
				readErr = err
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					c.Logger.Errorw("websocket read error", "error", err)
				}
				return
			}
			if err := c.handleMessageChecked(message); err != nil {
				readErr = fmt.Errorf("binance spot ws-api event dispatch: %w", err)
				c.resetPushedEvents()
				return
			}
		}
	}
}

func (c *WsAPIClient) reconnect() {
	// Check if already closed
	c.Mu.Lock()
	if c.isClosed {
		c.Mu.Unlock()
		return
	}
	c.Mu.Unlock()

	time.Sleep(c.ReconnectWait)

	c.Logger.Info("reconnecting...")
	// Use background context for reconnection attempt
	epoch, didConnect, err := c.connect(false)
	if err != nil {
		c.Logger.Errorw("reconnect failed", "error", err)
		c.Mu.Lock()
		closed := c.isClosed
		c.Mu.Unlock()
		if !closed {
			go c.reconnect()
		}
		return
	}
	if !didConnect {
		return
	}
	if c.beforePostReconnect != nil {
		c.beforePostReconnect()
	}
	if !c.connectionEpochCurrent(epoch) {
		return
	}
	c.eventMu.Lock()
	handler := c.postReconnect
	c.eventMu.Unlock()
	if handler != nil {
		go func() {
			if c.connectionEpochCurrent(epoch) {
				handler()
			}
		}()
	}
}

func (c *WsAPIClient) handleMessage(message []byte) {
	_ = c.handleMessageChecked(message)
}

func (c *WsAPIClient) handleMessageChecked(message []byte) error {
	if c.Debug {
		c.Logger.Debugw("Received", "bytes", len(message))
	}

	var resp struct {
		ID interface{} `json:"id"`
	}
	if err := json.Unmarshal(message, &resp); err == nil && resp.ID != nil {
		idStr := fmt.Sprintf("%v", resp.ID)
		c.PendingMu.Lock()
		if ch, ok := c.PendingRequests[idStr]; ok {
			ch <- message
		}
		c.PendingMu.Unlock()
		return nil
	}

	// No "id" field — this is a pushed event (e.g. user data stream)
	c.eventMu.Lock()
	handler := c.eventHandler
	dispatcher := c.eventDispatcherLocked()
	c.eventMu.Unlock()
	if handler != nil {
		msg := append([]byte(nil), message...)
		return dispatcher.DispatchChecked(func() {
			handler(msg)
		})
	}
	return nil
}

func (c *WsAPIClient) eventDispatcherLocked() *wsdispatch.Dispatcher {
	if c.eventDispatcher == nil {
		c.eventDispatcher = wsdispatch.NewBoundedDispatcher(wsdispatch.DefaultBufferLimit)
	}
	return c.eventDispatcher
}

func (c *WsAPIClient) pausePushedEvents() {
	c.eventMu.Lock()
	dispatcher := c.eventDispatcherLocked()
	c.eventMu.Unlock()
	dispatcher.Pause()
}

func (c *WsAPIClient) resumePushedEvents(beforeDrain func()) {
	c.eventMu.Lock()
	dispatcher := c.eventDispatcherLocked()
	c.eventMu.Unlock()
	dispatcher.Resume(beforeDrain)
}

func (c *WsAPIClient) resetPushedEvents() {
	c.eventMu.Lock()
	dispatcher := c.eventDispatcherLocked()
	c.eventMu.Unlock()
	dispatcher.Reset()
}

func (c *WsAPIClient) SendRequest(id string, req interface{}) ([]byte, error) {
	ch := make(chan []byte, 1)
	c.PendingMu.Lock()
	c.PendingRequests[id] = ch
	c.PendingMu.Unlock()

	defer func() {
		c.PendingMu.Lock()
		delete(c.PendingRequests, id)
		c.PendingMu.Unlock()
	}()

	if err := c.writeJSON(req); err != nil {
		return nil, err
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(10 * time.Second):
		return nil, fmt.Errorf("request timeout")
	}
}

func (c *WsAPIClient) writeJSON(v interface{}) error {
	c.WriteMu.Lock()
	defer c.WriteMu.Unlock()

	c.Mu.Lock()
	conn := c.Conn
	c.Mu.Unlock()

	if conn == nil {
		return fmt.Errorf("websocket not connected")
	}

	if c.Debug {
		c.Logger.Debugw("Sending", "request_type", wsDebugRequestSummary(v))
	}

	return conn.WriteJSON(v)
}

func wsDebugRequestSummary(v interface{}) string {
	return fmt.Sprintf("%T", v)
}

func (c *WsAPIClient) Close() {
	c.resetPushedEvents()
	c.Mu.Lock()
	c.isClosed = true
	conn := c.Conn
	if conn != nil {
		c.Conn = nil
		c.connEpoch++
	}
	done := c.Done
	c.Done = nil
	c.Mu.Unlock()

	if conn != nil {
		_ = conn.Close()
	}
	if done != nil {
		select {
		case <-done:
		default:
			if c.beforeDoneClose != nil {
				c.beforeDoneClose()
			}
			close(done)
		}
	}
}
