package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type PublicWSClient struct {
	url string

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.RWMutex
	writeMu  sync.Mutex
	conn     *websocket.Conn
	closed   bool
	subs     map[string]WSArg
	handlers map[string]func(json.RawMessage)
}

func NewPublicWSClient() *PublicWSClient {
	ctx, cancel := context.WithCancel(context.Background())
	return &PublicWSClient{
		ctx:      ctx,
		cancel:   cancel,
		url:      publicWSURL,
		subs:     make(map[string]WSArg),
		handlers: make(map[string]func(json.RawMessage)),
	}
}

type WSArg struct {
	InstType string `json:"instType"`
	Topic    string `json:"topic,omitempty"`
	Symbol   string `json:"symbol,omitempty"`
	Interval string `json:"interval,omitempty"`
	Channel  string `json:"channel,omitempty"`
	InstID   string `json:"instId,omitempty"`
}

type wsRequest struct {
	Op   string  `json:"op"`
	Args []WSArg `json:"args"`
}

type WSEnvelope struct {
	Event  string          `json:"event"`
	Code   NumberString    `json:"code"`
	Msg    string          `json:"msg"`
	Arg    WSArg           `json:"arg"`
	Action string          `json:"action"`
	Data   json.RawMessage `json:"data"`
}

type WSOrderBookData struct {
	Asks     [][]NumberString `json:"a"`
	Bids     [][]NumberString `json:"b"`
	Checksum int64            `json:"checksum"`
	PSeq     int64            `json:"pseq"`
	Seq      int64            `json:"seq"`
	TS       string           `json:"ts"`
}

type WSOrderBookMessage struct {
	Arg    WSArg             `json:"arg"`
	Action string            `json:"action"`
	Data   []WSOrderBookData `json:"data"`
	TS     int64             `json:"ts"`
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
		return fmt.Errorf("bitget ws: client closed")
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

func (c *PublicWSClient) Subscribe(ctx context.Context, arg WSArg, handler func(json.RawMessage)) error {
	return c.SubscribeMany(ctx, []WSArg{arg}, handler)
}

// SubscribeMany installs one handler for a bounded set of public channels and
// sends them in batched frames. Bitget limits clients to ten outbound messages
// per second; sending one frame per market makes all-market supervisors
// disconnect before their initial subscription completes.
func (c *PublicWSClient) SubscribeMany(ctx context.Context, args []WSArg, handler func(json.RawMessage)) error {
	if len(args) == 0 {
		return fmt.Errorf("bitget ws: at least one subscription is required")
	}
	if err := c.Connect(ctx); err != nil {
		return err
	}

	c.mu.Lock()
	registered := make([]string, 0, len(args))
	batchKeys := make(map[string]struct{}, len(args))
	for _, arg := range args {
		key := wsKey(arg)
		if _, exists := c.subs[key]; exists {
			c.mu.Unlock()
			return fmt.Errorf("bitget ws: duplicate subscription %s", key)
		}
		if _, exists := batchKeys[key]; exists {
			c.mu.Unlock()
			return fmt.Errorf("bitget ws: duplicate subscription %s", key)
		}
		batchKeys[key] = struct{}{}
		registered = append(registered, key)
	}
	for index, arg := range args {
		key := registered[index]
		c.subs[key] = arg
		c.handlers[key] = handler
	}
	c.mu.Unlock()

	if err := c.writeSubscriptions(ctx, args); err != nil {
		c.mu.Lock()
		for _, key := range registered {
			delete(c.subs, key)
			delete(c.handlers, key)
		}
		c.mu.Unlock()
		return err
	}
	return nil
}

const publicWSSubscribeBatchLimit = 50

func (c *PublicWSClient) writeSubscriptions(ctx context.Context, args []WSArg) error {
	for start := 0; start < len(args); start += publicWSSubscribeBatchLimit {
		end := start + publicWSSubscribeBatchLimit
		if end > len(args) {
			end = len(args)
		}
		if start > 0 {
			timer := time.NewTimer(110 * time.Millisecond)
			select {
			case <-ctx.Done():
				timer.Stop()
				return ctx.Err()
			case <-timer.C:
			}
		}
		if err := c.writeJSON(wsRequest{Op: "subscribe", Args: args[start:end]}); err != nil {
			return err
		}
	}
	return nil
}

func (c *PublicWSClient) Unsubscribe(ctx context.Context, arg WSArg) error {
	_ = ctx

	key := wsKey(arg)
	c.mu.Lock()
	delete(c.subs, key)
	delete(c.handlers, key)
	c.mu.Unlock()

	if err := c.writeJSON(wsRequest{
		Op:   "unsubscribe",
		Args: []WSArg{arg},
	}); err != nil && err.Error() != "bitget ws: not connected" {
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
		return fmt.Errorf("bitget ws: not connected")
	}

	c.writeMu.Lock()
	defer c.writeMu.Unlock()
	return conn.WriteJSON(v)
}

func (c *PublicWSClient) pingLoop(conn *websocket.Conn) {
	ticker := time.NewTicker(30 * time.Second)
	defer ticker.Stop()
	for range ticker.C {
		c.mu.RLock()
		active := c.conn == conn
		c.mu.RUnlock()
		if !active {
			return
		}

		c.writeMu.Lock()
		err := conn.WriteMessage(websocket.TextMessage, []byte("ping"))
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

		if string(payload) == "pong" {
			continue
		}

		var env WSEnvelope
		if err := json.Unmarshal(payload, &env); err != nil {
			continue
		}
		if env.Event != "" || (env.Arg.Topic == "" && env.Arg.Channel == "") {
			continue
		}

		key := wsKey(env.Arg)
		c.mu.RLock()
		handler := c.handlers[key]
		c.mu.RUnlock()
		if handler != nil {
			handler(payload)
		}
	}
}

func wsKey(arg WSArg) string {
	return arg.InstType + "|" + arg.Topic + "|" + arg.Symbol + "|" + arg.Channel + "|" + arg.InstID
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
	subs := make([]WSArg, 0, len(c.subs))
	for _, arg := range c.subs {
		subs = append(subs, arg)
	}
	c.mu.RUnlock()

	if err := c.writeSubscriptions(c.ctx, subs); err != nil {
		go c.reconnect()
		return
	}
}
