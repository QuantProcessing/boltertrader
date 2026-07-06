package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const (
	publicWSURLSpot    = "wss://stream.bybit.com/v5/public/spot"
	publicWSURLLinear  = "wss://stream.bybit.com/v5/public/linear"
	publicWSURLInverse = "wss://stream.bybit.com/v5/public/inverse"
	publicWSURLOption  = "wss://stream.bybit.com/v5/public/option"
)

type PublicWSClient struct {
	url string

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.RWMutex
	writeMu  sync.Mutex
	conn     *websocket.Conn
	closed   bool
	handlers map[string]func(json.RawMessage)
}

type wsRequest struct {
	Op   string   `json:"op"`
	Args []string `json:"args"`
}

type WSEnvelope struct {
	Topic string          `json:"topic"`
	Type  string          `json:"type"`
	TS    int64           `json:"ts"`
	Data  json.RawMessage `json:"data"`
}

type WSOrderBookData struct {
	Symbol   string           `json:"s"`
	Bids     [][]NumberString `json:"b"`
	Asks     [][]NumberString `json:"a"`
	UpdateID int64            `json:"u"`
	Seq      int64            `json:"seq"`
	CTS      int64            `json:"cts"`
}

type WSOrderBookMessage struct {
	Topic string          `json:"topic"`
	Type  string          `json:"type"`
	TS    int64           `json:"ts"`
	Data  WSOrderBookData `json:"data"`
}

func NewPublicWSClient(category string) *PublicWSClient {
	ctx, cancel := context.WithCancel(context.Background())
	return &PublicWSClient{
		url:      publicWSURL(category),
		ctx:      ctx,
		cancel:   cancel,
		handlers: make(map[string]func(json.RawMessage)),
	}
}

func publicWSURL(category string) string {
	switch category {
	case "spot":
		return publicWSURLSpot
	case "inverse":
		return publicWSURLInverse
	case "option":
		return publicWSURLOption
	default:
		return publicWSURLLinear
	}
}

func DecodeOrderBookMessage(payload []byte) (*WSOrderBookMessage, error) {
	var msg WSOrderBookMessage
	if err := json.Unmarshal(payload, &msg); err != nil {
		return nil, err
	}
	return &msg, nil
}

func (c *PublicWSClient) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return fmt.Errorf("bybit public ws: client closed")
	}
	if c.conn != nil {
		return nil
	}

	conn, _, err := websocketDialerFromEnvironment().DialContext(ctx, c.url, nil)
	if err != nil {
		return err
	}

	c.conn = conn
	go c.readLoop(conn)
	go c.pingLoop(conn)
	return nil
}

func (c *PublicWSClient) Subscribe(ctx context.Context, topic string, handler func(json.RawMessage)) error {
	if err := c.Connect(ctx); err != nil {
		return err
	}

	c.mu.Lock()
	c.handlers[topic] = handler
	c.mu.Unlock()

	if err := c.writeJSON(wsRequest{Op: "subscribe", Args: []string{topic}}); err != nil {
		c.mu.Lock()
		delete(c.handlers, topic)
		c.mu.Unlock()
		return err
	}
	return nil
}

func (c *PublicWSClient) Unsubscribe(ctx context.Context, topic string) error {
	_ = ctx

	c.mu.Lock()
	delete(c.handlers, topic)
	c.mu.Unlock()

	if err := c.writeJSON(wsRequest{Op: "unsubscribe", Args: []string{topic}}); err != nil && err.Error() != "bybit public ws: not connected" {
		return err
	}
	return nil
}

func (c *PublicWSClient) Close() error {
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

func (c *PublicWSClient) writeJSON(v any) error {
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()
	if conn == nil {
		return fmt.Errorf("bybit public ws: not connected")
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteJSON(v)
}

func (c *PublicWSClient) pingLoop(conn *websocket.Conn) {
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

func (c *PublicWSClient) readLoop(conn *websocket.Conn) {
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
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

		var env WSEnvelope
		if err := json.Unmarshal(payload, &env); err != nil {
			continue
		}
		if env.Topic == "" {
			continue
		}

		c.mu.RLock()
		handler := c.handlers[env.Topic]
		c.mu.RUnlock()
		if handler != nil {
			handler(payload)
		}
	}
}

func (c *PublicWSClient) reconnect() {
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
		if err := c.writeJSON(wsRequest{Op: "subscribe", Args: []string{topic}}); err != nil {
			go c.reconnect()
			return
		}
	}
}
