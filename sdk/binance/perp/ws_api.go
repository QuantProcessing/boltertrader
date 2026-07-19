package perp

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

type WsAPIClient struct {
	URL             string
	Conn            *websocket.Conn
	Mu              sync.Mutex
	WriteMu         sync.Mutex
	PendingRequests map[string]chan []byte
	PendingMu       sync.Mutex
	Done            chan struct{}
	ReconnectWait   time.Duration
	RequestTimeout  time.Duration
	Logger          *zap.SugaredLogger
	Debug           bool
	isClosed        bool
	ctx             context.Context
}

func NewWsAPIClient(ctx context.Context) *WsAPIClient {
	return NewWsAPIClientWithEndpointProfile(ctx, USDMMProductionEndpoints)
}

func NewDemoWsAPIClient(ctx context.Context) *WsAPIClient {
	return NewWsAPIClientWithEndpointProfile(ctx, USDMMDemoEndpoints)
}

func NewWsAPIClientWithEndpointProfile(ctx context.Context, profile EndpointProfile) *WsAPIClient {
	return newWsAPIClient(ctx, endpointOrDefault(profile.WSAPIBaseURL, WSAPIBaseURL))
}

func newWsAPIClient(ctx context.Context, url string) *WsAPIClient {
	return &WsAPIClient{
		URL:             url,
		PendingRequests: make(map[string]chan []byte),
		Done:            make(chan struct{}),
		ReconnectWait:   1 * time.Second,
		RequestTimeout:  10 * time.Second,
		Logger:          zap.NewNop().Sugar().Named("binance-perp-api"),
		Debug:           os.Getenv("DEBUG") == "true" || os.Getenv("DEBUG") == "1",
		ctx:             ctx,
	}
}

func (c *WsAPIClient) WithURL(url string) *WsAPIClient {
	c.URL = url
	return c
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
			c.Logger.Warnw("Invalid proxy URL")
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
	}
}

func (c *WsAPIClient) handleMessage(message []byte) {
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

	sent, err := c.writeJSON(req)
	if err != nil {
		if sent {
			return nil, fmt.Errorf("%w: %v", ErrWSOutcomeUnknown, err)
		}
		return nil, err
	}

	select {
	case resp := <-ch:
		return resp, nil
	case <-time.After(c.requestTimeout()):
		return nil, fmt.Errorf("%w", ErrWSOutcomeUnknown)
	}
}

func (c *WsAPIClient) requestTimeout() time.Duration {
	if c.RequestTimeout > 0 {
		return c.RequestTimeout
	}
	return 10 * time.Second
}

func (c *WsAPIClient) writeJSON(v interface{}) (bool, error) {
	c.WriteMu.Lock()
	defer c.WriteMu.Unlock()

	c.Mu.Lock()
	conn := c.Conn
	c.Mu.Unlock()

	if conn == nil {
		return false, fmt.Errorf("websocket not connected")
	}

	if c.Debug {
		c.Logger.Debugw("Sending", "request_type", wsDebugRequestSummary(v))
	}

	return true, conn.WriteJSON(v)
}

func wsDebugRequestSummary(v interface{}) string {
	return fmt.Sprintf("%T", v)
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

func (c *WsAPIClient) IsConnected() bool {
	c.Mu.Lock()
	defer c.Mu.Unlock()
	return c.Conn != nil && !c.isClosed
}
