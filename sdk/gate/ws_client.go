package sdk

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/gorilla/websocket"
)

type wsRequest struct {
	Time    int64   `json:"time"`
	Channel string  `json:"channel"`
	Event   string  `json:"event"`
	Payload any     `json:"payload,omitempty"`
	Auth    *WSAuth `json:"auth,omitempty"`
}

type WSAuth struct {
	Method string `json:"method"`
	Key    string `json:"KEY"`
	Sign   string `json:"SIGN"`
}

type wsSubscription struct {
	channel string
	payload []string
	handler func(json.RawMessage)
}

type WSClient struct {
	url       string
	product   string
	apiKey    string
	secretKey string
	now       func() time.Time

	ctx    context.Context
	cancel context.CancelFunc

	mu       sync.RWMutex
	writeMu  sync.Mutex
	conn     *websocket.Conn
	closed   bool
	subs     map[string]wsSubscription
	handlers map[string]func(json.RawMessage)
}

func NewWSClient(product string) (*WSClient, error) {
	ctx, cancel := context.WithCancel(context.Background())
	url, err := wsURLForProduct(product)
	if err != nil {
		cancel()
		return nil, err
	}
	return &WSClient{
		url:      url,
		product:  product,
		now:      time.Now,
		ctx:      ctx,
		cancel:   cancel,
		subs:     make(map[string]wsSubscription),
		handlers: make(map[string]func(json.RawMessage)),
	}, nil
}

func MustNewWSClient(product string) *WSClient {
	client, err := NewWSClient(product)
	if err != nil {
		panic(err)
	}
	return client
}

func (c *WSClient) WithCredentials(apiKey, secretKey string) *WSClient {
	c.apiKey = apiKey
	c.secretKey = secretKey
	return c
}

func (c *WSClient) WithURL(rawURL string) *WSClient {
	c.url = rawURL
	return c
}

func (c *WSClient) WithClock(now func() time.Time) *WSClient {
	if now != nil {
		c.now = now
	}
	return c
}

func (c *WSClient) Connect(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.closed {
		return fmt.Errorf("gate ws: client closed")
	}
	if c.conn != nil {
		return nil
	}

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

func (c *WSClient) Subscribe(ctx context.Context, channel string, payload []string, handler func(json.RawMessage)) error {
	if err := c.Connect(ctx); err != nil {
		return err
	}

	req, err := c.subscribeRequest(channel, "subscribe", payload)
	if err != nil {
		return err
	}

	key := wsKey(channel, payload)
	c.mu.Lock()
	c.subs[key] = wsSubscription{channel: channel, payload: append([]string(nil), payload...), handler: handler}
	c.handlers[key] = handler
	c.mu.Unlock()

	if err := c.writeJSON(req); err != nil {
		c.mu.Lock()
		delete(c.subs, key)
		delete(c.handlers, key)
		c.mu.Unlock()
		return err
	}
	return nil
}

func (c *WSClient) Unsubscribe(ctx context.Context, channel string, payload []string) error {
	_ = ctx
	key := wsKey(channel, payload)
	c.mu.Lock()
	delete(c.subs, key)
	delete(c.handlers, key)
	c.mu.Unlock()

	req, err := c.subscribeRequest(channel, "unsubscribe", payload)
	if err != nil {
		return err
	}
	if err := c.writeJSON(req); err != nil && err.Error() != "gate ws: not connected" {
		return err
	}
	return nil
}

func (c *WSClient) Close() error {
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

func (c *WSClient) subscribeRequest(channel, event string, payload []string) (wsRequest, error) {
	req := wsRequest{
		Time:    c.now().Unix(),
		Channel: channel,
		Event:   event,
		Payload: payload,
	}
	if !requiresAuth(channel) {
		return req, nil
	}
	if c.apiKey == "" || c.secretKey == "" {
		return wsRequest{}, fmt.Errorf("gate ws: credentials required for %s", channel)
	}
	req.Auth = &WSAuth{
		Method: "api_key",
		Key:    c.apiKey,
		Sign:   sign(c.secretKey, buildWSAuthPayload(channel, event, req.Time)),
	}
	return req, nil
}

func (c *WSClient) writeJSON(v any) error {
	c.mu.RLock()
	conn := c.conn
	c.mu.RUnlock()
	if conn == nil {
		return fmt.Errorf("gate ws: not connected")
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
		if err := json.Unmarshal(payload, &env); err != nil || env.Channel == "" {
			continue
		}
		if env.Event == "subscribe" || env.Event == "unsubscribe" {
			continue
		}

		c.mu.RLock()
		handler := c.handlers[wsKey(env.Channel, nil)]
		if handler == nil {
			for _, sub := range c.subs {
				if sub.channel == env.Channel {
					handler = sub.handler
					break
				}
			}
		}
		c.mu.RUnlock()
		if handler != nil {
			handler(payload)
		}
	}
}

func (c *WSClient) reconnect() {
	select {
	case <-c.ctx.Done():
		return
	case <-time.After(time.Second):
	}
	if err := c.Connect(c.ctx); err != nil {
		go c.reconnect()
		return
	}
	c.resubscribeAll()
}

func (c *WSClient) resubscribeAll() {
	c.mu.RLock()
	subs := make([]wsSubscription, 0, len(c.subs))
	for _, sub := range c.subs {
		subs = append(subs, sub)
	}
	c.mu.RUnlock()

	for _, sub := range subs {
		req, err := c.subscribeRequest(sub.channel, "subscribe", sub.payload)
		if err != nil {
			continue
		}
		_ = c.writeJSON(req)
	}
}

func DecodeWSEnvelope(payload []byte) (*WSEnvelope, error) {
	var env WSEnvelope
	if err := json.Unmarshal(payload, &env); err != nil {
		return nil, err
	}
	return &env, nil
}

func DecodeSpotOrderMessage(payload []byte) (*SpotOrderMessage, error) {
	env, err := DecodeWSEnvelope(payload)
	if err != nil {
		return nil, err
	}
	var orders []Order
	if len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, &orders); err != nil {
			return nil, err
		}
	}
	return &SpotOrderMessage{WSEnvelope: *env, Orders: orders}, nil
}

