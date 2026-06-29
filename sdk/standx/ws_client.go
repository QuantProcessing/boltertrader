package standx

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"github.com/gorilla/websocket"
	"go.uber.org/zap"
)

const (
	MarketStreamURL = "wss://perps.standx.com/ws-stream/v1"
	APIStreamURL    = "wss://perps.standx.com/ws-api/v1"
)

// WSClient is the base WebSocket client.
type WSClient struct {
	url    string
	conn   *websocket.Conn
	mu     sync.Mutex
	ctx    context.Context
	cancel context.CancelFunc
	logger *zap.SugaredLogger

	HandleMsg func([]byte)

	// Auth
	signer *Signer
	token  string

	// Connection State
	isConnected bool

	// Identify if this is Market or Order client (behavior might differ slightly)
	isOrderClient bool

	// Reconnect Hook
	OnReconnect func() error
}

// WsClient is kept as a compatibility alias for older callers.
type WsClient = WSClient

func NewWSClient(ctx context.Context, url string, logger *zap.SugaredLogger) *WSClient {
	c := &WSClient{
		url:    url,
		logger: logger,
	}
	// Create a child context for lifecycle management
	c.ctx, c.cancel = context.WithCancel(ctx)
	return c
}

func NewWsClient(ctx context.Context, url string, logger *zap.SugaredLogger) *WSClient {
	return NewWSClient(ctx, url, logger)
}

func (c *WSClient) SetCredentials(signer *Signer, token string) {
	c.signer = signer
	c.token = token
}

func (c *WSClient) Connect() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.isConnected {
		return nil
	}

	dialer := websocket.Dialer{
		Proxy:            http.ProxyFromEnvironment,
		HandshakeTimeout: 10 * time.Second,
	}

	conn, _, err := dialer.Dial(c.url, nil)
	if err != nil {
		return fmt.Errorf("dial failed: %w", err)
	}

	// 1. Setup Read Deadline (Server sends Ping every 10s)
	conn.SetReadLimit(512 * 1024)
	// Give a bit of buffer (e.g. 20s)
	if err := conn.SetReadDeadline(time.Now().Add(20 * time.Second)); err != nil {
		return fmt.Errorf("failed to set read deadline: %w", err)
	}

	// 2. Setup Ping Handler (Respond with Pong + Extend Deadline)
	// Gorilla default PingHandler already sends Pong. We just need to wrap it to extend deadline.
	defaultPingHandler := conn.PingHandler()
	conn.SetPingHandler(func(appData string) error {
		// Extend deadline
		if err := conn.SetReadDeadline(time.Now().Add(20 * time.Second)); err != nil {
			c.logger.Error("failed to reset read deadline on ping", zap.Error(err))
			// If we fail to set deadline, should we error out?
			// Usually yes, to force reconnect.
			return err
		}
		// Call default handler to send Pong
		return defaultPingHandler(appData)
	})

	c.conn = conn
	c.isConnected = true
	c.logger.Info("Connected to ", c.url)

	go c.readLoop()
	go c.pingLoop()

	return nil
}

func (c *WSClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isConnected
}

func (c *WSClient) Close() {
	c.cancel()
	c.mu.Lock()
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.isConnected = false
	c.mu.Unlock()
}

func (c *WSClient) readLoop() {
	// defer c.Close() // Do not close on exit, as it cancels context for the *new* connection if we are reconnecting

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			c.mu.Lock()
			conn := c.conn
			c.mu.Unlock()

			if conn == nil {
				return
			}

			_, msg, err := conn.ReadMessage()
			if err != nil {
				c.logger.Error("WS read error: ", err)
				c.reconnect()
				return
			}
			if c.HandleMsg != nil {
				c.HandleMsg(msg)
			}
		}
	}
}

func (c *WSClient) reconnect() {
	c.mu.Lock()
	c.isConnected = false
	if c.conn != nil {
		c.conn.Close()
		c.conn = nil
	}
	c.mu.Unlock()

	ticker := time.NewTicker(2 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.logger.Info("Reconnecting to ", c.url)
			if err := c.Connect(); err != nil {
				c.logger.Error("Reconnection failed: ", err)
				continue
			}

			// Reconnection successful
			if c.OnReconnect != nil {
				c.logger.Info("Executing OnReconnect hook...")
				if err := c.OnReconnect(); err != nil {
					c.logger.Error("OnReconnect hook failed: ", err)
					// If hook fails (e.g. auth failed), what to do?
					// Close and retry loop? Or assume partial success?
					// For now, log.
				} else {
					c.logger.Info("OnReconnect hook success")
				}
			}
			return
		}
	}
}

func (c *WSClient) pingLoop() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.mu.Lock()
			if c.conn != nil && c.isConnected {
				err := c.conn.WriteMessage(websocket.PingMessage, nil)
				if err != nil {
					c.logger.Warn("Failed to send ping: ", err)
				} else {
					// c.logger.Debug("Ping sent")
				}
			}
			c.mu.Unlock()
		}
	}
}

func (c *WSClient) WriteJSON(v interface{}) error {
	c.mu.Lock()
	defer c.mu.Unlock()
	if !c.isConnected || c.conn == nil {
		return fmt.Errorf("not connected")
	}
	return c.conn.WriteJSON(v)
}

func (c *WSClient) SetHandler(handler func([]byte)) {
	c.HandleMsg = handler
}
