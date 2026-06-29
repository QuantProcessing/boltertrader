package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

const defaultWSURL = "wss://ws.backpack.exchange"

type wsSubscribeRequest struct {
	Method    string   `json:"method"`
	Params    []string `json:"params"`
	Signature []string `json:"signature,omitempty"`
}

type wsSubscription struct {
	private bool
	handler func(json.RawMessage)
}

type WSClient struct {
	url        string
	apiKey     string
	privateKey string

	mu       sync.RWMutex
	writeMu  sync.Mutex
	conn     *websocket.Conn
	closed   bool
	handlers map[string]wsSubscription

	reconnectWait time.Duration
}

func NewWSClient() *WSClient {
	return &WSClient{
		url:           defaultWSURL,
		handlers:      make(map[string]wsSubscription),
		reconnectWait: 100 * time.Millisecond,
	}
}

func (c *WSClient) WithCredentials(apiKey, privateKey string) *WSClient {
	c.apiKey = apiKey
	c.privateKey = privateKey
	return c
}

func (c *WSClient) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.conn != nil {
		return nil
	}
	c.closed = false

	conn, _, err := websocket.DefaultDialer.DialContext(ctx, c.url, nil)
	if err != nil {
		return err
	}
	conn.SetPingHandler(func(appData string) error {
		return conn.WriteControl(websocket.PongMessage, []byte(appData), time.Now().Add(5*time.Second))
	})
	c.conn = conn

	go c.readLoop(conn)
	return nil
}

func (c *WSClient) Close() error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.closed = true
	if c.conn == nil {
		return nil
	}
	_ = c.conn.WriteControl(websocket.CloseMessage, websocket.FormatCloseMessage(websocket.CloseNormalClosure, ""), time.Now().Add(5*time.Second))
	err := c.conn.Close()
	c.conn = nil
	return err
}

func (c *WSClient) Subscribe(ctx context.Context, stream string, private bool, handler func(json.RawMessage)) error {
	if err := c.Connect(ctx); err != nil {
		return err
	}

	req, err := c.subscribeRequest(stream, private)
	if err != nil {
		return err
	}

	c.mu.Lock()
	c.handlers[stream] = wsSubscription{private: private, handler: handler}
	c.mu.Unlock()

	if err := c.writeJSON(req); err != nil {
		c.mu.Lock()
		delete(c.handlers, stream)
		c.mu.Unlock()
		return err
	}
	return nil
}

func (c *WSClient) Unsubscribe(ctx context.Context, stream string) error {
	_ = ctx

	c.mu.Lock()
	delete(c.handlers, stream)
	conn := c.conn
	c.mu.Unlock()

	if conn == nil {
		return nil
	}

	return c.writeJSON(wsSubscribeRequest{
		Method: "UNSUBSCRIBE",
		Params: []string{stream},
	})
}

func (c *WSClient) writeJSON(v any) error {
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()
	if conn == nil {
		return fmt.Errorf("backpack ws: not connected")
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteJSON(v)
}

func (c *WSClient) readLoop(conn *websocket.Conn) {
	for {
		_, payload, err := conn.ReadMessage()
		if err != nil {
			c.mu.Lock()
			closed := c.closed
			if c.conn == conn {
				c.conn = nil
			}
			c.mu.Unlock()
			if !closed {
				go c.reconnectAndResubscribe()
			}
			return
		}

		var env StreamEnvelope
		if err := json.Unmarshal(payload, &env); err != nil {
			continue
		}
		if env.Stream == "" {
			continue
		}

		c.mu.RLock()
		sub := c.handlers[env.Stream]
		c.mu.RUnlock()
		if sub.handler != nil {
			sub.handler(env.Data)
		}
	}
}

func (c *WSClient) subscribeRequest(stream string, private bool) (wsSubscribeRequest, error) {
	req := wsSubscribeRequest{
		Method: "SUBSCRIBE",
		Params: []string{stream},
	}
	if !private {
		return req, nil
	}
	timestamp := time.Now().UnixMilli()
	payload := buildSigningPayload("subscribe", nil, timestamp, defaultRecvWindow)
	signature, err := signPayload(c.privateKey, payload)
	if err != nil {
		return wsSubscribeRequest{}, err
	}
	req.Signature = []string{
		c.apiKey,
		signature,
		strconv.FormatInt(timestamp, 10),
		strconv.FormatInt(defaultRecvWindow, 10),
	}
	return req, nil
}

func (c *WSClient) reconnectAndResubscribe() {
	for {
		c.mu.RLock()
		closed := c.closed
		wait := c.reconnectWait
		hasSubscriptions := len(c.handlers) > 0
		c.mu.RUnlock()
		if closed || !hasSubscriptions {
			return
		}
		if wait <= 0 {
			wait = 100 * time.Millisecond
		}
		time.Sleep(wait)

		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := c.Connect(ctx)
		cancel()
		if err != nil {
			continue
		}
		c.resubscribeAll()
		return
	}
}

func (c *WSClient) resubscribeAll() {
	c.mu.RLock()
	snapshot := make(map[string]wsSubscription, len(c.handlers))
	for stream, sub := range c.handlers {
		snapshot[stream] = sub
	}
	c.mu.RUnlock()

	for stream, sub := range snapshot {
		req, err := c.subscribeRequest(stream, sub.private)
		if err != nil {
			continue
		}
		_ = c.writeJSON(req)
	}
}
