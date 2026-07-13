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
	PingInterval = 30 * time.Second
	ReadTimeout  = 60 * time.Second
)

// baseWsClient handles the underlying WebSocket connection for profile-owned clients.
type baseWsClient struct {
	url         string
	conn        *websocket.Conn
	mu          sync.Mutex
	connectMu   sync.Mutex
	writeMu     sync.Mutex
	ctx         context.Context
	onMessage   func([]byte)
	isConnected bool
	closed      bool
	Logger      *zap.SugaredLogger
}

func newBaseWsClient(ctx context.Context, url string, onMessage func([]byte)) *baseWsClient {
	return &baseWsClient{
		url:       url,
		onMessage: onMessage,
		ctx:       ctx,
		Logger:    zap.NewNop().Sugar().Named("nado-base"),
	}
}

func (c *baseWsClient) Connect() error {
	return c.connect(true)
}

func (c *baseWsClient) connect(explicit bool) error {
	c.connectMu.Lock()
	defer c.connectMu.Unlock()

	c.mu.Lock()
	if c.conn != nil {
		c.mu.Unlock()
		return nil
	}
	if !explicit && c.closed {
		c.mu.Unlock()
		return fmt.Errorf("nado websocket client is closed")
	}
	if explicit {
		c.closed = false
	}
	endpoint := c.url
	c.mu.Unlock()

	ctx, cancel := context.WithTimeout(c.ctx, 10*time.Second)
	defer cancel()
	conn, resp, err := websocket.DefaultDialer.DialContext(ctx, endpoint, nil)
	if err != nil {
		return fmt.Errorf("dial: %w %v", err, resp)
	}

	c.mu.Lock()
	if c.closed || c.ctx.Err() != nil {
		c.mu.Unlock()
		_ = conn.Close()
		return fmt.Errorf("nado websocket client is closed")
	}
	c.conn = conn
	c.isConnected = true
	c.mu.Unlock()

	go c.readLoop(conn)
	go c.pingLoop(conn)
	return nil
}

func (c *baseWsClient) IsConnected() bool {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.isConnected
}

func (c *baseWsClient) Close() {
	c.mu.Lock()
	c.closed = true
	conn := c.conn
	c.conn = nil
	c.isConnected = false
	c.mu.Unlock()
	if conn != nil {
		_ = conn.Close()
	}
}

func (c *baseWsClient) SendMessage(v interface{}) error {
	c.mu.Lock()
	conn := c.conn
	c.mu.Unlock()
	if conn == nil {
		return fmt.Errorf("not connected")
	}
	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteJSON(v)
}

func (c *baseWsClient) readLoop(conn *websocket.Conn) {
	defer func() {
		c.mu.Lock()
		if c.conn == conn {
			c.conn = nil
			c.isConnected = false
		}
		reconnect := !c.closed && c.ctx.Err() == nil
		c.mu.Unlock()
		_ = conn.Close()
		if reconnect {
			go c.reconnect()
		}
	}()

	for {
		select {
		case <-c.ctx.Done():
			return
		default:
			_, message, err := conn.ReadMessage()
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

			onMessage := c.onMessage
			if onMessage != nil {
				onMessage(message)
			}
		}
	}
}

func (c *baseWsClient) pingLoop(conn *websocket.Conn) {
	ticker := time.NewTicker(PingInterval)
	defer ticker.Stop()
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-ticker.C:
			c.mu.Lock()
			active := c.conn == conn && !c.closed
			c.mu.Unlock()
			if !active {
				return
			}
			c.writeMu.Lock()
			err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(5*time.Second))
			c.writeMu.Unlock()
			if err != nil {
				c.Logger.Errorw("Error sending ping", "error", err)
				_ = conn.Close()
				return
			}
		}
	}
}

func (c *baseWsClient) reconnect() {
	backoff := time.Second
	for {
		select {
		case <-c.ctx.Done():
			return
		case <-time.After(backoff):
			if err := c.connect(false); err == nil {
				return
			}
			c.mu.Lock()
			closed := c.closed
			c.mu.Unlock()
			if closed {
				return
			}
			if backoff < 30*time.Second {
				backoff *= 2
				if backoff > 30*time.Second {
					backoff = 30 * time.Second
				}
			}
		}
	}
}
