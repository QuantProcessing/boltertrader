package nado

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/gorilla/websocket"
)

const (
	WsURL              = "wss://gateway.prod.nado.xyz/v1/ws"        // Gateway WS for Executes/Queries
	WsSubscriptionsURL = "wss://gateway.prod.nado.xyz/v1/subscribe" // Subscriptions WS
	PingInterval       = 30 * time.Second
	ReadTimeout        = 60 * time.Second
)

// BaseWsClient handles the underlying WebSocket connection.
type BaseWsClient struct {
	url             string
	conn            *websocket.Conn
	mu              sync.Mutex
	ctx             context.Context
	onMessage       func([]byte)
	isConnected     bool
	reconnectSignal chan struct{}
	Logger          *zap.SugaredLogger
}

func NewBaseWsClient(ctx context.Context, url string, onMessage func([]byte)) *BaseWsClient {
	return &BaseWsClient{
		url:             url,
		onMessage:       onMessage,
		ctx:             ctx,
		reconnectSignal: make(chan struct{}, 1),
		Logger:          zap.NewNop().Sugar().Named("nado-base"),
	}
}

func (c *BaseWsClient) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	// If already connected, return
	if c.conn != nil {
		return nil
	}

	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, c.url, nil)
	if err != nil {
		return fmt.Errorf("dial: %w %v", err, resp)
	}
	c.conn = conn
	c.isConnected = true

	// Start read loop
	go c.readLoop()
	// Start ping loop
	go c.pingLoop()
	return nil
}

func (c *BaseWsClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isConnected
}

func (c *BaseWsClient) Close() {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.isConnected = false
}

func (c *BaseWsClient) SendMessage(v interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.conn == nil {
		return fmt.Errorf("not connected")
	}
	return c.conn.WriteJSON(v)
}

func (c *BaseWsClient) readLoop() {
	defer func() {
		c.mu.Lock()
		if c.conn != nil {
			c.conn.Close()
			c.conn = nil
		}
		c.isConnected = false
		c.mu.Unlock()
	}()

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			if c.conn == nil {
				return
			}
			// c.conn.SetReadDeadline(time.Now().Add(ReadTimeout))
			_, message, err := c.conn.ReadMessage()
			if err != nil {
				if websocket.IsCloseError(err, websocket.CloseNormalClosure, websocket.CloseGoingAway) {
					c.Logger.Debug("Websocket closed normally")
					return
				}
				// Ignore "use of closed network connection" error which happens on Close()
				if err.Error() != "" && (strings.Contains(err.Error(), "use of closed network connection") || strings.Contains(err.Error(), "closed")) {
					c.Logger.Debug("Websocket connection closed")
					return
				}
				c.Logger.Errorw("Error reading message", "error", err)
				return
			}

			if c.onMessage != nil {
				c.onMessage(message)
			}
		}
	}
}

func (c *BaseWsClient) pingLoop() {
	ticker := time.NewTicker(PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.mu.Lock()
			if c.conn != nil {
				// Send Ping frame
				err := c.conn.WriteControl(websocket.PingMessage, []byte{}, time.Now().Add(5*time.Second))
				if err != nil {
					c.Logger.Errorw("Error sending ping", "error", err)
				}
				c.Logger.Debug("Sent ping")
			} else {
				c.mu.Unlock()
				return
			}
			c.mu.Unlock()
		}
	}
}
