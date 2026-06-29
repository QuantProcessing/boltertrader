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
	ctx             context.Context

	// eventHandler handles pushed events (messages without an "id" field),
	// e.g. user data stream events like executionReport.
	eventHandler  func([]byte)
	postReconnect func()
	eventMu       sync.Mutex
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

func (c *WsAPIClient) Connect() error {
	c.Mu.Lock()
	defer c.Mu.Unlock()

	// Reset isClosed to allow restart
	c.isClosed = false

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
		} else {
			c.Logger.Warnw("Invalid proxy URL", "url", proxyURL, "error", err)
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
	c.Done = make(chan struct{})

	go c.readLoop()

	return nil
}

// IsConnected checks if the WebSocket connection is established
func (c *WsAPIClient) IsConnected() bool {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	return c.Conn != nil && !c.isClosed
}

func (c *WsAPIClient) readLoop() {
	defer func() {
		// Clean up connection
		c.Mu.Lock()
		if c.Conn != nil {
			c.Conn.Close()
			c.Conn = nil
		}
		c.Mu.Unlock()

		// Trigger reconnect if not intentionally closed
		c.Mu.Lock()
		closed := c.isClosed
		c.Mu.Unlock()

		if !closed {
			c.reconnect()
		}
	}()

	for {
		select {
		case <-c.Done:
			return
		default:
			c.Mu.Lock()
			conn := c.Conn
			c.Mu.Unlock()
			if conn == nil {
				return
			}
			_, message, err := conn.ReadMessage()
			if err != nil {
				if websocket.IsUnexpectedCloseError(err, websocket.CloseGoingAway, websocket.CloseAbnormalClosure) {
					c.Logger.Errorw("websocket read error", "error", err)
				}
				return
			}
			c.handleMessage(message)
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
	if err := c.Connect(); err != nil {
		c.Logger.Errorw("reconnect failed", "error", err)
		go c.reconnect()
		return
	}
	c.eventMu.Lock()
	handler := c.postReconnect
	c.eventMu.Unlock()
	if handler != nil {
		go handler()
	}
}

func (c *WsAPIClient) handleMessage(message []byte) {
	if c.Debug {
		c.Logger.Debugw("Received", "msg", string(message))
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
		return
	}

	// No "id" field — this is a pushed event (e.g. user data stream)
	c.eventMu.Lock()
	handler := c.eventHandler
	c.eventMu.Unlock()
	if handler != nil {
		handler(message)
	}
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
		reqBytes, _ := json.Marshal(v)
		c.Logger.Debugw("Sending", "msg", string(reqBytes))
	}

	return conn.WriteJSON(v)
}

func (c *WsAPIClient) Close() {
	c.Mu.Lock()
	defer c.Mu.Unlock()

	c.isClosed = true

	if c.Conn != nil {
		c.Conn.Close()
		c.Conn = nil
	}

	select {
	case <-c.Done:
	default:
		close(c.Done)
	}
}