func DecodeSpotBalanceMessage(payload []byte) (*SpotBalanceMessage, error) {
	env, err := DecodeWSEnvelope(payload)
	if err != nil {
		return nil, err
	}
	var balances []SpotBalance
	if len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, &balances); err != nil {
			return nil, err
		}
	}
	return &SpotBalanceMessage{WSEnvelope: *env, Balances: balances}, nil
}

func DecodeSpotUserTradeMessage(payload []byte) (*SpotUserTradeMessage, error) {
	env, err := DecodeWSEnvelope(payload)
	if err != nil {
		return nil, err
	}
	var trades []SpotUserTrade
	if len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, &trades); err != nil {
			return nil, err
		}
	}
	return &SpotUserTradeMessage{WSEnvelope: *env, Trades: trades}, nil
}

func DecodeFuturesOrderMessage(payload []byte) (*FuturesOrderMessage, error) {
	env, err := DecodeWSEnvelope(payload)
	if err != nil {
		return nil, err
	}
	var orders []FuturesOrder
	if len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, &orders); err != nil {
			return nil, err
		}
	}
	return &FuturesOrderMessage{WSEnvelope: *env, Orders: orders}, nil
}

func DecodeFuturesUserTradeMessage(payload []byte) (*FuturesUserTradeMessage, error) {
	env, err := DecodeWSEnvelope(payload)
	if err != nil {
		return nil, err
	}
	var trades []MyFuturesTrade
	if len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, &trades); err != nil {
			return nil, err
		}
	}
	return &FuturesUserTradeMessage{WSEnvelope: *env, Trades: trades}, nil
}

func DecodeFuturesBalanceMessage(payload []byte) (*FuturesBalanceMessage, error) {
	env, err := DecodeWSEnvelope(payload)
	if err != nil {
		return nil, err
	}
	var balances []FuturesBalance
	if len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, &balances); err != nil {
			return nil, err
		}
	}
	return &FuturesBalanceMessage{WSEnvelope: *env, Balances: balances}, nil
}

func DecodeFuturesPositionMessage(payload []byte) (*FuturesPositionMessage, error) {
	env, err := DecodeWSEnvelope(payload)
	if err != nil {
		return nil, err
	}
	var positions []Position
	if len(env.Result) > 0 {
		if err := json.Unmarshal(env.Result, &positions); err != nil {
			return nil, err
		}
	}
	return &FuturesPositionMessage{WSEnvelope: *env, Positions: positions}, nil
}

func wsURLForProduct(product string) (string, error) {
	switch product {
	case ProductSpot:
		return defaultSpotWSURL, nil
	case ProductFuturesUSDT:
		return defaultFuturesUSDTWSURL, nil
	default:
		return "", fmt.Errorf("gate ws: unsupported product %q", product)
	}
}

func wsKey(channel string, payload []string) string {
	if len(payload) == 0 {
		return channel
	}
	return channel + "|" + strings.Join(payload, ",")
}

func requiresAuth(channel string) bool {
	switch channel {
	case ChannelSpotOrder, ChannelSpotUserTrade, ChannelSpotBalance,
		ChannelFuturesOrder, ChannelFuturesUserTrade, ChannelFuturesBalance, ChannelFuturesPosition:
		return true
	default:
		return false
	}
}
