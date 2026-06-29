package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type PrivateWSClient struct {
	url       string
	apiKey    string
	secretKey string

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.RWMutex
	writeMu  sync.Mutex
	conn     *websocket.Conn
	closed   bool
	handlers map[string]func(json.RawMessage)
	authCh   chan error
}

type WSOrderMessage struct {
	Topic string        `json:"topic"`
	Data  []OrderRecord `json:"data"`
}

type WSExecutionMessage struct {
	Topic string            `json:"topic"`
	Data  []ExecutionRecord `json:"data"`
}

type WSPositionMessage struct {
	Topic string           `json:"topic"`
	Data  []PositionRecord `json:"data"`
}

func NewPrivateWSClient() *PrivateWSClient {
	ctx, cancel := context.WithCancel(context.Background())
	return &PrivateWSClient{
		url:      "wss://stream.bybit.com/v5/private",
		ctx:      ctx,
		cancel:   cancel,
		handlers: make(map[string]func(json.RawMessage)),
	}
}

func (c *PrivateWSClient) WithCredentials(apiKey, secretKey string) *PrivateWSClient {
	c.apiKey = apiKey
	c.secretKey = secretKey
	return c
}

type wsAuthRequest struct {
	ReqID string `json:"req_id,omitempty"`
	Op    string `json:"op"`
	Args  []any  `json:"args"`
}

type wsCommandRequest struct {
	ReqID string   `json:"req_id,omitempty"`
	Op    string   `json:"op"`
	Args  []string `json:"args"`
}

func DecodeOrderMessage(payload []byte) (*WSOrderMessage, error) {
	var msg WSOrderMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func DecodeExecutionMessage(payload []byte) (*WSExecutionMessage, error) {
	var msg WSExecutionMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func DecodePositionMessage(payload []byte) (*WSPositionMessage, error) {
	var msg WSPositionMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func (c *PrivateWSClient) Subscribe(ctx context.Context, topic string, handler func(json.RawMessage)) error {
	if err := c.Connect(ctx); err != nil {
		return err
	}

	c.mu.Lock()
	c.handlers[topic] = handler
	c.mu.Unlock()

	if err := c.writeJSON(wsCommandRequest{Op: "subscribe", Args: []string{topic}}); err != nil {
		c.mu.Lock()
		delete(c.handlers, topic)
		c.mu.Unlock()
		return err
	}
	return nil
}

func (c *PrivateWSClient) Unsubscribe(ctx context.Context, topic string) error {
	_ = ctx
	c.mu.Lock()
	delete(c.handlers, topic)
	c.mu.Unlock()

	if err := c.writeJSON(wsCommandRequest{Op: "unsubscribe", Args: []string{topic}}); err != nil && err.Error() != "bybit private ws: not connected" {
		return err
	}
	return nil
}

func (c *PrivateWSClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.closed = true
	if c.cancel != nil {
		c.cancel()
	}
	if c.conn == nil {
		return nil
	}

	_ = c.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(5*time.Second))
	err := c.conn.Close()
	c.conn = nil
	return err
}

func (c *PrivateWSClient) Connect(ctx context.Context) error {
	c.mu.Lock()
	if c.closed {
		c.mu.Unlock()
		return fmt.Errorf("bybit private ws: client closed")
	}
	if c.conn != nil {
		c.mu.Unlock()
		return nil
	}

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.url, nil)
	if err != nil {
		c.mu.Unlock()
		return err
	}

	c.conn = conn
	c.authCh = make(chan error, 1)
	go c.readLoop(conn)
	go c.pingLoop(conn)

	if err := c.sendAuthLocked(); err != nil {
		_ = conn.Close()
		c.conn = nil
		c.mu.Unlock()
		return err
	}

	authCh := c.authCh
	c.mu.Unlock()

	select {
	case err := <-authCh:
		return err
	case <-time.After(5 * time.Second):
		return fmt.Errorf("bybit private ws: auth timeout")
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (c *PrivateWSClient) sendAuthLocked() error {
	expires := time.Now().Add(10 * time.Second).UnixMilli()
	signature := sign(c.secretKey, fmt.Sprintf("GET/realtime%d", expires))
	return c.writeJSONLocked(wsAuthRequest{
		Op:   "auth",
		Args: []any{c.apiKey, expires, signature},
	})
}

func (c *PrivateWSClient) writeJSON(v any) error {
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()
	if conn == nil {
		return fmt.Errorf("bybit private ws: not connected")
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteJSON(v)
}

func (c *PrivateWSClient) writeJSONLocked(v any) error {
	if c.conn == nil {
		return fmt.Errorf("bybit private ws: not connected")
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return c.conn.WriteJSON(v)
}

func (c *PrivateWSClient) pingLoop(conn *websocket.Conn) {
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for range ticker.C {
		c.mu.RLock()
		active := c.conn == conn
		c.mu.RUnlock()
		if !active {
			return
		}

		c.writeMu.Lock()
		err := conn.WriteJSON(map[string]string{"op": "ping"})
		c.writeMu.Unlock()
		if err != nil {
			return
		}
	}
}

func (c *PrivateWSClient) readLoop(conn *websocket.Conn) {
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			c.mu.RLock()
			authCh := c.authCh
			c.mu.RUnlock()
			if authCh != nil {
				select {
				case authCh <- err:
				default:
				}
			}
			c.mu.Lock()
			if c.conn == conn {
				c.conn = nil
			}
			closed := c.closed
			c.mu.Unlock()
			if !closed {
				go c.reconnect()
			}
			return
		}

		var authResp struct {
			Success bool   `json:"success"`
			RetMsg  string `json:"ret_msg"`
			Op      string `json:"op"`
			Topic   string `json:"topic"`
		}
		if err := json.Unmarshal(payload, &authResp); err != nil {
			continue
		}

		if authResp.Op == "auth" {
			c.mu.RLock()
			authCh := c.authCh
			c.mu.RUnlock()
			if authCh != nil {
				if authResp.Success {
					authCh <- nil
				} else {
					authCh <- fmt.Errorf("bybit private ws: auth failed: %s", authResp.RetMsg)
				}
			}
			continue
		}

		if authResp.Topic == "" {
			continue
		}

		c.mu.RLock()
		handler := c.handlers[authResp.Topic]
		c.mu.RUnlock()
		if handler != nil {
			handler(payload)
		}
	}
}

func (c *PrivateWSClient) reconnect() {
	select {
	case <-c.ctx.Done():
		return
	case <-time.After(time.Second):
	}

	if err := c.Connect(c.ctx); err != nil {
		go c.reconnect()
		return
	}

	c.mu.RLock()
	topics := make([]string, 0, len(c.handlers))
	for topic := range c.handlers {
		topics = append(topics, topic)
	}
	c.mu.RUnlock()

	for _, topic := range topics {
		if err := c.writeJSON(wsCommandRequest{Op: "subscribe", Args: []string{topic}}); err != nil {
			go c.reconnect()
			return
		}
	}
}
